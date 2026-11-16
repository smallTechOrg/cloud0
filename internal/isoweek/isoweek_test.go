package isoweek

import (
	"errors"
	"testing"
	"time"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestParse_AcceptsValidIdsAndRoundTrips(t *testing.T) {
	cases := []struct {
		in   string
		want Week
	}{
		{"2026-W18", Week{2026, 18}},
		{"2026-W01", Week{2026, 1}},
		{"2026-W53", Week{2026, 53}},
		{"2027-W52", Week{2027, 52}},
		{"2020-W53", Week{2020, 53}},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("Parse(%q) = %v, want %v", c.in, got, c.want)
		}
		if got.String() != c.in || Format(got) != c.in {
			t.Fatalf("round trip %q -> %q", c.in, got.String())
		}
	}
}

func TestParse_RejectsMalformedAndNonexistentWeeks(t *testing.T) {
	bad := []string{
		"", "2026-W0", "2026-W1", "2026W18", "2026-w18", "26-W18", "2026-W54",
		"2026-W00", "2027-W53", "2025-W53", "2026-W18 ", " 2026-W18", "abcd-Wxx",
	}
	for _, in := range bad {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) accepted, want error", in)
		} else if !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) error %v is not ErrInvalid", in, err)
		}
	}
}

func TestWeeksInYear_Knows53WeekYears(t *testing.T) {
	cases := map[int]int{2015: 53, 2020: 53, 2024: 52, 2025: 52, 2026: 53, 2027: 52, 2032: 53}
	for year, want := range cases {
		if got := WeeksInYear(year); got != want {
			t.Errorf("WeeksInYear(%d) = %d, want %d", year, got, want)
		}
	}
}

func TestStart_W18of2026IsApril27(t *testing.T) {
	w := MustParse("2026-W18")
	if got := w.Start(); !got.Equal(date(2026, time.April, 27)) {
		t.Fatalf("Start = %s, want 2026-04-27", got.Format(time.RFC3339))
	}
	if got := w.End(); !got.Equal(date(2026, time.May, 4)) {
		t.Fatalf("End = %s, want 2026-05-04", got.Format(time.RFC3339))
	}
	if got := w.Sunday(); !got.Equal(date(2026, time.May, 3)) {
		t.Fatalf("Sunday = %s, want 2026-05-03", got.Format(time.RFC3339))
	}
	start, end := Bounds(w)
	if !start.Equal(w.Start()) || !end.Equal(w.End()) {
		t.Fatalf("Bounds mismatch: %s %s", start, end)
	}
	if start.Weekday() != time.Monday {
		t.Fatalf("Start is a %s, want Monday", start.Weekday())
	}
}

func TestStart_YearBoundaries(t *testing.T) {
	cases := []struct {
		id    string
		start time.Time
	}{
		{"2026-W01", date(2025, time.December, 29)},
		{"2026-W53", date(2026, time.December, 28)},
		{"2027-W01", date(2027, time.January, 4)},
		{"2025-W01", date(2024, time.December, 30)},
		{"2021-W01", date(2021, time.January, 4)},
		{"2020-W53", date(2020, time.December, 28)},
		{"2026-W42", date(2026, time.October, 12)},
		{"2026-W43", date(2026, time.October, 19)},
	}
	for _, c := range cases {
		w := MustParse(c.id)
		if got := w.Start(); !got.Equal(c.start) {
			t.Errorf("%s Start = %s, want %s", c.id, got.Format("2006-01-02"), c.start.Format("2006-01-02"))
		}
		// Every day of the week must map back to the same week.
		for d := 0; d < 7; d++ {
			day := w.Start().AddDate(0, 0, d).Add(23*time.Hour + 59*time.Minute)
			if back := Of(day); back != w {
				t.Errorf("Of(%s) = %v, want %v", day.Format(time.RFC3339), back, w)
			}
		}
	}
}

func TestOf_UsesUTCNotLocalZone(t *testing.T) {
	// Sunday 2026-05-03 23:30 UTC is still W18; in UTC+2 it is already Monday
	// 2026-05-04 01:30 local, which would be W19 if the zone leaked in.
	loc := time.FixedZone("plus2", 2*3600)
	tm := time.Date(2026, time.May, 4, 1, 30, 0, 0, loc)
	if got := Of(tm); got != MustParse("2026-W18") {
		t.Fatalf("Of(local) = %v, want 2026-W18", got)
	}
}

func TestNextPrev_RollOverYearsIncludingW53(t *testing.T) {
	cases := []struct{ from, next string }{
		{"2026-W52", "2026-W53"},
		{"2026-W53", "2027-W01"},
		{"2027-W52", "2028-W01"},
		{"2025-W52", "2026-W01"},
		{"2026-W18", "2026-W19"},
	}
	for _, c := range cases {
		from, next := MustParse(c.from), MustParse(c.next)
		if got := from.Next(); got != next {
			t.Errorf("%s.Next() = %v, want %v", c.from, got, c.next)
		}
		if got := next.Prev(); got != from {
			t.Errorf("%s.Prev() = %v, want %v", c.next, got, c.from)
		}
	}
}

func TestCurrentAndLastComplete_WithInjectedNow(t *testing.T) {
	now := time.Date(2026, time.October, 26, 9, 0, 0, 0, time.UTC) // Monday of W44
	if got := Current(now); got != MustParse("2026-W44") {
		t.Fatalf("Current = %v, want 2026-W44", got)
	}
	if got := LastComplete(now); got != MustParse("2026-W43") {
		t.Fatalf("LastComplete = %v, want 2026-W43", got)
	}
	// First Monday of 2027: last complete week is 2026-W53.
	now2 := date(2027, time.January, 4)
	if got := LastComplete(now2); got != MustParse("2026-W53") {
		t.Fatalf("LastComplete(2027-01-04) = %v, want 2026-W53", got)
	}
}

func TestRange_EnumeratesInclusiveAcrossYears(t *testing.T) {
	got, err := Range(MustParse("2026-W52"), MustParse("2027-W02"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-W52", "2026-W53", "2027-W01", "2027-W02"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	single, err := Range(MustParse("2026-W18"), MustParse("2026-W18"))
	if err != nil || len(single) != 1 {
		t.Fatalf("single-week range: %v %v", single, err)
	}
	if _, err := Range(MustParse("2026-W19"), MustParse("2026-W18")); err == nil {
		t.Fatal("reversed range accepted")
	}
	full, err := Range(MustParse("2026-W18"), MustParse("2026-W44"))
	if err != nil || len(full) != 27 {
		t.Fatalf("W18..W44 has %d weeks, want 27 (%v)", len(full), err)
	}
}

func TestCompare_OrdersChronologically(t *testing.T) {
	a, b := MustParse("2026-W53"), MustParse("2027-W01")
	if Compare(a, b) != -1 || Compare(b, a) != 1 || Compare(a, a) != 0 {
		t.Fatal("Compare ordering wrong")
	}
}

func TestStatusAt_PartialSettlingFinalWithInjectedNow(t *testing.T) {
	w := MustParse("2026-W42") // Mon 2026-10-12 .. Sun 2026-10-18; ends Mon 2026-10-19 00:00Z
	settle := 48 * time.Hour
	end := date(2026, time.October, 19)
	cases := []struct {
		name string
		now  time.Time
		want Status
	}{
		{"before start", date(2026, time.October, 1), StatusPartial},
		{"mid week", date(2026, time.October, 15), StatusPartial},
		{"one second before end", end.Add(-time.Second), StatusPartial},
		{"exactly at end", end, StatusSettling},
		{"one hour into settle", end.Add(time.Hour), StatusSettling},
		{"one second before settle elapses", end.Add(settle - time.Second), StatusSettling},
		{"exactly settle elapsed", end.Add(settle), StatusFinal},
		{"long after", date(2026, time.December, 1), StatusFinal},
	}
	for _, c := range cases {
		if got := StatusAt(w, c.now, settle); got != c.want {
			t.Errorf("%s: StatusAt = %s, want %s", c.name, got, c.want)
		}
	}
	// A zero settle window skips straight from partial to final.
	if got := StatusAt(w, end, 0); got != StatusFinal {
		t.Fatalf("zero settle at end = %s, want final", got)
	}
	// Ranks are monotonic so "stale" comparisons work.
	if !(StatusPartial.Rank() < StatusSettling.Rank() && StatusSettling.Rank() < StatusFinal.Rank()) {
		t.Fatal("status ranks not monotonic")
	}
	if Status("bogus").Rank() != 0 {
		t.Fatal("unknown status should rank 0")
	}
}

func TestMustParse_PanicsOnInvalid(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustParse did not panic")
		}
	}()
	MustParse("nope")
}
