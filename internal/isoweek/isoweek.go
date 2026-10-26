// Package isoweek implements the ISO-8601 week arithmetic cloud0 uses
// everywhere: parsing and formatting YYYY-Www identifiers, Monday–Sunday UTC
// bounds, the current and last-complete week, week ranges and the
// partial / settling / final status of a week relative to an injected "now".
//
// All functions are pure; every time.Time is interpreted in UTC.
package isoweek

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Status is the lifecycle stage of a week relative to now.
type Status string

const (
	// StatusPartial means the week has not ended yet.
	StatusPartial Status = "partial"
	// StatusSettling means the week ended but the billing export lag has not elapsed.
	StatusSettling Status = "settling"
	// StatusFinal means the week ended and the settle window has passed.
	StatusFinal Status = "final"
)

// Rank orders statuses so callers can compare lifecycle stages
// (partial < settling < final). Unknown statuses rank lowest.
func (s Status) Rank() int {
	switch s {
	case StatusPartial:
		return 1
	case StatusSettling:
		return 2
	case StatusFinal:
		return 3
	}
	return 0
}

// Week is one ISO week: the ISO year and the week number 1–53.
type Week struct {
	Year int
	Week int
}

// ErrInvalid is returned for identifiers that do not denote a real ISO week.
var ErrInvalid = errors.New("isoweek: invalid week id")

var idPattern = regexp.MustCompile(`^(\d{4})-W(\d{2})$`)

// Parse reads a YYYY-Www identifier and validates it against the ISO
// calendar (2026 has W53; 2027 does not).
func Parse(s string) (Week, error) {
	m := idPattern.FindStringSubmatch(s)
	if m == nil {
		return Week{}, fmt.Errorf("%w: %q (want YYYY-Www)", ErrInvalid, s)
	}
	var year, week int
	if _, err := fmt.Sscanf(m[1], "%d", &year); err != nil {
		return Week{}, fmt.Errorf("%w: %q", ErrInvalid, s)
	}
	if _, err := fmt.Sscanf(m[2], "%d", &week); err != nil {
		return Week{}, fmt.Errorf("%w: %q", ErrInvalid, s)
	}
	w := Week{Year: year, Week: week}
	if !w.Valid() {
		return Week{}, fmt.Errorf("%w: %q (year %d has %d weeks)", ErrInvalid, s, year, WeeksInYear(year))
	}
	return w, nil
}

// MustParse is Parse for compile-time constants; it panics on error.
func MustParse(s string) Week {
	w, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return w
}

// Valid reports whether the week exists in the ISO calendar.
func (w Week) Valid() bool {
	return w.Year >= 1 && w.Week >= 1 && w.Week <= WeeksInYear(w.Year)
}

// Format renders the week as YYYY-Www.
func Format(w Week) string { return w.String() }

// String renders the week as YYYY-Www.
func (w Week) String() string { return fmt.Sprintf("%04d-W%02d", w.Year, w.Week) }

// WeeksInYear returns 52 or 53: a year has 53 ISO weeks exactly when
// 28 December falls in week 53.
func WeeksInYear(year int) int {
	_, wk := time.Date(year, time.December, 28, 0, 0, 0, 0, time.UTC).ISOWeek()
	return wk
}

// Start is Monday 00:00:00 UTC of the week (inclusive).
func (w Week) Start() time.Time {
	// 4 January is always in ISO week 1.
	jan4 := time.Date(w.Year, time.January, 4, 0, 0, 0, 0, time.UTC)
	offset := (int(jan4.Weekday()) + 6) % 7 // days since Monday
	week1Monday := jan4.AddDate(0, 0, -offset)
	return week1Monday.AddDate(0, 0, (w.Week-1)*7)
}

// End is the following Monday 00:00:00 UTC (exclusive).
func (w Week) End() time.Time { return w.Start().AddDate(0, 0, 7) }

// Sunday is the last calendar day of the week (inclusive), for display and
// for the snapshots.week_end column.
func (w Week) Sunday() time.Time { return w.Start().AddDate(0, 0, 6) }

// Bounds returns [Start, End) — Monday 00:00 UTC to next Monday 00:00 UTC.
func Bounds(w Week) (start, end time.Time) { return w.Start(), w.End() }

// Of returns the ISO week containing t (evaluated in UTC).
func Of(t time.Time) Week {
	y, wk := t.UTC().ISOWeek()
	return Week{Year: y, Week: wk}
}

// Current is the week containing now.
func Current(now time.Time) Week { return Of(now) }

// LastComplete is the week before the one containing now.
func LastComplete(now time.Time) Week { return Current(now).Prev() }

// Next returns the following week, rolling the year over after W52/W53.
func (w Week) Next() Week {
	if w.Week < WeeksInYear(w.Year) {
		return Week{Year: w.Year, Week: w.Week + 1}
	}
	return Week{Year: w.Year + 1, Week: 1}
}

// Prev returns the preceding week, rolling back into W52/W53 of the prior year.
func (w Week) Prev() Week {
	if w.Week > 1 {
		return Week{Year: w.Year, Week: w.Week - 1}
	}
	return Week{Year: w.Year - 1, Week: WeeksInYear(w.Year - 1)}
}

// Compare orders weeks chronologically: -1 if a < b, 0 if equal, +1 if a > b.
func Compare(a, b Week) int {
	switch {
	case a.Year < b.Year:
		return -1
	case a.Year > b.Year:
		return 1
	case a.Week < b.Week:
		return -1
	case a.Week > b.Week:
		return 1
	}
	return 0
}

// Range enumerates every week from first to last inclusive, ascending.
// It returns an error when first is after last.
func Range(first, last Week) ([]Week, error) {
	if Compare(first, last) > 0 {
		return nil, fmt.Errorf("isoweek: range %s > %s", first, last)
	}
	var out []Week
	for w := first; ; w = w.Next() {
		out = append(out, w)
		if Compare(w, last) == 0 {
			break
		}
	}
	return out, nil
}

// StatusAt reports the lifecycle stage of w at now, given the settle window
// that must elapse after the week ends before it counts as final.
//
//	partial  : now <  End
//	settling : End <= now < End + settle
//	final    : now >= End + settle
func StatusAt(w Week, now time.Time, settle time.Duration) Status {
	end := w.End()
	now = now.UTC()
	switch {
	case now.Before(end):
		return StatusPartial
	case now.Before(end.Add(settle)):
		return StatusSettling
	default:
		return StatusFinal
	}
}
