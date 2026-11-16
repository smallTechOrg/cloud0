package httpapi

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/smallTechOrg/cloud0/internal/attribution"
	"github.com/smallTechOrg/cloud0/internal/costtable"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// registerAttributionWeekly wires the weekly product / environment tables:
// the Cost table's shape (rows = ISO weeks, deltas, anomaly flags) with one
// column per product (or environment) instead of per service.
func (d Deps) registerAttributionWeekly(r *router) {
	r.handle(http.MethodGet, apiPrefix+"/cost/by-product/weeks", d.handleWeeklyBy(attribution.DimensionProduct))
	r.handle(http.MethodGet, apiPrefix+"/cost/by-environment/weeks", d.handleWeeklyBy(attribution.DimensionEnvironment))
}

// groupOrder puts real products/environments first, then (shared), then the
// unattributed bucket, so the residue is always at the right edge.
func groupOrder(kind string) int {
	switch kind {
	case attribution.KindShared:
		return 1
	case attribution.KindUnattributed:
		return 2
	default:
		return 0
	}
}

func (d Deps) handleWeeklyBy(dim string) Handler {
	return func(w http.ResponseWriter, r *http.Request) *APIError {
		ctx := r.Context()
		q := r.URL.Query()
		now := d.now()
		if d.Attribution == nil || d.Attribution.NewEngine == nil {
			return errAttributionNotConfigured()
		}
		scope, aerr := d.scopeParam(q.Get("scope"))
		if aerr != nil {
			return aerr
		}
		from, to, aerr := d.weekRangeParams(q.Get("from"), q.Get("to"), now)
		if aerr != nil {
			return aerr
		}
		mode, aerr := d.bdAllocateParam(ctx, q.Get("allocate"))
		if aerr != nil {
			return aerr
		}
		eng, aerr := d.bdEngine(ctx)
		if aerr != nil {
			return aerr
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

		type colInfo struct {
			label, kind string
			net         float64 // range total, for ordering
		}
		cols := map[string]*colInfo{}
		sums := map[isoweek.Week]map[string]costtable.Sum{}
		for wk, snap := range latest {
			groups, _, err := attribution.BuildBreakdownKeyed(bdCostRows(rows[snap.ID]), eng, dim, d.Attribution.Allocator, mode, costtable.ColumnKeyFor)
			if err != nil {
				if errors.Is(err, attribution.ErrInvalidAllocate) {
					return errInvalidAllocate(string(mode))
				}
				return ErrInternal(err)
			}
			byKey := make(map[string]costtable.Sum, len(groups))
			for _, g := range groups {
				tot := g.Cells[attribution.ColumnTotal]
				if g.Net == 0 && tot.Gross == 0 {
					continue // an empty bucket (e.g. (shared) fully allocated away)
				}
				byKey[g.Key] = costtable.Sum{Net: g.Net, Gross: tot.Gross, Credits: tot.Credits}
				ci := cols[g.Key]
				if ci == nil {
					ci = &colInfo{label: g.Label, kind: g.Kind}
					cols[g.Key] = ci
				}
				if isoweek.Compare(wk, from) >= 0 {
					ci.net += g.Net
				}
			}
			sums[wk] = byKey
		}

		keys := make([]string, 0, len(cols))
		for k := range cols {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := cols[keys[i]], cols[keys[j]]
			if oa, ob := groupOrder(a.kind), groupOrder(b.kind); oa != ob {
				return oa < ob
			}
			if math.Abs(a.net-b.net) > 1e-9 {
				return a.net > b.net
			}
			return keys[i] < keys[j]
		})
		groups := make([]costtable.Column, 0, len(keys))
		for _, k := range keys {
			groups = append(groups, costtable.Column{Key: k, Label: cols[k].label, Group: cols[k].kind})
		}

		notes, err := store.AnnotationsInRange(ctx, d.Store.DB, from, to)
		if err != nil {
			return ErrInternal(err)
		}
		settings, err := store.GetThresholds(ctx, d.Store.DB)
		if err != nil {
			return ErrInternal(err)
		}
		table, err := costtable.AssembleGrouped(costtable.GroupedInput{
			Input: costtable.Input{From: from, To: to, Latest: latest, Annotations: notes,
				Thresholds: costtable.ThresholdsFrom(settings), Scope: scope, Now: now.Truncate(time.Second), Settle: d.settle()},
			Sums: sums, Groups: groups,
		})
		if err != nil {
			return ErrInternal(err)
		}
		writeJSON(w, http.StatusOK, weeklyBreakdown{Table: table, Dimension: dim, Allocate: mode})
		return nil
	}
}

// weeklyBreakdown is the GET /cost/by-{product,environment}/weeks body: the
// Cost table shape plus what the columns are and how (shared) was treated.
type weeklyBreakdown struct {
	costtable.Table
	Dimension string                   `json:"dimension"`
	Allocate  attribution.AllocateMode `json:"allocate"`
}
