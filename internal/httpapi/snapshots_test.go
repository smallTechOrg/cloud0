package httpapi

import (
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSnapshots_UnknownIdsAre404(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{}, &now)
	for _, p := range []string{"/api/v1/snapshots/999", "/api/v1/snapshots/abc", "/api/v1/snapshots/999/breakdown?level=service", "/api/v1/snapshots/-1"} {
		res, m, _ := f.doJSON(t, http.MethodGet, p, "")
		expectError(t, res, m, 404, "not_found")
	}
}

func TestBreakdown_ValidatesLevelAndScope(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{rows: phase1Rows}, &now)
	id := itoa(int64(num(f.refreshWeek(t, "2026-W42")["snapshots"].([]any)[0].(map[string]any)["id"])))
	cases := []struct{ name, query, code string }{
		{"level missing", "", "invalid_level"},
		{"level unknown", "level=project", "invalid_level"},
		{"sku without service", "level=sku", "invalid_level"},
		{"resource without sku", "level=resource&service=Compute+Engine", "invalid_level"},
		{"bad scope", "level=service&scope=project-z", "invalid_scope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/snapshots/"+id+"/breakdown?"+tc.query, "")
			expectError(t, res, m, 400, tc.code)
		})
	}
}

func TestSnapshot_ProvenanceAndBreakdownOverFakeRows(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{rows: phase1Rows}, &now)
	m := f.refreshWeek(t, "2026-W42")
	runID := int64(num(m["run"].(map[string]any)["id"]))
	id := int64(num(m["snapshots"].([]any)[0].(map[string]any)["id"]))

	res, p, raw := f.doJSON(t, http.MethodGet, "/api/v1/snapshots/"+itoa(id), "")
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %v", res.StatusCode, p)
	}
	want := map[string]any{"id": float64(id), "week": "2026-W42", "week_start": "2026-10-12", "week_end": "2026-10-18",
		"fetched_at": "2026-10-26T09:00:00Z", "status_at_fetch": "final", "week_status": "final", "stale": false,
		"run_id": float64(runID), "row_count": float64(6), "gross_total": float64(355), "credits_total": float64(-40), "net_total": float64(315)}
	for k, v := range want {
		if p[k] != v {
			t.Fatalf("%s = %v, want %v", k, p[k], v)
		}
	}
	if projects := p["projects"].([]any); len(projects) != 2 || projects[0] != "project-a" || projects[1] != "project-b" {
		t.Fatalf("projects = %v", p["projects"])
	}
	if !strings.Contains(p["formula"].(string), "SUM(cost) + SUM(credits.amount)") {
		t.Fatalf("formula = %v", p["formula"])
	}
	src := p["source"].(map[string]any)
	if src["source"] != "bigquery_billing" || src["job_id"] != "job-fake-1" || num(src["bytes_processed"]) != 4321 || num(src["bytes_billed"]) != 20971520 || src["cache_hit"] != false {
		t.Fatalf("source = %v", src)
	}
	if !strings.Contains(src["sql"].(string), "DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end") || !strings.Contains(src["sql"].(string), "`"+fakeTable+"`") {
		t.Fatalf("sql = %v", src["sql"])
	}
	params := src["params"].(map[string]any)
	if len(params) != 4 || params["part_start"] != "2026-10-11" || params["part_end"] != "2026-10-20" || params["usage_start"] != "2026-10-12T00:00:00Z" || params["usage_end"] != "2026-10-19T00:00:00Z" {
		t.Fatalf("params = %v", params)
	}
	if strings.Contains(string(raw), `"error"`) {
		t.Fatal("success body must not contain an error key")
	}

	// Stale: the clock moves on but the snapshot stays; refresh W44 (partial) then make it final.
	m44 := f.refreshWeek(t, "2026-W44")
	id44 := int64(num(m44["snapshots"].([]any)[0].(map[string]any)["id"]))
	now = now.AddDate(0, 0, 14)
	_, p44, _ := f.doJSON(t, http.MethodGet, "/api/v1/snapshots/"+itoa(id44), "")
	if p44["status_at_fetch"] != "partial" || p44["week_status"] != "final" || p44["stale"] != true {
		t.Fatalf("stale provenance = %v", p44)
	}
	now = phase1Now

	bd := func(query string) map[string]any {
		res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/snapshots/"+itoa(id)+"/breakdown?"+query, "")
		if res.StatusCode != 200 {
			t.Fatalf("breakdown %s: %d %v", query, res.StatusCode, m)
		}
		return m
	}
	svc := bd("level=service")
	if svc["level"] != "service" || svc["scope"] != "all" || svc["service"] != nil || svc["sku"] != nil || num(svc["snapshot_id"]) != float64(id) {
		t.Fatalf("service breakdown header = %v", svc)
	}
	rows := svc["rows"].([]any)
	if len(rows) != 4 || rows[0].(map[string]any)["key"] != "Compute Engine" || rows[0].(map[string]any)["column_key"] != "compute_engine" {
		t.Fatalf("service rows = %v", rows)
	}
	var monitoring map[string]any
	for _, r := range rows {
		if rr := r.(map[string]any); rr["key"] == "Cloud Monitoring" {
			monitoring = rr
		}
	}
	if monitoring == nil || monitoring["column_key"] != "svc:Cloud Monitoring" {
		t.Fatalf("Cloud Monitoring row = %v", monitoring)
	}
	if math.Abs(num(svc["totals"].(map[string]any)["net"])-315) > 1e-9 {
		t.Fatalf("service totals = %v", svc["totals"])
	}

	sku := bd("level=sku&service=" + url.QueryEscape("Compute Engine"))
	if sku["service"] != "Compute Engine" || sku["sku"] != nil {
		t.Fatalf("sku header = %v", sku)
	}
	skuRows := sku["rows"].([]any)
	if len(skuRows) != 2 || skuRows[0].(map[string]any)["key"] != "N1 Core" || skuRows[0].(map[string]any)["sku_id"] != "sku-N1 Core" || num(skuRows[0].(map[string]any)["net"]) != 170 {
		t.Fatalf("sku rows = %v", skuRows)
	}
	if num(sku["totals"].(map[string]any)["net"]) != 190 {
		t.Fatalf("Σ sku net %v != compute_engine cell 190", sku["totals"])
	}

	resq := bd("level=resource&service=" + url.QueryEscape("Compute Engine") + "&sku=" + url.QueryEscape("N1 Core"))
	if resq["service"] != "Compute Engine" || resq["sku"] != "N1 Core" {
		t.Fatalf("resource header = %v", resq)
	}
	rr := resq["rows"].([]any)
	if len(rr) != 2 || rr[0].(map[string]any)["key"] != "projects/1/instances/vm-a" || num(rr[0].(map[string]any)["net"]) != 150 || rr[0].(map[string]any)["project_id"] != "project-a" {
		t.Fatalf("resource rows = %v", rr)
	}
	if num(resq["totals"].(map[string]any)["net"]) != 170 {
		t.Fatalf("Σ resource net %v != sku net 170", resq["totals"])
	}
	unattr := bd("level=resource&service=Networking&sku=" + url.QueryEscape("LB Rule"))
	u := unattr["rows"].([]any)[0].(map[string]any)
	if u["key"] != "(unattributed)" || u["resource_name"] != nil || num(u["credits"]) != -30 || num(u["net"]) != 60 {
		t.Fatalf("unattributed row = %v", u)
	}

	// Scope filter at every level.
	scoped := bd("level=service&scope=project-b")
	if scoped["scope"] != "project-b" || num(scoped["totals"].(map[string]any)["net"]) != 120 {
		t.Fatalf("scoped service breakdown = %v", scoped)
	}
}
