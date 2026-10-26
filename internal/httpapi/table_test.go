package httpapi

import (
	"math"
	"net/http"
	"testing"
)

func TestCostTable_ValidatesScopeAndWeekRange(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{}, &now)
	cases := []struct{ name, query, code string }{
		{"unknown scope", "scope=project-z", "invalid_scope"},
		{"from malformed", "from=2026W18", "invalid_week"},
		{"to nonexistent", "to=2027-W53", "invalid_week"},
		{"to after current", "to=2026-W45", "week_out_of_range"},
		{"from before first", "from=2026-W17", "week_out_of_range"},
		{"from after to", "from=2026-W43&to=2026-W42", "week_out_of_range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table?"+tc.query, "")
			expectError(t, res, m, 400, tc.code)
		})
	}
}

func TestCostTable_EmptyStoreListsEveryWeekWithNullCellsAndFixedColumns(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{}, &now)

	res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table", "")
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %v", res.StatusCode, m)
	}
	if m["scope"] != "all" || m["from"] != "2026-W18" || m["to"] != "2026-W44" || m["generated_at"] != "2026-10-26T09:00:00Z" {
		t.Fatalf("header = %v", m)
	}
	th := m["thresholds"].(map[string]any)
	if num(th["pct"]) != 20 || num(th["abs_usd"]) != 50 {
		t.Fatalf("thresholds = %v", th)
	}
	wantKeys := []string{"week", "compute_engine", "networking", "cloud_sql", "gemini_api", "cloud_run", "gcs", "vm_manager", "cloud_build", "cloud_logging", "secret_manager", "artifact_registry", "total", "pct_change", "stub:groq", "stub:claude"}
	cols := m["columns"].([]any)
	if len(cols) != len(wantKeys) {
		t.Fatalf("columns = %d, want %d: %v", len(cols), len(wantKeys), cols)
	}
	for i, c := range cols {
		if c.(map[string]any)["key"] != wantKeys[i] {
			t.Fatalf("column %d = %v, want %s", i, c, wantKeys[i])
		}
	}
	rows := m["rows"].([]any)
	if len(rows) != 27 || rows[0].(map[string]any)["week"] != "2026-W18" || rows[26].(map[string]any)["week"] != "2026-W44" {
		t.Fatalf("rows = %d (%v .. %v)", len(rows), rows[0], rows[len(rows)-1])
	}
	for _, r := range rows {
		row := r.(map[string]any)
		if row["snapshot"] != nil || row["cells"] != nil || row["pct_change"] != nil || row["flagged"] != false || row["annotation"] != nil {
			t.Fatalf("not-fetched row must be null/false: %v", row)
		}
		for _, k := range []string{"week", "week_start", "week_end", "week_status", "snapshot", "cells", "pct_change", "flagged", "annotation"} {
			if _, ok := row[k]; !ok {
				t.Fatalf("row lacks %q: %v", k, row)
			}
		}
	}
	last := rows[26].(map[string]any)
	if last["week_status"] != "partial" || rows[25].(map[string]any)["week_status"] != "settling" || rows[24].(map[string]any)["week_status"] != "final" {
		t.Fatalf("week statuses wrong: %v %v %v", rows[24], rows[25], last)
	}
	// A narrowed range is honoured.
	_, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/cost/table?from=2026-W41&to=2026-W42", "")
	if rows := m["rows"].([]any); len(rows) != 2 || m["from"] != "2026-W41" || m["to"] != "2026-W42" {
		t.Fatalf("narrowed range = %v", m)
	}
}

func TestCostTable_RefreshedWeeksHaveCellsDeltasExtraColumnsScopeAndAnnotation(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{rows: phase1Rows}, &now)
	f.refreshWeek(t, "2026-W41")
	f.refreshWeek(t, "2026-W42")
	if res, _, _ := f.doJSON(t, http.MethodPut, "/api/v1/annotations/2026-W42", `{"body":"LB added"}`); res.StatusCode != 200 {
		t.Fatal("annotation PUT failed")
	}

	_, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table", "")
	// Cloud Monitoring is not a sheet column → extra column right before total.
	cols := m["columns"].([]any)
	var keys []string
	for _, c := range cols {
		keys = append(keys, c.(map[string]any)["key"].(string))
	}
	if keys[12] != "svc:Cloud Monitoring" || keys[13] != "total" || cols[12].(map[string]any)["extra"] != true {
		t.Fatalf("columns = %v", keys)
	}

	w42 := rowFor(t, m, "2026-W42")
	cells := cellsOf(t, w42)
	if num(cells["compute_engine"]["net"]) != 190 || num(cells["compute_engine"]["gross"]) != 200 || num(cells["compute_engine"]["credits"]) != -10 {
		t.Fatalf("compute_engine = %v", cells["compute_engine"])
	}
	if num(cells["networking"]["net"]) != 60 || num(cells["cloud_sql"]["net"]) != 60 || num(cells["svc:Cloud Monitoring"]["net"]) != 5 || num(cells["gcs"]["net"]) != 0 {
		t.Fatalf("cells = %v", cells)
	}
	if math.Abs(num(cells["total"]["net"])-315) > 1e-9 {
		t.Fatalf("total = %v", cells["total"])
	}
	// Deltas vs W41 (compute 100 → 190: Δ 90, 90 % → flagged at 20 %/$50; total 140 → 315).
	ce := cells["compute_engine"]
	if math.Abs(num(ce["delta_abs"])-90) > 1e-9 || math.Abs(num(ce["delta_pct"])-90) > 1e-9 || ce["flagged"] != true {
		t.Fatalf("compute_engine deltas = %v", ce)
	}
	if cells["networking"]["delta_pct"] != nil || cells["networking"]["flagged"] != false {
		t.Fatalf("networking (prev 0) deltas = %v", cells["networking"])
	}
	if w42["flagged"] != true || math.Abs(num(w42["pct_change"])-125) > 1e-9 || w42["annotation"] != "LB added" {
		t.Fatalf("row = %v", w42)
	}
	w41 := rowFor(t, m, "2026-W41")
	if cellsOf(t, w41)["total"]["delta_abs"] != nil || w41["pct_change"] != nil || w41["annotation"] != nil {
		t.Fatalf("first fetched week must have null deltas: %v", w41)
	}
	// from=W42 still computes deltas against W41 (looked up internally).
	_, narrowed, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table?from=2026-W42", "")
	if math.Abs(num(rowFor(t, narrowed, "2026-W42")["pct_change"])-125) > 1e-9 {
		t.Fatal("previous week outside from must still feed delta")
	}

	// Scope filter changes cells and total but not snapshot.row_count.
	_, scoped, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table?scope=project-b", "")
	srow := rowFor(t, scoped, "2026-W42")
	sc := cellsOf(t, srow)
	if scoped["scope"] != "project-b" || num(sc["compute_engine"]["net"]) != 0 || num(sc["cloud_sql"]["net"]) != 60 || num(sc["total"]["net"]) != 120 {
		t.Fatalf("scoped cells = %v", sc)
	}
	if num(srow["snapshot"].(map[string]any)["row_count"]) != 6 {
		t.Fatalf("row_count must be all projects: %v", srow["snapshot"])
	}
	if _, ok := sc["svc:Cloud Monitoring"]; ok {
		t.Fatal("extra column with zero net in the scoped range must disappear")
	}
}
