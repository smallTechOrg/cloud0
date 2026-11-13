package monclient

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/option"
	"google.golang.org/genproto/googleapis/api/monitoredres"
	"google.golang.org/protobuf/types/known/durationpb"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"

	"github.com/smallTechOrg/cloud0/internal/alerting"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

func goodDraft() alerting.UptimeDraft {
	return alerting.UptimeDraft{
		Project: "proj-a", DisplayName: "cloud0-test-x", Host: "example.com",
		Path: "/health", UseSSL: true, PeriodSeconds: 60,
	}
}

func TestValidateUptimeDraftDefaults(t *testing.T) {
	d, err := ValidateUptimeDraft(alerting.UptimeDraft{Project: "proj-a", DisplayName: " n ", Host: " Example.COM. ", UseSSL: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.Host != "example.com" || d.Path != "/" || d.Port != 443 || d.PeriodSeconds != 300 || d.TimeoutSeconds != 10 || d.DisplayName != "n" {
		t.Fatalf("defaults wrong: %+v", d)
	}
	d, err = ValidateUptimeDraft(alerting.UptimeDraft{Project: "p", DisplayName: "n", Host: "93.184.216.34", PeriodSeconds: 900})
	if err != nil || d.Port != 80 || d.Host != "93.184.216.34" {
		t.Fatalf("public ip: %+v %v", d, err)
	}
}

func TestValidateUptimeDraftRejectsSSRFHosts(t *testing.T) {
	hosts := []string{
		"", "localhost", "LOCALHOST", "foo.localhost", "127.0.0.1", "127.1", "0x7f.0.0.1", "2130706433",
		"10.0.0.5", "192.168.1.1", "172.16.0.9", "169.254.169.254", "0.0.0.0", "::1", "[::1]", "fe80::1", "fd00::1",
		"::ffff:127.0.0.1", "100.64.0.1", "224.0.0.1", "metadata.google.internal", "metadata", "db.internal",
		"printer.local", "intranet", "https://example.com", "example.com/path", "example.com:8080",
		"user@example.com", "exa mple.com", "-bad.example.com", "a..b.com",
	}
	for _, h := range hosts {
		d := goodDraft()
		d.Host = h
		if _, err := ValidateUptimeDraft(d); !errors.Is(err, alerting.ErrInvalidParams) {
			t.Errorf("host %q: want ErrInvalidParams, got %v", h, err)
		}
	}
}

func TestValidateUptimeDraftFieldErrors(t *testing.T) {
	cases := map[string]func(*alerting.UptimeDraft){
		"no project":    func(d *alerting.UptimeDraft) { d.Project = "" },
		"no name":       func(d *alerting.UptimeDraft) { d.DisplayName = "  " },
		"path no slash": func(d *alerting.UptimeDraft) { d.Path = "health" },
		"path space":    func(d *alerting.UptimeDraft) { d.Path = "/a b" },
		"period":        func(d *alerting.UptimeDraft) { d.PeriodSeconds = 120 },
		"timeout big":   func(d *alerting.UptimeDraft) { d.TimeoutSeconds = 61 },
		"timeout neg":   func(d *alerting.UptimeDraft) { d.TimeoutSeconds = -1 },
		"port":          func(d *alerting.UptimeDraft) { d.Port = 70000 },
		"match long":    func(d *alerting.UptimeDraft) { d.ContentMatch = strings.Repeat("x", 2000) },
	}
	for name, mut := range cases {
		d := goodDraft()
		mut(&d)
		if _, err := ValidateUptimeDraft(d); !errors.Is(err, alerting.ErrInvalidParams) {
			t.Errorf("%s: want ErrInvalidParams, got %v", name, err)
		}
	}
}

func TestUptimeViewConversion(t *testing.T) {
	d, _ := ValidateUptimeDraft(func() alerting.UptimeDraft { d := goodDraft(); d.ContentMatch = "ok"; return d }())
	cfg := uptimeConfig(d, nil)
	cfg.Name = "projects/proj-a/uptimeCheckConfigs/abc"
	v := uptimeView(cfg)
	if v.Name != cfg.Name || v.Project != "proj-a" || v.Host != "example.com" || v.Path != "/health" || v.Port != 443 ||
		!v.UseSSL || v.PeriodSeconds != 60 || v.TimeoutSeconds != 10 || v.ContentMatch != "ok" || !v.Managed {
		t.Fatalf("view: %+v", v)
	}
	if len(v.RawJSON) == 0 || !strings.Contains(string(v.RawJSON), "example.com") {
		t.Fatalf("raw json missing: %s", v.RawJSON)
	}
	// Unmanaged check with no explicit port.
	cfg2 := &monitoringpb.UptimeCheckConfig{
		Name: "projects/proj-b/uptimeCheckConfigs/z", DisplayName: "api",
		Resource:         &monitoringpb.UptimeCheckConfig_MonitoredResource{MonitoredResource: &monitoredres.MonitoredResource{Labels: map[string]string{"host": "api.example.com"}}},
		CheckRequestType: &monitoringpb.UptimeCheckConfig_HttpCheck_{HttpCheck: &monitoringpb.UptimeCheckConfig_HttpCheck{UseSsl: true, Path: "/h"}},
		Period:           durationpb.New(10 * time.Minute),
	}
	v2 := uptimeView(cfg2)
	if v2.Managed || v2.Port != 443 || v2.PeriodSeconds != 600 || v2.Project != "proj-b" {
		t.Fatalf("view2: %+v", v2)
	}
}

func TestUptimeConfigKeepsLabelsOnUpdate(t *testing.T) {
	base := &monitoringpb.UptimeCheckConfig{Name: "n", UserLabels: map[string]string{"team": "x"}}
	cfg := uptimeConfig(func() alerting.UptimeDraft { d, _ := ValidateUptimeDraft(goodDraft()); return d }(), base)
	if cfg.UserLabels["team"] != "x" || cfg.UserLabels[alerting.LabelManagedBy] != "" {
		t.Fatalf("update must keep labels and not claim an unmanaged check: %v", cfg.UserLabels)
	}
}

func TestUptimeWritesWithoutWriterNotConfigured(t *testing.T) {
	u := &UptimeClient{}
	ctx := context.Background()
	if _, err := u.Create(ctx, goodDraft()); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Fatalf("create: %v", err)
	}
	if _, err := u.Update(ctx, "projects/proj-a/uptimeCheckConfigs/x", goodDraft()); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Fatalf("update: %v", err)
	}
	if err := u.Delete(ctx, "projects/proj-a/uptimeCheckConfigs/x"); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Fatalf("delete: %v", err)
	}
	// Invalid input is reported before the missing-credentials error.
	bad := goodDraft()
	bad.Host = "localhost"
	if _, err := u.Create(ctx, bad); !errors.Is(err, alerting.ErrInvalidParams) {
		t.Fatalf("want invalid, got %v", err)
	}
	if err := u.Delete(ctx, "bogus"); !errors.Is(err, alerting.ErrInvalidParams) {
		t.Fatalf("bad name: %v", err)
	}
}

// ---- real GCP --------------------------------------------------------------

func usProjects(t *testing.T) []string {
	t.Helper()
	testenv.LoadDotEnv(t)
	var out []string
	for _, p := range strings.Split(os.Getenv("CLOUD0_GCP_PROJECTS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		t.Skip("CLOUD0_GCP_PROJECTS not set — fill .env to run real GCP tests")
	}
	return out
}

// usWriterCreds returns creds whose reader and writer both use the alerts key.
func usWriterCreds(t *testing.T) Creds {
	t.Helper()
	testenv.LoadDotEnv(t)
	p := strings.TrimSpace(os.Getenv(EnvAlertsCredentials))
	if p == "" {
		t.Skip("CLOUD0_ALERTS_CREDENTIALS not set — real GCP write tests need the cloud0-alerts key")
	}
	o := []option.ClientOption{option.WithCredentialsFile(p)}
	return Creds{Reader: o, Writer: o}
}

func TestUptimeRealListReadOnly(t *testing.T) {
	projects := usProjects(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	u, err := NewUptimeClient(ctx, Creds{})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	total := 0
	for i, p := range projects {
		checks, err := u.List(ctx, p)
		if err != nil {
			t.Fatalf("list project #%d: %v", i, err)
		}
		for _, c := range checks {
			total++
			if c.Name == "" || c.DisplayName == "" || c.Host == "" || c.Path == "" || c.Port == 0 ||
				c.PeriodSeconds < 60 || c.TimeoutSeconds < 1 || c.Project != p || len(c.RawJSON) == 0 {
				t.Fatalf("incomplete check view: %+v", c)
			}
			got, err := u.Get(ctx, c.Name)
			if err != nil || got.Name != c.Name || got.Host != c.Host {
				t.Fatalf("get %q: %+v %v", c.Name, got, err)
			}
		}
	}
	if total < 1 {
		t.Fatalf("expected at least one pre-existing uptime check, found %d", total)
	}
	if _, err := u.Get(ctx, "projects/"+projects[0]+"/uptimeCheckConfigs/does-not-exist-cloud0"); !errors.Is(err, alerting.ErrNotFound) {
		t.Fatalf("missing check: want ErrNotFound, got %v", err)
	}
}

func TestUptimeRealWriteLifecycle(t *testing.T) {
	project := usProjects(t)[0]
	creds := usWriterCreds(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	u, err := NewUptimeClient(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() }) // registered first => runs after the delete cleanup

	d := alerting.UptimeDraft{
		Project: project, DisplayName: alerting.TestPrefix + "uptime-" + time.Now().UTC().Format("150405"),
		Host: "example.com", Path: "/", UseSSL: true, PeriodSeconds: 300, TimeoutSeconds: 10, ContentMatch: "Example",
	}
	created, err := u.Create(ctx, d)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), time.Minute)
		defer dcancel()
		if err := u.Delete(dctx, created.Name); err != nil && !errors.Is(err, alerting.ErrNotFound) {
			t.Errorf("LEAKED RESOURCE: failed to delete test uptime check %s: %v", created.Name, err)
		}
	})
	if !strings.HasPrefix(created.Name, "projects/"+project+"/uptimeCheckConfigs/") || !created.Managed ||
		created.Host != "example.com" || created.PeriodSeconds != 300 || created.ContentMatch != "Example" || created.Port != 443 {
		t.Fatalf("created view: %+v", created)
	}
	got, err := u.Get(ctx, created.Name)
	if err != nil || got.DisplayName != d.DisplayName {
		t.Fatalf("get: %+v %v", got, err)
	}
	moved := d
	moved.Host = "example.org"
	if _, err := u.Update(ctx, created.Name, moved); !errors.Is(err, alerting.ErrInvalidParams) {
		t.Fatalf("host change must be rejected, got %v", err)
	}
	d.PeriodSeconds = 600
	d.ContentMatch = ""
	upd, err := u.Update(ctx, created.Name, d)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if upd.PeriodSeconds != 600 || upd.ContentMatch != "" || !upd.Managed {
		t.Fatalf("updated view: %+v", upd)
	}
	re, err := u.Get(ctx, created.Name)
	if err != nil || re.PeriodSeconds != 600 {
		t.Fatalf("re-get: %+v %v", re, err)
	}
	if err := u.Delete(ctx, created.Name); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := u.Get(ctx, created.Name); !errors.Is(err, alerting.ErrNotFound) {
		t.Fatalf("after delete want ErrNotFound, got %v", err)
	}
}

func TestUptimeRealRejectsUnsafeHostBeforeCall(t *testing.T) {
	project := usProjects(t)[0]
	creds := usWriterCreds(t)
	ctx := context.Background()
	u, err := NewUptimeClient(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	d := goodDraft()
	d.Project = project
	d.Host = "169.254.169.254"
	if _, err := u.Create(ctx, d); !errors.Is(err, alerting.ErrInvalidParams) {
		t.Fatalf("want ErrInvalidParams, got %v", err)
	}
}
