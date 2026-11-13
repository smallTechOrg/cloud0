package attribution

import (
	"sort"
	"time"

	"github.com/smallTechOrg/cloud0/internal/monthcost"
)

// TargetWeek is one share week: the rows of the latest weekly snapshot whose
// Monday falls in the month.
type TargetWeek struct {
	Week   string    // ISO week id, e.g. 2026-W42
	Sunday time.Time // last day of the week (UTC date)
	Rows   []CostRow
}

// TargetsInput is everything ComputeTargets needs; it performs no I/O.
type TargetsInput struct {
	Month        monthcost.Month
	Now          time.Time
	SettleHours  int
	Currency     string
	HasMonthData bool            // a month snapshot exists
	MonthRows    []monthcost.Row // scope-filtered rows of the month snapshot
	Weeks        []TargetWeek    // share weeks, ascending
	Engine       Engine
	History      []TargetHistory // every stored targets row
	// AccountFallback is settings target.monthly_total; used for the account
	// row only when the history holds no `*` row at all.
	AccountFallback *float64
}

// ResolveTargets returns, per product, the newest history row whose
// MonthFrom <= month ("a target applies from month_from until superseded").
// A product whose newest row has a nil Amount is present with a nil Amount
// (cleared), so callers can tell "cleared" from "never set".
func ResolveTargets(history []TargetHistory, month string) map[string]TargetHistory {
	best := map[string]TargetHistory{}
	for _, h := range history {
		if h.MonthFrom > month { // YYYY-MM sorts lexicographically
			continue
		}
		cur, ok := best[h.Product]
		if !ok || h.MonthFrom > cur.MonthFrom || (h.MonthFrom == cur.MonthFrom && h.CreatedAt.After(cur.CreatedAt)) {
			best[h.Product] = h
		}
	}
	return best
}

// ComputeTargets builds the GET /targets body. The account figures come from
// monthcost.SummaryWithSettle; every product's figure is share × account where
// share is the product's net over the share weeks, so products + shared +
// unattributed sum to the account net.
func ComputeTargets(in TargetsInput) TargetsResponse {
	m := in.Month
	window := m.Bounds()
	line := monthcost.SummaryWithSettle(in.MonthRows, m, nil, in.Now, in.SettleHours)
	accNet, accProj := line.Net, line.Projected
	if !in.HasMonthData {
		accNet, accProj = 0, 0
	}

	resp := TargetsResponse{
		Month: m.String(), MonthStart: window.Start.Format("2006-01-02"),
		MonthEnd:    window.End.AddDate(0, 0, -1).Format("2006-01-02"),
		Currency:    in.Currency,
		GeneratedAt: in.Now.UTC().Truncate(time.Second),
		MonthStatus: line.Status,
		ShareWeeks:  []string{},
		Products:    []TargetRow{},
		History:     append([]TargetHistory{}, in.History...),
	}
	sort.SliceStable(resp.History, func(i, j int) bool {
		a, b := resp.History[i], resp.History[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		if a.Product != b.Product {
			return a.Product < b.Product
		}
		return a.MonthFrom > b.MonthFrom
	})

	// Share weeks and bucket nets.
	byProduct := map[string]float64{}
	var shared, unattr, total float64
	for _, w := range in.Weeks {
		resp.ShareWeeks = append(resp.ShareWeeks, w.Week)
		day := w.Sunday.UTC()
		if today := time.Date(in.Now.Year(), in.Now.Month(), in.Now.Day(), 0, 0, 0, 0, time.UTC); day.After(today) {
			day = today
		}
		d := day.Format("2006-01-02")
		resp.CoveredUntil = &d
		for _, r := range w.Rows {
			total += r.Net
			ev := in.Engine.Evaluate(r.Row)
			switch {
			case ev.Shared:
				shared += r.Net
			case ev.Product != "":
				byProduct[ev.Product] += r.Net
			default:
				unattr += r.Net
			}
		}
	}
	haveShares := len(in.Weeks) > 0 && (total > 1e-9 || total < -1e-9)
	share := func(v float64) float64 {
		if !haveShares {
			return 0
		}
		return v / total
	}

	// Targets in force for this month.
	eff := ResolveTargets(in.History, m.String())
	targetOf := func(p string) (*float64, *string) {
		h, ok := eff[p]
		if !ok || h.Amount == nil {
			return nil, nil
		}
		v, mf := *h.Amount, h.MonthFrom
		return &v, &mf
	}

	build := func(p string, net, proj float64, hasData bool, sharePct *float64) TargetRow {
		t, mf := targetOf(p)
		row := TargetRow{Product: p, Target: t, MonthFrom: mf, Net: net, Projected: proj, SharePct: sharePct}
		if !hasData {
			row.Status = StatusNoData
			return row
		}
		row.Status = TargetStatus(proj, t)
		if t != nil {
			if *t > 0 {
				pct := 100 * net / *t
				row.Pct = &pct
			}
			rem := *t - net
			row.Remaining = &rem
		}
		return row
	}

	// Account row.
	acc := build(AccountProduct, accNet, accProj, in.HasMonthData, nil)
	if !historyHas(in.History, AccountProduct) && in.AccountFallback != nil {
		v := *in.AccountFallback
		acc.Target, acc.MonthFrom = &v, nil
		if in.HasMonthData {
			acc.Status = TargetStatus(accProj, acc.Target)
			if v > 0 {
				pct := 100 * accNet / v
				acc.Pct = &pct
			}
			rem := v - accNet
			acc.Remaining = &rem
		}
	}
	resp.Account = acc

	// Product rows: union of seen products and products with a target.
	names := map[string]bool{}
	for p := range byProduct {
		names[p] = true
	}
	for p, h := range eff {
		if p != AccountProduct && h.Amount != nil {
			names[p] = true
		}
	}
	for p := range names {
		s := share(byProduct[p])
		sp := 100 * s
		resp.Products = append(resp.Products, build(p, s*accNet, s*accProj, haveShares && in.HasMonthData, &sp))
	}
	sort.SliceStable(resp.Products, func(i, j int) bool {
		a, b := resp.Products[i], resp.Products[j]
		if a.Net != b.Net {
			return a.Net > b.Net
		}
		return a.Product < b.Product
	})

	resp.Shared = BucketProgress{Net: share(shared) * accNet, Projected: share(shared) * accProj, SharePct: 100 * share(shared)}
	resp.Unattributed = BucketProgress{Net: share(unattr) * accNet, Projected: share(unattr) * accProj, SharePct: 100 * share(unattr)}
	return resp
}

func historyHas(h []TargetHistory, product string) bool {
	for _, x := range h {
		if x.Product == product {
			return true
		}
	}
	return false
}
