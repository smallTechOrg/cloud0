package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/limits"
	"github.com/smallTechOrg/cloud0/internal/monthcost"
	"github.com/smallTechOrg/cloud0/internal/store"
)

type limitEntry struct {
	ID               string   `json:"id"`
	Service          string   `json:"service"`
	Label            string   `json:"label"`
	Unit             string   `json:"unit"`
	Allowance        float64  `json:"allowance"`
	DefaultAllowance float64  `json:"default_allowance"`
	Used             *float64 `json:"used"`
	Projected        *float64 `json:"projected"`
	Pct              *float64 `json:"pct"`
	Level            string   `json:"level"`
	WeekUsed         *float64 `json:"week_used"`
	Stub             bool     `json:"stub"`
	Reason           *string  `json:"reason"`
}

type limitsResponse struct {
	Week        string       `json:"week"`
	Month       string       `json:"month"`
	MonthStart  string       `json:"month_start"`
	MonthEnd    string       `json:"month_end"`
	WarnPct     float64      `json:"warn_pct"`
	UpdatedAt   *time.Time   `json:"updated_at"`
	GeneratedAt time.Time    `json:"generated_at"`
	Limits      []limitEntry `json:"limits"`
}

// weekMetrics loads (and caches) the rows of a week's latest metric snapshot.
type weekMetrics struct {
	ctx   context.Context
	st    *store.Store
	cache map[isoweek.Week][]gcpsource.MetricRow
	has   map[isoweek.Week]bool
}

func (m *weekMetrics) rows(w isoweek.Week) ([]gcpsource.MetricRow, bool, error) {
	if r, ok := m.cache[w]; ok {
		return r, m.has[w], nil
	}
	_, rows, err := store.LatestMetricSnapshot(m.ctx, m.st.DB, w)
	if errors.Is(err, store.ErrNotFound) {
		m.cache[w], m.has[w] = nil, false
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	m.cache[w], m.has[w] = rows, true
	return rows, true, nil
}

// limitMonth is the UTC calendar month containing the week's Sunday, clamped
// to the current month.
func limitMonth(week isoweek.Week, now time.Time) monthcost.Month {
	m := monthcost.Of(week.Sunday())
	if cur := monthcost.Of(now); cur.Before(m) {
		return cur
	}
	return m
}

// evaluateLimits computes every allowance's status for the week (spec/api.md
// → GET /limits). It is shared by the limits endpoints and the metric cells'
// limit_level.
func (d Deps) evaluateLimits(ctx context.Context, week isoweek.Week, now time.Time) (limitsResponse, error) {
	settings, err := store.GetSettingsWithPrefix(ctx, d.Store.DB, limits.SettingPrefix)
	if err != nil {
		return limitsResponse{}, err
	}
	overrides, err := limits.ParseOverrides(settings)
	if err != nil {
		return limitsResponse{}, err
	}
	warn := overrides[limits.WarnKey]
	defaults := limits.Defaults()
	allowances := limits.Apply(defaults, overrides)
	updated, err := store.LimitsUpdatedAt(ctx, d.Store.DB)
	if err != nil {
		return limitsResponse{}, err
	}

	month := limitMonth(week, now)
	window := month.Bounds()
	mw := &weekMetrics{ctx: ctx, st: d.Store, cache: map[isoweek.Week][]gcpsource.MetricRow{}, has: map[isoweek.Week]bool{}}

	// Weeks whose Monday is in the month, up to and including `week`.
	var weeks []isoweek.Week
	for w := week; !monthcost.Of(w.Start()).Before(month); w = w.Prev() {
		weeks = append(weeks, w)
		if isoweek.Compare(w, d.Config.FirstWeek) <= 0 {
			break
		}
	}
	weekRows, _, err := mw.rows(week)
	if err != nil {
		return limitsResponse{}, err
	}

	resp := limitsResponse{
		Week: week.String(), Month: month.String(), MonthStart: store.FormatDate(month.Start()),
		MonthEnd: store.FormatDate(window.End.AddDate(0, 0, -1)), WarnPct: warn, UpdatedAt: updated,
		GeneratedAt: now.Truncate(time.Second), Limits: make([]limitEntry, 0, len(allowances)),
	}
	for i, a := range allowances {
		e := limitEntry{ID: a.ID, Service: a.Service, Label: a.Label, Unit: a.Unit, Allowance: a.Monthly,
			DefaultAllowance: defaults[i].Monthly, Stub: a.Stub}
		if !a.Stub && a.Reason != "" {
			note := a.Reason
			e.Reason = &note
		}
		if a.Stub {
			st := limits.NoData(a)
			e.Level = st.Level
			reason := a.Reason
			e.Reason = &reason
			resp.Limits = append(resp.Limits, e)
			continue
		}
		var sum float64
		has := false
		for _, w := range weeks {
			rows, _, err := mw.rows(w)
			if err != nil {
				return limitsResponse{}, err
			}
			if v, ok := limits.UsageFromRows(rows, a); ok {
				sum += v
				has = true
			}
		}
		if !has {
			e.Level = limits.NoData(a).Level
			resp.Limits = append(resp.Limits, e)
			continue
		}
		var weekUsed float64
		if v, ok := limits.UsageFromRows(weekRows, a); ok {
			weekUsed = v
			e.WeekUsed = &weekUsed
		}
		st := limits.Evaluate(a, limits.Usage{MonthToDate: sum, Week: weekUsed}, window, now, warn)
		e.Used, e.Projected, e.Pct, e.Level = &st.Used, &st.Projected, &st.Pct, st.Level
		resp.Limits = append(resp.Limits, e)
	}
	return resp, nil
}

func (d Deps) handleGetLimits(w http.ResponseWriter, r *http.Request) *APIError {
	now := d.now()
	week, aerr := d.weekParam(r.URL.Query().Get("week"), now)
	if aerr != nil {
		return aerr
	}
	resp, err := d.evaluateLimits(r.Context(), week, now)
	if err != nil {
		return ErrInternal(err)
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (d Deps) handlePutLimits(w http.ResponseWriter, r *http.Request) *APIError {
	var body struct {
		Allowances map[string]float64 `json:"allowances"`
		WarnPct    *float64           `json:"warn_pct"`
	}
	if aerr := decodeBody(w, r, &body); aerr != nil {
		return aerr
	}
	if len(body.Allowances) == 0 && body.WarnPct == nil {
		return ErrInvalidRequest("Send at least one of allowances or warn_pct.").
			WithHint(`For example {"allowances":{"logging_gb":75},"warn_pct":90}.`)
	}
	known := map[string]bool{}
	for _, a := range limits.Defaults() {
		known[a.ID] = true
	}
	update := map[string]string{}
	for id, v := range body.Allowances {
		if !known[id] {
			return errInvalidLimit("The allowance id " + quote(id) + " is not a known limit.")
		}
		update[limits.SettingKey(id)] = strconv.FormatFloat(v, 'f', -1, 64)
	}
	if body.WarnPct != nil {
		update[limits.SettingKey(limits.WarnKey)] = strconv.FormatFloat(*body.WarnPct, 'f', -1, 64)
	}
	ctx := r.Context()
	merged, err := store.GetSettingsWithPrefix(ctx, d.Store.DB, limits.SettingPrefix)
	if err != nil {
		return ErrInternal(err)
	}
	for k, v := range update {
		merged[k] = v
	}
	if _, err := limits.ParseOverrides(merged); err != nil {
		return errInvalidLimit(err.Error())
	}
	now := d.now()
	week, aerr := d.weekParam(r.URL.Query().Get("week"), now)
	if aerr != nil {
		return aerr
	}
	if err := store.PutSettings(ctx, d.Store.DB, update, now); err != nil {
		return ErrInternal(err)
	}
	resp, err := d.evaluateLimits(ctx, week, now)
	if err != nil {
		return ErrInternal(err)
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func limitAllowances() []limits.Allowance { return limits.Defaults() }
