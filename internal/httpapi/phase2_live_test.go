package httpapi

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/billing"
	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/errorsrc"
	"github.com/smallTechOrg/cloud0/internal/limits"
	"github.com/smallTechOrg/cloud0/internal/logging"
	"github.com/smallTechOrg/cloud0/internal/monitoring"
	"github.com/smallTechOrg/cloud0/internal/refresh"
	"github.com/smallTechOrg/cloud0/internal/store"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

// TestLive_Phase2Journey refreshes real data (mode=all, then the smoke week)
// with every real source wired exactly as cmd/cloud0 does, then checks every
// Phase 2 endpoint against it.
func TestLive_Phase2Journey(t *testing.T) {
	testenv.RequireBigQuery(t)
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		t.Fatalf("config.Load from .env: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	src, err := billing.New(ctx, &cfg)
	if err != nil {
		t.Fatalf("billing.New: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	lister, closeMon, err := monitoring.NewLister(ctx)
	if err != nil {
		t.Fatalf("monitoring.NewLister: %v", err)
	}
	t.Cleanup(func() { _ = closeMon() })
	er, closeER, err := errorsrc.NewErrorReportingLister(ctx)
	if err != nil {
		t.Fatalf("NewErrorReportingLister: %v", err)
	}
	t.Cleanup(func() { _ = closeER() })
	logs, closeLogs, err := errorsrc.NewLogCounter(ctx, cfg.Projects)
	if err != nil {
		t.Fatalf("NewLogCounter: %v", err)
	}
	t.Cleanup(func() { _ = closeLogs() })

	st, err := store.Open(ctx, testenv.TempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runner := &refresh.Runner{Store: st, Source: src, FirstWeek: cfg.FirstWeek, Settle: time.Duration(cfg.SettleHours) * time.Hour,
		MarginDays: cfg.PartitionMarginDays, Projects: cfg.Projects,
		Month: refresh.BQMonthSource{Client: src.Client(), Table: cfg.BillingExportTable, MarginDays: cfg.PartitionMarginDays}}
	runner.Collectors = refresh.StandardCollectors(refresh.Sources{Projects: cfg.Projects, Monitoring: lister, ErrReporting: er, Logs: logs,
		BigQuery: src.Client(), BillingTable: cfg.BillingExportTable, MarginDays: cfg.PartitionMarginDays})
	logger := logging.NewWriter(&bytes.Buffer{}, "info")
	runner.Logger = logger
	deps := Deps{Config: cfg, Store: st, Logger: logger, Version: "test", Refresh: runner}
	srv := httptest.NewServer(NewHandler(deps))
	t.Cleanup(srv.Close)
	f := &fixture{srv: srv, store: st, deps: deps}
	week := cfg.SmokeWeek.String()

	// 1. mode=all: billing, month, then the ten week-window sources.
	m := f.refreshBody(t, `{"mode":"all"}`)
	run := asMap(t, m["run"])
	var names []string
	for _, s := range asList(t, run["sources"]) {
		sm := asMap(t, s)
		names = append(names, sm["source"].(string))
		if sm["status"] != "ok" {
			t.Logf("source %s: %v %v hint=%v", sm["source"], sm["error_code"], sm["error_message"], sm["fix_hint"])
		}
	}
	wantNames := "bigquery_billing,bigquery_month,monitoring_vm,monitoring_cloudsql,monitoring_cloudrun,monitoring_gcs,monitoring_logging,bigquery_usage,errors_cloudrun,errors_error_reporting,errors_load_balancer,errors_nginx"
	if strings.Join(names, ",") != wantNames {
		t.Fatalf("sources = %v", names)
	}
	if run["status"] != "ok" && run["status"] != "partial" {
		t.Fatalf("run status = %v", run["status"])
	}
	if asMap(t, asList(t, run["sources"])[0])["status"] != "ok" || asMap(t, asList(t, run["sources"])[1])["status"] != "ok" {
		t.Fatalf("billing / month cost must succeed: %v", run["sources"])
	}
	if len(asList(t, m["month_snapshots"])) < 2 || len(asList(t, m["metric_snapshots"])) != 1 {
		t.Fatalf("snapshots: month=%v metric=%v", m["month_snapshots"], m["metric_snapshots"])
	}

	// 2. Smoke week metrics + errors.
	m = f.refreshBody(t, `{"mode":"week","week":"`+week+`"}`)
	wrun := asMap(t, m["run"])
	if len(asList(t, wrun["sources"])) != 11 || len(asList(t, m["metric_snapshots"])) != 1 {
		t.Fatalf("week refresh = %v", wrun)
	}
	_, tbl, _ := f.doJSON(t, http.MethodGet, "/api/v1/metrics/table?week="+week, "")
	if tbl["snapshot"] == nil {
		t.Fatalf("no metric snapshot for %s: %v", week, tbl)
	}
	vm := asList(t, groupByName(t, tbl, "vm")["rows"])
	cpuCount, memOrNoAgent := 0, false
	for _, r := range vm {
		rm := asMap(t, r)
		if rm["resource"] == "" {
			continue
		}
		cells := asMap(t, rm["cells"])
		cpu := asMap(t, cells["cpu_pct"])
		if cpu["value"] != nil {
			if v := num(cpu["value"]); v <= 0 || v > 100 {
				t.Fatalf("vm %v cpu = %v", rm["resource"], v)
			}
			if asMap(t, cpu["provenance"])["query"] == "" {
				t.Fatalf("cpu cell without provenance query: %v", cpu)
			}
			cpuCount++
		}
		mem := asMap(t, cells["memory_pct"])
		if mem["value"] != nil || mem["gap"] == "no agent" {
			memOrNoAgent = true
		}
	}
	if cpuCount == 0 || !memOrNoAgent {
		t.Fatalf("vm rows = %v (cpu values %d, memory value or no agent %v)", vm, cpuCount, memOrNoAgent)
	}
	t.Logf("%s: %d VM rows, %d with CPU", week, len(vm), cpuCount)

	_, et, _ := f.doJSON(t, http.MethodGet, "/api/v1/errors/table?week="+week, "")
	var c4 float64
	var crRows int
	for _, r := range asList(t, et["rows"]) {
		rm := asMap(t, r)
		if rm["source"] == "cloudrun" && rm["c4xx"] != nil {
			crRows++
			c4 += num(rm["c4xx"])
		}
	}
	if et["snapshot"] == nil || crRows == 0 || c4 <= 0 {
		t.Fatalf("errors table: cloudrun rows=%d 4xx=%v (%v)", crRows, c4, et["rows"])
	}
	errGroup := asList(t, groupByName(t, tbl, "errors")["rows"])
	if len(errGroup) != crRows {
		t.Fatalf("errors group rows = %d, cloudrun error rows = %d", len(errGroup), crRows)
	}
	t.Logf("%s: cloudrun 4xx = %v over %d services; total rows %d", week, c4, crRows, len(asList(t, et["rows"])))

	// 3. Limits: a status for every allowance; non-stubs have a real level.
	_, lim, _ := f.doJSON(t, http.MethodGet, "/api/v1/limits?week="+week, "")
	withData := 0
	for _, l := range asList(t, lim["limits"]) {
		lm := asMap(t, l)
		switch lm["level"] {
		case "ok", "near", "over":
			withData++
			if lm["used"] == nil || lm["projected"] == nil || lm["pct"] == nil {
				t.Fatalf("limit %v has a level without numbers: %v", lm["id"], lm)
			}
		case "no data":
			t.Fatalf("non-stub limit %v has no data for %s", lm["id"], week)
		case "stub":
			if lm["stub"] != true || lm["reason"] == nil {
				t.Fatalf("stub %v: %v", lm["id"], lm)
			}
		default:
			t.Fatalf("limit %v level %v", lm["id"], lm["level"])
		}
		t.Logf("limit %-24v level=%-8v used=%v allowance=%v", lm["id"], lm["level"], lm["used"], lm["allowance"])
	}
	if len(asList(t, lim["limits"])) != len(limits.Defaults()) || withData == 0 {
		t.Fatalf("limits = %d entries, %d with data", len(asList(t, lim["limits"])), withData)
	}

	// 4. Month view: summary net > 0 and October equals the single aggregate.
	_, mt, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/month?from=2026-10&to=2026-10", "")
	sum := asMap(t, mt["summary"])
	if sum["has_data"] != true || num(sum["net"]) <= 0 || num(sum["projected"]) <= 0 {
		t.Fatalf("summary = %v", sum)
	}
	oct := asMap(t, asList(t, mt["rows"])[0])
	total := num(asMap(t, asMap(t, oct["cells"])["total"])["net"])
	indep := "SELECT SUM(cost) + SUM((SELECT IFNULL(SUM(c.amount), 0) FROM UNNEST(credits) AS c)) AS net\n" +
		"FROM `" + cfg.BillingExportTable + "`\n" +
		"WHERE DATE(_PARTITIONTIME) BETWEEN DATE '2026-09-30' AND DATE '2026-11-02'\n" +
		"  AND usage_start_time >= TIMESTAMP '2026-10-01 00:00:00+00'\n" +
		"  AND usage_start_time <  TIMESTAMP '2026-11-01 00:00:00+00'"
	it, err := src.Client().Query(indep).Read(ctx)
	if err != nil {
		t.Fatalf("independent query: %v", err)
	}
	var one struct{ Net float64 }
	if err := it.Next(&one); err != nil {
		t.Fatal(err)
	}
	if math.Abs(total-one.Net) > 0.005 || math.Abs(num(sum["net"])-total) > 0.005 {
		t.Fatalf("October total %.4f / summary %.4f != single aggregate %.4f", total, num(sum["net"]), one.Net)
	}
	t.Logf("October 2026 net %.2f == single aggregate %.2f; projected %.2f", total, one.Net, num(sum["projected"]))

	// 5. Run log: one row per source for both runs.
	_, runs, _ := f.doJSON(t, http.MethodGet, "/api/v1/runs?limit=5", "")
	list := asList(t, runs["runs"])
	if len(list) != 2 || len(asList(t, asMap(t, list[0])["sources"])) != 11 || len(asList(t, asMap(t, list[1])["sources"])) != 12 {
		t.Fatalf("run log = %v", list)
	}
	for _, s := range asList(t, asMap(t, list[1])["sources"]) {
		sm := asMap(t, s)
		if sm["status"] == "ok" && sm["rows_returned"] == nil {
			t.Fatalf("ok source without rows_returned: %v", sm)
		}
		if !strings.HasPrefix(sm["source"].(string), "bigquery_") && (sm["job_id"] != nil || sm["bytes_billed"] != nil) {
			t.Fatalf("non-BigQuery source with job stats: %v", sm)
		}
	}
}
