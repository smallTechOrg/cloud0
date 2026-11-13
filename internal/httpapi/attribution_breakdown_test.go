package httpapi

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/attribution"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// bdFakeEngine attributes by resource / sku so handler tests need no rules:
// vm-a → alpha/prod, vm-b → alpha/dev, db → beta/prod, "LB Rule" → shared.
type bdFakeEngine struct{}

func (bdFakeEngine) Evaluate(r attribution.Row) attribution.Evaluation {
	switch {
	case strings.HasSuffix(r.ResourceName, "vm-a"):
		return attribution.Evaluation{Product: "alpha", Environment: "prod"}
	case strings.HasSuffix(r.ResourceName, "vm-b"):
		return attribution.Evaluation{Product: "alpha", Environment: "dev"}
	case strings.HasSuffix(r.ResourceName, "/db"):
		return attribution.Evaluation{Product: "beta", Environment: "prod"}
	case r.SKU == "LB Rule":
		return attribution.Evaluation{Shared: true}
	}
	return attribution.Evaluation{}
}
func (bdFakeEngine) Explain(attribution.Row) []attribution.TrailStep { return nil }
func (bdFakeEngine) Rules() []attribution.Rule                       { return nil }

func bdDeps() *AttributionDeps {
	return &AttributionDeps{
		NewEngine: func([]attribution.Rule) (attribution.Engine, error) { return bdFakeEngine{}, nil },
		Allocator: attribution.NewAllocator(),
	}
}

// bdFixture is the Phase 1 fixture (W42 final) with attribution wired.
// W42 net: vm-a 150, vm-b 20, PD 20 (project-a), db 60, LB 60 (project-b), metrics 5 → 315.
func bdFixture(t *testing.T, deps *AttributionDeps) *fixture {
	t.Helper()
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{rows: phase1Rows}, &now)
	f.deps.Attribution = deps
	f.srv.Close()
	f.srv = httptest.NewServer(NewHandler(f.deps))
	t.Cleanup(f.srv.Close)
	f.refreshWeek(t, "2026-W42")
	return f
}

func bdGroups(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, g := range asList(t, m["groups"]) {
		out = append(out, asMap(t, g))
	}
	return out
}

func bdNets(t *testing.T, m map[string]any) map[string]float64 {
	out := map[string]float64{}
	for _, g := range bdGroups(t, m) {
		out[g["key"].(string)] = num(g["net"])
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

func TestByProduct_NoneEvenProportional(t *testing.T) {
	f := bdFixture(t, bdDeps())
	for _, tc := range []struct {
		mode  string
		alpha float64
		beta  float64
		notes int
	}{{"none", 170, 60, 0}, {"even", 200, 90, 1}, {"proportional", 170 + 60*170.0/230, 60 + 60*60.0/230, 1}} {
		res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/by-product?week=2026-W42&allocate="+tc.mode, "")
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d %v", tc.mode, res.StatusCode, m)
		}
		gs := bdGroups(t, m)
		keys := []string{}
		for _, g := range gs {
			keys = append(keys, g["key"].(string))
		}
		if strings.Join(keys, ",") != "alpha,beta,(shared),(unattributed)" {
			t.Fatalf("%s: keys %v", tc.mode, keys)
		}
		n := bdNets(t, m)
		if !near(n["alpha"], tc.alpha) || !near(n["beta"], tc.beta) || n["(unattributed)"] != 25 {
			t.Fatalf("%s: nets %v", tc.mode, n)
		}
		if tc.mode != "none" && (n["(shared)"] != 0 || num(gs[2]["allocated_net"]) != -60 || num(gs[2]["direct_net"]) != 60) {
			t.Fatalf("%s: shared %v", tc.mode, gs[2])
		}
		rec := asMap(t, m["reconciliation"])
		if rec["ok"] != true || !near(num(rec["cost_table_total"]), 315) || !near(num(rec["breakdown_total"]), 315) {
			t.Fatalf("%s: reconciliation %v", tc.mode, rec)
		}
		if m["allocate"] != tc.mode || m["dimension"] != "product" || m["snapshot"] == nil || len(asList(t, m["notes"])) != tc.notes {
			t.Fatalf("%s: header %v", tc.mode, m)
		}
		cols := asList(t, m["columns"])
		last := asMap(t, cols[len(cols)-1])
		if last["key"] != "total" || asMap(t, asMap(t, m["total"])["cells"])["total"] == nil {
			t.Fatalf("columns/total: %v", m["columns"])
		}
		// Σ groups' compute_engine cell = the cost table's cell (unfiltered).
		var ce float64
		for _, g := range gs {
			if c, ok := asMap(t, g["cells"])["compute_engine"]; ok {
				ce += num(asMap(t, c)["net"])
			}
		}
		if !near(ce, 150+20+20) {
			t.Fatalf("%s: compute_engine Σ = %v", tc.mode, ce)
		}
	}
}

func TestByProduct_ScopeAndEnvFilter(t *testing.T) {
	f := bdFixture(t, bdDeps())
	_, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/by-product?week=2026-W42&scope=project-b", "")
	n := bdNets(t, m)
	if n["beta"] != 60 || n["(shared)"] != 60 {
		t.Fatalf("scope nets %v", n)
	}
	if _, ok := n["alpha"]; ok || !near(num(asMap(t, m["reconciliation"])["cost_table_total"]), 120) || m["scope"] != "project-b" {
		t.Fatalf("scope: %v", m)
	}

	_, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/cost/by-product?week=2026-W42&env=prod", "")
	n = bdNets(t, m)
	if n["alpha"] != 150 || n["beta"] != 60 || n["(shared)"] != 0 {
		t.Fatalf("env=prod nets %v", n)
	}
	rec := asMap(t, m["reconciliation"])
	if rec["ok"] != true || !near(num(rec["cost_table_total"]), 210) || asMap(t, m["filter"])["env"] != "prod" {
		t.Fatalf("filtered reconciliation: %v", m)
	}
	found := false
	for _, s := range asList(t, m["notes"]) {
		found = found || strings.HasPrefix(s.(string), "Filtered view")
	}
	if !found {
		t.Fatalf("no Filtered view note: %v", m["notes"])
	}

	_, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/cost/by-product?week=2026-W42&env=(no%20environment)", "")
	if n = bdNets(t, m); n["(unattributed)"] != 25 || n["(shared)"] != 60 || n["alpha"] != 0 {
		t.Fatalf("no env nets %v", n)
	}
}

func TestByEnvironment(t *testing.T) {
	f := bdFixture(t, bdDeps())
	res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/by-environment?week=2026-W42", "")
	if res.StatusCode != 200 || m["dimension"] != "environment" {
		t.Fatalf("%d %v", res.StatusCode, m)
	}
	gs := bdGroups(t, m)
	var keys []string
	for _, g := range gs {
		keys = append(keys, g["key"].(string))
	}
	// prod 150+60=210, dev 20, shared 60, (no environment) 25
	if strings.Join(keys, ",") != "prod,dev,(shared),(no environment)" || gs[0]["kind"] != "environment" || gs[3]["kind"] != "unattributed" {
		t.Fatalf("keys %v", keys)
	}
	if n := bdNets(t, m); n["prod"] != 210 || n["dev"] != 20 || n["(no environment)"] != 25 || asMap(t, m["reconciliation"])["ok"] != true {
		t.Fatalf("nets %v", n)
	}
	_, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/cost/by-environment?week=2026-W42&allocate=even", "")
	if n := bdNets(t, m); n["prod"] != 240 || n["dev"] != 50 || n["(shared)"] != 0 {
		t.Fatalf("even nets %v", n)
	}
	_, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/cost/by-environment?week=2026-W42&product=beta", "")
	if n := bdNets(t, m); n["prod"] != 60 || n["dev"] != 0 || asMap(t, m["filter"])["product"] != "beta" {
		t.Fatalf("product=beta nets %v", n)
	}
}

func TestBreakdown_PersistedAllocateDefault(t *testing.T) {
	f := bdFixture(t, bdDeps())
	if err := store.PutSettings(context.Background(), f.store.DB, map[string]string{store.KeyAttributionAllocate: "even"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/by-product?week=2026-W42", "")
	if m["allocate"] != "even" || bdNets(t, m)["alpha"] != 200 {
		t.Fatalf("persisted default ignored: %v", m["allocate"])
	}
	_, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/cost/by-product?week=2026-W42&allocate=none", "")
	if m["allocate"] != "none" || bdNets(t, m)["alpha"] != 170 {
		t.Fatalf("explicit allocate must win: %v", m["allocate"])
	}
}

func TestBreakdown_EmptyWeekAndErrors(t *testing.T) {
	f := bdFixture(t, bdDeps())
	res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/by-product?week=2026-W30", "")
	if res.StatusCode != 200 || m["snapshot"] != nil || len(asList(t, m["groups"])) != 0 || len(asList(t, m["notes"])) != 1 ||
		!strings.Contains(m["notes"].([]any)[0].(string), "refresh") {
		t.Fatalf("empty week: %d %v", res.StatusCode, m)
	}
	for _, tc := range []struct {
		q, code string
		status  int
	}{
		{"week=2026-W42&allocate=bogus", "invalid_allocate", 400},
		{"week=nope", "invalid_week", 400},
		{"week=2026-W01", "week_out_of_range", 400},
		{"week=2026-W42&scope=nope", "invalid_scope", 400},
		{"week=2026-W42&env=Bad%20Name", "invalid_dimension", 400},
	} {
		for _, ep := range []string{"by-product", "by-environment"} {
			if ep == "by-environment" && strings.HasPrefix(tc.q, "week=2026-W42&env") {
				continue
			}
			res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/"+ep+"?"+tc.q, "")
			expectError(t, res, m, tc.status, tc.code)
		}
	}
	res, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/cost/by-environment?week=2026-W42&product=Bad%20Name", "")
	expectError(t, res, m, 400, "invalid_dimension")
}

func TestBreakdown_NotConfigured(t *testing.T) {
	f := bdFixture(t, nil)
	for _, p := range []string{"/api/v1/cost/by-product", "/api/v1/cost/by-environment"} {
		res, m, _ := f.doJSON(t, http.MethodGet, p+"?week=2026-W42", "")
		expectError(t, res, m, 503, "attribution_not_configured")
	}
	// the plain drill-down keeps working without attribution
	res, _, _ := f.doJSON(t, http.MethodGet, "/api/v1/snapshots/1/breakdown?level=service", "")
	if res.StatusCode != 200 {
		t.Fatalf("plain drill-down: %d", res.StatusCode)
	}
	res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/snapshots/1/breakdown?level=service&product=alpha", "")
	expectError(t, res, m, 503, "attribution_not_configured")
}

func bdSnapshotID(t *testing.T, f *fixture) int64 {
	t.Helper()
	_, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/by-product?week=2026-W42", "")
	return int64(num(asMap(t, m["snapshot"])["id"]))
}

func TestSnapshotBreakdown_ProductEnvironmentFilters(t *testing.T) {
	f := bdFixture(t, bdDeps())
	id := bdSnapshotID(t, f)
	base := "/api/v1/snapshots/" + strconv.FormatInt(id, 10) + "/breakdown?"

	_, m, _ := f.doJSON(t, http.MethodGet, base+"level=service&product=alpha", "")
	if !near(num(asMap(t, m["totals"])["net"]), 170) {
		t.Fatalf("product=alpha totals: %v", m["totals"])
	}
	rows := asList(t, m["rows"])
	if len(rows) != 1 || asMap(t, rows[0])["key"] != "Compute Engine" || asMap(t, rows[0])["column_key"] != "compute_engine" {
		t.Fatalf("rows: %v", rows)
	}
	// totals reconcile to the group cell of by-product
	_, bp, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/by-product?week=2026-W42", "")
	cell := asMap(t, asMap(t, bdGroups(t, bp)[0]["cells"])["compute_engine"])
	if !near(num(cell["net"]), num(asMap(t, rows[0])["net"])) {
		t.Fatalf("cell %v vs row %v", cell, rows[0])
	}

	_, m, _ = f.doJSON(t, http.MethodGet, base+"level=sku&service=Compute%20Engine&product=alpha&environment=dev", "")
	srows := asList(t, m["rows"])
	if len(srows) != 1 || asMap(t, srows[0])["key"] != "N1 Core" || !near(num(asMap(t, m["totals"])["net"]), 20) {
		t.Fatalf("sku rows: %v", m)
	}
	_, m, _ = f.doJSON(t, http.MethodGet, base+"level=resource&service=Compute%20Engine&sku=PD%20Capacity&product=(unattributed)", "")
	rrows := asList(t, m["rows"])
	if len(rrows) != 1 || asMap(t, rrows[0])["resource_name"] != nil || !near(num(asMap(t, rrows[0])["net"]), 20) {
		t.Fatalf("resource rows: %v", m)
	}
	_, m, _ = f.doJSON(t, http.MethodGet, base+"level=service&product=(shared)", "")
	if !near(num(asMap(t, m["totals"])["net"]), 60) {
		t.Fatalf("shared totals: %v", m["totals"])
	}
	_, m, _ = f.doJSON(t, http.MethodGet, base+"level=service&environment=(no%20environment)", "")
	if !near(num(asMap(t, m["totals"])["net"]), 85) { // 25 unattributed + 60 shared (no env)
		t.Fatalf("no env totals: %v", m["totals"])
	}
	res, em, _ := f.doJSON(t, http.MethodGet, base+"level=service&product=Not%20Valid", "")
	expectError(t, res, em, 400, "invalid_dimension")
	res, em, _ = f.doJSON(t, http.MethodGet, base+"level=service&environment=-x", "")
	expectError(t, res, em, 400, "invalid_dimension")
}

// TestLive_ByProductReconcilesWithCostTable refreshes the real 2026-W42 and
// checks that every allocate mode reconciles to the Cost table total.
func TestLive_ByProductReconcilesWithCostTable(t *testing.T) {
	f, cfg := newLiveFixture(t)
	f.deps.Attribution = &AttributionDeps{NewEngine: attribution.NewEngine, Allocator: attribution.NewAllocator()}
	f.srv.Close()
	f.srv = httptest.NewServer(NewHandler(f.deps))
	t.Cleanup(f.srv.Close)
	week := "2026-W42"
	_ = cfg
	f.refreshWeek(t, week)

	_, tbl, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table?from="+week+"&to="+week, "")
	want := num(cellsOf(t, rowFor(t, tbl, week))["total"]["net"])
	if want <= 0 {
		t.Fatalf("cost table total for %s = %v", week, want)
	}
	for _, ep := range []string{"by-product", "by-environment"} {
		for _, mode := range []string{"none", "even", "proportional"} {
			res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/"+ep+"?week="+week+"&allocate="+mode, "")
			if res.StatusCode != 200 {
				t.Fatalf("%s %s: %d %v", ep, mode, res.StatusCode, m)
			}
			var sum float64
			gs := bdGroups(t, m)
			for _, g := range gs {
				sum += num(g["net"])
			}
			if math.Abs(sum-want) > 0.005 || asMap(t, m["reconciliation"])["ok"] != true {
				t.Fatalf("%s %s: groups sum %.4f != cost table %.4f (%v)", ep, mode, sum, want, m["reconciliation"])
			}
			n := len(gs)
			if n < 2 || gs[n-2]["kind"] != "shared" || gs[n-1]["kind"] != "unattributed" {
				t.Fatalf("%s %s: shared/unattributed not last: %v", ep, mode, gs)
			}
			t.Logf("%s %s: %d groups, total %.2f, coverage %v", ep, mode, n, sum, m["coverage"])
		}
	}
}
