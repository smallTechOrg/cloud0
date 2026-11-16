package attribution

import (
	"errors"
	"math"
	"testing"
)

// fakeEngine decides by the row's ResourceName ("" = no resource).
type fakeEngine struct{ byName map[string]Evaluation }

func (f fakeEngine) Evaluate(r Row) Evaluation { return f.byName[r.ResourceName] }
func (f fakeEngine) Explain(Row) []TrailStep   { return nil }
func (f fakeEngine) Rules() []Rule             { return nil }

func bdI64(v int64) *int64 { return &v }

func cr(name, svc string, gross, credits float64) CostRow {
	return CostRow{Row: Row{ResourceName: name, Service: svc}, Gross: gross, Credits: credits, Net: gross + credits}
}

func testEngine() fakeEngine {
	return fakeEngine{byName: map[string]Evaluation{
		"a-prod":  {Product: "alpha", Environment: "prod", RuleID: bdI64(0), EnvironmentRuleID: bdI64(0)},
		"a-dev":   {Product: "alpha", Environment: "dev", RuleID: bdI64(2), EnvironmentRuleID: bdI64(3)},
		"b-prod":  {Product: "beta", Environment: "prod", RuleID: bdI64(5), EnvironmentRuleID: bdI64(3)},
		"shr":     {Shared: true, Environment: "prod", RuleID: bdI64(9)},
		"orphan":  {},
		"envonly": {Environment: "dev", EnvironmentRuleID: bdI64(3)},
	}}
}

func testRows() []CostRow {
	return []CostRow{
		cr("a-prod", "Compute Engine", 100, 0),
		cr("a-dev", "Cloud Run", 60, -10),
		cr("b-prod", "Compute Engine", 50, 0),
		cr("shr", "Networking", 30, 0),
		cr("shr", "Compute Engine", 10, 0),
		cr("orphan", "Cloud Run", 7, 0),
		cr("envonly", "Cloud Run", 3, 0),
	}
}

func sumNet(gs []Group) (s float64) {
	for _, g := range gs {
		s += g.Net
	}
	return
}

func TestBreakdownByProductNone(t *testing.T) {
	gs, notes, err := BuildBreakdown(testRows(), testEngine(), DimensionProduct, NewAllocator(), AllocateNone)
	if err != nil || len(notes) != 0 {
		t.Fatalf("err=%v notes=%v", err, notes)
	}
	want := []string{"alpha", "beta", KeyShared, KeyUnattributed}
	if len(gs) != 4 {
		t.Fatalf("groups = %+v", gs)
	}
	for i, k := range want {
		if gs[i].Key != k {
			t.Fatalf("order[%d] = %s, want %s", i, gs[i].Key, k)
		}
	}
	if gs[0].DirectNet != 150 || gs[0].Net != 150 || gs[0].AllocatedNet != 0 {
		t.Fatalf("alpha = %+v", gs[0])
	}
	if c := gs[0].Cells["compute engine"]; c.Net != 100 {
		t.Fatalf("alpha compute = %+v", gs[0].Cells)
	}
	if c := gs[0].Cells["cloud run"]; c.Net != 50 || c.Gross != 60 || c.Credits != -10 {
		t.Fatalf("alpha cloud run = %+v", c)
	}
	if gs[2].DirectNet != 40 || gs[3].DirectNet != 10 || gs[3].Kind != KindUnattributed || gs[2].Kind != KindShared {
		t.Fatalf("shared/unattr = %+v %+v", gs[2], gs[3])
	}
	if got := gs[0].RuleIDs; len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("rule ids = %v (builtin 100 outranks rule 2 at 50)", got)
	}
	if math.Abs(sumNet(gs)-250) > 1e-9 {
		t.Fatalf("sum = %v", sumNet(gs))
	}
	var pct float64
	for _, g := range gs {
		pct += g.SharePct
	}
	if math.Abs(pct-100) > 0.01 {
		t.Fatalf("share pct sum = %v", pct)
	}
}

func TestBreakdownByEnvironment(t *testing.T) {
	gs, _, err := BuildBreakdown(testRows(), testEngine(), DimensionEnvironment, NewAllocator(), AllocateNone)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, g := range gs {
		keys = append(keys, g.Key)
	}
	// prod 50+100=150 > dev 50+3=53 ; shared regardless of env; orphan -> (no environment)
	want := []string{"prod", "dev", KeyShared, KeyNoEnvironment}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v", keys)
		}
	}
	if gs[0].Net != 150 || gs[1].Net != 53 || gs[2].Net != 40 || gs[3].Net != 7 || gs[3].Kind != KindUnattributed {
		t.Fatalf("groups = %+v", gs)
	}
}

func TestBreakdownEmptyAndErrors(t *testing.T) {
	gs, notes, err := BuildBreakdown(nil, testEngine(), DimensionProduct, nil, AllocateEven)
	if err != nil || len(gs) != 2 || gs[0].Kind != KindShared || gs[1].Kind != KindUnattributed || len(notes) != 0 {
		t.Fatalf("empty: %v %v %v", gs, notes, err)
	}
	if _, _, err := BuildBreakdown(nil, testEngine(), "service", nil, AllocateNone); err == nil {
		t.Fatal("bad dimension accepted")
	}
	if _, _, err := BuildBreakdown(nil, testEngine(), DimensionProduct, nil, "bogus"); !errors.Is(err, ErrInvalidAllocate) {
		t.Fatalf("err = %v", err)
	}
}

func TestBreakdownAllocateEvenKeepsTotals(t *testing.T) {
	gs, notes, err := BuildBreakdown(testRows(), testEngine(), DimensionProduct, NewAllocator(), AllocateEven)
	if err != nil || len(notes) != 1 {
		t.Fatalf("err=%v notes=%v", err, notes)
	}
	// shared 40 -> 20 each: alpha 170, beta 70.
	if gs[0].Key != "alpha" || gs[0].Net != 170 || gs[1].Net != 70 || gs[2].Net != 0 || gs[2].AllocatedNet != -40 {
		t.Fatalf("groups = %+v", gs)
	}
	if math.Abs(sumNet(gs)-250) > 0.005 {
		t.Fatalf("sum = %v", sumNet(gs))
	}
	if c := gs[0].Cells["networking"]; c.Net != 15 {
		t.Fatalf("alpha networking = %+v", gs[0].Cells)
	}
}

func TestAllocatorProportionalMath(t *testing.T) {
	groups := []Group{
		{Key: "a", Kind: KindProduct, DirectNet: 75, Net: 75, Cells: map[string]BreakdownCell{"s": {Net: 75, Gross: 80, Credits: -5}, "total": {Net: 75, Gross: 80, Credits: -5}}},
		{Key: "b", Kind: KindProduct, DirectNet: 25, Net: 25, Cells: map[string]BreakdownCell{"total": {Net: 25, Gross: 25}}},
		{Key: "c", Kind: KindProduct, DirectNet: 0, Cells: map[string]BreakdownCell{"total": {}}},
		{Key: KeyShared, Kind: KindShared, DirectNet: 10, Net: 10, Cells: map[string]BreakdownCell{"s": {Net: 10, Gross: 12, Credits: -2}, "total": {Net: 10, Gross: 12, Credits: -2}}},
		{Key: KeyUnattributed, Kind: KindUnattributed, DirectNet: 5, Net: 5, Cells: map[string]BreakdownCell{"total": {Net: 5, Gross: 5}}},
	}
	out, notes, err := NewAllocator().Allocate(groups, AllocateProportional)
	if err != nil || len(notes) != 1 {
		t.Fatalf("err=%v notes=%v", err, notes)
	}
	if math.Abs(out[0].AllocatedNet-7.5) > 1e-9 || math.Abs(out[1].AllocatedNet-2.5) > 1e-9 || out[2].AllocatedNet != 0 {
		t.Fatalf("alloc = %+v", out)
	}
	if math.Abs(out[0].Cells["s"].Gross-(80+9)) > 1e-9 || math.Abs(out[0].Cells["s"].Credits-(-5-1.5)) > 1e-9 {
		t.Fatalf("gross/credits = %+v", out[0].Cells["s"])
	}
	if out[3].Net != 0 || out[3].AllocatedNet != -10 || out[3].DirectNet != 10 || len(out[3].Cells) != 1 {
		t.Fatalf("shared = %+v", out[3])
	}
	if out[4].Net != 5 || out[4].AllocatedNet != 0 {
		t.Fatalf("unattributed moved: %+v", out[4])
	}
	if sumNet(out) != 0 && math.Abs(sumNet(out)-sumNet(groups)) > 0.005 {
		t.Fatalf("not conserved")
	}
	// input untouched
	if groups[3].Net != 10 || groups[0].Net != 75 || groups[0].AllocatedNet != 0 {
		t.Fatal("input mutated")
	}
}

func TestAllocatorRoundingConservation(t *testing.T) {
	groups := []Group{
		{Key: "a", Kind: KindProduct, DirectNet: 1, Net: 1, Cells: map[string]BreakdownCell{"total": {Net: 1, Gross: 1}}},
		{Key: "b", Kind: KindProduct, DirectNet: 1, Net: 1, Cells: map[string]BreakdownCell{"total": {Net: 1, Gross: 1}}},
		{Key: "c", Kind: KindProduct, DirectNet: 1, Net: 1, Cells: map[string]BreakdownCell{"total": {Net: 1, Gross: 1}}},
		{Key: KeyShared, Kind: KindShared, DirectNet: 100, Net: 100, Cells: map[string]BreakdownCell{"total": {Net: 100, Gross: 100}}},
	}
	for _, m := range []AllocateMode{AllocateEven, AllocateProportional} {
		out, _, err := NewAllocator().Allocate(groups, m)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(sumNet(out)-103) > 0.005 {
			t.Fatalf("%s: sum = %v", m, sumNet(out))
		}
		var alloc float64
		for _, g := range out[:3] {
			alloc += g.AllocatedNet
		}
		if math.Abs(alloc-100) > 1e-9 {
			t.Fatalf("%s: allocated = %v", m, alloc)
		}
	}
}

func TestAllocatorNoEligibleGroups(t *testing.T) {
	groups := []Group{
		{Key: "a", Kind: KindProduct, Cells: map[string]BreakdownCell{"total": {}}},
		{Key: KeyShared, Kind: KindShared, DirectNet: 40, Net: 40, Cells: map[string]BreakdownCell{"total": {Net: 40, Gross: 40}}},
	}
	for _, m := range []AllocateMode{AllocateEven, AllocateProportional} {
		out, notes, err := NewAllocator().Allocate(groups, m)
		if err != nil || len(notes) != 1 || notes[0] != "No products to allocate shared cost to." {
			t.Fatalf("%s: notes=%v err=%v", m, notes, err)
		}
		if out[1].Net != 40 || out[1].AllocatedNet != 0 {
			t.Fatalf("shared moved: %+v", out[1])
		}
	}
}

func TestAllocatorProportionalZeroSumFallsBackToEven(t *testing.T) {
	groups := []Group{
		{Key: "a", Kind: KindProduct, DirectNet: 5, Net: 5, Cells: map[string]BreakdownCell{"total": {Net: 5}}},
		{Key: "b", Kind: KindProduct, DirectNet: -5, Net: -5, Cells: map[string]BreakdownCell{"total": {Net: -5}}},
		{Key: KeyShared, Kind: KindShared, DirectNet: 10, Net: 10, Cells: map[string]BreakdownCell{"total": {Net: 10}}},
	}
	out, notes, err := NewAllocator().Allocate(groups, AllocateProportional)
	if err != nil || len(notes) != 2 || out[0].AllocatedNet != 5 || out[1].AllocatedNet != 5 {
		t.Fatalf("out=%+v notes=%v err=%v", out, notes, err)
	}
}

func TestAllocatorInvalidAndNone(t *testing.T) {
	if _, _, err := NewAllocator().Allocate(nil, "x"); !errors.Is(err, ErrInvalidAllocate) {
		t.Fatalf("err = %v", err)
	}
	g := []Group{{Key: KeyShared, Kind: KindShared, DirectNet: 3, Net: 3}}
	out, notes, err := NewAllocator().Allocate(g, AllocateNone)
	if err != nil || len(notes) != 0 || out[0].Net != 3 {
		t.Fatalf("none: %+v %v %v", out, notes, err)
	}
}

func TestFilters(t *testing.T) {
	eng := testEngine()
	rows := testRows()
	if got := FilterRows(rows, eng, "dev", ""); len(got) != 2 {
		t.Fatalf("env=dev rows = %d", len(got))
	}
	if got := FilterRows(rows, eng, KeyNoEnvironment, ""); len(got) != 1 || got[0].ResourceName != "orphan" {
		t.Fatalf("no env = %+v", got)
	}
	if got := FilterRows(rows, eng, "", KeyShared); len(got) != 2 {
		t.Fatalf("shared = %d", len(got))
	}
	if got := FilterRows(rows, eng, "", KeyUnattributed); len(got) != 2 { // orphan + envonly
		t.Fatalf("unattributed = %d", len(got))
	}
	if got := FilterRows(rows, eng, "prod", "alpha"); len(got) != 1 {
		t.Fatalf("prod+alpha = %d", len(got))
	}
	if n, ok := NormalizeFilterName(" Alpha "); !ok || n != "alpha" {
		t.Fatal("normalize")
	}
	for _, bad := range []string{"", "Bad Name", "-x", "(other)"} {
		if _, ok := NormalizeFilterName(bad); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
	cov := BreakdownCoverage(rows, eng)
	if cov.TotalNet != 250 || cov.SharedNet != 40 || cov.UnattributedNet != 10 || math.Abs(cov.AttributedPct+cov.SharedPct+cov.UnattributedPct-100) > 0.01 {
		t.Fatalf("coverage = %+v", cov)
	}
}
