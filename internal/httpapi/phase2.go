package httpapi

import (
	"net/http"
	"time"

	"github.com/smallTechOrg/cloud0/internal/costtable"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/monthcost"
)

// Phase 2 error constructors (spec/api.md → Phase 2 additions).

func errInvalidMonth(s string) *APIError {
	return (&APIError{Status: http.StatusBadRequest, Code: "invalid_month",
		Message: "The month " + quote(s) + " is not a valid calendar month."}).
		WithHint("Use the form YYYY-MM, for example 2026-10.")
}

func errMonthOutOfRange(msg string) *APIError {
	return (&APIError{Status: http.StatusBadRequest, Code: "month_out_of_range", Message: msg}).
		WithHint("Pick months up to the current month, with from not after to.")
}

func errInvalidLimit(msg string) *APIError {
	return (&APIError{Status: http.StatusBadRequest, Code: "invalid_limit", Message: msg}).
		WithHint("Use a known allowance id with a finite number >= 0, and warn_pct in (0, 100]; see GET /api/v1/limits for the ids.")
}

// parseMonth validates a YYYY-MM query value.
func parseMonth(s string) (monthcost.Month, *APIError) {
	m, err := monthcost.Parse(s)
	if err != nil {
		return monthcost.Month{}, errInvalidMonth(s)
	}
	return m, nil
}

// weekParam reads the `week` query parameter: default_week when omitted,
// otherwise a valid week within first_week..current_week.
func (d Deps) weekParam(v string, now time.Time) (isoweek.Week, *APIError) {
	current := isoweek.Current(now)
	first := d.Config.FirstWeek
	if v == "" {
		return defaultWeek(first, current), nil
	}
	w, aerr := parseWeek(v)
	if aerr != nil {
		return isoweek.Week{}, aerr
	}
	if isoweek.Compare(w, first) < 0 || isoweek.Compare(w, current) > 0 {
		return isoweek.Week{}, errWeekOutOfRange("The week " + w.String() + " is outside " + first.String() + ".." + current.String() + ".")
	}
	return w, nil
}

// snapshotRef builds the SnapshotRef shared by the Phase 2 tables.
func snapshotRef(id int64, fetched time.Time, atFetch isoweek.Status, runID int64, rows int, current isoweek.Status) *costtable.SnapshotRef {
	return &costtable.SnapshotRef{ID: id, FetchedAt: fetched.UTC(), StatusAtFetch: atFetch, RunID: runID, RowCount: rows,
		Stale: costtable.Stale(atFetch, current)}
}

func (d Deps) settleHours() int { return d.Config.SettleHours }
