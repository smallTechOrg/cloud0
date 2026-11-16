package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// W41: alpha 100, beta 40 → 140. W42: alpha 170, beta 60, (shared) 60, (unattributed) 25 → 315.
func TestWeeklyByProduct_ColumnsRowsDeltasAndReconciliation(t *testing.T) {
	f := bdFixture(t, bdDeps())
	f.refreshWeek(t, "2026-W41")

	res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/by-product/weeks?from=2026-W41&to=2026-W43&allocate=none", "")
	if res.StatusCode != 200 {
		t.Fatalf("%d %v", res.StatusCode, m)
	}
	if m["dimension"] != "product" || m["allocate"] != "none" {
		t.Fatalf("header %v", m)
	}
	var keys []string
	for _, c := range asList(t, m["columns"]) {
		keys = append(keys, asMap(t, c)["key"].(string))
	}
	if got := strings.Join(keys, ","); got != "week,alpha,beta,(shared),(unattributed),total,pct_change" {
		t.Fatalf("columns = %s", got)
	}
	if g := asMap(t, asList(t, m["columns"])[3])["group"]; g != "shared" {
		t.Errorf("(shared) group = %v", g)
	}

	rows := asList(t, m["rows"])
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	w41, w42, w43 := asMap(t, rows[0]), asMap(t, rows[1]), asMap(t, rows[2])
	c41, c42 := asMap(t, w41["cells"]), asMap(t, w42["cells"])
	if !near(num(asMap(t, c41["alpha"])["net"]), 100) || !near(num(asMap(t, c41["total"])["net"]), 140) {
		t.Errorf("W41 cells %v", c41)
	}
	if v, ok := c41["(shared)"]; !ok || num(asMap(t, v)["net"]) != 0 {
		t.Errorf("W41 (shared) should be a zero cell, got %v", v)
	}
	alpha := asMap(t, c42["alpha"])
	if !near(num(alpha["net"]), 170) || !near(num(alpha["delta_abs"]), 70) || !near(num(alpha["delta_pct"]), 70) {
		t.Errorf("W42 alpha %v", alpha)
	}
	var sum float64
	for _, k := range []string{"alpha", "beta", "(shared)", "(unattributed)"} {
		sum += num(asMap(t, c42[k])["net"])
	}
	if !near(sum, 315) || !near(num(asMap(t, c42["total"])["net"]), 315) || !near(num(w42["pct_change"]), 125) {
		t.Errorf("W42 Σ %v total %v pct %v", sum, c42["total"], w42["pct_change"])
	}
	if w43["snapshot"] != nil || w43["cells"] != nil {
		t.Errorf("W43 was never fetched: %v", w43)
	}

	// Even allocation moves (shared) into the products; the total is unchanged.
	_, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/cost/by-product/weeks?from=2026-W42&to=2026-W42&allocate=even", "")
	c := asMap(t, asMap(t, asList(t, m["rows"])[0])["cells"])
	if !near(num(asMap(t, c["alpha"])["net"]), 200) || !near(num(asMap(t, c["total"])["net"]), 315) {
		t.Errorf("even: %v", c)
	}
	for _, col := range asList(t, m["columns"]) {
		if asMap(t, col)["key"] == "(shared)" {
			t.Errorf("fully allocated (shared) still has a column")
		}
	}
}

func TestWeeklyByEnvironment_AndErrors(t *testing.T) {
	f := bdFixture(t, bdDeps())
	res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/by-environment/weeks?from=2026-W42&to=2026-W42&allocate=none", "")
	if res.StatusCode != 200 || m["dimension"] != "environment" {
		t.Fatalf("%d %v", res.StatusCode, m)
	}
	c := asMap(t, asMap(t, asList(t, m["rows"])[0])["cells"])
	if !near(num(asMap(t, c["prod"])["net"]), 210) || !near(num(asMap(t, c["dev"])["net"]), 20) {
		t.Errorf("env cells %v", c)
	}
	for q, code := range map[string]string{"from=nope": "invalid_week", "allocate=half": "invalid_allocate", "scope=other": "invalid_scope"} {
		res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/by-product/weeks?"+q, "")
		if res.StatusCode != 400 || asMap(t, m["error"])["code"] != code {
			t.Errorf("%s: %d %v", q, res.StatusCode, m)
		}
	}
	f.deps.Attribution = nil
	g := newPhase1Fixture(t, &fakeSource{rows: phase1Rows}, &phase1Now)
	if res, _, _ := g.doJSON(t, http.MethodGet, "/api/v1/cost/by-product/weeks", ""); res.StatusCode != 503 {
		t.Errorf("no attribution: %d", res.StatusCode)
	}
}
