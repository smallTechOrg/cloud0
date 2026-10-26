package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/smallTechOrg/cloud0/internal/attribution"
	"github.com/smallTechOrg/cloud0/internal/costtable"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// registerAttributionBreakdown wires GET /cost/by-product and /cost/by-environment.
func (d Deps) registerAttributionBreakdown(r *router) {
	r.handle(http.MethodGet, apiPrefix+"/cost/by-product", d.handleBreakdownBy(attribution.DimensionProduct))
	r.handle(http.MethodGet, apiPrefix+"/cost/by-environment", d.handleBreakdownBy(attribution.DimensionEnvironment))
}

func errAttributionNotConfigured() *APIError {
	return (&APIError{Status: http.StatusServiceUnavailable, Code: "attribution_not_configured",
		Message: "Attribution is not configured on this server."}).
		WithHint("Restart cloud0 with the attribution engine wired (this is a deployment problem, not a data problem).")
}

func errInvalidAllocate(v string) *APIError {
	return (&APIError{Status: http.StatusBadRequest, Code: attribution.CodeInvalidAllocate,
		Message: "The allocate value " + quote(v) + " is not valid."}).WithHint("Use allocate=none, allocate=even or allocate=proportional.")
}

func errInvalidDimension(param, v string) *APIError {
	return (&APIError{Status: http.StatusBadRequest, Code: attribution.CodeInvalidDimension,
		Message: "The " + param + " filter " + quote(v) + " is not a valid name."}).
		WithHint("Use a lower-case name such as example-product, or (shared), (unattributed) or (no environment).")
}

// bdEngine compiles the engine from the stored rules (built-in label rules
// are part of the engine itself).
func (d Deps) bdEngine(ctx context.Context) (attribution.Engine, *APIError) {
	if d.Attribution == nil || d.Attribution.NewEngine == nil {
		return nil, errAttributionNotConfigured()
	}
	stored, err := store.ListAttributionRules(ctx, d.Store.DB)
	if err != nil {
		return nil, ErrInternal(err)
	}
	rules := make([]attribution.Rule, 0, len(stored))
	for _, s := range stored {
		id := s.ID
		rules = append(rules, attribution.Rule{ID: &id, Priority: s.Priority,
			Match:   attribution.Match{Field: s.MatchField, Op: s.MatchOp, Key: s.MatchKey, Value: s.MatchValue},
			Action:  attribution.Action{Product: s.Product, Environment: s.Environment, Shared: s.Shared},
			Enabled: s.Enabled, Source: s.Source, Note: s.Note})
	}
	eng, err := d.Attribution.NewEngine(rules)
	if err != nil {
		return nil, ErrInternal(err)
	}
	return eng, nil
}

// bdLabels parses labels_json ([{"key":..,"value":..}]); nil when absent.
func bdLabels(raw *string) map[string]string {
	if raw == nil {
		return nil
	}
	var kv []struct{ Key, Value string }
	if json.Unmarshal([]byte(*raw), &kv) != nil {
		return nil
	}
	m := make(map[string]string, len(kv))
	for _, e := range kv {
		m[e.Key] = e.Value
	}
	return m
}

func bdCostRows(rows []store.SnapshotRow) []attribution.CostRow {
	out := make([]attribution.CostRow, len(rows))
	for i, r := range rows {
		cr := attribution.CostRow{Row: attribution.Row{ProjectID: r.ProjectID, Service: r.Service, SKU: r.SKU, Labels: bdLabels(r.LabelsJSON)},
			Gross: r.Gross, Credits: r.Credits, Net: r.Net, LineItems: r.LineItems}
		if r.ResourceName != nil {
			cr.ResourceName = *r.ResourceName
		}
		if r.ResourceGlobalName != nil {
			cr.ResourceGlobalName = *r.ResourceGlobalName
		}
		out[i] = cr
	}
	return out
}

// bdAllocateParam resolves the allocate mode: query value, else the persisted default, else none.
func (d Deps) bdAllocateParam(ctx context.Context, v string) (attribution.AllocateMode, *APIError) {
	if v != "" {
		m := attribution.AllocateMode(v)
		if !m.Valid() {
			return "", errInvalidAllocate(v)
		}
		return m, nil
	}
	kv, err := store.GetSettingsWithPrefix(ctx, d.Store.DB, store.KeyAttributionAllocate)
	if err != nil {
		return "", ErrInternal(err)
	}
	if m := attribution.AllocateMode(kv[store.KeyAttributionAllocate]); m.Valid() {
		return m, nil
	}
	return attribution.AllocateNone, nil
}

func bdFilter(param, v string) (string, *APIError) {
	if v == "" {
		return "", nil
	}
	n, ok := attribution.NormalizeFilterName(v)
	if !ok {
		return "", errInvalidDimension(param, v)
	}
	return n, nil
}

func (d Deps) handleBreakdownBy(dim string) Handler {
	return func(w http.ResponseWriter, r *http.Request) *APIError {
		ctx := r.Context()
		q := r.URL.Query()
		now := d.now()
		if d.Attribution == nil || d.Attribution.NewEngine == nil {
			return errAttributionNotConfigured()
		}
		week, aerr := d.weekParam(q.Get("week"), now)
		if aerr != nil {
			return aerr
		}
		scope, aerr := d.scopeParam(q.Get("scope"))
		if aerr != nil {
			return aerr
		}
		mode, aerr := d.bdAllocateParam(ctx, q.Get("allocate"))
		if aerr != nil {
			return aerr
		}
		var envF, prodF string
		if dim == attribution.DimensionProduct {
			envF, aerr = bdFilter("env", q.Get("env"))
		} else {
			prodF, aerr = bdFilter("product", q.Get("product"))
		}
		if aerr != nil {
			return aerr
		}

		status := isoweek.StatusAt(week, now, d.settle())
		out := attribution.Breakdown{
			Dimension: dim, Week: week.String(), WeekStart: store.FormatDate(week.Start()), WeekEnd: store.FormatDate(week.Sunday()),
			WeekStatus: string(status), Scope: scope, Allocate: mode, GeneratedAt: now.Truncate(time.Second),
			Columns: []attribution.Column{}, Groups: []attribution.Group{},
			Total: attribution.BreakdownTotal{Cells: map[string]attribution.BreakdownCell{}}, Notes: []string{},
		}
		if envF != "" {
			out.Filter.Env = &envF
		}
		if prodF != "" {
			out.Filter.Product = &prodF
		}

		latest, err := store.LatestPerWeek(ctx, d.Store.DB, week, week)
		if err != nil {
			return ErrInternal(err)
		}
		snap, ok := latest[week]
		if !ok {
			out.Notes = append(out.Notes, "No snapshot for "+week.String()+" yet; run a refresh (POST /api/v1/refresh) to fetch it.")
			out.Reconciliation.OK = true
			writeJSON(w, http.StatusOK, out)
			return nil
		}
		eng, aerr := d.bdEngine(ctx)
		if aerr != nil {
			return aerr
		}
		storeRows, err := store.SnapshotRows(ctx, d.Store.DB, snap.ID, scope)
		if err != nil {
			return ErrInternal(err)
		}
		table, err := costtable.Assemble(costtable.Input{From: week, To: week, Latest: latest,
			Rows: map[int64][]store.SnapshotRow{snap.ID: storeRows}, Scope: scope, Now: now.Truncate(time.Second), Settle: d.settle()})
		if err != nil {
			return ErrInternal(err)
		}
		costTotal := table.Rows[0].Cells[costtable.KeyTotal].Net

		all := bdCostRows(storeRows)
		rows := attribution.FilterRows(all, eng, envF, prodF)
		filtered := envF != "" || prodF != ""
		out.LabelsCaptured = false
		for _, sr := range storeRows {
			if sr.LabelsJSON != nil {
				out.LabelsCaptured = true
				break
			}
		}

		groups, notes, err := attribution.BuildBreakdownKeyed(rows, eng, dim, d.Attribution.Allocator, mode, costtable.ColumnKeyFor)
		if err != nil {
			if errors.Is(err, attribution.ErrInvalidAllocate) {
				return errInvalidAllocate(string(mode))
			}
			return ErrInternal(err)
		}
		out.Groups, out.Notes = groups, notes

		// Columns: the Cost table's service columns + total, minus week/%change/stubs.
		extraSet := map[string]float64{}
		totals := map[string]attribution.BreakdownCell{}
		for _, row := range rows {
			key := costtable.ColumnKeyFor(row.Service)
			if costtable.IsExtraKey(key) {
				extraSet[key[len(costtable.ExtraKeyPrefix):]] += row.Net
			}
			for _, k := range []string{key, attribution.ColumnTotal} {
				c := totals[k]
				c.Net += row.Net
				c.Gross += row.Gross
				c.Credits += row.Credits
				totals[k] = c
			}
		}
		extras := make([]string, 0, len(extraSet))
		for s, n := range extraSet {
			if n != 0 {
				extras = append(extras, s)
			}
		}
		for _, c := range costtable.Columns(extras) {
			if c.Kind == costtable.KindService || c.Kind == costtable.KindTotal {
				out.Columns = append(out.Columns, attribution.Column{Key: c.Key, Label: c.Label, Kind: string(c.Kind), Services: c.Services, Extra: c.Extra})
			}
		}
		for k, c := range totals {
			if k == attribution.ColumnTotal || c != (attribution.BreakdownCell{}) {
				out.Total.Cells[k] = c
			}
		}
		if _, ok := out.Total.Cells[attribution.ColumnTotal]; !ok {
			out.Total.Cells[attribution.ColumnTotal] = attribution.BreakdownCell{}
		}

		out.Snapshot = &attribution.SnapshotRef{ID: snap.ID, FetchedAt: snap.FetchedAt.UTC(), StatusAtFetch: string(snap.StatusAtFetch),
			RunID: snap.RunID, RowCount: snap.RowCount, Stale: costtable.Stale(snap.StatusAtFetch, status)}
		out.Coverage = attribution.BreakdownCoverage(rows, eng)

		if filtered {
			costTotal = out.Total.Cells[attribution.ColumnTotal].Net
			out.Notes = append(out.Notes, "Filtered view: reconciled against the filtered net total, not the whole Cost table.")
		}
		var sum float64
		for _, g := range groups {
			sum += g.Net
		}
		diff := sum - costTotal
		out.Reconciliation = attribution.Reconciliation{CostTableTotal: costTotal, BreakdownTotal: sum, Diff: diff,
			OK: math.Abs(diff) <= attribution.ReconcileTolerance}
		writeJSON(w, http.StatusOK, out)
		return nil
	}
}

// breakdownFilterHook serves GET /snapshots/{id}/breakdown when `product` or
// `environment` is given: rows are filtered through the engine first, then
// grouped exactly like store.Breakdown. handled=false means no filter was
// requested and the caller continues with the plain SQL path.
func (d Deps) breakdownFilterHook(w http.ResponseWriter, r *http.Request, bq store.BreakdownQuery) (handled bool, aerr *APIError) {
	q := r.URL.Query()
	if q.Get("product") == "" && q.Get("environment") == "" {
		return false, nil
	}
	prod, aerr := bdFilter("product", q.Get("product"))
	if aerr != nil {
		return true, aerr
	}
	env, aerr := bdFilter("environment", q.Get("environment"))
	if aerr != nil {
		return true, aerr
	}
	ctx := r.Context()
	eng, aerr := d.bdEngine(ctx)
	if aerr != nil {
		return true, aerr
	}
	storeRows, err := store.SnapshotRows(ctx, d.Store.DB, bq.SnapshotID, bq.Scope)
	if err != nil {
		return true, ErrInternal(err)
	}
	keep := make([]store.SnapshotRow, 0, len(storeRows))
	for i, cr := range bdCostRows(storeRows) {
		if attribution.MatchesFilter(eng.Evaluate(cr.Row), env, prod) {
			keep = append(keep, storeRows[i])
		}
	}
	writeJSON(w, http.StatusOK, costtable.Breakdown(bdGroup(keep, bq)))
	return true, nil
}

// bdGroup mirrors store.Breakdown's grouping and ordering over in-memory rows.
func bdGroup(rows []store.SnapshotRow, bq store.BreakdownQuery) store.BreakdownResult {
	type gk struct{ a, b, c string }
	idx := map[gk]int{}
	res := store.BreakdownResult{Query: bq, Rows: []store.BreakdownRow{}}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for _, r := range rows {
		var k gk
		br := store.BreakdownRow{Level: bq.Level}
		switch bq.Level {
		case store.LevelService:
			k, br.Key = gk{a: r.Service}, r.Service
		case store.LevelSKU:
			if r.Service != bq.Service {
				continue
			}
			k, br.Key, br.SKUID = gk{a: r.SKU}, r.SKU, r.SKUID
		default:
			if r.Service != bq.Service || r.SKU != bq.SKU {
				continue
			}
			k = gk{r.ProjectID, str(r.ResourceName), str(r.ResourceGlobalName)}
			br.ProjectID, br.ResourceName, br.ResourceGlobalName = r.ProjectID, r.ResourceName, r.ResourceGlobalName
			br.Key = store.UnattributedKey
			if r.ResourceName != nil {
				br.Key = *r.ResourceName
			}
		}
		i, ok := idx[k]
		if !ok {
			i = len(res.Rows)
			idx[k] = i
			res.Rows = append(res.Rows, br)
		} else if bq.Level == store.LevelSKU && r.SKUID < res.Rows[i].SKUID {
			res.Rows[i].SKUID = r.SKUID
		}
		res.Rows[i].Gross += r.Gross
		res.Rows[i].Credits += r.Credits
		res.Rows[i].Net += r.Net
		res.Rows[i].LineItems += r.LineItems
		res.Totals.Gross += r.Gross
		res.Totals.Credits += r.Credits
		res.Totals.Net += r.Net
	}
	sort.SliceStable(res.Rows, func(i, j int) bool {
		a, b := res.Rows[i], res.Rows[j]
		if a.Net != b.Net {
			return a.Net > b.Net
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return strings.Compare(a.ProjectID, b.ProjectID) < 0
	})
	return res
}
