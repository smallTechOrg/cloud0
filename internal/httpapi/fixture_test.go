package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/billing"
	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/logging"
	"github.com/smallTechOrg/cloud0/internal/refresh"
	"github.com/smallTechOrg/cloud0/internal/store"
	"github.com/smallTechOrg/cloud0/internal/testenv"
	"github.com/smallTechOrg/cloud0/internal/webui"
)

// fakeTable is the placeholder id from .env.example — never a real table.
const fakeTable = "my-project.my_dataset.my_table"

// phase1Now is a Monday in 2026-W44: W42 is final, W43 settling, W44 partial.
var phase1Now = time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)

// fakeSource stands in for BigQuery in handler tests: it builds the real
// query text for provenance and returns the configured rows inside the
// window, or fails with err. When block is set, Fetch waits until it closes.
type fakeSource struct {
	mu      sync.Mutex
	rows    []billing.Row
	err     error
	block   chan struct{}
	entered chan struct{}
	calls   int
}

func (f *fakeSource) Fetch(_ context.Context, window billing.Window) (billing.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.block != nil {
		<-f.block
	}
	q, err := billing.BuildQuery(window, fakeTable)
	if err != nil {
		return billing.Result{}, err
	}
	res := billing.Result{Query: q, Stats: billing.JobStats{JobID: "job-fake-1"}}
	if f.err != nil {
		return res, f.err
	}
	for _, r := range f.rows {
		w := r.Week()
		if isoweek.Compare(w, window.From) >= 0 && isoweek.Compare(w, window.To) <= 0 {
			res.Rows = append(res.Rows, r)
		}
	}
	res.Stats.BytesProcessed, res.Stats.BytesBilled, res.Stats.RowCount = 4321, 20971520, len(res.Rows)
	return res, nil
}

func fakeRow(week string, project, service, sku string, resource *string, gross, credits float64) billing.Row {
	w := isoweek.MustParse(week)
	return billing.Row{ISOYear: w.Year, ISOWeek: w.Week, ProjectID: project, ServiceID: "svc-" + service, Service: service,
		SKUID: "sku-" + sku, SKU: sku, ResourceName: resource, Gross: gross, Credits: credits, Net: gross + credits, LineItems: 2}
}

func strp(s string) *string { return &s }

// phase1Rows is the shared fake dataset: two projects, two weeks, a credited
// service, an unattributed resource and a service the sheet has no column for.
var phase1Rows = []billing.Row{
	fakeRow("2026-W41", "project-a", "Compute Engine", "N1 Core", strp("projects/1/instances/vm-a"), 100, 0),
	fakeRow("2026-W41", "project-b", "Cloud SQL", "DB Hours", strp("projects/2/instances/db"), 40, 0),
	fakeRow("2026-W42", "project-a", "Compute Engine", "N1 Core", strp("projects/1/instances/vm-a"), 150, 0),
	fakeRow("2026-W42", "project-a", "Compute Engine", "N1 Core", strp("projects/1/instances/vm-b"), 30, -10),
	fakeRow("2026-W42", "project-a", "Compute Engine", "PD Capacity", nil, 20, 0),
	fakeRow("2026-W42", "project-b", "Cloud SQL", "DB Hours", strp("projects/2/instances/db"), 60, 0),
	fakeRow("2026-W42", "project-b", "Networking", "LB Rule", nil, 90, -30),
	fakeRow("2026-W42", "project-a", "Cloud Monitoring", "Metrics", nil, 5, 0),
}

// newPhase1Fixture builds the handler fixture with a refresh runner on the
// given source and a controllable clock (*now is read on every call).
func newPhase1Fixture(t *testing.T, src refresh.Source, now *time.Time) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, testenv.TempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	return newFixtureOn(t, st, cfg, src, now)
}

// newFixtureOn wires a handler over an already-open store (used to reopen the
// same DB file for state-survival tests).
func newFixtureOn(t *testing.T, st *store.Store, cfg config.Config, src refresh.Source, now *time.Time) *fixture {
	t.Helper()
	logs := &bytes.Buffer{}
	logger := logging.NewWriter(logs, "info")
	clock := func() time.Time { return *now }
	runner := &refresh.Runner{Store: st, Source: src, Logger: logger, FirstWeek: cfg.FirstWeek,
		Settle: time.Duration(cfg.SettleHours) * time.Hour, MarginDays: cfg.PartitionMarginDays, Now: clock}
	deps := Deps{
		Config:  cfg,
		Store:   st,
		Logger:  logger,
		Version: "test",
		Now:     clock,
		UI:      http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `<body data-cloud0-ui="real">`) }),
		UIMode:  webui.ModeReal,
		Refresh: runner,
	}
	srv := httptest.NewServer(NewHandler(deps))
	t.Cleanup(srv.Close)
	return &fixture{srv: srv, store: st, logs: logs, deps: deps}
}

// newLiveFixture is the real thing: config from .env, the real BigQuery
// source, the startup dry-run feeding /health, and the wall clock.
func newLiveFixture(t *testing.T) (*fixture, config.Config) {
	t.Helper()
	testenv.RequireBigQuery(t)
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		t.Fatalf("config.Load from .env: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	src, err := billing.New(ctx, &cfg)
	if err != nil {
		t.Fatalf("billing.New: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	dry, dryErr := src.DryRun(ctx)
	if dryErr != nil {
		f := billing.Classify(dryErr)
		t.Fatalf("startup dry-run failed: %v [code=%s hint=%s]", dryErr, f.Code, f.Hint)
	}
	t.Logf("dry-run ok: week=%s bytes_processed=%d", dry.Week, dry.BytesProcessed)

	st, err := store.Open(ctx, testenv.TempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	f := newFixtureOn(t, st, cfg, src, &now)
	health := NewBigQueryHealth(time.Now(), dryErr)
	f.deps.BigQuery = func() *BigQueryHealth { h := health; return &h }
	f.deps.Now = nil // wall clock for the live journey
	f.srv.Close()
	f.srv = httptest.NewServer(NewHandler(f.deps))
	t.Cleanup(f.srv.Close)
	return f, cfg
}

// doJSON sends an optional JSON body and decodes a JSON response (nil map when
// the body is empty or not JSON). raw is the exact body for key-presence checks.
func (f *fixture) doJSON(t *testing.T, method, path, body string) (*http.Response, map[string]any, []byte) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	if len(raw) > 0 && strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s %s: body is not a JSON object: %q", method, path, raw)
		}
	}
	return res, m, raw
}

// expectError asserts the envelope shape and code for an error response.
func expectError(t *testing.T, res *http.Response, m map[string]any, status int, code string) map[string]any {
	t.Helper()
	if res.StatusCode != status {
		t.Fatalf("%s: status %d, want %d: %v", res.Request.URL.Path, res.StatusCode, status, m)
	}
	e, _ := m["error"].(map[string]any)
	if e == nil || e["code"] != code || e["message"] == "" {
		t.Fatalf("%s: envelope %v, want code %s", res.Request.URL.Path, m, code)
	}
	if _, ok := e["hint"]; !ok {
		t.Fatalf("hint key must be present (string or null): %v", e)
	}
	return e
}

// refreshWeek runs one refresh through the API and returns the response.
func (f *fixture) refreshWeek(t *testing.T, week string) map[string]any {
	t.Helper()
	res, m, _ := f.doJSON(t, http.MethodPost, "/api/v1/refresh", `{"mode":"week","week":"`+week+`"}`)
	if res.StatusCode != 200 {
		t.Fatalf("refresh %s: status %d: %v", week, res.StatusCode, m)
	}
	return m
}

func rowFor(t *testing.T, table map[string]any, week string) map[string]any {
	t.Helper()
	rows, _ := table["rows"].([]any)
	for _, r := range rows {
		row := r.(map[string]any)
		if row["week"] == week {
			return row
		}
	}
	t.Fatalf("no row for %s in %d rows", week, len(rows))
	return nil
}

func cellsOf(t *testing.T, row map[string]any) map[string]map[string]any {
	t.Helper()
	cells, ok := row["cells"].(map[string]any)
	if !ok {
		t.Fatalf("row %v has no cells object", row["week"])
	}
	out := make(map[string]map[string]any, len(cells))
	for k, v := range cells {
		out[k] = v.(map[string]any)
	}
	return out
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func (f *fixture) logCount(msg string) int {
	return strings.Count(f.logs.String(), `"msg":"`+msg+`"`)
}
