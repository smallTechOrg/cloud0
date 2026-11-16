package refresh

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/smallTechOrg/cloud0/internal/billing"
	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/monthcost"
	"github.com/smallTechOrg/cloud0/internal/store"
)

func prov(name string) gcpsource.Provenance {
	return gcpsource.Provenance{Source: name, Query: `metric.type="x/` + name + `"`, Aggregation: `{"align":"ALIGN_MEAN"}`,
		Window: gcpsource.Window{Start: testNow.AddDate(0, 0, -10), End: testNow}, FetchedAt: testNow}
}

// metricCollector returns one resource row with one numeric cell.
func metricCollector(name, group, project, resource, col string, v float64, owns ...Owned) Collector {
	return Collector{Name: name, Owns: owns, Fetch: func(_ context.Context, _ gcpsource.Window) gcpsource.Result {
		p := prov(name)
		return gcpsource.Result{Source: name, Status: gcpsource.StatusOK, Provenance: []gcpsource.Provenance{p},
			Metrics: []gcpsource.MetricRow{{Group: group, Project: project, Resource: resource, Cells: map[string]gcpsource.Cell{col: gcpsource.V(v, "%", p)}}}}
	}}
}

func failingCollector(name string, owns []Owned, errSrc string) Collector {
	return Collector{Name: name, Owns: owns, ErrorSource: errSrc, Fetch: func(_ context.Context, _ gcpsource.Window) gcpsource.Result {
		r := gcpsource.Result{Source: name}
		r.Finish(testNow, status.Error(codes.PermissionDenied, "caller lacks permission"), "roles/monitoring.viewer", "monitoring.googleapis.com")
		return r
	}}
}

func errorsCollector(name string, c4xx int64) Collector {
	return Collector{Name: name, ErrorSource: gcpsource.ErrSrcCloudRun, Fetch: func(_ context.Context, _ gcpsource.Window) gcpsource.Result {
		v, z := c4xx, int64(0)
		return gcpsource.Result{Source: name, Status: gcpsource.StatusOK, Errors: []gcpsource.ErrorRow{
			{Source: gcpsource.ErrSrcCloudRun, Project: "project-a", Service: "svc-a", C2xx: &z, C3xx: &z, C4xx: &v, C5xx: &z, Provenance: prov(name)}}}
	}}
}

type fakeMonth struct {
	rows []monthcost.Row
	err  error
	got  [2]monthcost.Month
}

func (f *fakeMonth) FetchMonths(_ context.Context, from, to monthcost.Month) ([]monthcost.Row, gcpsource.Provenance, error) {
	f.got = [2]monthcost.Month{from, to}
	p := gcpsource.Provenance{Source: monthcost.SourceName, Query: "SELECT month FROM t", Aggregation: `{"usage_start":"2026-09-01T00:00:00Z","margin":1}`}
	return f.rows, p, f.err
}

func sourceNames(run store.Run) []string {
	var out []string
	for _, s := range run.Sources {
		out = append(out, s.Source)
	}
	return out
}

func findSource(t *testing.T, run store.Run, name string) store.RunSource {
	t.Helper()
	for _, s := range run.Sources {
		if s.Source == name {
			return s
		}
	}
	t.Fatalf("no source %s in %v", name, sourceNames(run))
	return store.RunSource{}
}

func TestRun_AllMode_PartialWhenOneCollectorFails(t *testing.T) {
	w42 := isoweek.MustParse("2026-W42")
	src := &fakeSource{rows: []billing.Row{row(w42, "project-a", "Compute Engine", "N1", nil, 100, 0)}}
	h := newHarness(t, src, testNow)
	fm := &fakeMonth{rows: []monthcost.Row{
		{Month: "2026-09", InvoiceMonth: "202609", ProjectID: "project-a", Service: "Compute Engine", Gross: 10, Credits: -1, Net: 9},
		{Month: "2026-10", InvoiceMonth: "202610", ProjectID: "project-a", Service: "Compute Engine", Gross: 20, Net: 20},
	}}
	h.runner.Month = fm
	h.runner.Projects = []string{"project-a"}
	h.runner.Collectors = []Collector{
		metricCollector("monitoring_vm", gcpsource.GroupVM, "project-a", "vm-a", gcpsource.MCPU, 12.5, columnsOf(gcpsource.GroupVM, gcpsource.MCPU, gcpsource.MMemory, gcpsource.MDisk)),
		failingCollector("monitoring_logging", []Owned{columnsOf(gcpsource.GroupLogging, gcpsource.MLoggingGB)}, ""),
		errorsCollector("errors_cloudrun", 7),
		failingCollector("errors_nginx", nil, gcpsource.ErrSrcNginx),
	}

	res, err := h.runner.Run(context.Background(), ModeAll, isoweek.Week{})
	if err != nil {
		t.Fatal(err)
	}
	run := res.Run
	if run.Status != store.RunPartial || run.Trigger != store.TriggerRefreshAll {
		t.Fatalf("run = %+v", run)
	}
	want := []string{"bigquery_billing", "bigquery_month", "monitoring_vm", "monitoring_logging", "errors_cloudrun", "errors_nginx"}
	got := sourceNames(run)
	if len(got) != len(want) {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sources = %v, want %v", got, want)
		}
	}
	bad := findSource(t, run, "monitoring_logging")
	if bad.Status != store.SourceFailed || bad.ErrorCode == nil || *bad.ErrorCode != gcpsource.StatusPermissionDenied ||
		bad.FixHint == nil || *bad.FixHint != "grant the service account roles/monitoring.viewer" || bad.ErrorMessage == nil {
		t.Fatalf("failed source = %+v", bad)
	}
	if bad.JobID != nil || bad.BytesBilled != nil || bad.CacheHit != nil {
		t.Fatalf("non-BigQuery source must have null job stats: %+v", bad)
	}
	vm := findSource(t, run, "monitoring_vm")
	if vm.Status != store.SourceOK || vm.RowsReturned == nil || *vm.RowsReturned != 1 || vm.JobID != nil {
		t.Fatalf("vm source = %+v", vm)
	}
	full, err := store.GetRun(context.Background(), h.store.DB, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s := findSource(t, full, "monitoring_vm"); s.SQLText != `metric.type="x/monitoring_vm"` || len(s.Params) == 0 {
		t.Fatalf("vm sql/params = %q %s", s.SQLText, s.Params)
	}

	// Billing snapshots for every week, month snapshots for Sep and Oct.
	if len(res.Snapshots) != 5 {
		t.Fatalf("snapshots = %d, want 5 (W40..W44)", len(res.Snapshots))
	}
	if len(res.MonthSnapshots) != 2 || res.MonthSnapshots[0].Month != "2026-09" || res.MonthSnapshots[1].Month != "2026-10" {
		t.Fatalf("month snapshots = %+v", res.MonthSnapshots)
	}
	if fm.got[0].String() != "2026-09" || fm.got[1].String() != "2026-10" {
		t.Fatalf("month range = %v", fm.got)
	}
	if res.MonthSnapshots[0].NetTotal != 9 {
		t.Fatalf("sep net = %v", res.MonthSnapshots[0].NetTotal)
	}
	if res.MonthSnapshots[1].RunSourceID == nil || *res.MonthSnapshots[1].RunSourceID != findSource(t, run, "bigquery_month").ID {
		t.Fatalf("month snapshot run_source_id = %v", res.MonthSnapshots[1].RunSourceID)
	}

	// Metric snapshot for the last complete week (W43) merges the good rows
	// and shows "source failed" for the failed source's cells.
	if len(res.MetricSnaps) != 1 || res.MetricSnaps[0].WeekID != "2026-W43" {
		t.Fatalf("metric snapshots = %+v", res.MetricSnaps)
	}
	_, rows, err := store.LatestMetricSnapshot(context.Background(), h.store.DB, isoweek.MustParse("2026-W43"))
	if err != nil {
		t.Fatal(err)
	}
	var sawVM, sawFailed bool
	for _, r := range rows {
		if r.Group == gcpsource.GroupVM && r.Resource == "vm-a" && r.Cells[gcpsource.MCPU].Value != nil && *r.Cells[gcpsource.MCPU].Value == 12.5 {
			sawVM = true
		}
		if c, ok := r.Cells[gcpsource.MLoggingGB]; r.Group == gcpsource.GroupLogging && ok && c.Value == nil && c.Gap == "source failed" &&
			c.Hint == "grant the service account roles/monitoring.viewer" && c.Unit == "GB" && c.Provenance.Source == "monitoring_logging" {
			sawFailed = true
		}
	}
	if !sawVM || !sawFailed {
		t.Fatalf("rows = %+v (vm=%v failed=%v)", rows, sawVM, sawFailed)
	}
	// Error batch: the good Cloud Run row and a "source failed" nginx row.
	_, errRows, err := store.LatestErrorRows(context.Background(), h.store.DB, isoweek.MustParse("2026-W43"))
	if err != nil {
		t.Fatal(err)
	}
	if len(errRows) != 2 || errRows[0].C4xx == nil || *errRows[0].C4xx != 7 || errRows[1].Source != gcpsource.ErrSrcNginx || errRows[1].Gap != "source failed" || errRows[1].C4xx != nil {
		t.Fatalf("error rows = %+v", errRows)
	}
}

func columnsOf(group string, keys ...string) Owned { return Owned{Group: group, Columns: keys} }

func TestRun_BillingFailureStillRunsAndStoresOtherSources(t *testing.T) {
	h := newHarness(t, &fakeSource{err: errors.New("boom")}, testNow)
	h.runner.Collectors = []Collector{metricCollector("monitoring_vm", gcpsource.GroupVM, "project-a", "vm-a", gcpsource.MCPU, 40)}
	res, err := h.runner.Run(context.Background(), ModeWeek, isoweek.MustParse("2026-W42"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Run.Status != store.RunFailed || len(res.Run.Sources) != 2 || res.Run.Sources[0].Status != store.SourceFailed || res.Run.Sources[1].Status != store.SourceOK {
		t.Fatalf("run = %+v", res.Run)
	}
	if len(res.Snapshots) != 0 || len(res.MetricSnaps) != 1 || res.MetricSnaps[0].WeekID != "2026-W42" {
		t.Fatalf("snapshots = %+v metrics = %+v", res.Snapshots, res.MetricSnaps)
	}
}

func TestRun_PanickingCollectorIsContained(t *testing.T) {
	h := newHarness(t, &fakeSource{}, testNow)
	h.runner.Collectors = []Collector{
		{Name: "monitoring_vm", Fetch: func(context.Context, gcpsource.Window) gcpsource.Result { panic("kaboom") }},
		metricCollector("monitoring_gcs", gcpsource.GroupGCS, "project-a", "b", gcpsource.MClassAOps, 3),
	}
	res, err := h.runner.Run(context.Background(), ModeWeek, isoweek.MustParse("2026-W42"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Run.Status != store.RunPartial || res.Run.Sources[1].Status != store.SourceFailed || res.Run.Sources[2].Status != store.SourceOK {
		t.Fatalf("run = %+v", res.Run)
	}
}

func TestRun_CollectorConcurrencyIsBounded(t *testing.T) {
	h := newHarness(t, &fakeSource{}, testNow)
	var cur, peak int32
	var mu sync.Mutex
	for i := 0; i < 10; i++ {
		name := "errors_x" + string(rune('a'+i))
		h.runner.Collectors = append(h.runner.Collectors, Collector{Name: name, Fetch: func(context.Context, gcpsource.Window) gcpsource.Result {
			n := atomic.AddInt32(&cur, 1)
			mu.Lock()
			if n > peak {
				peak = n
			}
			mu.Unlock()
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt32(&cur, -1)
			return gcpsource.Result{Source: name, Status: gcpsource.StatusOK}
		}})
	}
	res, err := h.runner.Run(context.Background(), ModeWeek, isoweek.MustParse("2026-W42"))
	if err != nil || res.Run.Status != store.RunOK || len(res.Run.Sources) != 11 {
		t.Fatalf("run = %+v err=%v", res.Run, err)
	}
	if peak < 2 || peak > MaxConcurrency {
		t.Fatalf("peak concurrency = %d, want 2..%d", peak, MaxConcurrency)
	}
}

func TestRun_MonthMode(t *testing.T) {
	h := newHarness(t, &fakeSource{}, testNow)
	fm := &fakeMonth{rows: []monthcost.Row{{Month: "2026-09", InvoiceMonth: "202609", ProjectID: "p", Service: "Cloud Run", Gross: 5, Net: 5}}}
	h.runner.Month = fm
	h.runner.Collectors = []Collector{metricCollector("monitoring_vm", gcpsource.GroupVM, "p", "v", gcpsource.MCPU, 1)}
	res, err := h.runner.RunRequest(context.Background(), Request{Mode: ModeMonth, Month: monthcost.Month{Year: 2026, Month: 9}})
	if err != nil {
		t.Fatal(err)
	}
	run := res.Run
	if run.Trigger != store.TriggerRefreshMonth || run.Status != store.RunOK || run.From != "2026-W36" || run.To != "2026-W40" {
		t.Fatalf("run = %+v", run)
	}
	if got := sourceNames(run); len(got) != 1 || got[0] != "bigquery_month" {
		t.Fatalf("sources = %v", got)
	}
	if len(res.Snapshots) != 0 || len(res.MetricSnaps) != 0 || len(res.MonthSnapshots) != 1 || res.MonthSnapshots[0].NetTotal != 5 {
		t.Fatalf("res = %+v", res)
	}

	// Month source failure: run failed, classified on the source.
	fm.err = status.Error(codes.PermissionDenied, "denied")
	res, err = h.runner.RunRequest(context.Background(), Request{Mode: ModeMonth, Month: monthcost.Month{Year: 2026, Month: 10}})
	if err != nil || res.Run.Status != store.RunFailed || res.Run.Sources[0].ErrorCode == nil || *res.Run.Sources[0].ErrorCode != gcpsource.StatusPermissionDenied {
		t.Fatalf("run = %+v err=%v", res.Run, err)
	}
	if res.Run.To != "2026-W44" {
		t.Fatalf("current-month run should clamp to today's week: %+v", res.Run)
	}

	if _, err := h.runner.RunRequest(context.Background(), Request{Mode: ModeMonth, Month: monthcost.Month{Year: 2026, Month: 11}}); !errors.Is(err, ErrMonthOutOfRange) {
		t.Fatalf("future month err = %v", err)
	}
	if _, err := h.runner.RunRequest(context.Background(), Request{Mode: ModeMonth, Month: monthcost.Month{Year: 2026, Month: 13}}); !errors.Is(err, ErrInvalidMonth) {
		t.Fatalf("bad month err = %v", err)
	}
}

func TestRun_ConcurrentRunStillBusyWithCollectors(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	h := newHarness(t, &fakeSource{}, testNow)
	h.runner.Collectors = []Collector{{Name: "monitoring_vm", Fetch: func(context.Context, gcpsource.Window) gcpsource.Result {
		entered <- struct{}{}
		<-block
		return gcpsource.Result{Source: "monitoring_vm", Status: gcpsource.StatusOK}
	}}}
	done := make(chan error, 1)
	go func() {
		_, err := h.runner.Run(context.Background(), ModeWeek, isoweek.MustParse("2026-W42"))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("collector never started")
	}
	if _, err := h.runner.Run(context.Background(), ModeAll, isoweek.Week{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStandardCollectors_OrderAndUnavailableClientsFailClassified(t *testing.T) {
	cs := StandardCollectors(Sources{Projects: []string{"p"}, MonitoringErr: errors.New("PERMISSION_DENIED: no creds"), Now: func() time.Time { return testNow }})
	want := []string{"monitoring_vm", "monitoring_cloudsql", "monitoring_cloudrun", "monitoring_gcs", "monitoring_logging", "bigquery_usage",
		"errors_cloudrun", "errors_error_reporting", "errors_load_balancer", "errors_nginx"}
	if len(cs) != len(want) {
		t.Fatalf("collectors = %d", len(cs))
	}
	w := gcpsource.Window{Start: testNow.AddDate(0, 0, -7), End: testNow}
	for i, c := range cs {
		if c.Name != want[i] {
			t.Fatalf("collector %d = %s, want %s", i, c.Name, want[i])
		}
		res := c.Fetch(context.Background(), w)
		if res.Status == gcpsource.StatusOK || res.Status == "" {
			t.Fatalf("%s with no client must fail, got %q", c.Name, res.Status)
		}
	}
	if res := cs[0].Fetch(context.Background(), w); res.Status != gcpsource.StatusPermissionDenied || res.Hint == "" {
		t.Fatalf("vm = %+v", res)
	}
}
