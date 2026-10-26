package attribution

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/monthcost"
)

// tgtEngine attributes by resource-name prefix: "shared-*" is shared, "p1-*"
// and "p2-*" belong to products p1/p2, anything else is unattributed.
type tgtEngine struct{}

func (tgtEngine) Evaluate(r Row) Evaluation {
	switch {
	case strings.HasPrefix(r.ResourceName, "shared-"):
		return Evaluation{Shared: true}
	case strings.HasPrefix(r.ResourceName, "p1-"):
		return Evaluation{Product: "p1"}
	case strings.HasPrefix(r.ResourceName, "p2-"):
		return Evaluation{Product: "p2"}
	}
	return Evaluation{}
}
func (tgtEngine) Explain(Row) []TrailStep { return nil }
func (tgtEngine) Rules() []Rule           { return nil }

type noRulesEngine struct{}

func (noRulesEngine) Evaluate(Row) Evaluation { return Evaluation{} }
func (noRulesEngine) Explain(Row) []TrailStep { return nil }
func (noRulesEngine) Rules() []Rule           { return nil }

func tcost(name string, net float64) CostRow {
	return CostRow{Row: Row{ResourceName: name}, Net: net}
}

func fp(v float64) *float64 { return &v }

var oct = monthcost.Month{Year: 2026, Month: time.October}

func baseInput() TargetsInput {
	return TargetsInput{
		Month: oct, Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), SettleHours: 48, Currency: "INR",
		HasMonthData: true,
		MonthRows:    []monthcost.Row{{Month: "2026-10", Net: 600}, {Month: "2026-10", Net: 400}, {Month: "2026-09", Net: 9999}},
		Weeks: []TargetWeek{
			{Week: "2026-W41", Sunday: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC), Rows: []CostRow{tcost("p1-a", 50), tcost("p2-a", 20), tcost("shared-x", 10)}},
			{Week: "2026-W42", Sunday: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), Rows: []CostRow{tcost("p1-b", 10), tcost("zzz", 10)}},
		},
		Engine: tgtEngine{},
	}
}

func sumAll(r TargetsResponse) float64 {
	s := r.Shared.Net + r.Unattributed.Net
	for _, p := range r.Products {
		s += p.Net
	}
	return s
}

func TestTargets_ShareAllocationSumsToMonthNet(t *testing.T) {
	r := ComputeTargets(baseInput())
	if r.Account.Net != 1000 {
		t.Fatalf("account net = %v, want 1000 (other-month rows ignored)", r.Account.Net)
	}
	if math.Abs(sumAll(r)-r.Account.Net) > 0.005 {
		t.Fatalf("Σ = %v, account %v", sumAll(r), r.Account.Net)
	}
	// weeks total 100: p1 60, p2 20, shared 10, unattributed 10.
	if len(r.Products) != 2 || r.Products[0].Product != "p1" || math.Abs(r.Products[0].Net-600) > 1e-9 {
		t.Fatalf("products = %+v", r.Products)
	}
	if math.Abs(*r.Products[0].SharePct-60) > 1e-9 || math.Abs(r.Shared.SharePct-10) > 1e-9 || math.Abs(r.Unattributed.SharePct-10) > 1e-9 {
		t.Fatalf("shares: %+v %+v %+v", r.Products[0], r.Shared, r.Unattributed)
	}
	if r.Month != "2026-10" || r.MonthStart != "2026-10-01" || r.MonthEnd != "2026-10-31" || r.MonthStatus != "partial" {
		t.Fatalf("header = %+v", r)
	}
	if sw := r.ShareWeeks; len(sw) != 2 || sw[1] != "2026-W42" {
		t.Fatalf("share weeks = %v", sw)
	}
	// Sunday 18th is after today (16th) -> clamped.
	if r.CoveredUntil == nil || *r.CoveredUntil != "2026-10-16" {
		t.Fatalf("covered_until = %v", r.CoveredUntil)
	}
}

func TestTargets_PartialMonthProjectionAndStatus(t *testing.T) {
	in := baseInput()
	in.History = []TargetHistory{
		{Product: "*", MonthFrom: "2026-09", Amount: fp(2000)},
		{Product: "p1", MonthFrom: "2026-10", Amount: fp(1000)},
		{Product: "p2", MonthFrom: "2026-10", Amount: fp(100)},
		{Product: "ghost", MonthFrom: "2026-10", Amount: fp(50)},
	}
	r := ComputeTargets(in)
	wantProj := monthcost.Project(1000, oct, in.Now)
	if math.Abs(r.Account.Projected-wantProj) > 1e-9 || wantProj <= 1000 {
		t.Fatalf("account projected = %v, want %v", r.Account.Projected, wantProj)
	}
	if r.Account.Status != StatusOnTrack || r.Account.MonthFrom == nil || *r.Account.MonthFrom != "2026-09" {
		t.Fatalf("account = %+v", r.Account)
	}
	if math.Abs(*r.Account.Pct-50) > 1e-9 || math.Abs(*r.Account.Remaining-1000) > 1e-9 {
		t.Fatalf("pct/remaining = %v %v", *r.Account.Pct, *r.Account.Remaining)
	}
	byName := map[string]TargetRow{}
	for _, p := range r.Products {
		byName[p.Product] = p
	}
	// p1: net 600, projected ~1.9x -> 1000 target: projected > 1000 and <= 1100?
	p1 := byName["p1"]
	if want := TargetStatus(p1.Projected, fp(1000)); p1.Status != want {
		t.Fatalf("p1 status %s want %s", p1.Status, want)
	}
	if byName["p2"].Status != StatusOver { // net 200 projected ~ 400 vs 100
		t.Fatalf("p2 = %+v", byName["p2"])
	}
	if g := byName["ghost"]; g.Net != 0 || g.Target == nil || g.Status != StatusOnTrack {
		t.Fatalf("ghost (target, no cost) = %+v", g)
	}
	if math.Abs(sumAll(r)-r.Account.Net) > 0.005 {
		t.Fatalf("Σ changed with targets")
	}
}

func TestTargets_NoSnapshotsAndNoMonthData(t *testing.T) {
	in := baseInput()
	in.Weeks = nil
	in.History = []TargetHistory{{Product: "p1", MonthFrom: "2026-10", Amount: fp(10)}}
	r := ComputeTargets(in)
	if r.Account.Net != 1000 || len(r.ShareWeeks) != 0 || r.CoveredUntil != nil {
		t.Fatalf("resp = %+v", r)
	}
	if len(r.Products) != 1 || r.Products[0].Status != StatusNoData || r.Products[0].Net != 0 || r.Products[0].Pct != nil {
		t.Fatalf("products = %+v", r.Products)
	}
	if r.Shared.Net != 0 || r.Unattributed.Net != 0 {
		t.Fatalf("buckets = %+v %+v", r.Shared, r.Unattributed)
	}

	in = baseInput()
	in.HasMonthData, in.MonthRows = false, nil
	r = ComputeTargets(in)
	if r.Account.Status != StatusNoData || r.Account.Net != 0 || r.Account.Target != nil {
		t.Fatalf("account without month data = %+v", r.Account)
	}
	for _, p := range r.Products {
		if p.Status != StatusNoData || p.Net != 0 {
			t.Fatalf("product %+v", p)
		}
	}
}

func TestTargets_NoRulesEverythingUnattributed(t *testing.T) {
	in := baseInput()
	in.Engine = noRulesEngine{}
	r := ComputeTargets(in)
	if len(r.Products) != 0 || r.Shared.Net != 0 {
		t.Fatalf("products=%v shared=%v", r.Products, r.Shared)
	}
	if math.Abs(r.Unattributed.Net-1000) > 0.005 || math.Abs(r.Unattributed.SharePct-100) > 1e-9 {
		t.Fatalf("unattributed = %+v", r.Unattributed)
	}
}

func TestTargets_ZeroWeekTotalIsNoData(t *testing.T) {
	in := baseInput()
	in.Weeks = []TargetWeek{{Week: "2026-W41", Sunday: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC), Rows: []CostRow{tcost("p1-a", 0)}}}
	r := ComputeTargets(in)
	if math.IsNaN(r.Shared.Net) || math.IsNaN(r.Unattributed.Net) {
		t.Fatal("NaN leaked")
	}
	for _, p := range r.Products {
		if p.Status != StatusNoData || math.IsNaN(p.Net) {
			t.Fatalf("%+v", p)
		}
	}
}

func TestTargets_EffectiveMonthResolution(t *testing.T) {
	h := []TargetHistory{
		{Product: "p1", MonthFrom: "2026-08", Amount: fp(100)},
		{Product: "p1", MonthFrom: "2026-10", Amount: fp(300)},
		{Product: "p1", MonthFrom: "2026-12", Amount: nil}, // cleared from Dec
		{Product: "*", MonthFrom: "2026-11", Amount: fp(5000)},
	}
	cases := []struct {
		month string
		want  *float64
	}{{"2026-07", nil}, {"2026-08", fp(100)}, {"2026-09", fp(100)}, {"2026-10", fp(300)}, {"2026-11", fp(300)}}
	for _, c := range cases {
		got, ok := ResolveTargets(h, c.month)["p1"]
		switch {
		case c.want == nil && ok:
			t.Errorf("%s: want none, got %+v", c.month, got)
		case c.want != nil && (!ok || got.Amount == nil || *got.Amount != *c.want):
			t.Errorf("%s: got %+v want %v", c.month, got, *c.want)
		}
	}
	if got := ResolveTargets(h, "2027-01")["p1"]; got.Amount != nil {
		t.Errorf("cleared in Dec should resolve nil, got %+v", got)
	}
	if _, ok := ResolveTargets(h, "2026-10")["*"]; ok {
		t.Error("account target from Nov must not apply in Oct")
	}

	// A target set with month_from 2026-11 does not show in an October response.
	in := baseInput()
	in.History = h
	r := ComputeTargets(in)
	if r.Account.Target != nil {
		t.Fatalf("account target = %v", *r.Account.Target)
	}
	if len(r.Products) == 0 || r.Products[0].Product != "p1" || *r.Products[0].Target != 300 || *r.Products[0].MonthFrom != "2026-10" {
		t.Fatalf("p1 = %+v", r.Products)
	}
	// History is newest-first and complete.
	if len(r.History) != 4 {
		t.Fatalf("history = %v", r.History)
	}
}

func TestTargets_AccountFallbackSettingAndClearedRow(t *testing.T) {
	in := baseInput()
	in.AccountFallback = fp(4000)
	r := ComputeTargets(in)
	if r.Account.Target == nil || *r.Account.Target != 4000 || r.Account.MonthFrom != nil {
		t.Fatalf("fallback account = %+v", r.Account)
	}
	if r.Account.Status == StatusNoTarget || math.Abs(*r.Account.Pct-25) > 1e-9 {
		t.Fatalf("fallback status/pct = %+v", r.Account)
	}
	// A cleared `*` row beats the fallback.
	in.History = []TargetHistory{{Product: "*", MonthFrom: "2026-09", Amount: nil}}
	r = ComputeTargets(in)
	if r.Account.Target != nil || r.Account.Status != StatusNoTarget {
		t.Fatalf("cleared account = %+v", r.Account)
	}
}
