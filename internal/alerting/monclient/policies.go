package monclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"github.com/smallTechOrg/cloud0/internal/alerting"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Policies implements alerting.PolicyClient over the Monitoring AlertPolicyService.
type Policies struct {
	read  *monitoring.AlertPolicyClient
	write *monitoring.AlertPolicyClient // nil = not configured
	chans *Channels                     // reader, resolves channel refs
}

var _ alerting.PolicyClient = (*Policies)(nil)

// NewPolicies builds the reader client (plus channel lookup) and, when
// creds.Writer is set, the writer client.
func NewPolicies(ctx context.Context, creds Creds) (*Policies, error) {
	r, err := monitoring.NewAlertPolicyClient(ctx, creds.Reader...)
	if err != nil {
		return nil, fmt.Errorf("monclient: alert policy reader client: %w", err)
	}
	ch, err := NewChannels(ctx, Creds{Reader: creds.Reader})
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	p := &Policies{read: r, chans: ch}
	if creds.Writer != nil {
		w, err := monitoring.NewAlertPolicyClient(ctx, creds.Writer...)
		if err != nil {
			_ = r.Close()
			_ = ch.Close()
			return nil, fmt.Errorf("monclient: alert policy writer client: %w", err)
		}
		p.write = w
	}
	return p, nil
}

// Close releases all clients.
func (p *Policies) Close() error {
	first := p.read.Close()
	if p.write != nil {
		if err := p.write.Close(); first == nil {
			first = err
		}
	}
	if err := p.chans.Close(); first == nil {
		first = err
	}
	return first
}

// channelIndex maps channel name → ref for a project; lookup failures degrade
// to name-only refs rather than failing the policy read.
func (p *Policies) channelIndex(ctx context.Context, project string) map[string]alerting.Channel {
	idx := map[string]alerting.Channel{}
	chs, err := p.chans.List(ctx, project)
	if err != nil {
		return idx
	}
	for _, c := range chs {
		idx[c.Name] = c
	}
	return idx
}

// List returns every policy of the project ordered by display name.
func (p *Policies) List(ctx context.Context, project string) ([]alerting.PolicyView, error) {
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("%w: project is required", alerting.ErrInvalidParams)
	}
	it := p.read.ListAlertPolicies(ctx, &monitoringpb.ListAlertPoliciesRequest{Name: "projects/" + project})
	var raw []*monitoringpb.AlertPolicy
	for {
		ap, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, mapErr(err, false, roleAlertEditor, project)
		}
		raw = append(raw, ap)
	}
	idx := p.channelIndex(ctx, project)
	out := make([]alerting.PolicyView, 0, len(raw))
	for _, ap := range raw {
		out = append(out, policyToView(ap, idx))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DisplayName != out[j].DisplayName {
			return out[i].DisplayName < out[j].DisplayName
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Get returns one policy.
func (p *Policies) Get(ctx context.Context, name string) (alerting.PolicyView, error) {
	if !validName(name, "alertPolicies") {
		return alerting.PolicyView{}, fmt.Errorf("%w: policy name must look like %s", alerting.ErrInvalidParams, policyNamePattern)
	}
	ap, err := p.read.GetAlertPolicy(ctx, &monitoringpb.GetAlertPolicyRequest{Name: name})
	if err != nil {
		return alerting.PolicyView{}, mapErr(err, false, roleAlertEditor, projectOf(name))
	}
	return policyToView(ap, p.channelIndex(ctx, projectOf(name))), nil
}

// Create makes a policy in project from REST AlertPolicy JSON (name ignored).
func (p *Policies) Create(ctx context.Context, project string, policy json.RawMessage) (alerting.PolicyView, error) {
	if p.write == nil {
		return alerting.PolicyView{}, alerting.ErrNotConfigured
	}
	if strings.TrimSpace(project) == "" {
		return alerting.PolicyView{}, fmt.Errorf("%w: project is required", alerting.ErrInvalidParams)
	}
	ap, err := ParsePolicy(policy)
	if err != nil {
		return alerting.PolicyView{}, err
	}
	ap.Name = ""
	got, err := p.write.CreateAlertPolicy(ctx, &monitoringpb.CreateAlertPolicyRequest{Name: "projects/" + project, AlertPolicy: ap})
	if err != nil {
		return alerting.PolicyView{}, mapErr(err, true, roleAlertEditor, project)
	}
	return policyToView(got, p.channelIndex(ctx, project)), nil
}

// Update fully replaces the policy `name` with the given JSON (no field mask),
// preserving the name.
func (p *Policies) Update(ctx context.Context, name string, policy json.RawMessage) (alerting.PolicyView, error) {
	if p.write == nil {
		return alerting.PolicyView{}, alerting.ErrNotConfigured
	}
	if !validName(name, "alertPolicies") {
		return alerting.PolicyView{}, fmt.Errorf("%w: policy name must look like %s", alerting.ErrInvalidParams, policyNamePattern)
	}
	ap, err := ParsePolicy(policy)
	if err != nil {
		return alerting.PolicyView{}, err
	}
	if ap.Name != "" && ap.Name != name {
		return alerting.PolicyView{}, fmt.Errorf("%w: policy JSON name %q does not match target %q", alerting.ErrInvalidParams, ap.Name, name)
	}
	ap.Name = name
	got, err := p.write.UpdateAlertPolicy(ctx, &monitoringpb.UpdateAlertPolicyRequest{AlertPolicy: ap})
	if err != nil {
		return alerting.PolicyView{}, mapErr(err, true, roleAlertEditor, projectOf(name))
	}
	return policyToView(got, p.channelIndex(ctx, projectOf(name))), nil
}

// Delete removes a policy.
func (p *Policies) Delete(ctx context.Context, name string) error {
	if p.write == nil {
		return alerting.ErrNotConfigured
	}
	if !validName(name, "alertPolicies") {
		return fmt.Errorf("%w: policy name must look like %s", alerting.ErrInvalidParams, policyNamePattern)
	}
	if err := p.write.DeleteAlertPolicy(ctx, &monitoringpb.DeleteAlertPolicyRequest{Name: name}); err != nil {
		return mapErr(err, true, roleAlertEditor, projectOf(name))
	}
	return nil
}

// SetEnabled flips only the `enabled` field (field mask), leaving the rest untouched.
func (p *Policies) SetEnabled(ctx context.Context, name string, enabled bool) (alerting.PolicyView, error) {
	if p.write == nil {
		return alerting.PolicyView{}, alerting.ErrNotConfigured
	}
	if !validName(name, "alertPolicies") {
		return alerting.PolicyView{}, fmt.Errorf("%w: policy name must look like %s", alerting.ErrInvalidParams, policyNamePattern)
	}
	got, err := p.write.UpdateAlertPolicy(ctx, &monitoringpb.UpdateAlertPolicyRequest{
		UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"enabled"}},
		AlertPolicy: &monitoringpb.AlertPolicy{Name: name, Enabled: wrapperspb.Bool(enabled)},
	})
	if err != nil {
		return alerting.PolicyView{}, mapErr(err, true, roleAlertEditor, projectOf(name))
	}
	return policyToView(got, p.channelIndex(ctx, projectOf(name))), nil
}

// ---- JSON <-> proto ----------------------------------------------------------

// ParsePolicy decodes REST AlertPolicy JSON (camelCase or original names).
// Unknown fields are rejected with ErrInvalidParams. Server-owned records are cleared.
func ParsePolicy(raw json.RawMessage) (*monitoringpb.AlertPolicy, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("%w: policy JSON is empty", alerting.ErrInvalidParams)
	}
	ap := &monitoringpb.AlertPolicy{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, ap); err != nil {
		return nil, fmt.Errorf("%w: policy JSON: %v", alerting.ErrInvalidParams, err)
	}
	ap.CreationRecord = nil
	ap.MutationRecord = nil
	return ap, nil
}

// policyJSON renders the policy in the Monitoring REST (camelCase) shape.
func policyJSON(ap *monitoringpb.AlertPolicy) json.RawMessage {
	b, err := protojson.MarshalOptions{UseProtoNames: false}.Marshal(ap)
	if err != nil {
		return json.RawMessage("null")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return json.RawMessage("null")
	}
	return buf.Bytes()
}

// ---- conversion (pure) --------------------------------------------------------

func policyToView(ap *monitoringpb.AlertPolicy, channels map[string]alerting.Channel) alerting.PolicyView {
	labels := ap.GetUserLabels()
	v := alerting.PolicyView{
		Name:        ap.GetName(),
		Project:     projectOf(ap.GetName()),
		DisplayName: ap.GetDisplayName(),
		Enabled:     ap.GetEnabled() == nil || ap.GetEnabled().GetValue(),
		Managed:     labels[alerting.LabelManagedBy] == alerting.LabelManagedByVal,
		Conditions:  []alerting.ConditionSummary{},
		Channels:    []alerting.ChannelRef{},
		Warnings:    []string{},
		RawJSON:     policyJSON(ap),
	}
	if ap.GetSeverity() != monitoringpb.AlertPolicy_SEVERITY_UNSPECIFIED {
		v.Severity = ap.GetSeverity().String()
	}
	if v.Managed {
		v.TemplateID = labels[alerting.LabelTemplate]
		v.Instance = labels[alerting.LabelInstance]
	}
	if mt := ap.GetMutationRecord().GetMutateTime(); mt != nil {
		t := mt.AsTime()
		v.UpdatedAt = &t
	}
	for _, c := range ap.GetConditions() {
		v.Conditions = append(v.Conditions, conditionSummary(c))
	}
	for _, n := range ap.GetNotificationChannels() {
		ref := alerting.ChannelRef{Name: n}
		if c, ok := channels[n]; ok {
			ref.DisplayName, ref.Type, ref.VerificationStatus = c.DisplayName, c.Type, c.VerificationStatus
		}
		v.Channels = append(v.Channels, ref)
	}
	v.Warnings = policyWarnings(v)
	if !v.Managed {
		v.AdoptShape = DetectAdoptShape(v)
	}
	return v
}

func policyWarnings(v alerting.PolicyView) []string {
	w := []string{}
	for _, c := range v.Conditions {
		if c.Type == "threshold" && c.DurationSeconds != nil && *c.DurationSeconds == 0 {
			w = append(w, fmt.Sprintf("Condition %q: duration is 0 s: fires on a single sample.", c.DisplayName))
		}
		if filterPinsOneInstance(c.Filter) {
			w = append(w, fmt.Sprintf("Condition %q covers specific instances only; new VMs are not alerted.", c.DisplayName))
		}
	}
	if len(v.Channels) == 0 {
		w = append(w, "No notification channels: this policy notifies nobody.")
	}
	for _, c := range v.Channels {
		if c.Type == "email" && c.VerificationStatus != "" && c.VerificationStatus != "VERIFIED" {
			w = append(w, fmt.Sprintf("Email channel %q is not verified: notifications may not be delivered.", c.DisplayName))
		}
	}
	return w
}

// filterPinsOneInstance spots filters that restrict a gce_instance metric to
// named instances.
func filterPinsOneInstance(filter string) bool {
	n := strings.ReplaceAll(filter, " ", "")
	return strings.Contains(n, "instance_id=\"") || strings.Contains(n, "instance_id=monitoring") ||
		strings.Contains(n, "system_labels.name=")
}

// DetectAdoptShape recognises the known unmanaged policy shapes by display name
// (case-insensitive) and condition signature; "" when none matches.
func DetectAdoptShape(v alerting.PolicyView) string {
	if v.Managed {
		return ""
	}
	name := strings.ToLower(v.DisplayName)
	anyFilter := func(sub string) bool {
		for _, c := range v.Conditions {
			if strings.Contains(strings.ToLower(c.Filter), sub) {
				return true
			}
		}
		return false
	}
	anyDuration := func(sec int) bool {
		for _, c := range v.Conditions {
			if c.DurationSeconds != nil && *c.DurationSeconds == sec {
				return true
			}
		}
		return false
	}
	switch {
	case strings.Contains(name, "zero-backend cpu spike") && anyFilter("cpu"):
		return alerting.ShapeZeroCPUSpike
	case strings.Contains(name, "high memory load") && anyFilter("memory"):
		return alerting.ShapeHighMemory
	case strings.Contains(name, "smalltech backend down") && (anyFilter("uptime") || anyFilter("check_passed")):
		return alerting.ShapeUptimeBackend
	case strings.Contains(name, "smalltech down for 2 hours") && (anyFilter("uptime") || anyFilter("check_passed") || anyDuration(7200)):
		return alerting.ShapeUptimeTwoHours
	}
	return ""
}

func conditionSummary(c *monitoringpb.AlertPolicy_Condition) alerting.ConditionSummary {
	cs := alerting.ConditionSummary{DisplayName: c.GetDisplayName(), Type: "other", Summary: "Unsupported condition type (see raw JSON)"}
	switch {
	case c.GetConditionThreshold() != nil:
		t := c.GetConditionThreshold()
		cs.Type = "threshold"
		cs.Filter = t.GetFilter()
		cs.Comparison = t.GetComparison().String()
		th := t.GetThresholdValue()
		cs.Threshold = &th
		d := int(t.GetDuration().GetSeconds())
		cs.DurationSeconds = &d
		cs.Summary = thresholdSummary(t)
	case c.GetConditionAbsent() != nil:
		a := c.GetConditionAbsent()
		cs.Type = "absent"
		cs.Filter = a.GetFilter()
		d := int(a.GetDuration().GetSeconds())
		cs.DurationSeconds = &d
		cs.Summary = fmt.Sprintf("%s: no data for %s", metricLabel(metricType(a.GetFilter())), humanDuration(d))
	case c.GetConditionMatchedLog() != nil:
		cs.Type = "log_match"
		cs.Filter = c.GetConditionMatchedLog().GetFilter()
		cs.Summary = "Log entry matches: " + cs.Filter
	case c.GetConditionMonitoringQueryLanguage() != nil:
		q := c.GetConditionMonitoringQueryLanguage()
		cs.Filter = q.GetQuery()
		d := int(q.GetDuration().GetSeconds())
		cs.DurationSeconds = &d
		cs.Summary = "MQL query condition for " + humanDuration(d)
	case c.GetConditionPrometheusQueryLanguage() != nil:
		q := c.GetConditionPrometheusQueryLanguage()
		cs.Filter = q.GetQuery()
		d := int(q.GetDuration().GetSeconds())
		cs.DurationSeconds = &d
		cs.Summary = "PromQL condition for " + humanDuration(d)
	}
	return cs
}

var cmpSymbol = map[monitoringpb.ComparisonType]string{
	monitoringpb.ComparisonType_COMPARISON_GT: ">",
	monitoringpb.ComparisonType_COMPARISON_GE: ">=",
	monitoringpb.ComparisonType_COMPARISON_LT: "<",
	monitoringpb.ComparisonType_COMPARISON_LE: "<=",
	monitoringpb.ComparisonType_COMPARISON_EQ: "=",
	monitoringpb.ComparisonType_COMPARISON_NE: "!=",
}

func thresholdSummary(t *monitoringpb.AlertPolicy_Condition_MetricThreshold) string {
	mt := metricType(t.GetFilter())
	sym := cmpSymbol[t.GetComparison()]
	if sym == "" {
		sym = "?"
	}
	d := int(t.GetDuration().GetSeconds())
	dur := "on a single sample"
	if d > 0 {
		dur = "for " + humanDuration(d)
	}
	if strings.HasSuffix(mt, "uptime_check/check_passed") {
		return fmt.Sprintf("Uptime check failing from %s %s location(s) %s", sym, formatNum(t.GetThresholdValue()), dur)
	}
	value := formatNum(t.GetThresholdValue())
	switch {
	case strings.HasSuffix(mt, "percent_used"):
		value += " %"
	case strings.HasSuffix(mt, "utilization") || t.GetDenominatorFilter() != "":
		value = formatNum(t.GetThresholdValue()*100) + " %"
	}
	return fmt.Sprintf("%s %s %s %s", metricLabel(mt), sym, value, dur)
}

func formatNum(f float64) string {
	f = math.Round(f*1e6) / 1e6
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func humanDuration(sec int) string {
	d := time.Duration(sec) * time.Second
	switch {
	case sec <= 0:
		return "0 s"
	case sec < 60:
		return fmt.Sprintf("%d s", sec)
	case sec%3600 == 0:
		return fmt.Sprintf("%d h", int(d.Hours()))
	case sec%60 == 0:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d s", sec)
}

// metricType extracts the value of metric.type = "..." from a monitoring filter.
func metricType(filter string) string {
	i := strings.Index(filter, "metric.type")
	if i < 0 {
		return ""
	}
	rest := filter[i+len("metric.type"):]
	q := strings.IndexAny(rest, "\"'")
	if q < 0 {
		return ""
	}
	quote := rest[q]
	rest = rest[q+1:]
	e := strings.IndexByte(rest, quote)
	if e < 0 {
		return ""
	}
	return rest[:e]
}

var metricLabels = map[string]string{
	"compute.googleapis.com/instance/cpu/utilization":     "CPU utilization",
	"agent.googleapis.com/cpu/utilization":                "CPU utilization",
	"agent.googleapis.com/memory/percent_used":            "Memory used",
	"agent.googleapis.com/disk/percent_used":              "Disk used",
	"run.googleapis.com/request_count":                    "Requests",
	"loadbalancing.googleapis.com/https/request_count":    "LB requests",
	"cloudsql.googleapis.com/database/up":                 "Database up",
	"cloudsql.googleapis.com/database/cpu/utilization":    "Cloud SQL CPU utilization",
	"cloudsql.googleapis.com/database/disk/utilization":   "Cloud SQL disk utilization",
	"cloudsql.googleapis.com/database/memory/utilization": "Cloud SQL memory utilization",
	"cloudsql.googleapis.com/database/memory/total_usage": "Cloud SQL memory usage",
}

func metricLabel(mt string) string {
	if mt == "" {
		return "Metric"
	}
	if l, ok := metricLabels[mt]; ok {
		return l
	}
	parts := strings.Split(mt, "/")
	if len(parts) >= 2 {
		return strings.ReplaceAll(strings.Join(parts[len(parts)-2:], " "), "_", " ")
	}
	return mt
}
