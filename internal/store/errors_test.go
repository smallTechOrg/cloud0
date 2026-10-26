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
		{Source: gcpsource.ErrSrcCloudRun, Project: "proj-a", Service: "svc-a", C2xx: i64(1000), C3xx: i64(0), C4xx: i64(12), C5xx: i64(0), Provenance: p},
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
