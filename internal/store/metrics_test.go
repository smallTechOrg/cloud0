package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

func testProv(src string) gcpsource.Provenance {
	w := isoweek.MustParse("2026-W43")
	return gcpsource.Provenance{Source: src, Query: `metric.type="x" AND resource.labels.instance_id="1"`, Aggregation: `{"align":"ALIGN_MEAN"}`,
		Window: gcpsource.Window{Start: w.Start(), End: w.End()}, FetchedAt: testNow}
}

func fixtureMetricRows() []gcpsource.MetricRow {
	p := testProv("monitoring_vm")
	return []gcpsource.MetricRow{
		{Group: gcpsource.GroupVM, Project: "proj-a", Resource: "vm-a", Cells: map[string]gcpsource.Cell{
			gcpsource.MCPU:    gcpsource.V(12.5, "%", p),
			gcpsource.MMemory: gcpsource.G("no agent", "install the Ops Agent", "%", p),
		}},
		{Group: gcpsource.GroupGCS, Project: "proj-b", Resource: "", Cells: map[string]gcpsource.Cell{
			gcpsource.MClassAOps: gcpsource.V(0, "count", p),
		}},
	}
}

func TestMetricSnapshot_RoundTripsGapsProvenanceAndLatestWins(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	week := isoweek.MustParse("2026-W43")
	run, _ := seedRunAndSource(t, s.DB, week, week)

	want := fixtureMetricRows()
	first, err := InsertMetricSnapshot(ctx, s.DB, NewMetricSnapshot{Week: week, FetchedAt: testNow, StatusAtFetch: isoweek.StatusSettling, RunID: run.ID, Rows: want})
	if err != nil {
		t.Fatal(err)
	}
	if first.RowCount != 2 || first.WeekStart != "2026-10-19" || first.WeekEnd != "2026-10-25" {
		t.Fatalf("snapshot = %+v", first)
	}
	got, rows, err := GetMetricSnapshot(ctx, s.DB, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != first.ID || !reflect.DeepEqual(rows, want) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", rows, want)
	}
	if rows[0].Cells[gcpsource.MMemory].Value != nil || rows[0].Cells[gcpsource.MMemory].Gap != "no agent" {
		t.Fatalf("gap lost: %+v", rows[0].Cells[gcpsource.MMemory])
	}
	if v := rows[1].Cells[gcpsource.MClassAOps].Value; v == nil || *v != 0 {
		t.Fatalf("explicit zero lost: %v", v)
	}

	second, err := InsertMetricSnapshot(ctx, s.DB, NewMetricSnapshot{Week: week, FetchedAt: testNow.Add(time.Hour), StatusAtFetch: isoweek.StatusFinal, RunID: run.ID, Rows: want[:1]})
	if err != nil {
		t.Fatal(err)
	}
	latest, lrows, err := LatestMetricSnapshot(ctx, s.DB, week)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != second.ID || len(lrows) != 1 || latest.StatusAtFetch != isoweek.StatusFinal {
		t.Fatalf("latest = %+v rows=%d", latest, len(lrows))
	}
}

func TestMetricSnapshot_NotFoundAndInvalid(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	week := isoweek.MustParse("2026-W43")
	if _, _, err := LatestMetricSnapshot(ctx, s.DB, week); !errors.Is(err, ErrNotFound) {
		t.Fatalf("latest err = %v", err)
	}
	if _, _, err := GetMetricSnapshot(ctx, s.DB, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get err = %v", err)
	}
	bad := []NewMetricSnapshot{
		{Week: isoweek.Week{Year: 2027, Week: 53}, FetchedAt: testNow, StatusAtFetch: isoweek.StatusFinal, RunID: 1},
		{Week: week, FetchedAt: testNow, StatusAtFetch: "x", RunID: 1},
		{Week: week, FetchedAt: testNow, StatusAtFetch: isoweek.StatusFinal},
		{Week: week, StatusAtFetch: isoweek.StatusFinal, RunID: 1},
	}
	for i, b := range bad {
		if _, err := InsertMetricSnapshot(ctx, s.DB, b); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("case %d err = %v", i, err)
		}
	}
	// Unknown run id violates the foreign key.
	if _, err := InsertMetricSnapshot(ctx, s.DB, NewMetricSnapshot{Week: week, FetchedAt: testNow, StatusAtFetch: isoweek.StatusFinal, RunID: 999}); err == nil {
		t.Fatal("dangling run id accepted")
	}
}

func TestMetricSnapshot_SurvivesReopenAndMigrationKeepsMonthTrigger(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	week := isoweek.MustParse("2026-W43")
	run, _ := seedRunAndSource(t, s.DB, week, week)
	if _, err := InsertMetricSnapshot(ctx, s.DB, NewMetricSnapshot{Week: week, FetchedAt: testNow, StatusAtFetch: isoweek.StatusFinal, RunID: run.ID, Rows: fixtureMetricRows()}); err != nil {
		t.Fatal(err)
	}
	// 0002 relaxed the runs CHECK: month runs are accepted, bogus triggers still are not.
	if _, err := CreateRun(ctx, s.DB, NewRun{Trigger: TriggerRefreshMonth, From: week, To: week, StartedAt: testNow}); err != nil {
		t.Fatalf("refresh_month rejected: %v", err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO runs ("trigger", from_iso_year, from_iso_week, to_iso_year, to_iso_week, started_at, status, created_at) VALUES ('bogus', 2026, 43, 2026, 43, 'x', 'ok', 'x')`); err == nil {
		t.Fatal("bogus trigger accepted by CHECK")
	}
	path := s.Path()
	_ = s.Close()
	re, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	if n, err := re.Pending(ctx); err != nil || n != 0 {
		t.Fatalf("pending = %d, %v", n, err)
	}
	_, rows, err := LatestMetricSnapshot(ctx, re.DB, week)
	if err != nil || !reflect.DeepEqual(rows, fixtureMetricRows()) {
		t.Fatalf("after reopen: %v %+v", err, rows)
	}
}
