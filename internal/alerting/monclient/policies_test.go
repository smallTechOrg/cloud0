package monclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"github.com/smallTechOrg/cloud0/internal/alerting"
	"github.com/smallTechOrg/cloud0/internal/testenv"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func mustPB(t *testing.T, js string) *monitoringpb.AlertPolicy {
	t.Helper()
	ap, err := ParsePolicy(json.RawMessage(js))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	return ap
}

const cpuPolicyJSON = `{
 "name":"projects/proj-a/alertPolicies/222","displayName":"Zero-Backend CPU Spike","enabled":true,"combiner":"OR",
 "notificationChannels":["projects/proj-a/notificationChannels/111"],
 "conditions":[{"displayName":"CPU > 80%","conditionThreshold":{
   "filter":"metric.type=\"compute.googleapis.com/instance/cpu/utilization\" resource.type=\"gce_instance\" metadata.system_labels.name=\"vm-a\"",
   "comparison":"COMPARISON_GT","thresholdValue":0.8,"duration":"300s"}}]}`

func TestPolicyToViewUnmanagedCPU(t *testing.T) {
	idx := map[string]alerting.Channel{
		"projects/proj-a/notificationChannels/111": {Name: "projects/proj-a/notificationChannels/111", DisplayName: "Alert", Type: "email", VerificationStatus: "UNVERIFIED"},
	}
	v := policyToView(mustPB(t, cpuPolicyJSON), idx)
	if v.Project != "proj-a" || v.Managed || v.TemplateID != "" || v.Severity != "" || !v.Enabled {
		t.Fatalf("basic fields wrong: %+v", v)
	}
	if v.AdoptShape != alerting.ShapeZeroCPUSpike {
		t.Fatalf("adopt shape = %q", v.AdoptShape)
	}
	if len(v.Conditions) != 1 || v.Conditions[0].Summary != "CPU utilization > 80 % for 5 min" {
		t.Fatalf("summary = %+v", v.Conditions)
	}
	c := v.Conditions[0]
	if c.Type != "threshold" || c.Comparison != "COMPARISON_GT" || *c.Threshold != 0.8 || *c.DurationSeconds != 300 {
		t.Fatalf("condition = %+v", c)
	}
	if len(v.Channels) != 1 || v.Channels[0].DisplayName != "Alert" || v.Channels[0].VerificationStatus != "UNVERIFIED" {
		t.Fatalf("channels = %+v", v.Channels)
	}
	joined := strings.Join(v.Warnings, "|")
	if !strings.Contains(joined, "new VMs are not alerted") || !strings.Contains(joined, "not verified") {
		t.Fatalf("warnings = %v", v.Warnings)
	}
	var raw map[string]any
	if err := json.Unmarshal(v.RawJSON, &raw); err != nil || raw["displayName"] != "Zero-Backend CPU Spike" {
		t.Fatalf("raw json camelCase missing: %s (%v)", v.RawJSON, err)
	}
}

func TestPolicyToViewManagedLabelsAndSeverity(t *testing.T) {
	v := policyToView(mustPB(t, `{"name":"projects/proj-b/alertPolicies/9","displayName":"cloud0: Uptime","enabled":false,"severity":"CRITICAL","combiner":"OR",
	 "userLabels":{"managed-by":"cloud0","cloud0-template":"uptime_down","cloud0-instance":"critical"},
	 "conditions":[{"displayName":"u","conditionThreshold":{"filter":"metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\"","comparison":"COMPARISON_GT","thresholdValue":1,"duration":"7200s"}}]}`), nil)
	if !v.Managed || v.TemplateID != "uptime_down" || v.Instance != "critical" || v.Severity != "CRITICAL" || v.Enabled {
		t.Fatalf("view = %+v", v)
	}
	if got := v.Conditions[0].Summary; got != "Uptime check failing from > 1 location(s) for 2 h" {
		t.Fatalf("summary = %q", got)
	}
	if v.AdoptShape != "" {
		t.Fatalf("managed policies have no adopt shape")
	}
	if len(v.Warnings) == 0 || !strings.Contains(strings.Join(v.Warnings, "|"), "notifies nobody") {
		t.Fatalf("expected no-channel warning: %v", v.Warnings)
	}
}

func TestUserLabelNotManagedByOther(t *testing.T) {
	v := policyToView(mustPB(t, `{"displayName":"x","userLabels":{"managed-by":"terraform","cloud0-template":"vm_cpu_high"}}`), nil)
	if v.Managed || v.TemplateID != "" {
		t.Fatalf("other managers must not count as cloud0: %+v", v)
	}
}

func TestZeroDurationWarningAndMemoryShape(t *testing.T) {
	v := policyToView(mustPB(t, `{"name":"projects/p/alertPolicies/1","displayName":"High memory load","enabled":true,"combiner":"OR",
	 "conditions":[{"displayName":"mem","conditionThreshold":{"filter":"metric.type=\"agent.googleapis.com/memory/percent_used\" resource.type=\"gce_instance\"","comparison":"COMPARISON_GT","thresholdValue":85}}]}`), nil)
	if v.AdoptShape != alerting.ShapeHighMemory {
		t.Fatalf("shape = %q", v.AdoptShape)
	}
	if got := v.Conditions[0].Summary; got != "Memory used > 85 % on a single sample" {
		t.Fatalf("summary = %q", got)
	}
	if !strings.Contains(strings.Join(v.Warnings, "|"), "duration is 0 s") {
		t.Fatalf("warnings = %v", v.Warnings)
	}
}

func TestUptimeShapes(t *testing.T) {
	mk := func(name, dur string) alerting.PolicyView {
		return policyToView(mustPB(t, fmt.Sprintf(`{"displayName":%q,"conditions":[{"displayName":"c","conditionThreshold":{"filter":"metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\"","comparison":"COMPARISON_GT","thresholdValue":1,"duration":%q}}]}`, name, dur)), nil)
	}
	if s := mk("SmallTech backend down uptime failure", "600s").AdoptShape; s != alerting.ShapeUptimeBackend {
		t.Fatalf("backend shape %q", s)
	}
	if s := mk("smalltech down for 2 hours", "7200s").AdoptShape; s != alerting.ShapeUptimeTwoHours {
		t.Fatalf("2h shape %q", s)
	}
	if s := mk("something else", "600s").AdoptShape; s != "" {
		t.Fatalf("other shape %q", s)
	}
}

func TestLogMatchAndRatioSummaries(t *testing.T) {
	v := policyToView(mustPB(t, `{"displayName":"x","combiner":"OR","conditions":[
	 {"displayName":"log","conditionMatchedLog":{"filter":"resource.type=\"cloud_run_revision\" AND severity>=ERROR"}},
	 {"displayName":"ratio","conditionThreshold":{"filter":"metric.type=\"run.googleapis.com/request_count\"","denominatorFilter":"metric.type=\"run.googleapis.com/request_count\"","comparison":"COMPARISON_GT","thresholdValue":0.02,"duration":"600s"}},
	 {"displayName":"abs","conditionAbsent":{"filter":"metric.type=\"agent.googleapis.com/cpu/utilization\"","duration":"900s"}}]}`), nil)
	want := []string{
		`Log entry matches: resource.type="cloud_run_revision" AND severity>=ERROR`,
		"Requests > 2 % for 10 min",
		"CPU utilization: no data for 15 min",
	}
	for i, w := range want {
		if v.Conditions[i].Summary != w {
			t.Errorf("cond %d summary = %q, want %q", i, v.Conditions[i].Summary, w)
		}
	}
	if v.Conditions[0].Type != "log_match" || v.Conditions[2].Type != "absent" {
		t.Errorf("types = %v / %v", v.Conditions[0].Type, v.Conditions[2].Type)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[int]string{0: "0 s", 30: "30 s", 90: "90 s", 300: "5 min", 3600: "1 h", 7200: "2 h", 5400: "90 min"}
	for in, want := range cases {
		if got := humanDuration(in); got != want {
			t.Errorf("humanDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePolicyRejectsUnknownAndEmpty(t *testing.T) {
	for _, js := range []string{`{"displayName":"x","bogusField":1}`, `{"conditions":[{"conditionThreshold":{"nope":true}}]}`, ``, `null`, `{not json`} {
		_, err := ParsePolicy(json.RawMessage(js))
		if !errors.Is(err, alerting.ErrInvalidParams) {
			t.Errorf("ParsePolicy(%q) err = %v, want ErrInvalidParams", js, err)
		}
	}
	if _, err := ParsePolicy(json.RawMessage(`{"displayName":"x","bogusField":1}`)); err == nil || !strings.Contains(err.Error(), "bogusField") {
		t.Errorf("error should name the unknown field: %v", err)
	}
}

func TestParsePolicyClearsServerRecordsAndAcceptsProtoNames(t *testing.T) {
	ap, err := ParsePolicy(json.RawMessage(`{"display_name":"x","creationRecord":{"mutatedBy":"someone"},"mutationRecord":{"mutatedBy":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ap.DisplayName != "x" || ap.CreationRecord != nil || ap.MutationRecord != nil {
		t.Fatalf("ap = %v", ap)
	}
}

func TestPolicyJSONRoundTrip(t *testing.T) {
	ap := mustPB(t, cpuPolicyJSON)
	back, err := ParsePolicy(policyJSON(ap))
	if err != nil {
		t.Fatal(err)
	}
	if protojson.Format(ap) != protojson.Format(back) {
		t.Fatal("round trip differs")
	}
}

func TestMapErr(t *testing.T) {
	cases := []struct {
		err   error
		write bool
		want  error
		sub   string
	}{
		{status.Error(codes.PermissionDenied, "denied"), true, alerting.ErrPermissionDenied, roleAlertEditor},
		{status.Error(codes.PermissionDenied, "denied"), false, alerting.ErrPermissionDenied, roleViewer},
		{status.Error(codes.NotFound, "no such policy"), false, alerting.ErrNotFound, "no such policy"},
		{status.Error(codes.InvalidArgument, "bad filter"), true, alerting.ErrInvalidParams, "bad filter"},
		{status.Error(codes.PermissionDenied, "Cloud Monitoring API has not been used in project 1 before or it is disabled"), false, alerting.ErrAPIDisabled, "gcloud services enable"},
		{status.Error(codes.Unavailable, "boom"), false, alerting.ErrUpstream, "boom"},
		{errors.New("plain"), false, alerting.ErrUpstream, "plain"},
	}
	for _, c := range cases {
		got := mapErr(c.err, c.write, roleAlertEditor, "proj-a")
		if !errors.Is(got, c.want) || !strings.Contains(got.Error(), c.sub) {
			t.Errorf("mapErr(%v) = %v, want %v containing %q", c.err, got, c.want, c.sub)
		}
	}
	if mapErr(nil, false, "", "") != nil {
		t.Error("nil must map to nil")
	}
	if !errors.Is(mapErr(context.Canceled, false, "", ""), context.Canceled) {
		t.Error("context errors pass through")
	}
	if st, code := alerting.CodeFor(mapErr(status.Error(codes.PermissionDenied, "x"), true, roleAlertEditor, "p")); st != 403 || code != "permission_denied" {
		t.Errorf("CodeFor = %d %s", st, code)
	}
}

func TestWritesRequireWriter(t *testing.T) {
	p := &Policies{}
	ctx := context.Background()
	if _, err := p.Create(ctx, "p", json.RawMessage(`{}`)); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Errorf("Create: %v", err)
	}
	if _, err := p.Update(ctx, "projects/p/alertPolicies/1", json.RawMessage(`{}`)); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Errorf("Update: %v", err)
	}
	if err := p.Delete(ctx, "projects/p/alertPolicies/1"); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Errorf("Delete: %v", err)
	}
	if _, err := p.SetEnabled(ctx, "projects/p/alertPolicies/1", true); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Errorf("SetEnabled: %v", err)
	}
	c := &Channels{}
	if _, err := c.Create(ctx, alerting.ChannelDraft{}); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Errorf("channel Create: %v", err)
	}
	if err := c.Delete(ctx, "projects/p/notificationChannels/1"); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Errorf("channel Delete: %v", err)
	}
}

func TestNameValidation(t *testing.T) {
	p := &Policies{write: nil}
	_ = p
	if validName("projects/p/alertPolicies/1", "alertPolicies") != true || validName("projects/p/alertPolicies", "alertPolicies") || validName("projects//alertPolicies/1", "alertPolicies") || validName("x/p/alertPolicies/1", "alertPolicies") {
		t.Fatal("validName wrong")
	}
}

// ---- real GCP ------------------------------------------------------------------

func firstProject(t *testing.T) (all []string) {
	t.Helper()
	testenv.LoadDotEnv(t)
	for _, p := range strings.Split(os.Getenv("CLOUD0_GCP_PROJECTS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			all = append(all, p)
		}
	}
	if len(all) == 0 {
		t.Skip("CLOUD0_GCP_PROJECTS not set — fill .env to run real Monitoring tests")
	}
	return all
}

func TestRealListPolicies(t *testing.T) {
	projects := firstProject(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	p, err := NewPolicies(ctx, Creds{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	total := 0
	for _, proj := range projects {
		views, err := p.List(ctx, proj)
		if err != nil {
			t.Fatalf("List(%s): %v", "project", err)
		}
		for _, v := range views {
			total++
			if v.Name == "" || v.DisplayName == "" || v.Project != proj || len(v.RawJSON) < 2 || v.Conditions == nil || v.Channels == nil || v.Warnings == nil {
				t.Fatalf("incomplete view: name=%q project=%q conds=%d", v.Name, v.Project, len(v.Conditions))
			}
			for _, c := range v.Conditions {
				if c.Summary == "" || c.Type == "" {
					t.Fatalf("condition without summary/type in %s", v.Name)
				}
			}
			got, err := p.Get(ctx, v.Name)
			if err != nil || got.Name != v.Name {
				t.Fatalf("Get(%s) = %v, %v", v.Name, got.Name, err)
			}
		}
	}
	if total < 1 {
		t.Fatalf("expected at least 1 alert policy across projects, got %d", total)
	}
	if _, err := p.Get(ctx, projectsName(projects[0])+"/alertPolicies/0"); !errors.Is(err, alerting.ErrNotFound) && !errors.Is(err, alerting.ErrInvalidParams) {
		t.Fatalf("missing policy should be not_found/invalid, got %v", err)
	}
}

func projectsName(p string) string { return "projects/" + p }

func TestRealPolicyWriteLifecycle(t *testing.T) {
	projects := firstProject(t)
	if strings.TrimSpace(os.Getenv(EnvAlertsCredentials)) == "" {
		t.Skip("CLOUD0_ALERTS_CREDENTIALS not set — real alert-policy write tests need the cloud0-alerts key")
	}
	project := projects[0]
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	p, err := NewPolicies(ctx, CredsFromEnv())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })

	display := fmt.Sprintf("%spolicy-%d", alerting.TestPrefix, time.Now().UnixNano())
	body := func(threshold float64, enabled bool, name string) json.RawMessage {
		m := map[string]any{
			"displayName": display, "combiner": "OR", "enabled": enabled,
			"userLabels": map[string]string{"cloud0-test": "true"},
			"conditions": []any{map[string]any{
				"displayName": "cloud0-test cpu",
				"conditionThreshold": map[string]any{
					"filter":         `metric.type="compute.googleapis.com/instance/cpu/utilization" resource.type="gce_instance" metadata.system_labels.name="cloud0-test-nonexistent"`,
					"comparison":     "COMPARISON_GT",
					"thresholdValue": threshold,
					"duration":       "300s",
					"aggregations":   []any{map[string]any{"alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_MEAN"}},
				},
			}},
		}
		if name != "" {
			m["name"] = name
		}
		b, _ := json.Marshal(m)
		return b
	}

	created, err := p.Create(ctx, project, body(0.9, false, ""))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	name := created.Name
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		err := p.Delete(cctx, name)
		if err != nil && !errors.Is(err, alerting.ErrNotFound) {
			t.Errorf("LOUD: cleanup failed to delete test policy %s: %v", name, err)
		}
	})
	if created.Enabled || created.DisplayName != display || len(created.Channels) != 0 || created.Managed {
		t.Fatalf("created = enabled:%v display:%q channels:%d", created.Enabled, created.DisplayName, len(created.Channels))
	}

	got, err := p.Get(ctx, name)
	if err != nil || got.DisplayName != display || *got.Conditions[0].Threshold != 0.9 {
		t.Fatalf("Get = %+v, %v", got.Conditions, err)
	}

	upd, err := p.Update(ctx, name, body(0.95, false, ""))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if upd.Name != name || *upd.Conditions[0].Threshold != 0.95 || upd.Enabled {
		t.Fatalf("Update result threshold=%v enabled=%v name=%s", *upd.Conditions[0].Threshold, upd.Enabled, upd.Name)
	}
	if _, err := p.Update(ctx, name, body(0.95, false, "projects/"+project+"/alertPolicies/other")); !errors.Is(err, alerting.ErrInvalidParams) {
		t.Fatalf("name mismatch should be invalid_params, got %v", err)
	}

	on, err := p.SetEnabled(ctx, name, true)
	if err != nil || !on.Enabled || *on.Conditions[0].Threshold != 0.95 {
		t.Fatalf("SetEnabled(true) = %v, %v", on.Enabled, err)
	}
	off, err := p.SetEnabled(ctx, name, false)
	if err != nil || off.Enabled {
		t.Fatalf("SetEnabled(false) = %v, %v", off.Enabled, err)
	}

	listed, err := p.List(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range listed {
		found = found || v.Name == name
	}
	if !found {
		t.Fatal("created policy missing from List")
	}

	if err := p.Delete(ctx, name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := p.Get(ctx, name); !errors.Is(err, alerting.ErrNotFound) {
		t.Fatalf("after delete Get err = %v, want ErrNotFound", err)
	}
}

func TestRealWriteWithoutAlertsCredsIsNotConfigured(t *testing.T) {
	projects := firstProject(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p, err := NewPolicies(ctx, Creds{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Create(ctx, projects[0], json.RawMessage(`{"displayName":"cloud0-test-never"}`)); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}
