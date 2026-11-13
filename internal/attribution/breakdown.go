package attribution

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

const maxRuleIDs = 5

var filterNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// NormalizeFilterName lower-cases/trims a product or environment filter and
// reports whether it is a valid name or one of the special bucket keys.
func NormalizeFilterName(s string) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(s))
	switch n {
	case KeyShared, KeyUnattributed, KeyNoEnvironment:
		return n, true
	}
	return n, filterNamePattern.MatchString(n)
}

// MatchesFilter reports whether the evaluation passes the optional env /
// product filters (already normalised; "" = no filter).
func MatchesFilter(ev Evaluation, env, product string) bool {
	if env != "" {
		e := strings.ToLower(ev.Environment)
		if e == "" || e == KeyNoEnvironment {
			if env != KeyNoEnvironment && env != KeyUnattributed {
				return false
			}
		} else if env != e {
			return false
		}
	}
	if product != "" {
		switch product {
		case KeyShared:
			if !ev.Shared {
				return false
			}
		case KeyUnattributed:
			if ev.Shared || ev.Product != "" {
				return false
			}
		default:
			if ev.Shared || strings.ToLower(ev.Product) != product {
				return false
			}
		}
	}
	return true
}

// FilterRows keeps the rows whose evaluation passes the filters.
func FilterRows(rows []CostRow, eng Engine, env, product string) []CostRow {
	if env == "" && product == "" {
		return rows
	}
	out := make([]CostRow, 0, len(rows))
	for _, r := range rows {
		if MatchesFilter(eng.Evaluate(r.Row), env, product) {
			out = append(out, r)
		}
	}
	return out
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// BreakdownCoverage splits the rows' net by attribution outcome (product
// dimension, regardless of which breakdown is shown).
func BreakdownCoverage(rows []CostRow, eng Engine) Coverage {
	var c Coverage
	for _, r := range rows {
		ev := eng.Evaluate(r.Row)
		c.TotalNet += r.Net
		switch {
		case ev.Shared:
			c.SharedNet += r.Net
		case ev.Product == "":
			c.UnattributedNet += r.Net
		default:
			c.AttributedNet += r.Net
		}
	}
	if c.TotalNet != 0 {
		c.AttributedPct = round3(100 * c.AttributedNet / c.TotalNet)
		c.SharedPct = round3(100 * c.SharedNet / c.TotalNet)
		c.UnattributedPct = round3(100 - c.AttributedPct - c.SharedPct)
	}
	return c
}

// DefaultColumnKey is the cell key BuildBreakdown uses per service when the
// caller supplies none: the trimmed, lower-cased service description.
func DefaultColumnKey(service string) string { return strings.ToLower(strings.TrimSpace(service)) }

// BuildBreakdown groups rows by dimension ("product" | "environment"),
// allocates the shared group per mode and orders the result. Cells are keyed
// by DefaultColumnKey(service) plus "total".
func BuildBreakdown(rows []CostRow, eng Engine, dim string, alloc Allocator, mode AllocateMode) ([]Group, []string, error) {
	return BuildBreakdownKeyed(rows, eng, dim, alloc, mode, DefaultColumnKey)
}

type bdAcc struct {
	g     Group
	rules map[int64]float64
}

// BuildBreakdownKeyed is BuildBreakdown with a caller-supplied service →
// column-key mapping (the HTTP layer passes the Cost table's).
func BuildBreakdownKeyed(rows []CostRow, eng Engine, dim string, alloc Allocator, mode AllocateMode, colKey func(string) string) ([]Group, []string, error) {
	if dim != DimensionProduct && dim != DimensionEnvironment {
		return nil, nil, fmt.Errorf("attribution: unknown breakdown dimension %q", dim)
	}
	if !mode.Valid() {
		return nil, nil, fmt.Errorf("%w: %q", ErrInvalidAllocate, string(mode))
	}
	if alloc == nil {
		alloc = NewAllocator()
	}
	if colKey == nil {
		colKey = DefaultColumnKey
	}
	unKey, unKind := KeyUnattributed, KindUnattributed
	if dim == DimensionEnvironment {
		unKey = KeyNoEnvironment
	}
	groupKind := KindProduct
	if dim == DimensionEnvironment {
		groupKind = KindEnvironment
	}
	accs := map[string]*bdAcc{}
	get := func(key, kind string) *bdAcc {
		a, ok := accs[key]
		if !ok {
			a = &bdAcc{g: Group{Key: key, Label: key, Kind: kind, Cells: map[string]BreakdownCell{ColumnTotal: {}}, RuleIDs: []int64{}}, rules: map[int64]float64{}}
			accs[key] = a
		}
		return a
	}
	get(KeyShared, KindShared)
	get(unKey, unKind)

	for _, r := range rows {
		ev := eng.Evaluate(r.Row)
		var a *bdAcc
		var rid *int64
		switch {
		case ev.Shared:
			a, rid = get(KeyShared, KindShared), ev.RuleID
		case dim == DimensionProduct:
			rid = ev.RuleID
			if ev.Product == "" {
				a = get(KeyUnattributed, KindUnattributed)
			} else {
				a = get(ev.Product, groupKind)
			}
		default:
			rid = ev.EnvironmentRuleID
			if ev.Environment == "" || ev.Environment == KeyNoEnvironment {
				a = get(KeyNoEnvironment, KindUnattributed)
			} else {
				a = get(ev.Environment, groupKind)
			}
		}
		add := func(key string) {
			c := a.g.Cells[key]
			c.Net += r.Net
			c.Gross += r.Gross
			c.Credits += r.Credits
			a.g.Cells[key] = c
		}
		add(colKey(r.Service))
		add(ColumnTotal)
		if rid != nil {
			a.rules[*rid] += r.Net
		}
	}

	groups := make([]Group, 0, len(accs))
	for _, a := range accs {
		g := a.g
		for k, c := range g.Cells {
			if k != ColumnTotal && c == (BreakdownCell{}) {
				delete(g.Cells, k)
			}
		}
		g.DirectNet = g.Cells[ColumnTotal].Net
		g.Net = g.DirectNet
		ids := make([]int64, 0, len(a.rules))
		for id := range a.rules {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			if a.rules[ids[i]] != a.rules[ids[j]] {
				return a.rules[ids[i]] > a.rules[ids[j]]
			}
			return ids[i] < ids[j]
		})
		if len(ids) > maxRuleIDs {
			ids = ids[:maxRuleIDs]
		}
		g.RuleIDs = append(g.RuleIDs, ids...)
		groups = append(groups, g)
	}

	groups, notes, err := alloc.Allocate(groups, mode)
	if err != nil {
		return nil, nil, err
	}
	if notes == nil {
		notes = []string{}
	}
	rank := func(k string) int {
		switch k {
		case KindShared:
			return 1
		case KindUnattributed:
			return 2
		}
		return 0
	}
	sort.SliceStable(groups, func(i, j int) bool {
		ri, rj := rank(groups[i].Kind), rank(groups[j].Kind)
		if ri != rj {
			return ri < rj
		}
		if groups[i].Net != groups[j].Net {
			return groups[i].Net > groups[j].Net
		}
		return groups[i].Key < groups[j].Key
	})
	var total float64
	for _, g := range groups {
		total += g.Net
	}
	if total != 0 {
		for i := range groups {
			groups[i].SharePct = round3(100 * groups[i].Net / total)
		}
	}
	return groups, notes, nil
}
