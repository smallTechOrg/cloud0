package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/smallTechOrg/cloud0/internal/attribution"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/monthcost"
	"github.com/smallTechOrg/cloud0/internal/store"
)

const maxTargetProductLen = 64

func (d Deps) registerAttributionTargets(r *router) {
	r.handle(http.MethodGet, apiPrefix+"/targets", d.handleGetTargets)
	r.handle(http.MethodPut, apiPrefix+"/targets", d.handlePutTargets)
}

func errInvalidTarget(msg string) *APIError {
	return (&APIError{Status: http.StatusBadRequest, Code: attribution.CodeInvalidTarget, Message: msg}).
		WithHint(`Send {"month_from":"2026-11","targets":[{"product":"*","amount":25000},{"product":"example-product","amount":null}]}: product is "*" or a lowercase name, amount a number >= 0 or null to clear.`)
}

// targetsMonth reads the optional `month` query value; default the current
// month, never after it.
func (d Deps) targetsMonth(v string, now time.Time) (monthcost.Month, *APIError) {
	cur := monthcost.Of(now)
	if v == "" {
		return cur, nil
	}
	m, aerr := parseMonth(v)
	if aerr != nil {
		return m, aerr
	}
	if cur.Before(m) {
		return m, errMonthOutOfRange("The month " + m.String() + " is after the current month " + cur.String() + ".")
	}
	return m, nil
}

// targetsRules loads the stored rules into a compiled engine.
func (d Deps) targetsEngine(ctx context.Context) (attribution.Engine, *APIError) {
	stored, err := store.ListAttributionRules(ctx, d.Store.DB)
	if err != nil {
		return nil, ErrInternal(err)
	}
	rules := make([]attribution.Rule, 0, len(stored))
	for _, s := range stored {
		id := s.ID
		rules = append(rules, attribution.Rule{
			ID: &id, Priority: s.Priority, Enabled: s.Enabled, Source: s.Source, Note: s.Note,
			Match:  attribution.Match{Field: s.MatchField, Op: s.MatchOp, Key: s.MatchKey, Value: s.MatchValue},
			Action: attribution.Action{Product: s.Product, Environment: s.Environment, Shared: s.Shared},
		})
	}
	eng, err := d.Attribution.NewEngine(rules)
	if err != nil {
		return nil, ErrInternal(err)
	}
	return eng, nil
}

// targetsLabels parses labels_json ([{"key":..,"value":..}]); nil when absent
// or unparsable (no labels known).
func targetsLabels(raw *string) map[string]string {
	if raw == nil || *raw == "" {
		return nil
	}
	var kv []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if json.Unmarshal([]byte(*raw), &kv) != nil {
		return nil
	}
	out := make(map[string]string, len(kv))
	for _, e := range kv {
		out[e.Key] = e.Value
	}
	return out
}

func targetsCostRows(rows []store.SnapshotRow) []attribution.CostRow {
	out := make([]attribution.CostRow, 0, len(rows))
	for _, r := range rows {
		row := attribution.Row{ProjectID: r.ProjectID, Service: r.Service, SKU: r.SKU, Labels: targetsLabels(r.LabelsJSON)}
		if r.ResourceName != nil {
			row.ResourceName = *r.ResourceName
		}
		if r.ResourceGlobalName != nil {
			row.ResourceGlobalName = *r.ResourceGlobalName
		}
		out = append(out, attribution.CostRow{Row: row, Gross: r.Gross, Credits: r.Credits, Net: r.Net, LineItems: r.LineItems})
	}
	return out
}

// targetsShareWeeks lists, ascending, the ISO weeks whose Monday falls in the
// month, that are not after the current week and not before first_week.
func (d Deps) targetsShareWeeks(m monthcost.Month, now time.Time) []isoweek.Week {
	top := isoweek.Current(now)
	if last := isoweek.Of(m.Bounds().End.AddDate(0, 0, -1)); isoweek.Compare(last, top) < 0 {
		top = last
	}
	var weeks []isoweek.Week
	for w := top; !monthcost.Of(w.Start()).Before(m); w = w.Prev() {
		if isoweek.Compare(w, d.Config.FirstWeek) < 0 {
			break
		}
		weeks = append(weeks, w)
	}
	for i, j := 0, len(weeks)-1; i < j; i, j = i+1, j-1 {
		weeks[i], weeks[j] = weeks[j], weeks[i]
	}
	return weeks
}

func (d Deps) buildTargets(ctx context.Context, m monthcost.Month, scope string, now time.Time) (attribution.TargetsResponse, *APIError) {
	if d.Attribution == nil || d.Attribution.NewEngine == nil {
		return attribution.TargetsResponse{}, (&APIError{Status: http.StatusServiceUnavailable, Code: "attribution_not_configured",
			Message: "Attribution is not configured on this server."}).WithHint("Restart cloud0 from the current build; the attribution engine is wired at startup.")
	}
	in := attribution.TargetsInput{Month: m, Now: now, SettleHours: d.settleHours(), Currency: d.Config.Currency}

	_, mrows, err := store.LatestMonthSnapshot(ctx, d.Store.DB, m)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return attribution.TargetsResponse{}, ErrInternal(err)
	default:
		in.HasMonthData = true
		for _, row := range mrows {
			if inScope(row.ProjectID, scope) {
				in.MonthRows = append(in.MonthRows, row)
			}
		}
	}

	if weeks := d.targetsShareWeeks(m, now); len(weeks) > 0 {
		snaps, err := store.LatestPerWeek(ctx, d.Store.DB, weeks[0], weeks[len(weeks)-1])
		if err != nil {
			return attribution.TargetsResponse{}, ErrInternal(err)
		}
		for _, w := range weeks {
			snap, ok := snaps[w]
			if !ok {
				continue
			}
			rows, err := store.SnapshotRows(ctx, d.Store.DB, snap.ID, scope)
			if err != nil {
				return attribution.TargetsResponse{}, ErrInternal(err)
			}
			in.Weeks = append(in.Weeks, attribution.TargetWeek{Week: w.String(), Sunday: w.Sunday(), Rows: targetsCostRows(rows)})
		}
	}

	eng, aerr := d.targetsEngine(ctx)
	if aerr != nil {
		return attribution.TargetsResponse{}, aerr
	}
	in.Engine = eng

	stored, err := store.ListTargets(ctx, d.Store.DB)
	if err != nil {
		return attribution.TargetsResponse{}, ErrInternal(err)
	}
	for _, t := range stored {
		in.History = append(in.History, attribution.TargetHistory{Product: t.Product, MonthFrom: t.MonthFrom, Amount: t.Amount, CreatedAt: t.CreatedAt})
	}
	if in.AccountFallback, err = store.GetMonthlyTarget(ctx, d.Store.DB); err != nil {
		return attribution.TargetsResponse{}, ErrInternal(err)
	}
	return attribution.ComputeTargets(in), nil
}

func (d Deps) handleGetTargets(w http.ResponseWriter, r *http.Request) *APIError {
	q := r.URL.Query()
	now := d.now()
	m, aerr := d.targetsMonth(q.Get("month"), now)
	if aerr != nil {
		return aerr
	}
	scope, aerr := d.scopeParam(q.Get("scope"))
	if aerr != nil {
		return aerr
	}
	resp, aerr := d.buildTargets(r.Context(), m, scope, now)
	if aerr != nil {
		return aerr
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (d Deps) handlePutTargets(w http.ResponseWriter, r *http.Request) *APIError {
	var body attribution.TargetsUpdate
	if aerr := decodeBody(w, r, &body); aerr != nil {
		return aerr
	}
	q := r.URL.Query()
	now := d.now()
	m, aerr := d.targetsMonth(q.Get("month"), now)
	if aerr != nil {
		return aerr
	}
	scope, aerr := d.scopeParam(q.Get("scope"))
	if aerr != nil {
		return aerr
	}

	monthFrom := monthcost.Of(now).String()
	if v := strings.TrimSpace(body.MonthFrom); v != "" {
		parsed, err := monthcost.Parse(v)
		if err != nil {
			return errInvalidTarget("month_from " + quote(v) + " is not a valid month (YYYY-MM).")
		}
		monthFrom = parsed.String()
	}
	if len(body.Targets) == 0 {
		return errInvalidTarget("targets must contain at least one entry.")
	}
	var entries []store.Target
	seen := map[string]bool{}
	for i, e := range body.Targets {
		p := strings.ToLower(strings.TrimSpace(e.Product))
		idx := "targets[" + strconv.Itoa(i) + "]: "
		switch {
		case len(p) > maxTargetProductLen:
			return errInvalidTarget(idx + "product name is longer than 64 characters.")
		case p != attribution.AccountProduct && !store.ValidProductName(p):
			return errInvalidTarget(idx + "product " + quote(e.Product) + ` must be "*" or a name like example-product (lowercase letters, digits, - and _).`)
		case seen[p]:
			return errInvalidTarget(idx + "product " + quote(p) + " appears more than once.")
		}
		seen[p] = true
		t := store.Target{Product: p, MonthFrom: monthFrom, Amount: e.Amount}
		if err := store.ValidateTarget(t); err != nil {
			return errInvalidTarget(idx + "amount must be a finite number >= 0, or null to clear.")
		}
		entries = append(entries, t)
	}

	ctx := r.Context()
	err := d.Store.WithTx(ctx, func(tx *sql.Tx) error {
		if err := store.PutTargets(ctx, tx, entries, now); err != nil {
			return err
		}
		for _, t := range entries {
			if t.Product != attribution.AccountProduct {
				continue
			}
			v := ""
			if t.Amount != nil {
				v = strconv.FormatFloat(*t.Amount, 'f', -1, 64)
			}
			if err := store.PutSettings(ctx, tx, map[string]string{store.KeyMonthlyTarget: v}, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidInput) {
			return errInvalidTarget(err.Error())
		}
		return ErrInternal(err)
	}
	resp, aerr := d.buildTargets(ctx, m, scope, now)
	if aerr != nil {
		return aerr
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}
