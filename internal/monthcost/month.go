// Package monthcost is the calendar-month cost source (Phase 2 Month view,
// spec/capabilities/month-view.md): the Phase 1 query shape grouped by UTC
// calendar month, project and service (plus invoice.month for comparison),
// partition-filtered with the configured margin, and pure helpers for the
// month status label, linear projection and the Summary line.
//
// month.go is pure: no I/O, no clock.
package monthcost

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// Month status labels (spec/capabilities/month-view.md → Business Rules).
const (
	StatusPartial  = "partial"
	StatusSettling = "settling"
	StatusFinal    = "final"
)

// DefaultSettleHours mirrors the CLOUD0_SETTLE_HOURS default; Summary uses it.
const DefaultSettleHours = 48

// ErrInvalidMonth means a month string is not YYYY-MM with a month 01..12.
var ErrInvalidMonth = errors.New("monthcost: invalid month (want YYYY-MM)")

// Month is a UTC calendar month.
type Month struct {
	Year  int
	Month time.Month
}

// Parse reads "YYYY-MM".
func Parse(s string) (Month, error) {
	if len(s) != 7 || s[4] != '-' {
		return Month{}, fmt.Errorf("%w: %q", ErrInvalidMonth, s)
	}
	y, err1 := strconv.Atoi(s[:4])
	m, err2 := strconv.Atoi(s[5:])
	if err1 != nil || err2 != nil || y < 1 || m < 1 || m > 12 {
		return Month{}, fmt.Errorf("%w: %q", ErrInvalidMonth, s)
	}
	return Month{Year: y, Month: time.Month(m)}, nil
}

// Of returns the UTC calendar month containing t.
func Of(t time.Time) Month {
	u := t.UTC()
	return Month{Year: u.Year(), Month: u.Month()}
}

// String renders "YYYY-MM", the same form as the query's month column.
func (m Month) String() string { return fmt.Sprintf("%04d-%02d", m.Year, int(m.Month)) }

// Start is 00:00 UTC on the 1st.
func (m Month) Start() time.Time { return time.Date(m.Year, m.Month, 1, 0, 0, 0, 0, time.UTC) }

// Next is the following month (December rolls to January).
func (m Month) Next() Month { return Of(m.Start().AddDate(0, 1, 0)) }

// Bounds is the half-open UTC window [1st 00:00, next 1st 00:00).
func (m Month) Bounds() gcpsource.Window {
	return gcpsource.Window{Start: m.Start(), End: m.Next().Start()}
}

// Before reports whether m is strictly earlier than o.
func (m Month) Before(o Month) bool {
	return m.Year < o.Year || (m.Year == o.Year && m.Month < o.Month)
}

// Status labels m relative to now: "partial" for the current or a later
// month, "settling" until settleHours after the month end, else "final".
func Status(m Month, now time.Time, settleHours int) string {
	end := m.Bounds().End
	now = now.UTC()
	switch {
	case now.Before(end):
		return StatusPartial
	case now.Before(end.Add(time.Duration(settleHours) * time.Hour)):
		return StatusSettling
	}
	return StatusFinal
}

// Project linearly extrapolates a month-to-date net to the month end by the
// elapsed fraction of the month. A completed month returns net unchanged; a
// month with nothing elapsed (now at or before its start) also returns net.
func Project(netToDate float64, m Month, now time.Time) float64 {
	w := m.Bounds()
	elapsed := now.Sub(w.Start)
	total := w.End.Sub(w.Start)
	if elapsed <= 0 || elapsed >= total {
		return netToDate
	}
	return netToDate * float64(total) / float64(elapsed)
}

// SummaryLine backs "Current month net X · target Y · projected Z".
type SummaryLine struct {
	Month     string
	Gross     float64
	Credits   float64
	Net       float64
	Target    *float64 // nil when no monthly target is set
	Projected float64
	Status    string
}

// Summary totals the rows belonging to m (others are ignored) with the
// default settle period.
func Summary(rows []Row, m Month, target *float64, now time.Time) SummaryLine {
	return SummaryWithSettle(rows, m, target, now, DefaultSettleHours)
}

// SummaryWithSettle is Summary with an explicit CLOUD0_SETTLE_HOURS.
func SummaryWithSettle(rows []Row, m Month, target *float64, now time.Time, settleHours int) SummaryLine {
	key := m.String()
	s := SummaryLine{Month: key, Status: Status(m, now, settleHours)}
	if target != nil {
		v := *target
		s.Target = &v
	}
	for _, r := range rows {
		if r.Month != key {
			continue
		}
		s.Gross += r.Gross
		s.Credits += r.Credits
		s.Net += r.Net
	}
	s.Projected = Project(s.Net, m, now)
	return s
}
