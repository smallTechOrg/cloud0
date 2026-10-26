package httpapi

import (
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
)

func TestRefresh_RejectsMalformedBodiesAndBadWeeks(t *testing.T) {
	now := phase1Now // current week 2026-W44, first week 2026-W18
	f := newPhase1Fixture(t, &fakeSource{rows: phase1Rows}, &now)
	cases := []struct {
		name, body, code string
	}{
		{"not json", `{"mode":`, "invalid_request"},
		{"empty body", ``, "invalid_request"},
		{"array body", `[]`, "invalid_request"},
		{"unknown field", `{"mode":"week","week":"2026-W42","force":true}`, "invalid_request"},
		{"missing mode", `{"week":"2026-W42"}`, "invalid_request"},
		{"bad mode", `{"mode":"month","week":"2026-W42"}`, "invalid_request"},
		{"mode wrong type", `{"mode":1}`, "invalid_request"},
		{"week missing", `{"mode":"week"}`, "invalid_request"},
		{"week wrong type", `{"mode":"week","week":42}`, "invalid_request"},
		{"trailing data", `{"mode":"all"} {"mode":"all"}`, "invalid_request"},
		{"week malformed", `{"mode":"week","week":"2026-42"}`, "invalid_week"},
		{"week nonexistent", `{"mode":"week","week":"2027-W53"}`, "invalid_week"},
		{"week after current", `{"mode":"week","week":"2026-W45"}`, "week_out_of_range"},
		{"week before first", `{"mode":"week","week":"2026-W17"}`, "week_out_of_range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, m, _ := f.doJSON(t, http.MethodPost, "/api/v1/refresh", tc.body)
			expectError(t, res, m, 400, tc.code)
		})
	}
	_, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/runs", "")
	if runs, _ := m["runs"].([]any); len(runs) != 0 {
		t.Fatalf("rejected requests must not create runs: %v", m)
	}
	if f.logCount("refresh.start") != 0 {
		t.Fatal("rejected requests must not log refresh.start")
	}
}

func TestRefresh_SourceFailureIs200WithFailedRunAndNoSnapshot(t *testing.T) {
	now := phase1Now
	src := &fakeSource{err: &googleapi.Error{Code: 403, Message: "Access Denied", Errors: []googleapi.ErrorItem{{Reason: "accessDenied", Message: "Access Denied: Table"}}}}
	f := newPhase1Fixture(t, src, &now)

	res, m, raw := f.doJSON(t, http.MethodPost, "/api/v1/refresh", `{"mode":"week","week":"2026-W42"}`)
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %v", res.StatusCode, m)
	}
	run, _ := m["run"].(map[string]any)
	if run["status"] != "failed" || run["trigger"] != "refresh_week" || run["from"] != "2026-W42" || run["to"] != "2026-W42" || run["error"] != nil {
		t.Fatalf("run = %v", run)
	}
	if snaps, _ := m["snapshots"].([]any); snaps == nil || len(snaps) != 0 {
		t.Fatalf("snapshots must be [] on failure: %s", raw)
	}
	sources, _ := run["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources = %v", run["sources"])
	}
	s := sources[0].(map[string]any)
	if s["status"] != "failed" || s["source"] != "bigquery_billing" || s["error_code"] != "permission_denied" || s["error_message"] == nil {
		t.Fatalf("source = %v", s)
	}
	if hint, _ := s["fix_hint"].(string); !strings.Contains(hint, "roles/bigquery.jobUser") {
		t.Fatalf("fix_hint = %v", s["fix_hint"])
	}
	for _, k := range []string{"id", "duration_ms", "job_id", "bytes_processed", "bytes_billed", "cache_hit", "rows_returned", "error_code", "error_message", "fix_hint"} {
		if _, ok := s[k]; !ok {
			t.Fatalf("source lacks %q: %v", k, s)
		}
	}
	if _, ok := s["sql"]; ok {
		t.Fatal("refresh response must not carry sql")
	}
	if _, ok := run["snapshot_ids"]; ok {
		t.Fatal("refresh run object has no snapshot_ids (spec/api.md → POST /refresh)")
	}
	for _, k := range []string{"id", "trigger", "from", "to", "started_at", "finished_at", "duration_ms", "status", "error", "sources"} {
		if _, ok := run[k]; !ok {
			t.Fatalf("run lacks %q: %v", k, run)
		}
	}
	tbl, _, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table", "")
	if tbl.StatusCode != 200 {
		t.Fatal("table must still load after a failed refresh")
	}
}

func TestRefresh_ConcurrentSecondRequestIs409(t *testing.T) {
	now := phase1Now
	src := &fakeSource{rows: phase1Rows, block: make(chan struct{}), entered: make(chan struct{}, 1)}
	f := newPhase1Fixture(t, src, &now)

	type outcome struct {
		res *http.Response
		m   map[string]any
	}
	first := make(chan outcome, 1)
	go func() {
		res, m, _ := f.doJSON(t, http.MethodPost, "/api/v1/refresh", `{"mode":"week","week":"2026-W42"}`)
		first <- outcome{res, m}
	}()
	select {
	case <-src.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first refresh never reached the source")
	}

	res, m, _ := f.doJSON(t, http.MethodPost, "/api/v1/refresh", `{"mode":"all"}`)
	e := expectError(t, res, m, 409, "refresh_in_progress")
	if e["hint"] != "Wait for the current refresh to finish, then retry." {
		t.Fatalf("hint = %v", e["hint"])
	}
	close(src.block)
	out := <-first
	if out.res.StatusCode != 200 || out.m["run"].(map[string]any)["status"] != "ok" {
		t.Fatalf("first refresh = %d %v", out.res.StatusCode, out.m)
	}
	_, runs, _ := f.doJSON(t, http.MethodGet, "/api/v1/runs", "")
	if list, _ := runs["runs"].([]any); len(list) != 1 {
		t.Fatalf("runs = %v, want exactly the one that ran", runs)
	}
}

func TestRefresh_WeekAndAllModes_ReturnSnapshotsPerSpec(t *testing.T) {
	now := phase1Now
	src := &fakeSource{rows: phase1Rows}
	f := newPhase1Fixture(t, src, &now)

	m := f.refreshWeek(t, "2026-W42")
	run := m["run"].(map[string]any)
	if run["status"] != "ok" || run["error"] != nil || num(run["duration_ms"]) < 0 || run["finished_at"] == nil {
		t.Fatalf("run = %v", run)
	}
	snaps, _ := m["snapshots"].([]any)
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %v", snaps)
	}
	s := snaps[0].(map[string]any)
	if s["week"] != "2026-W42" || s["status_at_fetch"] != "final" || num(s["row_count"]) != 6 || math.Abs(num(s["net_total"])-315) > 1e-9 {
		t.Fatalf("snapshot = %v", s)
	}
	for _, k := range []string{"id", "week", "status_at_fetch", "row_count", "net_total"} {
		if _, ok := s[k]; !ok {
			t.Fatalf("snapshot lacks %q", k)
		}
	}
	source := run["sources"].([]any)[0].(map[string]any)
	if source["status"] != "ok" || source["job_id"] != "job-fake-1" || num(source["rows_returned"]) != 6 || num(source["bytes_billed"]) != 20971520 || source["cache_hit"] != false || source["error_code"] != nil {
		t.Fatalf("source = %v", source)
	}

	// mode:"all" → one job, one snapshot per week first→current (W18..W44 = 27 weeks).
	res, m, _ := f.doJSON(t, http.MethodPost, "/api/v1/refresh", `{"mode":"all"}`)
	if res.StatusCode != 200 {
		t.Fatalf("refresh all: %d %v", res.StatusCode, m)
	}
	run = m["run"].(map[string]any)
	snaps, _ = m["snapshots"].([]any)
	if run["trigger"] != "refresh_all" || run["from"] != "2026-W18" || run["to"] != "2026-W44" || len(snaps) != 27 {
		t.Fatalf("refresh all: run=%v snapshots=%d", run, len(snaps))
	}
	if src.calls != 2 {
		t.Fatalf("source calls = %d, want 2 (one job per refresh)", src.calls)
	}
	byWeek := map[string]map[string]any{}
	for _, v := range snaps {
		sn := v.(map[string]any)
		byWeek[sn["week"].(string)] = sn
	}
	if num(byWeek["2026-W41"]["row_count"]) != 2 || num(byWeek["2026-W19"]["row_count"]) != 0 || byWeek["2026-W44"]["status_at_fetch"] != "partial" || byWeek["2026-W43"]["status_at_fetch"] != "settling" {
		t.Fatalf("per-week snapshots wrong: %v %v %v", byWeek["2026-W41"], byWeek["2026-W19"], byWeek["2026-W44"])
	}
	if f.logCount("refresh.start") != 2 || f.logCount("source.done") != 2 || f.logCount("refresh.done") != 2 {
		t.Fatalf("refresh log lines missing:\n%s", f.logs.String())
	}
}

// TestLive_SmokeWeekJourney is the S3 gate against the REAL BigQuery export
// (keys from .env): refresh → table → provenance → drill-down → run log →
// health, with the reconciliation invariants from spec/roadmap.md.
func TestLive_SmokeWeekJourney(t *testing.T) {
	f, cfg := newLiveFixture(t)
	week := cfg.SmokeWeek.String()
	const tol = 0.005

	// Health reflects the successful startup dry-run.
	res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/health", "")
	bq, _ := m["bigquery"].(map[string]any)
	if res.StatusCode != 200 || bq == nil || bq["status"] != "ok" || bq["error_code"] != nil || bq["message"] != nil || bq["fix_hint"] != nil || bq["checked_at"] == nil {
		t.Fatalf("health.bigquery = %v", m)
	}

	// 1. Refresh the smoke week.
	m = f.refreshWeek(t, week)
	run := m["run"].(map[string]any)
	if run["status"] != "ok" {
		t.Fatalf("run = %v", run)
	}
	snaps := m["snapshots"].([]any)
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %v", snaps)
	}
	firstSnap := snaps[0].(map[string]any)
	snapID := int64(num(firstSnap["id"]))
	if num(firstSnap["row_count"]) <= 100 || num(firstSnap["net_total"]) <= 0 {
		t.Fatalf("snapshot = %v", firstSnap)
	}

	// 2. Table: the smoke week row, totals reconcile, other weeks null.
	res, tbl, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table?scope=all", "")
	if res.StatusCode != 200 {
		t.Fatalf("table: %d %v", res.StatusCode, tbl)
	}
	row := rowFor(t, tbl, week)
	cells := cellsOf(t, row)
	compute := num(cells["compute_engine"]["net"])
	if compute <= 0 {
		t.Fatalf("compute_engine.net = %f, want > 0", compute)
	}
	columns := tbl["columns"].([]any)
	var sum float64
	for _, c := range columns {
		col := c.(map[string]any)
		if col["kind"] == "service" {
			cell, ok := cells[col["key"].(string)]
			if !ok {
				t.Fatalf("no cell for service column %v", col["key"])
			}
			sum += num(cell["net"])
		}
	}
	total := num(cells["total"]["net"])
	if math.Abs(total-sum) > tol {
		t.Fatalf("total.net %f != Σ service cells %f", total, sum)
	}
	if math.Abs(total-num(firstSnap["net_total"])) > tol {
		t.Fatalf("total.net %f != snapshot net_total %f", total, num(firstSnap["net_total"]))
	}
	t.Logf("W42 compute_engine.net=%.2f total.net=%.2f Σ(service cells)=%.2f", compute, total, sum)
	if vm := cells["vm_manager"]; num(vm["gross"]) > 0 && math.Abs(num(vm["net"])) > tol {
		t.Logf("vm_manager gross=%.2f net=%.2f (fully-credited service expected net≈0)", num(vm["gross"]), num(vm["net"]))
	}
	for _, r := range tbl["rows"].([]any) {
		rr := r.(map[string]any)
		if rr["week"] != week && (rr["cells"] != nil || rr["snapshot"] != nil) {
			t.Fatalf("week %v has cells without a snapshot: %v", rr["week"], rr)
		}
	}
	snapMeta := row["snapshot"].(map[string]any)
	if int64(num(snapMeta["id"])) != snapID || snapMeta["stale"] != false || row["week_status"] != "final" {
		t.Fatalf("row snapshot meta = %v status=%v", snapMeta, row["week_status"])
	}

	// 3. Second refresh → a newer snapshot; the table shows it; two runs listed.
	m2 := f.refreshWeek(t, week)
	secondID := int64(num(m2["snapshots"].([]any)[0].(map[string]any)["id"]))
	if secondID <= snapID {
		t.Fatalf("second snapshot id %d not after first %d", secondID, snapID)
	}
	_, tbl2, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table", "")
	if int64(num(rowFor(t, tbl2, week)["snapshot"].(map[string]any)["id"])) != secondID {
		t.Fatal("table does not show the latest snapshot")
	}
	_, runsBody, raw := f.doJSON(t, http.MethodGet, "/api/v1/runs", "")
	runs := runsBody["runs"].([]any)
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	newest := runs[0].(map[string]any)
	if int64(num(newest["id"])) != int64(num(m2["run"].(map[string]any)["id"])) || newest["status"] != "ok" {
		t.Fatalf("newest run = %v", newest)
	}
	if strings.Contains(string(raw), `"sql"`) {
		t.Fatal("GET /runs must omit sql")
	}
	src := newest["sources"].([]any)[0].(map[string]any)
	if src["job_id"] == nil || src["job_id"] == "" || num(src["rows_returned"]) <= 100 || src["bytes_billed"] == nil {
		t.Fatalf("run source = %v", src)
	}
	if ids := newest["snapshot_ids"].([]any); len(ids) != 1 || int64(num(ids[0])) != secondID {
		t.Fatalf("snapshot_ids = %v", newest["snapshot_ids"])
	}
	_, detail, _ := f.doJSON(t, http.MethodGet, "/api/v1/runs/"+itoa(int64(num(newest["id"]))), "")
	dsrc := detail["sources"].([]any)[0].(map[string]any)
	if sql, _ := dsrc["sql"].(string); !strings.Contains(sql, "_PARTITIONTIME") {
		t.Fatalf("GET /runs/{id} source lacks sql: %v", dsrc)
	}
	if params, _ := dsrc["params"].(map[string]any); len(params) != 4 {
		t.Fatalf("GET /runs/{id} params = %v", dsrc["params"])
	}

	// 4. Provenance.
	res, prov, _ := f.doJSON(t, http.MethodGet, "/api/v1/snapshots/"+itoa(secondID), "")
	if res.StatusCode != 200 {
		t.Fatalf("snapshot: %d %v", res.StatusCode, prov)
	}
	source := prov["source"].(map[string]any)
	sql, _ := source["sql"].(string)
	if !strings.Contains(sql, "DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end") || !strings.Contains(sql, "`"+cfg.BillingExportTable+"`") {
		t.Fatal("provenance sql is not the executed text")
	}
	params := source["params"].(map[string]any)
	if len(params) != 4 || params["usage_start"] == nil || params["usage_end"] == nil || params["part_start"] == nil || params["part_end"] == nil {
		t.Fatalf("params = %v", params)
	}
	if source["job_id"] == nil || source["source"] != "bigquery_billing" || (num(source["bytes_billed"]) == 0 && source["cache_hit"] != true) {
		t.Fatalf("source stats = %v", source)
	}
	if prov["week"] != week || prov["status_at_fetch"] != "final" || prov["week_status"] != "final" || prov["stale"] != false || prov["formula"] == nil {
		t.Fatalf("provenance = %v", prov)
	}
	if projects, _ := prov["projects"].([]any); len(projects) == 0 {
		t.Fatalf("projects = %v", prov["projects"])
	}
	if math.Abs(num(prov["net_total"])-total) > tol || math.Abs(num(prov["gross_total"])+num(prov["credits_total"])-num(prov["net_total"])) > tol {
		t.Fatalf("totals = gross %v credits %v net %v (table total %f)", prov["gross_total"], prov["credits_total"], prov["net_total"], total)
	}

	// 5. Drill-down reconciliation (service → sku → resource).
	bd := func(query string) map[string]any {
		res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/snapshots/"+itoa(secondID)+"/breakdown?"+query, "")
		if res.StatusCode != 200 {
			t.Fatalf("breakdown %s: %d %v", query, res.StatusCode, m)
		}
		return m
	}
	svc := bd("level=service")
	if math.Abs(num(svc["totals"].(map[string]any)["net"])-total) > tol {
		t.Fatalf("Σ service rows %v != total %f", svc["totals"], total)
	}
	for _, r := range svc["rows"].([]any) {
		if rr := r.(map[string]any); rr["column_key"] == nil || rr["column_key"] == "" {
			t.Fatalf("service row lacks column_key: %v", rr)
		}
	}
	sku := bd("level=sku&service=" + url.QueryEscape("Compute Engine"))
	skuRows := sku["rows"].([]any)
	skuTotal := num(sku["totals"].(map[string]any)["net"])
	if len(skuRows) == 0 || math.Abs(skuTotal-compute) > tol {
		t.Fatalf("Σ sku net %f != compute_engine cell %f (%d rows)", skuTotal, compute, len(skuRows))
	}
	t.Logf("reconcile sku: Σ %d Compute Engine SKU rows = %.4f vs cell %.4f", len(skuRows), skuTotal, compute)
	top := skuRows[0].(map[string]any)
	if top["sku_id"] == nil {
		t.Fatalf("sku row lacks sku_id: %v", top)
	}
	resq := bd("level=resource&service=" + url.QueryEscape("Compute Engine") + "&sku=" + url.QueryEscape(top["key"].(string)))
	resRows := resq["rows"].([]any)
	resTotal := num(resq["totals"].(map[string]any)["net"])
	if len(resRows) == 0 || math.Abs(resTotal-num(top["net"])) > tol {
		t.Fatalf("Σ resource net %f != sku %q net %f", resTotal, top["key"], num(top["net"]))
	}
	t.Logf("reconcile resource: Σ %d resource rows of %q = %.4f vs sku %.4f", len(resRows), top["key"], resTotal, num(top["net"]))
	for _, k := range []string{"key", "project_id", "resource_name", "resource_global_name", "gross", "credits", "net", "line_items"} {
		if _, ok := resRows[0].(map[string]any)[k]; !ok {
			t.Fatalf("resource row lacks %q: %v", k, resRows[0])
		}
	}
	net := bd("level=sku&service=Networking")
	credited := false
	for _, r := range net["rows"].([]any) {
		if num(r.(map[string]any)["credits"]) < 0 {
			credited = true
		}
	}
	if !credited {
		t.Fatal("Networking at sku level should show a row with credits < 0")
	}

	// 6. Scope: a single project never exceeds "all" and keeps row_count.
	project := cfg.Projects[0]
	_, scoped, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table?scope="+url.QueryEscape(project), "")
	srow := rowFor(t, scoped, week)
	if scoped["scope"] != project || num(cellsOf(t, srow)["total"]["net"]) > total+tol || num(srow["snapshot"].(map[string]any)["row_count"]) != num(firstSnap["row_count"]) {
		t.Fatalf("scoped row = %v", srow)
	}

	// 7. Logs.
	if f.logCount("refresh.start") != 2 || f.logCount("source.done") != 2 || f.logCount("refresh.done") != 2 {
		t.Fatalf("refresh log lines: start=%d source=%d done=%d", f.logCount("refresh.start"), f.logCount("source.done"), f.logCount("refresh.done"))
	}
	if strings.Contains(f.logs.String(), "SELECT") {
		t.Fatal("SQL must never be logged")
	}
}

func itoa(n int64) string {
	return strings.TrimSpace(strings.Replace(strings.Replace(string(rune(0)), "\x00", "", 1), "", "", 0)) + formatInt(n)
}

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
