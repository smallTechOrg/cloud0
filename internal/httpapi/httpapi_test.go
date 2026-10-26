package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/logging"
	"github.com/smallTechOrg/cloud0/internal/store"
	"github.com/smallTechOrg/cloud0/internal/testenv"
	"github.com/smallTechOrg/cloud0/internal/webui"
)

type fixture struct {
	srv   *httptest.Server
	store *store.Store
	logs  *bytes.Buffer
	deps  Deps
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(func(k string) string {
		return map[string]string{
			"CLOUD0_GCP_PROJECTS":         "project-a,project-b",
			"CLOUD0_BILLING_EXPORT_TABLE": "my-project.my_dataset.my_table",
			"CLOUD0_BIGQUERY_PROJECT":     "my-project",
			"CLOUD0_FIRST_WEEK":           "2026-W18",
			"CLOUD0_SETTLE_HOURS":         "48",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newFixture(t *testing.T, migrate bool, now time.Time) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, testenv.TempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if migrate {
		if _, err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	logs := &bytes.Buffer{}
	deps := Deps{
		Config:  testConfig(t),
		Store:   st,
		Logger:  logging.NewWriter(logs, "info"),
		Version: "test",
		Now:     func() time.Time { return now },
		UI:      http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `<body data-cloud0-ui="real">`) }),
		UIMode:  webui.ModeReal,
	}
	srv := httptest.NewServer(NewHandler(deps))
	t.Cleanup(srv.Close)
	return &fixture{srv: srv, store: st, logs: logs, deps: deps}
}

func (f *fixture) do(t *testing.T, method, path string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	var m map[string]any
	if len(body) > 0 && strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("%s %s: body is not JSON: %q", method, path, body)
		}
	}
	return res, m
}

func TestHealth_ReportsOKWithRealUIAndNullBigQuery(t *testing.T) {
	f := newFixture(t, true, time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC))
	res, m := f.do(t, http.MethodGet, "/api/v1/health")
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %v", res.StatusCode, m)
	}
	if m["ok"] != true || m["ui"] != "real" || m["db"] != "ok" || m["migrations"] != "current" || m["version"] != "test" {
		t.Fatalf("health body: %v", m)
	}
	if v, present := m["bigquery"]; !present || v != nil {
		t.Fatalf("bigquery should be present and null in the scaffold: %v", m)
	}
	if _, has := m["error"]; has {
		t.Fatal("success body must not contain an error key")
	}
	if res.Header.Get("X-Request-Id") == "" {
		t.Fatal("missing X-Request-Id")
	}
}

func TestHealth_Returns503UnhealthyWhenMigrationsPending(t *testing.T) {
	f := newFixture(t, false, time.Now())
	res, m := f.do(t, http.MethodGet, "/api/v1/health")
	if res.StatusCode != 503 {
		t.Fatalf("status %d, want 503: %v", res.StatusCode, m)
	}
	e, _ := m["error"].(map[string]any)
	if e["code"] != "unhealthy" || !strings.Contains(e["hint"].(string), "migrate up") {
		t.Fatalf("error envelope: %v", m)
	}
	if !strings.Contains(f.logs.String(), `"level":"ERROR"`) || !strings.Contains(f.logs.String(), `"msg":"request"`) {
		t.Fatalf("5xx should log an ERROR request line: %s", f.logs.String())
	}
}

func TestHealth_Returns503WhenDatabaseClosed(t *testing.T) {
	f := newFixture(t, true, time.Now())
	_ = f.store.Close()
	res, m := f.do(t, http.MethodGet, "/api/v1/health")
	if res.StatusCode != 503 {
		t.Fatalf("status %d, want 503: %v", res.StatusCode, m)
	}
	e, _ := m["error"].(map[string]any)
	if e["code"] != "unhealthy" || e["hint"] == nil {
		t.Fatalf("error envelope: %v", m)
	}
}

func TestHealth_IncludesBigQueryObjectWhenProvided(t *testing.T) {
	f := newFixture(t, true, time.Now())
	checked := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	f.deps.BigQuery = func() *BigQueryHealth { return &BigQueryHealth{Status: "ok", CheckedAt: checked} }
	srv := httptest.NewServer(NewHandler(f.deps))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	bq, _ := m["bigquery"].(map[string]any)
	if bq["status"] != "ok" || bq["checked_at"] != "2026-10-26T09:00:00Z" {
		t.Fatalf("bigquery object: %v", m)
	}
	for _, k := range []string{"error_code", "message", "fix_hint"} {
		if v, ok := bq[k]; !ok || v != nil {
			t.Fatalf("bigquery.%s should be present and null: %v", k, bq)
		}
	}
}

func TestConfig_ReturnsProjectsAndWeeksFromInjectedNow(t *testing.T) {
	f := newFixture(t, true, time.Date(2026, 10, 26, 9, 15, 30, 0, time.UTC)) // Monday of 2026-W44
	res, m := f.do(t, http.MethodGet, "/api/v1/config")
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %v", res.StatusCode, m)
	}
	projects, _ := m["projects"].([]any)
	if len(projects) != 2 || projects[0] != "project-a" || projects[1] != "project-b" {
		t.Fatalf("projects: %v", m["projects"])
	}
	if m["first_week"] != "2026-W18" || m["current_week"] != "2026-W44" || m["default_week"] != "2026-W43" {
		t.Fatalf("weeks: %v", m)
	}
	if m["settle_hours"].(float64) != 48 || m["now"] != "2026-10-26T09:15:30Z" {
		t.Fatalf("settle/now: %v", m)
	}
}

func TestConfig_DefaultWeekNeverPrecedesFirstWeek(t *testing.T) {
	// now inside the first week itself: current == first, default would be first-1.
	f := newFixture(t, true, time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)) // 2026-W18
	_, m := f.do(t, http.MethodGet, "/api/v1/config")
	if m["current_week"] != "2026-W18" || m["default_week"] != "2026-W18" {
		t.Fatalf("default_week clamped wrong: %v", m)
	}
	// Year rollover: first Monday of 2027 → current 2027-W01, default 2026-W53.
	f2 := newFixture(t, true, time.Date(2027, 1, 4, 12, 0, 0, 0, time.UTC))
	_, m2 := f2.do(t, http.MethodGet, "/api/v1/config")
	if m2["current_week"] != "2027-W01" || m2["default_week"] != "2026-W53" {
		t.Fatalf("rollover wrong: %v", m2)
	}
}

func TestRouting_UnknownAPIPathIs404EnvelopeAndWrongMethodIs405(t *testing.T) {
	f := newFixture(t, true, time.Now())
	res, m := f.do(t, http.MethodGet, "/api/v1/nope")
	if res.StatusCode != 404 {
		t.Fatalf("status %d: %v", res.StatusCode, m)
	}
	e, _ := m["error"].(map[string]any)
	if e["code"] != "not_found" || e["message"] == "" {
		t.Fatalf("404 envelope: %v", m)
	}
	if _, ok := e["hint"]; !ok {
		t.Fatalf("hint key must be present (null): %v", e)
	}
	res, m = f.do(t, http.MethodPost, "/api/v1/health")
	if res.StatusCode != 405 {
		t.Fatalf("status %d, want 405: %v", res.StatusCode, m)
	}
	e, _ = m["error"].(map[string]any)
	if e["code"] != "method_not_allowed" || res.Header.Get("Allow") != "GET" {
		t.Fatalf("405 envelope/Allow: %v %q", m, res.Header.Get("Allow"))
	}
	if !strings.Contains(f.logs.String(), `"level":"WARN"`) {
		t.Fatalf("4xx should log WARN: %s", f.logs.String())
	}
}

func TestRouting_NonAPIPathsGoToTheUI(t *testing.T) {
	f := newFixture(t, true, time.Now())
	for _, p := range []string{"/", "/anything/else", "/_next/static/x.css"} {
		res, err := http.Get(f.srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(body), `data-cloud0-ui="real"`) {
			t.Fatalf("%s: status %d body %q", p, res.StatusCode, body)
		}
	}
	// Exactly one request log line per request.
	lines := strings.Count(strings.TrimSpace(f.logs.String()), "\n") + 1
	if lines != 3 {
		t.Fatalf("expected 3 log lines, got %d:\n%s", lines, f.logs.String())
	}
}

func TestEnvelope_PanicBecomes500WithoutStackTrace(t *testing.T) {
	logs := &bytes.Buffer{}
	logger := logging.NewWriter(logs, "info")
	h := logging.Requests(logger)(wrap(logger, func(http.ResponseWriter, *http.Request) *APIError { panic("kaboom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/x", nil))
	if rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "kaboom") || strings.Contains(body, "goroutine") {
		t.Fatalf("500 body leaks internals: %s", body)
	}
	if !strings.Contains(body, `"code":"internal_error"`) {
		t.Fatalf("500 body: %s", body)
	}
	if !strings.Contains(logs.String(), "kaboom") {
		t.Fatalf("panic cause should be in the server log: %s", logs.String())
	}
}
