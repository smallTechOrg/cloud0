package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/monthcost"
)

func monthRowsFixture(month string) []monthcost.Row {
	return []monthcost.Row{
		{Month: month, InvoiceMonth: "202610", ProjectID: "proj-a", ServiceID: "svc-ce", Service: "Compute Engine", Gross: 100, Credits: -10, Net: 90},
		{Month: month, InvoiceMonth: "202610", ProjectID: "proj-b", ServiceID: "svc-net", Service: "Networking", Gross: 40, Credits: -40, Net: 0},
	}
}

func TestMonthSnapshot_RoundTripTotalsAndLatestPerMonth(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	week := isoweek.MustParse("2026-W43")
	run, src := seedRunAndSource(t, s.DB, week, week)
	oct, _ := monthcost.Parse("2026-10")
	sep, _ := monthcost.Parse("2026-09")
	nov, _ := monthcost.Parse("2026-11")

	snap, err := InsertMonthSnapshot(ctx, s.DB, NewMonthSnapshot{Month: oct, FetchedAt: testNow, StatusAtFetch: isoweek.StatusPartial, RunID: run.ID, RunSourceID: &src.ID, Rows: monthRowsFixture("2026-10")})
	if err != nil {
		t.Fatal(err)
	}
	if snap.GrossTotal != 140 || snap.CreditsTotal != -50 || snap.NetTotal != 90 || snap.MonthStart != "2026-10-01" || snap.MonthEnd != "2026-10-31" {
		t.Fatalf("snap = %+v", snap)
	}
	got, rows, err := GetMonthSnapshot(ctx, s.DB, snap.ID)
	if err != nil || !reflect.DeepEqual(rows, monthRowsFixture("2026-10")) || got.RunSourceID == nil || *got.RunSourceID != src.ID {
		t.Fatalf("get: %+v %+v %v", got, rows, err)
	}
	newer, err := InsertMonthSnapshot(ctx, s.DB, NewMonthSnapshot{Month: oct, FetchedAt: testNow.Add(time.Hour), StatusAtFetch: isoweek.StatusPartial, RunID: run.ID, Rows: monthRowsFixture("2026-10")[:1]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InsertMonthSnapshot(ctx, s.DB, NewMonthSnapshot{Month: sep, FetchedAt: testNow, StatusAtFetch: isoweek.StatusFinal, RunID: run.ID, Rows: monthRowsFixture("2026-09")}); err != nil {
		t.Fatal(err)
	}
	data, err := LatestMonthsInRange(ctx, s.DB, sep, nov)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 2 || data[0].Snapshot.Month != "2026-09" || data[1].Snapshot.ID != newer.ID || len(data[1].Rows) != 1 {
		t.Fatalf("range = %+v", data)
	}
	if _, _, err := LatestMonthSnapshot(ctx, s.DB, nov); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestMonthSnapshot_InvalidInputs(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	oct, _ := monthcost.Parse("2026-10")
	sep, _ := monthcost.Parse("2026-09")
	cases := []NewMonthSnapshot{
		{Month: monthcost.Month{}, FetchedAt: testNow, StatusAtFetch: isoweek.StatusFinal, RunID: 1},
		{Month: oct, FetchedAt: testNow, StatusAtFetch: "bad", RunID: 1},
		{Month: oct, FetchedAt: testNow, StatusAtFetch: isoweek.StatusFinal, RunID: 1, Rows: monthRowsFixture("2026-09")},
	}
	for i, c := range cases {
		if _, err := InsertMonthSnapshot(ctx, s.DB, c); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("case %d err = %v", i, err)
		}
	}
	if _, err := LatestMonthsInRange(ctx, s.DB, oct, sep); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("reversed range err = %v", err)
	}
}
