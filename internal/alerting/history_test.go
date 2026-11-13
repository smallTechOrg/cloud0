package alerting

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeIncidents struct {
	by  map[string][]Incident
	err map[string]error
}

func (f fakeIncidents) List(_ context.Context, p string, _ IncidentFilter) ([]Incident, error) {
	if e := f.err[p]; e != nil {
		return nil, e
	}
	return f.by[p], nil
}

var histStart = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)

func histWindow(week string) (time.Time, time.Time, error) {
	if week != "2026-W43" {
		return time.Time{}, time.Time{}, errors.New("bad week")
	}
	return histStart, histStart.AddDate(0, 0, 7), nil
}

func TestHistoryAggregatesPerPolicy(t *testing.T) {
	closed := histStart.Add(2 * time.Hour)
	f := fakeIncidents{by: map[string][]Incident{"proj-a": {
		{ID: "1", PolicyName: "p/1", PolicyDisplayName: "CPU", Severity: "WARNING", State: "closed", OpenedAt: histStart.Add(time.Hour), ClosedAt: &closed, ResourceName: "vm-b"},
		{ID: "2", PolicyName: "p/1", PolicyDisplayName: "CPU", Severity: "CRITICAL", State: "open", OpenedAt: histStart.Add(48 * time.Hour), ResourceName: "vm-a"},
		{ID: "3", PolicyName: "p/1", PolicyDisplayName: "CPU", Severity: "ERROR", State: "closed", OpenedAt: histStart.Add(50 * time.Hour), ResourceName: "vm-a"},
		{ID: "4", PolicyName: "p/2", PolicyDisplayName: "Down", Severity: "ERROR", State: "closed", OpenedAt: histStart.Add(3 * time.Hour), ResourceName: "db"},
		{ID: "5", PolicyName: "p/2", PolicyDisplayName: "Down", Severity: "ERROR", State: "closed", OpenedAt: histStart.AddDate(0, 0, 8)}, // outside week
	}}}
	h := NewHistoryProvider(f, []string{"proj-a"}, histWindow, func() time.Time { return histStart })
	rows, err := h.History(context.Background(), "2026-W43")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	r := rows[0]
	if r.PolicyName != "p/1" || r.Opened != 3 || r.StillOpen != 1 || r.Severity != SevCritical ||
		len(r.Resources) != 2 || r.Resources[0] != "vm-a" || r.Resources[1] != "vm-b" ||
		!r.FirstOpenedAt.Equal(histStart.Add(time.Hour)) || !r.LastOpenedAt.Equal(histStart.Add(50*time.Hour)) {
		t.Fatalf("bad row: %+v", r)
	}
	if rows[1].Opened != 1 || rows[1].PolicyDisplayName != "Down" {
		t.Fatalf("bad second row: %+v", rows[1])
	}
}

func TestHistoryEmptyAndErrors(t *testing.T) {
	boom := errors.New("boom")
	f := fakeIncidents{by: map[string][]Incident{"proj-a": nil}, err: map[string]error{"proj-b": boom}}
	h := NewHistoryProvider(f, []string{"proj-a"}, histWindow, nil)
	rows, err := h.History(context.Background(), "2026-W43")
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty: %v %#v", err, rows)
	}
	if _, err := h.History(context.Background(), "2026-W99"); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("bad week: %v", err)
	}
	h2 := NewHistoryProvider(f, []string{"proj-a", "proj-b"}, histWindow, nil)
	if _, err := h2.History(context.Background(), "2026-W43"); !errors.Is(err, ErrUpstream) {
		t.Fatalf("want upstream error, got %v", err)
	}
	_, failed, err := h2.Run(context.Background(), "2026-W43")
	if err != nil || len(failed) != 1 || failed[0].Project != "proj-b" {
		t.Fatalf("run: %v %+v", err, failed)
	}
}
