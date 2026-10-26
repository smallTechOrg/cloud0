package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/smallTechOrg/cloud0/internal/costtable"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// settle is CLOUD0_SETTLE_HOURS as a duration.
func (d Deps) settle() time.Duration {
	return time.Duration(d.Config.SettleHours) * time.Hour
}

// parseWeek validates a WeekID taken from a path or query parameter.
func parseWeek(s string) (isoweek.Week, *APIError) {
	w, err := isoweek.Parse(s)
	if err != nil {
		return isoweek.Week{}, &APIError{Status: http.StatusBadRequest, Code: "invalid_week",
			Message: "The week " + quote(s) + " is not a valid ISO week.",
			Hint:    ptrTo("Use the form YYYY-Www, for example 2026-W43 (2026 has 53 weeks, 2027 has 52).")}
	}
	return w, nil
}

func ptrTo(s string) *string { return &s }

func quote(s string) string {
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return `"` + s + `"`
}

func errWeekOutOfRange(msg string) *APIError {
	return (&APIError{Status: http.StatusBadRequest, Code: "week_out_of_range", Message: msg}).
		WithHint("Pick weeks from CLOUD0_FIRST_WEEK up to the current week (see GET /api/v1/config).")
}

// scopeParam validates scope against "all" and the configured projects.
func (d Deps) scopeParam(scope string) (string, *APIError) {
	if scope == "" || scope == store.ScopeAll {
		return store.ScopeAll, nil
	}
	if slices.Contains(d.Config.Projects, scope) {
		return scope, nil
	}
	return "", (&APIError{Status: http.StatusBadRequest, Code: "invalid_scope",
		Message: "The scope " + quote(scope) + " is neither \"all\" nor a configured project."}).
		WithHint("Use \"all\" or one of the project ids from GET /api/v1/config.")
}

func (d Deps) handleCostTable(w http.ResponseWriter, r *http.Request) *APIError {
	ctx := r.Context()
	q := r.URL.Query()
	now := d.now()
	current := isoweek.Current(now)
	first := d.Config.FirstWeek

	scope, aerr := d.scopeParam(q.Get("scope"))
	if aerr != nil {
		return aerr
	}
	from, to := first, current
	if v := q.Get("from"); v != "" {
		if from, aerr = parseWeek(v); aerr != nil {
			return aerr
		}
	}
	if v := q.Get("to"); v != "" {
		if to, aerr = parseWeek(v); aerr != nil {
			return aerr
		}
	}
	if isoweek.Compare(from, first) < 0 {
		return errWeekOutOfRange("The from week " + from.String() + " is before the first week " + first.String() + ".")
	}
	if isoweek.Compare(to, current) > 0 {
		return errWeekOutOfRange("The to week " + to.String() + " is after the current week " + current.String() + ".")
	}
	if isoweek.Compare(from, to) > 0 {
		return errWeekOutOfRange("The from week " + from.String() + " is after the to week " + to.String() + ".")
	}

	latest, err := store.LatestPerWeek(ctx, d.Store.DB, from.Prev(), to)
	if err != nil {
		return ErrInternal(err)
	}
	ids := make([]int64, 0, len(latest))
	for _, s := range latest {
		ids = append(ids, s.ID)
	}
	rows, err := store.RowsForSnapshots(ctx, d.Store.DB, ids, scope)
	if err != nil {
		return ErrInternal(err)
	}
	notes, err := store.AnnotationsInRange(ctx, d.Store.DB, from, to)
	if err != nil {
		return ErrInternal(err)
	}
	settings, err := store.GetThresholds(ctx, d.Store.DB)
	if err != nil {
		return ErrInternal(err)
	}
	table, err := costtable.Assemble(costtable.Input{
		From: from, To: to, Latest: latest, Rows: rows, Annotations: notes,
		Thresholds: costtable.ThresholdsFrom(settings), Scope: scope,
		Now: now.Truncate(time.Second), Settle: d.settle(),
	})
	if err != nil {
		return ErrInternal(err)
	}
	writeJSON(w, http.StatusOK, table)
	return nil
}

// storeErr maps a store.ErrNotFound to 404 and everything else to 500.
func storeErr(err error, notFound string) *APIError {
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound(notFound)
	}
	return ErrInternal(err)
}
