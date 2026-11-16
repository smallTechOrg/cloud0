package refresh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/billing"
	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/logging"
	"github.com/smallTechOrg/cloud0/internal/store"
	"github.com/smallTechOrg/cloud0/internal/testenv"
	"google.golang.org/api/googleapi"
)

// fakeTable is the placeholder table id from .env.example — never a real one.
const fakeTable = "my-project.my_dataset.my_table"

// testNow is a Monday inside 2026-W44 (so W43 is settling with a 48 h window
// and W42 is final).
var testNow = time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)

// fakeSource is a thin billing stand-in: it builds the real query text for
// provenance, returns the rows it was given that fall inside the window, or
// fails with a fixed error. When block is set, Fetch waits until it is closed.
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
	res.Stats.BytesProcessed, res.Stats.BytesBilled, res.Stats.RowCount = 1234, 10485760, len(res.Rows)
	return res, nil
}

func row(week isoweek.Week, project, service, sku string, resource *string, gross, credits float64) billing.Row {
	return billing.Row{ISOYear: week.Year, ISOWeek: week.Week, ProjectID: project, ServiceID: "svc-" + service, Service: service,
		SKUID: "sku-" + sku, SKU: sku, ResourceName: resource, Gross: gross, Credits: credits, Net: gross + credits, LineItems: 3}
}

func strp(s string) *string { return &s }

type harness struct {
	runner *Runner
	store  *store.Store
	logs   *bytes.Buffer
}

func newHarness(t *testing.T, src Source, now time.Time) *harness {
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
	logs := &bytes.Buffer{}
	r := &Runner{
		Store:      st,
		Source:     src,
		Logger:     logging.NewWriter(logs, "info"),
		FirstWeek:  isoweek.MustParse("2026-W40"),
		Settle:     48 * time.Hour,
		MarginDays: 1,
		Now:        func() time.Time { return now },
	}
	return &harness{runner: r, store: st, logs: logs}
}

func (h *harness) logLines(t *testing.T, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(h.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func TestRun_WeekMode_WritesSourceSnapshotAndFinishesOK(t *testing.T) {
	week := isoweek.MustParse("2026-W42")
	src := &fakeSource{rows: []billing.Row{
		row(week, "project-a", "Compute Engine", "N1 Core", strp("projects/1/instances/vm-a"), 100, 0),
		row(week, "project-a", "Compute Engine", "N1 Core", strp("projects/1/instances/vm-b"), 50, -10),
		row(week, "project-b", "Cloud SQL", "DB Hours", nil, 20, 0),
		row(isoweek.MustParse("2026-W43"), "project-a", "Cloud Run", "CPU", nil, 999, 0), // outside the window: ignored
	}}
	h := newHarness(t, src, testNow)

	res, err := h.runner.Run(context.Background(), ModeWeek, week)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	run := res.Run
	if run.Status != store.RunOK || run.Trigger != store.TriggerRefreshWeek || run.From != "2026-W42" || run.To != "2026-W42" {
		t.Fatalf("run = %+v", run)
	}
	if run.FinishedAt == nil || run.DurationMs == nil || run.Error != nil {
		t.Fatalf("run not finished cleanly: %+v", run)
	}
	if len(run.Sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(run.Sources))
	}
	s := run.Sources[0]
	if s.Status != store.SourceOK || s.Source != billing.SourceName || s.JobID == nil || *s.JobID != "job-fake-1" || s.RowsReturned == nil || *s.RowsReturned != 3 {
		t.Fatalf("source = %+v", s)
	}
	if s.BytesBilled == nil || *s.BytesBilled != 10485760 || s.CacheHit == nil || *s.CacheHit {
		t.Fatalf("source stats = %+v", s)
	}
	if !strings.Contains(s.SQLText, "DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end") {
		t.Fatalf("sql_text not stored verbatim: %q", s.SQLText)
	}
	var params map[string]string
	if err := json.Unmarshal(s.Params, &params); err != nil || len(params) != 4 {
		t.Fatalf("params = %s (%v)", s.Params, err)
	}
	if params["usage_start"] != "2026-10-12T00:00:00Z" || params["usage_end"] != "2026-10-19T00:00:00Z" || params["part_start"] != "2026-10-11" || params["part_end"] != "2026-10-20" {
		t.Fatalf("params = %v", params)
	}

	if len(res.Snapshots) != 1 || len(run.SnapshotIDs) != 1 {
		t.Fatalf("snapshots = %+v ids = %v", res.Snapshots, run.SnapshotIDs)
	}
	snap := res.Snapshots[0]
	if snap.Week != "2026-W42" || snap.RowCount != 3 || snap.StatusAtFetch != isoweek.StatusFinal || math.Abs(snap.NetTotal-160) > 1e-9 {
		t.Fatalf("snapshot = %+v", snap)
	}
	stored, err := store.GetSnapshot(context.Background(), h.store.DB, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RunID != run.ID || stored.RunSourceID != s.ID || !stored.FetchedAt.Equal(testNow) || stored.GrossTotal != 170 || stored.CreditsTotal != -10 {
		t.Fatalf("stored snapshot = %+v", stored)
	}
	rows, err := store.SnapshotRows(context.Background(), h.store.DB, snap.ID, store.ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[2].ResourceName != nil || rows[0].Net != 100 {
		t.Fatalf("rows = %+v", rows)
	}

	for _, msg := range []string{"refresh.start", "source.done", "refresh.done"} {
		lines := h.logLines(t, msg)
		if len(lines) != 1 {
			t.Fatalf("%s lines = %d, want 1:\n%s", msg, len(lines), h.logs.String())
		}
		if int64(lines[0]["run_id"].(float64)) != run.ID {
			t.Fatalf("%s run_id = %v", msg, lines[0]["run_id"])
		}
	}
	start := h.logLines(t, "refresh.start")[0]
	if start["mode"] != "week" || start["from"] != "2026-W42" || start["to"] != "2026-W42" {
		t.Fatalf("refresh.start = %v", start)
	}
	done := h.logLines(t, "source.done")[0]
	if done["status"] != "ok" || done["job_id"] != "job-fake-1" || done["rows"].(float64) != 3 || done["bytes_billed"].(float64) != 10485760 {
		t.Fatalf("source.done = %v", done)
	}
	fin := h.logLines(t, "refresh.done")[0]
	if fin["status"] != "ok" || fin["snapshots"].(float64) != 1 {
		t.Fatalf("refresh.done = %v", fin)
	}
	if strings.Contains(h.logs.String(), "SELECT") {
		t.Fatal("SQL must never be logged")
	}
}

func TestRun_AllMode_WritesOneSnapshotPerWeekIncludingEmptyWeeks(t *testing.T) {
	src := &fakeSource{rows: []billing.Row{
		row(isoweek.MustParse("2026-W40"), "project-a", "Compute Engine", "N1 Core", nil, 10, 0),
		row(isoweek.MustParse("2026-W42"), "project-a", "Compute Engine", "N1 Core", nil, 30, -5),
		row(isoweek.MustParse("2026-W44"), "project-b", "Cloud Run", "CPU", nil, 1, 0),
	}}
	h := newHarness(t, src, testNow) // current week 2026-W44, first 2026-W40 → 5 weeks

	res, err := h.runner.Run(context.Background(), ModeAll, isoweek.Week{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Run.Trigger != store.TriggerRefreshAll || res.Run.From != "2026-W40" || res.Run.To != "2026-W44" || res.Run.Status != store.RunOK {
		t.Fatalf("run = %+v", res.Run)
	}
	if len(res.Snapshots) != 5 {
		t.Fatalf("snapshots = %d, want 5: %+v", len(res.Snapshots), res.Snapshots)
	}
	want := map[string]struct {
		rows   int
		net    float64
		status isoweek.Status
	}{
		"2026-W40": {1, 10, isoweek.StatusFinal},
		"2026-W41": {0, 0, isoweek.StatusFinal},
		"2026-W42": {1, 25, isoweek.StatusFinal},
		"2026-W43": {0, 0, isoweek.StatusSettling},
		"2026-W44": {1, 1, isoweek.StatusPartial},
	}
	srcID := int64(0)
	for i, s := range res.Snapshots {
		w, ok := want[s.Week]
		if !ok || s.RowCount != w.rows || math.Abs(s.NetTotal-w.net) > 1e-9 || s.StatusAtFetch != w.status {
			t.Errorf("snapshot %d = %+v, want %+v", i, s, w)
		}
		if i > 0 && s.ID <= res.Snapshots[i-1].ID {
			t.Errorf("snapshot ids not ascending: %+v", res.Snapshots)
		}
		stored, _ := store.GetSnapshot(context.Background(), h.store.DB, s.ID)
		if srcID == 0 {
			srcID = stored.RunSourceID
		} else if stored.RunSourceID != srcID {
			t.Errorf("snapshot %s has run_source_id %d, want %d (one source row for all weeks)", s.Week, stored.RunSourceID, srcID)
		}
	}
	if src.calls != 1 {
		t.Fatalf("Fetch calls = %d, want exactly one BigQuery job", src.calls)
	}
	if fin := h.logLines(t, "refresh.done"); len(fin) != 1 || fin[0]["snapshots"].(float64) != 5 {
		t.Fatalf("refresh.done = %v", fin)
	}
}

func TestRun_SourceFailure_RecordsFailedRunWithHintAndNoSnapshot(t *testing.T) {
	src := &fakeSource{err: &googleapi.Error{Code: 404, Message: "Not found: Table x", Errors: []googleapi.ErrorItem{{Reason: "notFound", Message: "Not found: Table x"}}}}
	h := newHarness(t, src, testNow)

	res, err := h.runner.Run(context.Background(), ModeWeek, isoweek.MustParse("2026-W42"))
	if err != nil {
		t.Fatalf("a source failure is not a Run error: %v", err)
	}
	if res.Run.Status != store.RunFailed || res.Run.Error != nil || res.Run.FinishedAt == nil {
		t.Fatalf("run = %+v", res.Run)
	}
	if len(res.Snapshots) != 0 || len(res.Run.SnapshotIDs) != 0 {
		t.Fatalf("no snapshot may be written on failure: %+v", res)
	}
	if len(res.Run.Sources) != 1 {
		t.Fatalf("sources = %+v", res.Run.Sources)
	}
	s := res.Run.Sources[0]
	if s.Status != store.SourceFailed || s.ErrorCode == nil || *s.ErrorCode != string(billing.CodeTableNotFound) {
		t.Fatalf("source = %+v", s)
	}
	if s.FixHint == nil || !strings.Contains(*s.FixHint, "CLOUD0_BILLING_EXPORT_TABLE") || s.ErrorMessage == nil || *s.ErrorMessage == "" {
		t.Fatalf("source error fields = %+v", s)
	}
	if s.JobID == nil || *s.JobID != "job-fake-1" || s.BytesBilled != nil || s.RowsReturned != nil {
		t.Fatalf("failed source stats = %+v", s)
	}
	if !strings.Contains(s.SQLText, "_PARTITIONTIME") {
		t.Fatal("failed source must still carry the SQL it tried to run")
	}
	done := h.logLines(t, "source.done")
	if len(done) != 1 || done[0]["status"] != "failed" || done[0]["error_code"] != "table_not_found" || done[0]["fix_hint"] == nil {
		t.Fatalf("source.done = %v", done)
	}
	if fin := h.logLines(t, "refresh.done"); len(fin) != 1 || fin[0]["status"] != "failed" || fin[0]["snapshots"].(float64) != 0 {
		t.Fatalf("refresh.done = %v", fin)
	}
	runs, _ := store.ListRuns(context.Background(), h.store.DB, 10)
	if len(runs) != 1 || runs[0].Status != store.RunFailed {
		t.Fatalf("stored runs = %+v", runs)
	}
}

func TestRun_NilSourceRecordsCredentialsFailure(t *testing.T) {
	h := newHarness(t, nil, testNow)
	h.runner.SourceErr = errors.New("billing: credentials unavailable: could not find default credentials")

	res, err := h.runner.Run(context.Background(), ModeWeek, isoweek.MustParse("2026-W42"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Run.Status != store.RunFailed || len(res.Run.Sources) != 1 {
		t.Fatalf("run = %+v", res.Run)
	}
	s := res.Run.Sources[0]
	if s.ErrorCode == nil || *s.ErrorCode != string(billing.CodeCredentialsMissing) || s.FixHint == nil || !strings.Contains(*s.FixHint, "GOOGLE_APPLICATION_CREDENTIALS") {
		t.Fatalf("source = %+v", s)
	}
	if string(s.Params) != "{}" {
		t.Fatalf("params without a query must be {}: %s", s.Params)
	}
}

func TestRun_RejectsInvalidModeAndWeeksOutsideFirstToCurrent(t *testing.T) {
	h := newHarness(t, &fakeSource{}, testNow)
	cases := []struct {
		name string
		mode Mode
		week isoweek.Week
		want error
	}{
		{"bogus mode", Mode("monthly"), isoweek.MustParse("2026-W42"), ErrInvalidMode},
		{"empty mode", Mode(""), isoweek.MustParse("2026-W42"), ErrInvalidMode},
		{"nonexistent week", ModeWeek, isoweek.Week{Year: 2027, Week: 53}, ErrInvalidWeek},
		{"zero week", ModeWeek, isoweek.Week{}, ErrInvalidWeek},
		{"after current", ModeWeek, isoweek.MustParse("2026-W45"), ErrWeekOutOfRange},
		{"before first", ModeWeek, isoweek.MustParse("2026-W39"), ErrWeekOutOfRange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.runner.Run(context.Background(), tc.mode, tc.week)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	runs, _ := store.ListRuns(context.Background(), h.store.DB, 10)
	if len(runs) != 0 {
		t.Fatalf("rejected requests must not create runs: %+v", runs)
	}
	// The current (partial) week itself is allowed.
	if _, err := h.runner.Run(context.Background(), ModeWeek, isoweek.MustParse("2026-W44")); err != nil {
		t.Fatalf("current week must be refreshable: %v", err)
	}
}

func TestRun_SecondConcurrentRunReturnsErrBusy(t *testing.T) {
	src := &fakeSource{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	h := newHarness(t, src, testNow)
	week := isoweek.MustParse("2026-W42")

	type outcome struct {
		res Result
		err error
	}
	first := make(chan outcome, 1)
	go func() {
		res, err := h.runner.Run(context.Background(), ModeWeek, week)
		first <- outcome{res, err}
	}()
	select {
	case <-src.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first run never reached the source")
	}

	_, err := h.runner.Run(context.Background(), ModeWeek, week)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("second run err = %v, want ErrBusy", err)
	}
	close(src.block)
	out := <-first
	if out.err != nil || out.res.Run.Status != store.RunOK {
		t.Fatalf("first run = %+v / %v", out.res.Run, out.err)
	}
	// The lock is released: a third run succeeds and creates a second snapshot.
	res, err := h.runner.Run(context.Background(), ModeWeek, week)
	if err != nil || res.Snapshots[0].ID <= out.res.Snapshots[0].ID {
		t.Fatalf("third run = %+v / %v", res, err)
	}
	runs, _ := store.ListRuns(context.Background(), h.store.DB, 10)
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2 (the busy request must not create a run)", len(runs))
	}
}

func TestRun_SmokeWeekAgainstRealBigQuery(t *testing.T) {
	testenv.RequireBigQuery(t)
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		t.Fatalf("config.Load from .env: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	src, err := billing.New(ctx, &cfg)
	if err != nil {
		t.Fatalf("billing.New: %v", err)
	}
	defer src.Close()

	h := newHarness(t, src, time.Now())
	h.runner.FirstWeek, h.runner.Settle, h.runner.MarginDays = cfg.FirstWeek, time.Duration(cfg.SettleHours)*time.Hour, cfg.PartitionMarginDays
	h.runner.Now = func() time.Time { return time.Now().UTC() }

	res, err := h.runner.Run(ctx, ModeWeek, cfg.SmokeWeek)
	if err != nil {
		t.Fatalf("Run(%s): %v", cfg.SmokeWeek, err)
	}
	if res.Run.Status != store.RunOK {
		s := res.Run.Sources
		t.Fatalf("run failed: %+v sources=%+v", res.Run, s)
	}
	if len(res.Snapshots) != 1 || res.Snapshots[0].RowCount <= 100 || res.Snapshots[0].NetTotal <= 0 {
		t.Fatalf("snapshots = %+v", res.Snapshots)
	}
	s := res.Run.Sources[0]
	if s.JobID == nil || *s.JobID == "" || s.BytesBilled == nil || (*s.BytesBilled == 0 && (s.CacheHit == nil || !*s.CacheHit)) {
		t.Fatalf("job statistics missing: %+v", s)
	}
	if !strings.Contains(s.SQLText, "DATE(_PARTITIONTIME) BETWEEN") || !strings.Contains(s.SQLText, "`"+cfg.BillingExportTable+"`") {
		t.Fatal("stored SQL is not the executed text")
	}
	rows, err := store.SnapshotRows(ctx, h.store.DB, res.Snapshots[0].ID, store.ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	var compute float64
	for _, r := range rows {
		if r.Service == "Compute Engine" {
			compute += r.Net
		}
	}
	if compute <= 0 {
		t.Fatalf("Compute Engine net = %f, want > 0", compute)
	}
	t.Logf("smoke week %s: rows=%d net_total=%.2f compute_engine=%.2f job=%s bytes_billed=%d cache_hit=%v",
		cfg.SmokeWeek, res.Snapshots[0].RowCount, res.Snapshots[0].NetTotal, compute, *s.JobID, *s.BytesBilled, s.CacheHit != nil && *s.CacheHit)
}
