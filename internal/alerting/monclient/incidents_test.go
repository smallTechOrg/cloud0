package monclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/alerting"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

const incFakeAlerts = `{"alerts":[
 {"name":"projects/proj-a/alerts/0abc","state":"OPEN","openTime":"2026-10-27T07:40:00Z",
  "resource":{"type":"gce_instance","labels":{"instance_id":"123","project_id":"proj-a"}},
  "metadata":{"systemLabels":{"name":"vm-a"}},
  "metric":{"type":"compute.googleapis.com/instance/cpu/utilization"},
  "policy":{"name":"projects/proj-a/alertPolicies/333","displayName":"cloud0: VM CPU high","severity":"WARNING"}},
 {"name":"projects/proj-a/alerts/0def","state":"CLOSED","openTime":"2026-10-20T07:40:00Z","closeTime":"2026-10-20T08:00:00Z",
  "resource":{"type":"cloud_run_revision","labels":{"service_name":"svc-a"}},
  "policy":{"name":"projects/proj-a/alertPolicies/444","displayName":"Run 5xx","severity":"error"}}
],"nextPageToken":""}`

func TestIncidentsListConvertsAndFilters(t *testing.T) {
	var gotPath, gotFilter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotFilter = r.URL.Query().Get("filter")
		_, _ = w.Write([]byte(incFakeAlerts))
	}))
	defer srv.Close()
	c := newIncidentsWith(srv.Client(), srv.URL)

	all, err := c.List(context.Background(), "proj-a", alerting.IncidentFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/projects/proj-a/alerts" || gotFilter != "" {
		t.Fatalf("path %q filter %q", gotPath, gotFilter)
	}
	if len(all) != 2 || all[0].ID != "0abc" {
		t.Fatalf("incidents: %+v", all)
	}
	a := all[0]
	if a.State != "open" || a.ClosedAt != nil || a.ResourceName != "vm-a" || a.ResourceType != "gce_instance" ||
		a.Severity != "WARNING" || a.PolicyDisplayName != "cloud0: VM CPU high" ||
		a.URL != "https://console.cloud.google.com/monitoring/alerting/alerts/0abc?project=proj-a" {
		t.Fatalf("bad conversion: %+v", a)
	}
	b := all[1]
	if b.State != "closed" || b.ClosedAt == nil || b.ResourceName != "svc-a" || b.Severity != "ERROR" {
		t.Fatalf("bad closed conversion: %+v", b)
	}

	open, err := c.List(context.Background(), "proj-a", alerting.IncidentFilter{State: "open"})
	if err != nil || len(open) != 1 || !strings.Contains(gotFilter, `state="OPEN"`) {
		t.Fatalf("open: %v %+v filter %q", err, open, gotFilter)
	}
	from := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	wk, err := c.List(context.Background(), "proj-a", alerting.IncidentFilter{From: from, To: to})
	if err != nil || len(wk) != 1 || wk[0].ID != "0def" || !strings.Contains(gotFilter, "open_time") || strings.Contains(gotFilter, `open_time<"`) {
		t.Fatalf("window: %v %+v filter %q", err, wk, gotFilter)
	}
}

func TestIncidentsListErrors(t *testing.T) {
	status := http.StatusForbidden
	body := `{"error":{"message":"Permission denied","status":"PERMISSION_DENIED"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := newIncidentsWith(srv.Client(), srv.URL)
	if _, err := c.List(context.Background(), "proj-a", alerting.IncidentFilter{}); err == nil || !incIsCode(err, 403) {
		t.Fatalf("want permission_denied, got %v", err)
	}
	status = http.StatusInternalServerError
	if _, err := c.List(context.Background(), "proj-a", alerting.IncidentFilter{}); err == nil || !incIsCode(err, 502) {
		t.Fatalf("want upstream, got %v", err)
	}
}

func incIsCode(err error, want int) bool {
	s, _ := alerting.CodeFor(err)
	return s == want
}

func TestIncidentsRealListAllProjects(t *testing.T) {
	testenv.LoadDotEnv(t)
	projects := incRealProjects(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := NewIncidentClient(ctx, Creds{})
	if err != nil {
		t.Fatalf("NewIncidentClient: %v", err)
	}
	for _, p := range projects {
		open, err := c.List(ctx, p, alerting.IncidentFilter{State: "open"})
		if err != nil {
			t.Fatalf("open incidents for project #%d: %v", incIndexOf(projects, p), err)
		}
		recent, err := c.List(ctx, p, alerting.IncidentFilter{From: time.Now().Add(-14 * 24 * time.Hour)})
		if err != nil {
			t.Fatalf("recent incidents: %v", err)
		}
		for _, i := range recent {
			if i.OpenedAt.IsZero() || i.URL == "" || i.State == "" {
				t.Fatalf("incomplete incident: %+v", i)
			}
		}
		t.Logf("project #%d: %d open, %d opened in last 14d", incIndexOf(projects, p), len(open), len(recent))
	}
}

func incRealProjects(t *testing.T) []string {
	t.Helper()
	raw := os.Getenv("CLOUD0_GCP_PROJECTS")
	if raw == "" {
		t.Skip("CLOUD0_GCP_PROJECTS not set — fill .env to run real GCP tests")
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func incIndexOf(l []string, s string) int {
	for i, v := range l {
		if v == s {
			return i
		}
	}
	return -1
}
