package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

func i64(v int64) *int64 { return &v }

func fixtureErrorRows() []gcpsource.ErrorRow {
	p := testProv("monitoring_cloudrun_errors")
	return []gcpsource.ErrorRow{
		{Source: gcpsource.ErrSrcCloudRun, Project: "proj-a", Service: "svc-a", C2xx: i64(1000), C3xx: i64(0), C4xx: i64(12), C5xx: i64(0), ByCode: map[string]int64{"200": 1000, "404": 11, "401": 1}, Provenance: p},
		{Source: gcpsource.ErrSrcErrorReporting, Project: "proj-a", Service: "svc-a", Groups: i64(3), Occurrences: i64(40), Provenance: p},
		{Source: gcpsource.ErrSrcLB, Project: "proj-a", Service: "", Gap: "logging disabled", Hint: "enable logging on the backend service", Provenance: p},
	}
}

func TestErrorRows_RoundTripGapsAndLatestRunWins(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	week := isoweek.MustParse("2026-W43")
	r1, _ := seedRunAndSource(t, s.DB, week, week)
	r2, _ := seedRunAndSource(t, s.DB, week, week)

	want := fixtureErrorRows()
	b1, err := InsertErrorRows(ctx, s.DB, NewErrorBatch{Week: week, FetchedAt: testNow, StatusAtFetch: isoweek.StatusSettling, RunID: r1.ID, Rows: want})
	if err != nil || b1.RowCount != 3 {
		t.Fatalf("insert: %+v %v", b1, err)
	}
	_, got, err := LatestErrorRows(ctx, s.DB, week)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
	if got[2].C4xx != nil || got[2].Gap == "" {
		t.Fatalf("gap row must have nil counts: %+v", got[2])
	}
	if got[0].C3xx == nil || *got[0].C3xx != 0 {
		t.Fatalf("explicit zero lost")
	}

	if _, err := InsertErrorRows(ctx, s.DB, NewErrorBatch{Week: week, FetchedAt: testNow.Add(1), StatusAtFetch: isoweek.StatusFinal, RunID: r2.ID, Rows: want[:1]}); err != nil {
		t.Fatal(err)
	}
	b, rows, err := LatestErrorRows(ctx, s.DB, week)
	if err != nil || b.RunID != r2.ID || len(rows) != 1 || b.StatusAtFetch != isoweek.StatusFinal {
		t.Fatalf("latest = %+v %d %v", b, len(rows), err)
	}
	if _, old, err := ErrorRowsForRun(ctx, s.DB, r1.ID, week); err != nil || len(old) != 3 {
		t.Fatalf("for run: %d %v", len(old), err)
	}
}

func TestErrorRows_NotFoundAndInvalid(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	week := isoweek.MustParse("2026-W43")
	if _, _, err := LatestErrorRows(ctx, s.DB, week); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := InsertErrorRows(ctx, s.DB, NewErrorBatch{Week: week, FetchedAt: testNow, StatusAtFetch: "x", RunID: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorRows_ByCodeRoundTripsAndNilStaysNil(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	week := isoweek.MustParse("2026-W43")
	r, _ := seedRunAndSource(t, s.DB, week, week)
	if _, err := InsertErrorRows(ctx, s.DB, NewErrorBatch{Week: week, FetchedAt: testNow, StatusAtFetch: isoweek.StatusSettling, RunID: r.ID, Rows: fixtureErrorRows()}); err != nil {
		t.Fatal(err)
	}
	_, got, err := LatestErrorRows(ctx, s.DB, week)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ByCode["404"] != 11 || got[0].ByCode["401"] != 1 || got[1].ByCode != nil {
		t.Fatalf("by_code: %v / %v", got[0].ByCode, got[1].ByCode)
	}
	if j := ErrorRowToJSON(got[0]); j.ByCode["404"] != 11 {
		t.Fatalf("wire by_code: %+v", j.ByCode)
	}
}

// TestMigration0003_UpgradesExisting0002DB applies 0001+0002, stores an error
// row the old way, then migrates to head: the old row survives with a NULL
// by_code and new rows carry codes.
func TestMigration0003_UpgradesExisting0002DB(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	p, err := s.provider()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatalf("UpTo(2): %v", err)
	}
	week := isoweek.MustParse("2026-W43")
	r, _ := seedRunAndSource(t, s.DB, week, week)
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO error_rows (run_id, iso_year, iso_week, fetched_at, status_at_fetch, source, project_id, service, c4xx, provenance_json)
VALUES (?, 2026, 43, ?, 'settling', 'cloudrun', 'proj-a', 'svc-a', 7, '{}')`, r.ID, FormatTime(testNow)); err != nil {
		t.Fatalf("0002-shaped insert: %v", err)
	}
	applied, err := s.Migrate(ctx)
	if err != nil || len(applied) != 3 || applied[0].Source != "0003_error_codes.sql" || applied[1].Source != "0004_alerts.sql" || applied[2].Source != "0005_attribution.sql" {
		t.Fatalf("Migrate: %+v %v", applied, err)
	}
	_, rows, err := LatestErrorRows(ctx, s.DB, week)
	if err != nil || len(rows) != 1 || *rows[0].C4xx != 7 || rows[0].ByCode != nil {
		t.Fatalf("old row after upgrade: %+v %v", rows, err)
	}
	r2, _ := seedRunAndSource(t, s.DB, week, week)
	want := gcpsource.ErrorRow{Source: gcpsource.ErrSrcCloudRun, Project: "proj-a", Service: "svc-a", C4xx: i64(9), ByCode: map[string]int64{"404": 9}, Provenance: testProv("x")}
	if _, err := InsertErrorRows(ctx, s.DB, NewErrorBatch{Week: week, FetchedAt: testNow, StatusAtFetch: isoweek.StatusSettling, RunID: r2.ID, Rows: []gcpsource.ErrorRow{want}}); err != nil {
		t.Fatal(err)
	}
	if _, rows, _ = LatestErrorRows(ctx, s.DB, week); rows[0].ByCode["404"] != 9 {
		t.Fatalf("new row by_code: %+v", rows[0])
	}
}
