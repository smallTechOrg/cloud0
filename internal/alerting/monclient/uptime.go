package monclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/api/iterator"
	"google.golang.org/genproto/googleapis/api/monitoredres"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/smallTechOrg/cloud0/internal/alerting"
)

const (
	usRoleUptimeEditor = "roles/monitoring.uptimeCheckConfigEditor"
	usRoleSnoozeEditor = "roles/monitoring.snoozeEditor"
)

// uptimeNameRE matches projects/<p>/uptimeCheckConfigs/<id>.
var uptimeNameRE = regexp.MustCompile(`^projects/([^/]+)/uptimeCheckConfigs/([^/]+)$`)

// UptimeClient implements alerting.UptimeClient over the Monitoring
// UptimeCheckClient. Reads use the reader credentials, writes the alerts
// credentials (nil writer => alerting.ErrNotConfigured).
type UptimeClient struct {
	reader *monitoring.UptimeCheckClient
	writer *monitoring.UptimeCheckClient
}

var _ alerting.UptimeClient = (*UptimeClient)(nil)

// NewUptimeClient builds the uptime client from the shared credentials.
func NewUptimeClient(ctx context.Context, c Creds) (*UptimeClient, error) {
	r, err := monitoring.NewUptimeCheckClient(ctx, c.Reader...)
	if err != nil {
		return nil, fmt.Errorf("%w: uptime reader client: %v", alerting.ErrUpstream, err)
	}
	u := &UptimeClient{reader: r}
	if len(c.Writer) > 0 {
		w, err := monitoring.NewUptimeCheckClient(ctx, c.Writer...)
		if err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("%w: uptime writer client: %v", alerting.ErrUpstream, err)
		}
		u.writer = w
	}
	return u, nil
}

// Close releases both connections.
func (u *UptimeClient) Close() error {
	var errs []error
	if u.reader != nil {
		errs = append(errs, u.reader.Close())
	}
	if u.writer != nil {
		errs = append(errs, u.writer.Close())
	}
	return errors.Join(errs...)
}

// List returns the project's uptime checks ordered by display name.
func (u *UptimeClient) List(ctx context.Context, project string) ([]alerting.UptimeCheck, error) {
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("%w: project is required", alerting.ErrInvalidParams)
	}
	out := []alerting.UptimeCheck{}
	it := u.reader.ListUptimeCheckConfigs(ctx, &monitoringpb.ListUptimeCheckConfigsRequest{Parent: "projects/" + project})
	for {
		cfg, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, mapErr(err, false, "", project)
		}
		out = append(out, uptimeView(cfg))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out, nil
}

// Get returns one uptime check.
func (u *UptimeClient) Get(ctx context.Context, name string) (alerting.UptimeCheck, error) {
	proj, err := parseUptimeName(name)
	if err != nil {
		return alerting.UptimeCheck{}, err
	}
	cfg, err := u.reader.GetUptimeCheckConfig(ctx, &monitoringpb.GetUptimeCheckConfigRequest{Name: name})
	if err != nil {
		return alerting.UptimeCheck{}, mapErr(err, false, "", proj)
	}
	return uptimeView(cfg), nil
}

// Create validates the draft and creates a cloud0-managed HTTP(S) check.
func (u *UptimeClient) Create(ctx context.Context, d alerting.UptimeDraft) (alerting.UptimeCheck, error) {
	d, err := ValidateUptimeDraft(d)
	if err != nil {
		return alerting.UptimeCheck{}, err
	}
	if u.writer == nil {
		return alerting.UptimeCheck{}, alerting.ErrNotConfigured
	}
	cfg, err := u.writer.CreateUptimeCheckConfig(ctx, &monitoringpb.CreateUptimeCheckConfigRequest{
		Parent:            "projects/" + d.Project,
		UptimeCheckConfig: uptimeConfig(d, nil),
	})
	if err != nil {
		return alerting.UptimeCheck{}, mapErr(err, true, usRoleUptimeEditor, d.Project)
	}
	return uptimeView(cfg), nil
}

// Update replaces the editable fields of an existing check with the draft.
// The draft's Project must match the check's project (or be empty).
func (u *UptimeClient) Update(ctx context.Context, name string, d alerting.UptimeDraft) (alerting.UptimeCheck, error) {
	proj, err := parseUptimeName(name)
	if err != nil {
		return alerting.UptimeCheck{}, err
	}
	if d.Project == "" {
		d.Project = proj
	}
	if d.Project != proj {
		return alerting.UptimeCheck{}, fmt.Errorf("%w: project %q does not match %q", alerting.ErrInvalidParams, d.Project, proj)
	}
	d, err = ValidateUptimeDraft(d)
	if err != nil {
		return alerting.UptimeCheck{}, err
	}
	if u.writer == nil {
		return alerting.UptimeCheck{}, alerting.ErrNotConfigured
	}
	cur, err := u.writer.GetUptimeCheckConfig(ctx, &monitoringpb.GetUptimeCheckConfigRequest{Name: name})
	if err != nil {
		return alerting.UptimeCheck{}, mapErr(err, true, usRoleUptimeEditor, proj)
	}
	if h := cur.GetMonitoredResource().GetLabels()["host"]; h != "" && !strings.EqualFold(h, d.Host) {
		return alerting.UptimeCheck{}, fmt.Errorf("%w: host of an existing uptime check cannot be changed (create a new check instead)", alerting.ErrInvalidParams)
	}
	next := uptimeConfig(d, cur)
	next.Name = name
	got, err := u.writer.UpdateUptimeCheckConfig(ctx, &monitoringpb.UpdateUptimeCheckConfigRequest{
		UptimeCheckConfig: next,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{
			"display_name", "http_check", "period", "timeout", "content_matchers",
		}},
	})
	if err != nil {
		return alerting.UptimeCheck{}, mapErr(err, true, usRoleUptimeEditor, proj)
	}
	return uptimeView(got), nil
}

// Delete removes an uptime check.
func (u *UptimeClient) Delete(ctx context.Context, name string) error {
	proj, err := parseUptimeName(name)
	if err != nil {
		return err
	}
	if u.writer == nil {
		return alerting.ErrNotConfigured
	}
	if err := u.writer.DeleteUptimeCheckConfig(ctx, &monitoringpb.DeleteUptimeCheckConfigRequest{Name: name}); err != nil {
		return mapErr(err, true, usRoleUptimeEditor, proj)
	}
	return nil
}

func parseUptimeName(name string) (project string, err error) {
	m := uptimeNameRE.FindStringSubmatch(name)
	if m == nil {
		return "", fmt.Errorf("%w: uptime check name must look like projects/<project>/uptimeCheckConfigs/<id>", alerting.ErrInvalidParams)
	}
	return m[1], nil
}

// ---- Draft validation ------------------------------------------------------

var (
	usHostLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// usBlockedSuffixes are internal-only DNS zones.
	usBlockedSuffixes = []string{".local", ".localhost", ".internal", ".localdomain", ".lan", ".home", ".corp", ".intranet", ".private", ".cluster.local"}
	usValidPeriods    = map[int]bool{60: true, 300: true, 600: true, 900: true}
)

// ValidateUptimeDraft normalises (defaults path "/", port 443/80, timeout 10 s)
// and validates a draft: public hostnames only. Errors wrap ErrInvalidParams.
func ValidateUptimeDraft(d alerting.UptimeDraft) (alerting.UptimeDraft, error) {
	bad := func(format string, a ...any) (alerting.UptimeDraft, error) {
		return d, fmt.Errorf("%w: %s", alerting.ErrInvalidParams, fmt.Sprintf(format, a...))
	}
	d.Project = strings.TrimSpace(d.Project)
	d.DisplayName = strings.TrimSpace(d.DisplayName)
	if d.Project == "" {
		return bad("project is required")
	}
	if d.DisplayName == "" || len(d.DisplayName) > 100 {
		return bad("display_name is required (max 100 characters)")
	}
	host, err := validatePublicHost(d.Host)
	if err != nil {
		return d, err
	}
	d.Host = host
	if d.Path == "" {
		d.Path = "/"
	}
	if !strings.HasPrefix(d.Path, "/") || strings.ContainsAny(d.Path, " \t\r\n\x00") || len(d.Path) > 1000 {
		return bad("path must start with / and contain no whitespace")
	}
	if d.Port == 0 {
		if d.UseSSL {
			d.Port = 443
		} else {
			d.Port = 80
		}
	}
	if d.Port < 1 || d.Port > 65535 {
		return bad("port must be 1-65535")
	}
	if d.PeriodSeconds == 0 {
		d.PeriodSeconds = 300
	}
	if !usValidPeriods[d.PeriodSeconds] {
		return bad("period_seconds must be one of 60, 300, 600, 900")
	}
	if d.TimeoutSeconds == 0 {
		d.TimeoutSeconds = 10
	}
	if d.TimeoutSeconds < 1 || d.TimeoutSeconds > 60 {
		return bad("timeout_seconds must be 1-60")
	}
	if len(d.ContentMatch) > 1024 {
		return bad("content_match is too long (max 1024)")
	}
	return d, nil
}

// validatePublicHost accepts a bare public hostname (or public IP literal) and
// rejects schemes, ports, paths, localhost, private/loopback/link-local/
// metadata addresses and internal-only zones (SSRF guard).
func validatePublicHost(raw string) (string, error) {
	bad := func(format string, a ...any) (string, error) {
		return "", fmt.Errorf("%w: host: %s", alerting.ErrInvalidParams, fmt.Sprintf(format, a...))
	}
	h := strings.ToLower(strings.TrimSpace(raw))
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return bad("host is required")
	}
	if strings.ContainsAny(h, "/:?#@ \t\r\n\\%") && net.ParseIP(h) == nil {
		return bad("must be a bare hostname without scheme, port, path or credentials")
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		ip = ip.Unmap()
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
			ip.IsUnspecified() || ip.IsMulticast() || usIsSpecialIP(ip) {
			return bad("%s is not a public address", ip)
		}
		return ip.String(), nil
	}
	if h == "localhost" || h == "metadata" || strings.HasPrefix(h, "metadata.") {
		return bad("%q is not a public hostname", h)
	}
	for _, s := range usBlockedSuffixes {
		if strings.HasSuffix(h, s) {
			return bad("%q is an internal-only zone", h)
		}
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 || len(h) > 253 {
		return bad("%q is not a fully qualified public hostname", h)
	}
	for _, l := range labels {
		if !usHostLabelRE.MatchString(l) {
			return bad("%q is not a valid hostname", h)
		}
	}
	// A purely numeric/hex TLD means an obfuscated IP (2130706433, 0x7f.1, 127.1).
	tld := labels[len(labels)-1]
	if regexp.MustCompile(`^(0x[0-9a-f]*|[0-9]+)$`).MatchString(tld) {
		return bad("%q looks like a numeric IP, not a hostname", h)
	}
	return h, nil
}

var usSpecialPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"100.64.0.0/10",   // CGNAT
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // reserved
		"169.254.0.0/16",  // link-local incl. metadata 169.254.169.254
		"fc00::/7",        // ULA
		"fe80::/10",       // link-local
		"64:ff9b::/96",    // NAT64
		"2001:db8::/32",   // documentation
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

func usIsSpecialIP(ip netip.Addr) bool {
	for _, p := range usSpecialPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// ---- Conversion ------------------------------------------------------------

// uptimeConfig builds the proto for a validated draft. base (an existing
// config) keeps labels, regions and other fields cloud0 does not edit.
func uptimeConfig(d alerting.UptimeDraft, base *monitoringpb.UptimeCheckConfig) *monitoringpb.UptimeCheckConfig {
	cfg := &monitoringpb.UptimeCheckConfig{}
	if base != nil {
		cfg.Name = base.GetName()
		cfg.UserLabels = base.GetUserLabels()
		cfg.SelectedRegions = base.GetSelectedRegions()
		cfg.CheckerType = base.GetCheckerType()
	}
	if cfg.UserLabels == nil {
		cfg.UserLabels = map[string]string{}
	}
	if base == nil {
		cfg.UserLabels[alerting.LabelManagedBy] = alerting.LabelManagedByVal
	}
	cfg.DisplayName = d.DisplayName
	if base != nil && base.GetMonitoredResource() != nil {
		cfg.Resource = base.Resource // the resource of a config is immutable
	} else {
		cfg.Resource = &monitoringpb.UptimeCheckConfig_MonitoredResource{
			MonitoredResource: &monitoredres.MonitoredResource{
				Type:   "uptime_url",
				Labels: map[string]string{"project_id": d.Project, "host": d.Host},
			},
		}
	}
	hc := &monitoringpb.UptimeCheckConfig_HttpCheck{
		RequestMethod: monitoringpb.UptimeCheckConfig_HttpCheck_GET,
		UseSsl:        d.UseSSL,
		Path:          d.Path,
		Port:          int32(d.Port),
		ValidateSsl:   d.UseSSL,
	}
	if base != nil && base.GetHttpCheck() != nil {
		// keep headers/auth/content type cloud0 does not expose
		old := base.GetHttpCheck()
		hc.Headers, hc.MaskHeaders, hc.AuthInfo = old.GetHeaders(), old.GetMaskHeaders(), old.GetAuthInfo()
		hc.ContentType, hc.CustomContentType = old.GetContentType(), old.GetCustomContentType()
	}
	cfg.CheckRequestType = &monitoringpb.UptimeCheckConfig_HttpCheck_{HttpCheck: hc}
	cfg.Period = durationpb.New(time.Duration(d.PeriodSeconds) * time.Second)
	cfg.Timeout = durationpb.New(time.Duration(d.TimeoutSeconds) * time.Second)
	cfg.ContentMatchers = nil
	if d.ContentMatch != "" {
		cfg.ContentMatchers = []*monitoringpb.UptimeCheckConfig_ContentMatcher{{
			Content: d.ContentMatch,
			Matcher: monitoringpb.UptimeCheckConfig_ContentMatcher_CONTAINS_STRING,
		}}
	}
	return cfg
}

// uptimeView converts the proto to the UI view. PoliciesUsing is not part of the
// frozen type; the engine derives it from policies.
func uptimeView(cfg *monitoringpb.UptimeCheckConfig) alerting.UptimeCheck {
	v := alerting.UptimeCheck{Name: cfg.GetName(), DisplayName: cfg.GetDisplayName()}
	if m := uptimeNameRE.FindStringSubmatch(cfg.GetName()); m != nil {
		v.Project = m[1]
	}
	if mr := cfg.GetMonitoredResource(); mr != nil {
		v.Host = mr.GetLabels()["host"]
		if v.Project == "" {
			v.Project = mr.GetLabels()["project_id"]
		}
	}
	if hc := cfg.GetHttpCheck(); hc != nil {
		v.Path, v.UseSSL, v.Port = hc.GetPath(), hc.GetUseSsl(), int(hc.GetPort())
		if v.Port == 0 {
			v.Port = 80
			if v.UseSSL {
				v.Port = 443
			}
		}
	}
	if p := cfg.GetPeriod(); p != nil {
		v.PeriodSeconds = int(p.AsDuration() / time.Second)
	}
	if t := cfg.GetTimeout(); t != nil {
		v.TimeoutSeconds = int(t.AsDuration() / time.Second)
	}
	if cm := cfg.GetContentMatchers(); len(cm) > 0 {
		v.ContentMatch = cm[0].GetContent()
	}
	v.Managed = cfg.GetUserLabels()[alerting.LabelManagedBy] == alerting.LabelManagedByVal
	if raw, err := protojson.Marshal(cfg); err == nil {
		v.RawJSON = json.RawMessage(raw)
	}
	return v
}
