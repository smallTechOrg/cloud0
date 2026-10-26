package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/limits"
	"github.com/smallTechOrg/cloud0/internal/monthcost"
	"github.com/smallTechOrg/cloud0/internal/refresh"
	"github.com/smallTechOrg/cloud0/internal/store"
)

func p2prov(name string) gcpsource.Provenance {
	return gcpsource.Provenance{Source: name, Query: `metric.type="x"`, Aggregation: `{"align":"ALIGN_MEAN"}`,
		Window: gcpsource.Window{Start: phase1Now.AddDate(0, 0, -14), End: phase1Now.AddDate(0, 0, -7)}, FetchedAt: phase1Now}
}

func fcell(v float64, unit, src string) gcpsource.Cell { return gcpsource.V(v, unit, p2prov(src)) }

func metricRowOf(group, project, resource string, cells map[string]gcpsource.Cell) gcpsource.MetricRow {
	return gcpsource.MetricRow{Group: group, Project: project, Resource: resource, Cells: cells}
}

func collector(name string, owns []refresh.Owned, rows ...gcpsource.MetricRow) refresh.Collector {
	return refresh.Collector{Name: name, Owns: owns, Fetch: func(context.Context, gcpsource.Window) gcpsource.Result {
		return gcpsource.Result{Source: name, Status: gcpsource.StatusOK, Metrics: rows, Provenance: []gcpsource.Provenance{p2prov(name)}}
	}}
}

type fakeMonthSrc struct{ rows []monthcost.Row }

func (f fakeMonthSrc) FetchMonths(_ context.Context, _, _ monthcost.Month) ([]monthcost.Row, gcpsource.Provenance, error) {
	return f.rows, gcpsource.Provenance{Source: monthcost.SourceName, Query: "SELECT 1", Aggregation: `{"usage_start":"2026-09-01T00:00:00Z"}`}, nil
}

func i64(v int64) *int64 { return &v }

// p2Fixture wires the Phase 1 fixture with fake Phase 2 sources.
func p2Fixture(t *testing.T) *fixture {
	t.Helper()
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{rows: phase1Rows}, &now)
	r := f.deps.Refresh
	r.Projects = []string{"project-a", "project-b"}
	r.Month = fakeMonthSrc{rows: []monthcost.Row{
		{Month: "2026-09", InvoiceMonth: "202609", ProjectID: "project-a", ServiceID: "s1", Service: "Compute Engine", Gross: 100, Credits: -10, Net: -1},
		{Month: "2026-09", InvoiceMonth: "202609", ProjectID: "project-b", ServiceID: "s2", Service: "Cloud SQL", Gross: 50, Net: 50},
		{Month: "2026-10", InvoiceMonth: "202610", ProjectID: "project-a", ServiceID: "s1", Service: "Compute Engine", Gross: 200, Credits: -20, Net: 0},
		{Month: "2026-10", InvoiceMonth: "202610", ProjectID: "project-b", ServiceID: "s2", Service: "Cloud SQL", Gross: 60, Net: 0},
		{Month: "2026-10", InvoiceMonth: "202610", ProjectID: "project-a", ServiceID: "s3", Service: "Cloud Monitoring", Gross: 5, Net: 0},
	}}
	util := []string{gcpsource.MCPU, gcpsource.MMemory, gcpsource.MDisk}
	r.Collectors = []refresh.Collector{
		collector("monitoring_vm", []refresh.Owned{{Group: gcpsource.GroupVM, Columns: util}},
			metricRowOf(gcpsource.GroupVM, "project-a", "vm-a", map[string]gcpsource.Cell{
				gcpsource.MCPU:    fcell(12.5, "%", "monitoring_vm"),
				gcpsource.MMemory: gcpsource.G("no agent", "install the Ops Agent", "%", p2prov("monitoring_vm")),
			}),
			metricRowOf(gcpsource.GroupVM, "project-b", "vm-b", map[string]gcpsource.Cell{gcpsource.MCPU: fcell(30, "%", "monitoring_vm")})),
		collector("monitoring_cloudrun", nil,
			metricRowOf(gcpsource.GroupCloudRun, "project-a", "svc-a", map[string]gcpsource.Cell{gcpsource.MRequests: fcell(1000000, "count", "monitoring_cloudrun")})),
		{Name: "monitoring_logging", Owns: []refresh.Owned{{Group: gcpsource.GroupLogging, Columns: []string{gcpsource.MLoggingGB}}},
			Fetch: func(context.Context, gcpsource.Window) gcpsource.Result {
				res := gcpsource.Result{Source: "monitoring_logging"}
				res.Finish(phase1Now, status.Error(codes.PermissionDenied, "denied"), "roles/monitoring.viewer", "monitoring.googleapis.com")
				return res
			}},
		{Name: "errors_cloudrun", ErrorSource: gcpsource.ErrSrcCloudRun, Fetch: func(context.Context, gcpsource.Window) gcpsource.Result {
			return gcpsource.Result{Source: "errors_cloudrun", Status: gcpsource.StatusOK, Errors: []gcpsource.ErrorRow{
				{Source: gcpsource.ErrSrcCloudRun, Project: "project-a", Service: "svc-a", C2xx: i64(1000), C3xx: i64(0), C4xx: i64(12), C5xx: i64(1), Provenance: p2prov("errors_cloudrun")},
				{Source: gcpsource.ErrSrcCloudRun, Project: "project-b", Service: "svc-b", Gap: "no data", Hint: "no requests", Provenance: p2prov("errors_cloudrun")},
				{Source: gcpsource.ErrSrcErrorReporting, Project: "project-a", Service: "svc-a", Groups: i64(3), Occurrences: i64(40), Provenance: p2prov("errors_cloudrun")},
			}}
		}},
	}
	return f
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %v", v)
	}
	return m
}

func asList(t *testing.T, v any) []any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("not an array: %v", v)
	}
	return l
}

func (f *fixture) refreshBody(t *testing.T, body string) map[string]any {
	t.Helper()
	res, m, _ := f.doJSON(t, http.MethodPost, "/api/v1/refresh", body)
	if res.StatusCode != 200 {
		t.Fatalf("refresh %s: %d %v", body, res.StatusCode, m)
	}
	return m
}

func groupByName(t *testing.T, tbl map[string]any, name string) map[string]any {
	t.Helper()
	for _, g := range asList(t, tbl["groups"]) {
		if gm := asMap(t, g); gm["group"] == name {
			return gm
		}
	}
	t.Fatalf("no group %s", name)
	return nil
}

func TestRefresh_AllModePartial_ResponseAndRunLog(t *testing.T) {
	f := p2Fixture(t)
	m := f.refreshBody(t, `{"mode":"all"}`)
	run := asMap(t, m["run"])
	if run["status"] != "partial" || run["trigger"] != "refresh_all" {
		t.Fatalf("run = %v", run)
	}
	var names []string
	for _, s := range asList(t, run["sources"]) {
		names = append(names, asMap(t, s)["source"].(string))
	}
	if got := strings.Join(names, ","); got != "bigquery_billing,bigquery_month,monitoring_vm,monitoring_cloudrun,monitoring_logging,errors_cloudrun" {
		t.Fatalf("sources = %s", got)
	}
	ms := asList(t, m["metric_snapshots"])
	mo := asList(t, m["month_snapshots"])
	if len(ms) != 1 || asMap(t, ms[0])["week"] != "2026-W43" || len(mo) != 7 || asMap(t, mo[0])["month"] != "2026-04" || asMap(t, mo[5])["month"] != "2026-09" || num(asMap(t, mo[6])["net_total"]) != 245 {
		t.Fatalf("metric=%v month=%v", ms, mo)
	}
	logging := asMap(t, asList(t, run["sources"])[4])
	if logging["status"] != "failed" || logging["error_code"] != "permission_denied" || !strings.Contains(logging["fix_hint"].(string), "roles/monitoring.viewer") ||
		logging["job_id"] != nil || logging["bytes_billed"] != nil || logging["cache_hit"] != nil {
		t.Fatalf("logging source = %v", logging)
	}

	// Run log: one row per source, list omits sql, detail has it.
	_, runs, raw := f.doJSON(t, http.MethodGet, "/api/v1/runs", "")
	if strings.Contains(string(raw), `"sql"`) {
		t.Fatal("list must omit sql")
	}
	first := asMap(t, asList(t, runs["runs"])[0])
	if len(asList(t, first["sources"])) != 6 {
		t.Fatalf("run log sources = %v", first["sources"])
	}
	_, detail, _ := f.doJSON(t, http.MethodGet, "/api/v1/runs/"+itoa(int64(num(run["id"]))), "")
	vm := asMap(t, asList(t, detail["sources"])[2])
	if vm["source"] != "monitoring_vm" || vm["sql"] != `metric.type="x"` || vm["params"] == nil {
		t.Fatalf("vm detail = %v", vm)
	}
}

func TestRefresh_MonthModeAndValidation(t *testing.T) {
	f := p2Fixture(t)
	m := f.refreshBody(t, `{"mode":"month","month":"2026-09"}`)
	run := asMap(t, m["run"])
	if run["trigger"] != "refresh_month" || run["status"] != "ok" || len(asList(t, run["sources"])) != 1 ||
		len(asList(t, m["snapshots"])) != 0 || len(asList(t, m["metric_snapshots"])) != 0 || len(asList(t, m["month_snapshots"])) != 1 {
		t.Fatalf("month refresh = %v", m)
	}
	for _, c := range []struct{ body, code string }{
		{`{"mode":"month","month":"2026-13"}`, "invalid_month"},
		{`{"mode":"month","month":"October"}`, "invalid_month"},
		{`{"mode":"month","month":"2026-11"}`, "month_out_of_range"},
		{`{"mode":"month"}`, "invalid_request"},
		{`{"mode":"quarter"}`, "invalid_request"},
	} {
		res, body, _ := f.doJSON(t, http.MethodPost, "/api/v1/refresh", c.body)
		expectError(t, res, body, 400, c.code)
	}
}

func TestMetricsTable_GroupsGapsScopeAndLimitLevels(t *testing.T) {
	f := p2Fixture(t)

	// Before any refresh: snapshot null, seven empty groups in order.
	res, empty, _ := f.doJSON(t, http.MethodGet, "/api/v1/metrics/table", "")
	if res.StatusCode != 200 || empty["snapshot"] != nil || empty["week"] != "2026-W43" || empty["week_status"] != "settling" {
		t.Fatalf("empty = %v", empty)
	}
	order := []string{"vm", "cloudsql", "networking", "cloudrun", "gcs", "logging", "errors"}
	for i, g := range asList(t, empty["groups"]) {
		gm := asMap(t, g)
		if gm["group"] != order[i] || len(asList(t, gm["rows"])) != 0 || len(asList(t, gm["columns"])) == 0 {
			t.Fatalf("group %d = %v", i, gm)
		}
	}

	f.refreshBody(t, `{"mode":"all"}`)
	_, tbl, _ := f.doJSON(t, http.MethodGet, "/api/v1/metrics/table", "")
	snap := asMap(t, tbl["snapshot"])
	if snap["run_id"] == nil || snap["stale"] != false || snap["status_at_fetch"] != "settling" || tbl["scope"] != "all" || tbl["week_start"] != "2026-10-19" {
		t.Fatalf("snapshot = %v", snap)
	}
	vm := groupByName(t, tbl, "vm")
	if vm["label"] != "VM" || len(asList(t, vm["columns"])) != 3 {
		t.Fatalf("vm group = %v", vm)
	}
	rows := asList(t, vm["rows"])
	if len(rows) != 2 {
		t.Fatalf("vm rows = %v", rows)
	}
	a := asMap(t, asMap(t, rows[0])["cells"])
	cpu, mem, disk := asMap(t, a["cpu_pct"]), asMap(t, a["memory_pct"]), asMap(t, a["disk_pct"])
	if num(cpu["value"]) != 12.5 || cpu["gap"] != nil || cpu["unit"] != "%" || asMap(t, cpu["provenance"])["source"] != "monitoring_vm" {
		t.Fatalf("cpu = %v", cpu)
	}
	if mem["value"] != nil || mem["gap"] != "no agent" || mem["hint"] != "install the Ops Agent" {
		t.Fatalf("mem = %v", mem)
	}
	if disk["value"] != nil || disk["gap"] != "not collected" || disk["hint"] != nil || asMap(t, disk["provenance"])["source"] != "" || disk["unit"] != "%" {
		t.Fatalf("disk = %v", disk)
	}
	if _, ok := cpu["limit_level"]; !ok || cpu["limit_level"] != nil {
		t.Fatalf("cpu limit_level must be present and null: %v", cpu)
	}

	// Cloud Run requests carry the limit level of cloudrun_requests (1M of 2M/month, projected over).
	cr := asMap(t, asMap(t, asMap(t, asList(t, groupByName(t, tbl, "cloudrun")["rows"])[0])["cells"])["requests"])
	if num(cr["value"]) != 1000000 || (cr["limit_level"] != "ok" && cr["limit_level"] != "near" && cr["limit_level"] != "over") {
		t.Fatalf("requests cell = %v", cr)
	}

	// Failed source → "source failed" with the fix hint, for every project.
	lg := asList(t, groupByName(t, tbl, "logging")["rows"])
	if len(lg) != 2 {
		t.Fatalf("logging rows = %v", lg)
	}
	c := asMap(t, asMap(t, asMap(t, lg[0])["cells"])["logging_gb"])
	if c["gap"] != "source failed" || !strings.Contains(c["hint"].(string), "roles/monitoring.viewer") || c["value"] != nil || c["unit"] != "GB" {
		t.Fatalf("logging cell = %v", c)
	}

	// Errors group derived from the Cloud Run error rows only.
	er := asList(t, groupByName(t, tbl, "errors")["rows"])
	if len(er) != 2 {
		t.Fatalf("errors rows = %v", er)
	}
	e0 := asMap(t, er[0])
	if e0["project"] != "project-a" || e0["resource"] != "svc-a" || num(asMap(t, asMap(t, e0["cells"])["c4xx"])["value"]) != 12 || num(asMap(t, asMap(t, e0["cells"])["c5xx"])["value"]) != 1 {
		t.Fatalf("errors row = %v", e0)
	}
	e1 := asMap(t, asMap(t, asMap(t, er[1])["cells"])["c4xx"])
	if e1["value"] != nil || e1["gap"] != "no data" || e1["hint"] != "no requests" {
		t.Fatalf("gap error cell = %v", e1)
	}

	// Scope filters by project at read time.
	_, scoped, _ := f.doJSON(t, http.MethodGet, "/api/v1/metrics/table?scope=project-b", "")
	vmB := asList(t, groupByName(t, scoped, "vm")["rows"])
	if len(vmB) != 1 || asMap(t, vmB[0])["project"] != "project-b" || len(asList(t, groupByName(t, scoped, "cloudrun")["rows"])) != 0 {
		t.Fatalf("scoped vm = %v", vmB)
	}

	// Errors.
	for _, c := range []struct{ q, code string }{
		{"?week=2026-W99", "invalid_week"},
		{"?week=2026-W10", "week_out_of_range"},
		{"?week=2026-W45", "week_out_of_range"},
		{"?scope=nope", "invalid_scope"},
	} {
		res, body, _ := f.doJSON(t, http.MethodGet, "/api/v1/metrics/table"+c.q, "")
		expectError(t, res, body, 400, c.code)
	}
	res, body, _ := f.doJSON(t, http.MethodPost, "/api/v1/metrics/table", "")
	expectError(t, res, body, 405, "method_not_allowed")
}

func TestErrorsTable_RowsTotalsScopeAndEmpty(t *testing.T) {
	f := p2Fixture(t)
	_, empty, _ := f.doJSON(t, http.MethodGet, "/api/v1/errors/table", "")
	tot := asMap(t, empty["totals"])
	if empty["snapshot"] != nil || len(asList(t, empty["rows"])) != 0 || tot["c4xx"] != nil || tot["groups"] != nil || tot["occurrences"] != nil {
		t.Fatalf("empty = %v", empty)
	}

	run := asMap(t, f.refreshBody(t, `{"mode":"all"}`)["run"])
	_, tbl, _ := f.doJSON(t, http.MethodGet, "/api/v1/errors/table", "")
	snap := asMap(t, tbl["snapshot"])
	if int64(num(snap["id"])) != int64(num(run["id"])) || int64(num(snap["run_id"])) != int64(num(run["id"])) || num(snap["row_count"]) != 3 {
		t.Fatalf("snapshot = %v", snap)
	}
	rows := asList(t, tbl["rows"])
	if len(rows) != 3 {
		t.Fatalf("rows = %v", rows)
	}
	r0 := asMap(t, rows[0])
	if r0["source"] != "cloudrun" || num(r0["c4xx"]) != 12 || r0["groups"] != nil || r0["gap"] != nil || asMap(t, r0["provenance"])["source"] != "errors_cloudrun" {
		t.Fatalf("row 0 = %v", r0)
	}
	r1 := asMap(t, rows[1])
	if r1["c4xx"] != nil || r1["gap"] != "no data" || r1["hint"] != "no requests" {
		t.Fatalf("row 1 = %v", r1)
	}
	r2 := asMap(t, rows[2])
	if r2["source"] != "error_reporting" || num(r2["groups"]) != 3 || num(r2["occurrences"]) != 40 || r2["c4xx"] != nil {
		t.Fatalf("row 2 = %v", r2)
	}
	tot = asMap(t, tbl["totals"])
	if num(tot["c2xx"]) != 1000 || num(tot["c4xx"]) != 12 || num(tot["c5xx"]) != 1 || num(tot["groups"]) != 3 || num(tot["occurrences"]) != 40 {
		t.Fatalf("totals = %v", tot)
	}

	_, b, _ := f.doJSON(t, http.MethodGet, "/api/v1/errors/table?scope=project-b", "")
	rb := asList(t, b["rows"])
	tb := asMap(t, b["totals"])
	if len(rb) != 1 || tb["c4xx"] != nil || tb["groups"] != nil {
		t.Fatalf("project-b = %v", b)
	}
	res, body, _ := f.doJSON(t, http.MethodGet, "/api/v1/errors/table?scope=zzz", "")
	expectError(t, res, body, 400, "invalid_scope")
	res, body, _ = f.doJSON(t, http.MethodGet, "/api/v1/errors/table?week=bad", "")
	expectError(t, res, body, 400, "invalid_week")
}

func limitByID(t *testing.T, body map[string]any, id string) map[string]any {
	t.Helper()
	for _, l := range asList(t, body["limits"]) {
		if lm := asMap(t, l); lm["id"] == id {
			return lm
		}
	}
	t.Fatalf("no limit %s", id)
	return nil
}

func TestLimits_GetPutValidationAndMonthToDate(t *testing.T) {
	f := p2Fixture(t)
	// Two weeks of the same month (Monday Oct 12 and Oct 19): month-to-date sums both.
	f.deps.Refresh.Collectors = []refresh.Collector{collector("monitoring_logging", nil,
		metricRowOf(gcpsource.GroupLogging, "project-a", "", map[string]gcpsource.Cell{gcpsource.MLoggingGB: fcell(5, "GB", "monitoring_logging")}))}
	// Stand-in clock: W42 refresh then W43 refresh.
	f.refreshBody(t, `{"mode":"week","week":"2026-W42"}`)
	f.refreshBody(t, `{"mode":"week","week":"2026-W43"}`)

	res, lim, _ := f.doJSON(t, http.MethodGet, "/api/v1/limits", "")
	if res.StatusCode != 200 || lim["week"] != "2026-W43" || lim["month"] != "2026-10" || lim["month_start"] != "2026-10-01" || lim["month_end"] != "2026-10-31" ||
		num(lim["warn_pct"]) != 80 || lim["updated_at"] != nil {
		t.Fatalf("limits = %v", lim)
	}
	all := asList(t, lim["limits"])
	if len(all) != len(limits.Defaults()) {
		t.Fatalf("limits = %d", len(all))
	}
	lg := limitByID(t, lim, "logging_gb")
	if num(lg["used"]) != 10 || num(lg["week_used"]) != 5 || num(lg["allowance"]) != 50 || num(lg["default_allowance"]) != 50 || lg["stub"] != false || lg["reason"] != nil {
		t.Fatalf("logging_gb = %v", lg)
	}
	if proj := num(lg["projected"]); proj <= 10 || lg["pct"] == nil || lg["level"] == "no data" {
		t.Fatalf("logging_gb projection = %v", lg)
	}
	stub := limitByID(t, lim, "groq_llama4_scout_rpm")
	if stub["level"] != "stub" || stub["stub"] != true || stub["reason"] == nil || stub["used"] != nil || stub["pct"] != nil {
		t.Fatalf("stub = %v", stub)
	}
	nd := limitByID(t, lim, "gcs_class_a_ops")
	if nd["level"] != "no data" || nd["used"] != nil || nd["week_used"] != nil || nd["projected"] != nil {
		t.Fatalf("no-data limit = %v", nd)
	}

	// W42 only counts W42's snapshot in the month.
	_, l42, _ := f.doJSON(t, http.MethodGet, "/api/v1/limits?week=2026-W42", "")
	if num(limitByID(t, l42, "logging_gb")["used"]) != 5 {
		t.Fatalf("W42 used = %v", limitByID(t, l42, "logging_gb"))
	}

	// PUT: lower the allowance → over; response is the GET body; persisted.
	res, put, _ := f.doJSON(t, http.MethodPut, "/api/v1/limits", `{"allowances":{"logging_gb":10,"gcs_class_a_ops":0},"warn_pct":90}`)
	if res.StatusCode != 200 || num(put["warn_pct"]) != 90 || put["updated_at"] == nil {
		t.Fatalf("put = %d %v", res.StatusCode, put)
	}
	lg = limitByID(t, put, "logging_gb")
	if num(lg["allowance"]) != 10 || num(lg["default_allowance"]) != 50 || lg["level"] != "over" {
		t.Fatalf("after put = %v", lg)
	}
	_, again, _ := f.doJSON(t, http.MethodGet, "/api/v1/limits", "")
	if num(limitByID(t, again, "logging_gb")["allowance"]) != 10 || num(again["warn_pct"]) != 90 {
		t.Fatalf("not persisted: %v", again)
	}
	// Reset by PUTting the default; stubs are editable.
	_, reset, _ := f.doJSON(t, http.MethodPut, "/api/v1/limits", `{"allowances":{"logging_gb":50,"groq_llama4_scout_rpm":60}}`)
	if num(limitByID(t, reset, "logging_gb")["allowance"]) != 50 || num(limitByID(t, reset, "groq_llama4_scout_rpm")["allowance"]) != 60 || num(reset["warn_pct"]) != 90 {
		t.Fatalf("reset = %v", reset)
	}

	// Validation: nothing is saved on failure.
	before, _ := store.GetSettingsWithPrefix(context.Background(), f.store.DB, "limit.")
	for _, c := range []struct{ body, code string }{
		{`{"allowances":{"nope":5}}`, "invalid_limit"},
		{`{"allowances":{"logging_gb":-1}}`, "invalid_limit"},
		{`{"warn_pct":0}`, "invalid_limit"},
		{`{"warn_pct":101}`, "invalid_limit"},
		{`{"allowances":{"logging_gb":5},"warn_pct":150}`, "invalid_limit"},
		{`{"allowances":{"logging_gb":"lots"}}`, "invalid_request"},
		{`{"warn_pct":"x"}`, "invalid_request"},
		{`{}`, "invalid_request"},
		{``, "invalid_request"},
		{`{"unknown":1}`, "invalid_request"},
	} {
		res, body, _ := f.doJSON(t, http.MethodPut, "/api/v1/limits", c.body)
		expectError(t, res, body, 400, c.code)
	}
	after, _ := store.GetSettingsWithPrefix(context.Background(), f.store.DB, "limit.")
	if len(before) != len(after) {
		t.Fatalf("settings changed by rejected PUTs: %v -> %v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("setting %s changed %q -> %q", k, v, after[k])
		}
	}
	res, body, _ := f.doJSON(t, http.MethodGet, "/api/v1/limits?week=zzz", "")
	expectError(t, res, body, 400, "invalid_week")
}

func TestCostMonth_RowsDeltasScopeSummaryAndErrors(t *testing.T) {
	f := p2Fixture(t)

	// Never fetched: rows without snapshots, summary has_data false.
	res, empty, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/month?from=2026-09&to=2026-10", "")
	if res.StatusCode != 200 {
		t.Fatalf("empty: %v", empty)
	}
	sum := asMap(t, empty["summary"])
	if sum["has_data"] != false || num(sum["net"]) != 0 || sum["target"] != nil || sum["month"] != "2026-10" || sum["status"] != "partial" {
		t.Fatalf("empty summary = %v", sum)
	}
	for _, r := range asList(t, empty["rows"]) {
		rm := asMap(t, r)
		if rm["snapshot"] != nil || rm["cells"] != nil || rm["pct_change"] != nil || len(asList(t, rm["invoice_months"])) != 0 {
			t.Fatalf("empty row = %v", rm)
		}
	}

	f.refreshBody(t, `{"mode":"all"}`)
	if err := store.PutSettings(context.Background(), f.store.DB, map[string]string{store.KeyMonthlyTarget: "2500"}, phase1Now); err != nil {
		t.Fatal(err)
	}
	_, tbl, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/month?from=2026-09&to=2026-10", "")
	cols := asList(t, tbl["columns"])
	first := asMap(t, cols[0])
	if first["key"] != "month" || first["kind"] != "month" || first["label"] != "Month" {
		t.Fatalf("first column = %v", first)
	}
	var keys []string
	for _, c := range cols {
		keys = append(keys, asMap(t, c)["key"].(string))
	}
	k := strings.Join(keys, ",")
	if !strings.HasPrefix(k, "month,compute_engine,networking,cloud_sql,") || !strings.Contains(k, "artifact_registry,svc:Cloud Monitoring,total,pct_change,stub:groq,stub:claude") {
		t.Fatalf("columns = %s", k)
	}
	if num(asMap(t, tbl["thresholds"])["pct"]) != 20 {
		t.Fatalf("thresholds = %v", tbl["thresholds"])
	}
	rows := asList(t, tbl["rows"])
	sep, oct := asMap(t, rows[0]), asMap(t, rows[1])
	if sep["month"] != "2026-09" || oct["month"] != "2026-10" || sep["month_status"] != "final" || oct["month_status"] != "partial" || sep["month_start"] != "2026-09-01" || oct["month_end"] != "2026-10-31" {
		t.Fatalf("rows = %v / %v", sep, oct)
	}
	sc := asMap(t, sep["cells"])
	// Sep: compute gross 100, credits -10 → net recomputed by the store = 90; cloud sql 50.
	if num(asMap(t, sc["compute_engine"])["net"]) != 90 || num(asMap(t, sc["compute_engine"])["credits"]) != -10 || num(asMap(t, sc["total"])["net"]) != 140 {
		t.Fatalf("sep cells = %v", sc)
	}
	// August has an (empty) snapshot, so the delta exists but its pct is null (previous 0).
	if num(asMap(t, sc["compute_engine"])["delta_abs"]) != 90 || asMap(t, sc["compute_engine"])["delta_pct"] != nil || sep["pct_change"] != nil || sep["flagged"] != false {
		t.Fatalf("sep delta vs empty Aug: %v", sc["compute_engine"])
	}
	oc := asMap(t, oct["cells"])
	if num(asMap(t, oc["compute_engine"])["net"]) != 180 || num(asMap(t, oc["svc:Cloud Monitoring"])["net"]) != 5 || num(asMap(t, oc["total"])["net"]) != 245 {
		t.Fatalf("oct cells = %v", oc)
	}
	tc := asMap(t, oc["total"])
	if num(tc["delta_abs"]) != 105 || num(tc["delta_pct"]) != 75 || tc["flagged"] != true || oct["flagged"] != true || num(oct["pct_change"]) != 75 {
		t.Fatalf("oct total = %v", tc)
	}
	if inv := asList(t, oct["invoice_months"]); len(inv) != 1 || inv[0] != "202610" {
		t.Fatalf("invoice_months = %v", inv)
	}
	snap := asMap(t, oct["snapshot"])
	if snap["stale"] != false || snap["status_at_fetch"] != "partial" || num(snap["row_count"]) != 3 {
		t.Fatalf("snapshot = %v", snap)
	}
	if _, ok := sc["stub:groq"]; ok {
		t.Fatal("stub columns have no cells")
	}

	// Summary: current month, target from settings, projected linearly.
	sm := asMap(t, tbl["summary"])
	if sm["has_data"] != true || num(sm["net"]) != 245 || num(sm["target"]) != 2500 || sm["status"] != "partial" || num(sm["projected"]) <= 245 {
		t.Fatalf("summary = %v", sm)
	}

	// Scope filter on project_id; delta uses the same scope.
	_, scoped, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/month?from=2026-10&to=2026-10&scope=project-b", "")
	so := asMap(t, asList(t, scoped["rows"])[0])
	if num(asMap(t, asMap(t, so["cells"])["total"])["net"]) != 60 || num(asMap(t, asMap(t, so["cells"])["total"])["delta_abs"]) != 10 {
		t.Fatalf("scoped = %v", so)
	}
	if _, ok := asMap(t, so["cells"])["svc:Cloud Monitoring"]; ok {
		t.Fatal("extra column must disappear when out of scope")
	}
	if num(asMap(t, scoped["summary"])["net"]) != 60 {
		t.Fatalf("scoped summary = %v", scoped["summary"])
	}

	// Defaults: first_week's month .. current month.
	_, def, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/month", "")
	dr := asList(t, def["rows"])
	if def["from"] != "2026-04" || def["to"] != "2026-10" || len(dr) != 7 {
		t.Fatalf("defaults: from=%v to=%v rows=%d", def["from"], def["to"], len(dr))
	}

	for _, c := range []struct{ q, code string }{
		{"?from=2026-13", "invalid_month"},
		{"?to=2026-1", "invalid_month"},
		{"?from=abc", "invalid_month"},
		{"?to=2026-11", "month_out_of_range"},
		{"?from=2026-10&to=2026-09", "month_out_of_range"},
		{"?scope=nope", "invalid_scope"},
	} {
		res, body, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/month"+c.q, "")
		expectError(t, res, body, 400, c.code)
	}
}

func TestCostMonth_NoPreviousSnapshotMeansNullDeltas(t *testing.T) {
	f := p2Fixture(t)
	f.refreshBody(t, `{"mode":"month","month":"2026-10"}`)
	_, tbl, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/month?from=2026-10&to=2026-10", "")
	row := asMap(t, asList(t, tbl["rows"])[0])
	total := asMap(t, asMap(t, row["cells"])["total"])
	if total["delta_abs"] != nil || total["delta_pct"] != nil || total["flagged"] != false || row["pct_change"] != nil || row["flagged"] != false {
		t.Fatalf("total = %v", total)
	}
	// Other months stay snapshot-less.
	res, body, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/month?from=2026-09&to=2026-10", "")
	if res.StatusCode != 200 || asMap(t, asList(t, body["rows"])[0])["snapshot"] != nil {
		t.Fatalf("body = %v", body)
	}
}
