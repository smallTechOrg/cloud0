package monthcost

import (
	"math"
	"strings"
	"testing"
	"time"
)

const tol = 0.005

func approx(a, b float64) bool { return math.Abs(a-b) <= tol }

func TestParse_AcceptsYYYYMMAndRejectsGarbage(t *testing.T) {
	m, err := Parse("2026-10")
	if err != nil || m.Year != 2026 || m.Month != time.October {
		t.Fatalf("Parse(2026-10) = %+v, %v", m, err)
	}
	if m.String() != "2026-10" {
		t.Fatalf("String() = %q", m.String())
	}
	for _, bad := range []string{"", "2026-13", "2026-00", "2026-1", "26-10", "2026/10", "2026-10-01", "abcd-ef"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted, want error", bad)
		}
	}
}

func TestBounds_IsUTCCalendarMonthIncludingDecemberRollover(t *testing.T) {
	cases := []struct {
		in         string
		start, end string
	}{
		{"2026-10", "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z"},
		{"2026-12", "2026-12-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"2028-02", "2028-02-01T00:00:00Z", "2028-03-01T00:00:00Z"},
	}
	for _, c := range cases {
		m, _ := Parse(c.in)
		w := m.Bounds()
		if got := w.Start.Format(time.RFC3339); got != c.start {
			t.Errorf("%s start = %s, want %s", c.in, got, c.start)
		}
		if got := w.End.Format(time.RFC3339); got != c.end {
			t.Errorf("%s end = %s, want %s", c.in, got, c.end)
		}
		if w.Start.Location() != time.UTC {
			t.Errorf("%s start not UTC", c.in)
		}
	}
	dec, _ := Parse("2026-12")
	if n := dec.Next(); n.String() != "2027-01" {
		t.Errorf("Dec.Next() = %s", n)
	}
	if feb, _ := Parse("2028-02"); feb.Bounds().Hours() != 29*24 {
		t.Errorf("leap Feb hours = %v", feb.Bounds().Hours())
	}
}

func TestOf_ReturnsUTCMonthOfInstant(t *testing.T) {
	// 2026-10-31 23:30 in UTC-5 is 2026-11-01 04:30 UTC.
	loc := time.FixedZone("x", -5*3600)
	if got := Of(time.Date(2026, 10, 31, 23, 30, 0, 0, loc)).String(); got != "2026-11" {
		t.Fatalf("Of = %s, want 2026-11", got)
	}
}

func TestStatus_LabelsPartialSettlingFinalWithInjectedClock(t *testing.T) {
	oct, _ := Parse("2026-10")
	sep, _ := Parse("2026-09")
	nov, _ := Parse("2026-11")
	cases := []struct {
		name string
		m    Month
		now  string
		want string
	}{
		{"current month", oct, "2026-10-26T09:00:00Z", StatusPartial},
		{"future month", nov, "2026-10-26T09:00:00Z", StatusPartial},
		{"first instant of month", oct, "2026-10-01T00:00:00Z", StatusPartial},
		{"just ended", sep, "2026-10-01T00:00:00Z", StatusSettling},
		{"47h after end", sep, "2026-10-02T23:59:59Z", StatusSettling},
		{"exactly settle hours after end", sep, "2026-10-03T00:00:00Z", StatusFinal},
		{"long past", sep, "2026-10-26T09:00:00Z", StatusFinal},
	}
	for _, c := range cases {
		now, _ := time.Parse(time.RFC3339, c.now)
		if got := Status(c.m, now, 48); got != c.want {
			t.Errorf("%s: Status = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestProject_ScalesLinearlyOnDay10OfA31DayMonth(t *testing.T) {
	oct, _ := Parse("2026-10")
	// Ten full days elapsed (Oct 1..10) of 31.
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	if got := Project(100, oct, now); !approx(got, 310) {
		t.Fatalf("Project = %v, want 310", got)
	}
	// Half-way through day 10 (9.5 days elapsed).
	now = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if got := Project(95, oct, now); !approx(got, 310) {
		t.Fatalf("Project = %v, want 310", got)
	}
}

func TestProject_FullOrFutureMonthReturnsNetUnchanged(t *testing.T) {
	sep, _ := Parse("2026-09")
	nov, _ := Parse("2026-11")
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	if got := Project(1234.56, sep, now); got != 1234.56 {
		t.Errorf("completed month projection = %v, want net", got)
	}
	if got := Project(0, nov, now); got != 0 {
		t.Errorf("future month projection = %v, want 0", got)
	}
	oct, _ := Parse("2026-10")
	if got := Project(5, oct, oct.Bounds().Start); got != 5 {
		t.Errorf("zero-elapsed projection = %v, want net (no divide by zero)", got)
	}
}

func TestSummary_SumsOnlyTheMonthAndProjects(t *testing.T) {
	oct, _ := Parse("2026-10")
	rows := []Row{
		{Month: "2026-10", Service: "Compute Engine", Gross: 120, Credits: -20, Net: 100},
		{Month: "2026-10", Service: "Networking", Gross: 60, Credits: -10, Net: 50},
		{Month: "2026-09", Service: "Compute Engine", Gross: 999, Credits: 0, Net: 999},
	}
	target := 3000.0
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	s := Summary(rows, oct, &target, now)
	if s.Month != "2026-10" || !approx(s.Net, 150) || !approx(s.Gross, 180) || !approx(s.Credits, -30) {
		t.Fatalf("Summary = %+v", s)
	}
	if s.Target == nil || *s.Target != 3000 {
		t.Fatalf("Target = %v", s.Target)
	}
	if !approx(s.Projected, 465) {
		t.Fatalf("Projected = %v, want 465", s.Projected)
	}
	if s.Status != StatusPartial {
		t.Fatalf("Status = %s", s.Status)
	}
}

func TestSummary_EmptyRowsAndNilTarget(t *testing.T) {
	sep, _ := Parse("2026-09")
	s := Summary(nil, sep, nil, time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	if s.Net != 0 || s.Projected != 0 || s.Target != nil || s.Status != StatusFinal {
		t.Fatalf("Summary = %+v", s)
	}
}

func TestBuildQuery_HasPartitionFilterCorrelatedCreditsAndMonthGrouping(t *testing.T) {
	sql := BuildQuery("p.d.t")
	for _, want := range []string{
		"FORMAT_TIMESTAMP('%Y-%m', usage_start_time, 'UTC') AS month",
		"invoice.month         AS invoice_month",
		"SUM((SELECT IFNULL(SUM(c.amount), 0) FROM UNNEST(credits) AS c)) AS credits",
		"FROM `p.d.t`",
		"WHERE DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end",
		"usage_start_time >= @usage_start",
		"usage_start_time <  @usage_end",
		"GROUP BY 1, 2, 3, 4, 5",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "JOIN") {
		t.Errorf("SQL must not JOIN UNNEST(credits)")
	}
}

func TestParams_AddsMarginToPartitionDates(t *testing.T) {
	oct, _ := Parse("2026-10")
	dec, _ := Parse("2026-12")
	p, err := NewParams(oct, dec, 2)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Map()
	want := map[string]string{
		"part_start":  "2026-09-29",
		"part_end":    "2027-01-03",
		"usage_start": "2026-10-01T00:00:00Z",
		"usage_end":   "2027-01-01T00:00:00Z",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	if _, err := NewParams(dec, oct, 1); err == nil {
		t.Error("from after to accepted")
	}
	if _, err := NewParams(oct, oct, -1); err == nil {
		t.Error("negative margin accepted")
	}
}
