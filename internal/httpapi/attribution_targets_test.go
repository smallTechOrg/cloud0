package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/attribution"
	"github.com/smallTechOrg/cloud0/internal/billing"
	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/logging"
	"github.com/smallTechOrg/cloud0/internal/refresh"
	"github.com/smallTechOrg/cloud0/internal/store"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

// targetsFixture is the Phase 2 fixture (billing weeks + month snapshots from
// fakes) with the attribution engine wired and three stored rules:
// vm-a -> alpha, db -> beta, Networking -> shared.
func targetsFixture(t *testing.T, refresh bool) *fixture {
	t.Helper()
	f := p2Fixture(t)
	f.deps.Attribution = &AttributionDeps{NewEngine: attribution.NewEngine}
	f.srv.Close()
	f.srv = httptest.NewServer(NewHandler(f.deps))
	t.Cleanup(f.srv.Close)
	rules := []store.AttributionRule{
		{Priority: 300, MatchField: "resource_name", MatchOp: "contains", MatchValue: "vm-a", Product: "alpha", Enabled: true, Source: "user"},
		{Priority: 301, MatchField: "resource_name", MatchOp: "contains", MatchValue: "db", Product: "beta", Enabled: true, Source: "user"},
		{Priority: 900, MatchField: "service", MatchOp: "equals", MatchValue: "Networking", Shared: true, Enabled: true, Source: "user"},
	}
	if _, err := store.ReplaceAttributionRules(context.Background(), f.store.DB, rules, phase1Now); err != nil {
		t.Fatal(err)
	}
	if refresh {
		f.refreshBody(t, `{"mode":"all"}`)
	}
	return f
}

func (f *fixture) getTargets(t *testing.T, query string) map[string]any {
	t.Helper()
	res, m, raw := f.doJSON(t, http.MethodGet, "/api/v1/targets"+query, "")
	if res.StatusCode != 200 {
		t.Fatalf("GET /targets%s: %d %s", query, res.StatusCode, raw)
	}
	return m
}

func sumTargets(t *testing.T, m map[string]any) float64 {
	t.Helper()
	s := num(asMap(t, m["shared"])["net"]) + num(asMap(t, m["unattributed"])["net"])
	for _, p := range asList(t, m["products"]) {
		s += num(asMap(t, p)["net"])
	}
	return s
}

func productRow(t *testing.T, m map[string]any, name string) map[string]any {
	t.Helper()
	for _, p := range asList(t, m["products"]) {
		if pm := asMap(t, p); pm["product"] == name {
			return pm
		}
	}
	t.Fatalf("no product %q in %v", name, m["products"])
	return nil
}

func TestTargets_RoundTripAndReconciliation(t *testing.T) {
	f := targetsFixture(t, true)
	_, cm, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/month", "")
	monthNet := num(asMap(t, cm["summary"])["net"])

	before := f.getTargets(t, "")
	if before["month"] != "2026-10" || before["month_status"] != "partial" || before["currency"] == "" {
		t.Fatalf("header = %v", before)
	}
	acc := asMap(t, before["account"])
	if acc["target"] != nil || acc["status"] != "no_target" || math.Abs(num(acc["net"])-monthNet) > 0.005 {
		t.Fatalf("account before = %v (month net %v)", acc, monthNet)
	}

	res, put, raw := f.doJSON(t, http.MethodPut, "/api/v1/targets",
		`{"month_from":"2026-10","targets":[{"product":"*","amount":2000},{"product":" Alpha ","amount":500},{"product":"gamma","amount":10}]}`)
	if res.StatusCode != 200 {
		t.Fatalf("PUT: %d %s", res.StatusCode, raw)
	}
	got := f.getTargets(t, "")
	if !reflect.DeepEqual(put, got) {
		t.Fatalf("PUT body differs from GET:\nput %v\nget %v", put, got)
	}
	if s := sumTargets(t, got); math.Abs(s-num(asMap(t, got["account"])["net"])) > 0.005 {
		t.Fatalf("Σ rows %v != account net %v", s, got["account"])
	}
	acc = asMap(t, got["account"])
	if num(acc["target"]) != 2000 || acc["month_from"] != "2026-10" || acc["status"] == "no_target" {
		t.Fatalf("account = %v", acc)
	}
	// Shares from the two stored weeks: alpha 250, beta 100, shared 60, unattributed 45 of 455.
	alpha := productRow(t, got, "alpha")
	if want := 100 * 250.0 / 455.0; math.Abs(num(alpha["share_pct"])-want) > 1e-6 {
		t.Fatalf("alpha share = %v want %v", alpha["share_pct"], want)
	}
	if want := num(acc["net"]) * 250 / 455; math.Abs(num(alpha["net"])-want) > 0.005 || num(alpha["target"]) != 500 {
		t.Fatalf("alpha = %v want net %v", alpha, want)
	}
	if want := 100 * 60.0 / 455.0; math.Abs(num(asMap(t, got["shared"])["share_pct"])-want) > 1e-6 {
		t.Fatalf("shared = %v", got["shared"])
	}
	if g := productRow(t, got, "gamma"); g["status"] == "no_target" || num(g["net"]) != 0 {
		t.Fatalf("gamma = %v", g)
	}
	if sw := asList(t, got["share_weeks"]); len(sw) < 2 || sw[len(sw)-1] != "2026-W42" && sw[len(sw)-1] != "2026-W44" {
		t.Fatalf("share_weeks = %v", sw)
	}
	if len(asList(t, got["history"])) != 3 {
		t.Fatalf("history = %v", got["history"])
	}

	// The account target is the Month tab's target.
	st, err := store.GetMonthlyTarget(context.Background(), f.store.DB)
	if err != nil || st == nil || *st != 2000 {
		t.Fatalf("setting = %v %v", st, err)
	}
	_, cm, _ = f.doJSON(t, http.MethodGet, "/api/v1/cost/month", "")
	if num(asMap(t, cm["summary"])["target"]) != 2000 {
		t.Fatalf("month summary = %v", cm["summary"])
	}

	// Clearing the account target clears the setting and the row.
	_, put, _ = f.doJSON(t, http.MethodPut, "/api/v1/targets", `{"targets":[{"product":"*","amount":null}]}`)
	if asMap(t, put["account"])["target"] != nil || asMap(t, put["account"])["status"] != "no_target" {
		t.Fatalf("cleared account = %v", put["account"])
	}
	if st, _ := store.GetMonthlyTarget(context.Background(), f.store.DB); st != nil {
		t.Fatalf("setting after clear = %v", *st)
	}
}

func TestTargets_EffectiveMonthAcrossMonths(t *testing.T) {
	f := targetsFixture(t, true)
	for _, body := range []string{
		`{"month_from":"2026-09","targets":[{"product":"alpha","amount":100}]}`,
		`{"month_from":"2026-10","targets":[{"product":"alpha","amount":300}]}`,
		`{"month_from":"2026-11","targets":[{"product":"alpha","amount":999}]}`,
	} {
		if res, m, _ := f.doJSON(t, http.MethodPut, "/api/v1/targets", body); res.StatusCode != 200 {
			t.Fatalf("PUT %s: %v", body, m)
		}
	}
	for month, want := range map[string]float64{"2026-09": 100, "2026-10": 300} {
		a := productRow(t, f.getTargets(t, "?month="+month), "alpha")
		if num(a["target"]) != want || a["month_from"] != month {
			t.Fatalf("%s alpha = %v want %v", month, a, want)
		}
	}
	// September has no weekly share data of its own: no_data, not zeros-as-fact.
	if a := productRow(t, f.getTargets(t, "?month=2026-09"), "alpha"); a["status"] != "no_data" {
		t.Fatalf("sept alpha = %v", a)
	}
	// Re-PUT of the same (product, month_from) replaces the row.
	f.doJSON(t, http.MethodPut, "/api/v1/targets", `{"month_from":"2026-10","targets":[{"product":"alpha","amount":400}]}`)
	m := f.getTargets(t, "")
	if num(productRow(t, m, "alpha")["target"]) != 400 || len(asList(t, m["history"])) != 3 {
		t.Fatalf("after replace: %v / history %v", productRow(t, m, "alpha"), m["history"])
	}
}

func TestTargets_InvalidPayloads(t *testing.T) {
	f := targetsFixture(t, false)
	long := strings.Repeat("a", 65)
	cases := map[string]string{
		"negative":         `{"targets":[{"product":"alpha","amount":-1}]}`,
		"bad product":      `{"targets":[{"product":"Bad Name!","amount":1}]}`,
		"empty product":    `{"targets":[{"product":"  ","amount":1}]}`,
		"too long":         `{"targets":[{"product":"` + long + `","amount":1}]}`,
		"bad month_from":   `{"month_from":"2026-13","targets":[{"product":"alpha","amount":1}]}`,
		"month_from text":  `{"month_from":"soon","targets":[{"product":"alpha","amount":1}]}`,
		"empty targets":    `{"targets":[]}`,
		"missing targets":  `{"month_from":"2026-10"}`,
		"duplicate":        `{"targets":[{"product":"alpha","amount":1},{"product":"alpha","amount":2}]}`,
		"valid then wrong": `{"targets":[{"product":"*","amount":5},{"product":"alpha","amount":-3}]}`,
	}
	for name, body := range cases {
		res, m, _ := f.doJSON(t, http.MethodPut, "/api/v1/targets", body)
		t.Run(name, func(t *testing.T) { expectError(t, res, m, 400, "invalid_target") })
	}
	// Nothing was saved by any of them (atomic).
	if h := asList(t, f.getTargets(t, "")["history"]); len(h) != 0 {
		t.Fatalf("history after rejected PUTs = %v", h)
	}
	if st, _ := store.GetMonthlyTarget(context.Background(), f.store.DB); st != nil {
		t.Fatalf("setting written by a rejected PUT: %v", *st)
	}
	for name, body := range map[string]string{
		"not json":       `nope`,
		"string amount":  `{"targets":[{"product":"alpha","amount":"5"}]}`,
		"unknown field":  `{"targets":[{"product":"alpha","amount":5}],"extra":1}`,
		"two documents":  `{"targets":[{"product":"alpha","amount":5}]} {}`,
		"overflow float": `{"targets":[{"product":"alpha","amount":1e999}]}`,
	} {
		res, m, _ := f.doJSON(t, http.MethodPut, "/api/v1/targets", body)
		t.Run(name, func(t *testing.T) { expectError(t, res, m, 400, "invalid_request") })
	}
	res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/targets?month=oct", "")
	expectError(t, res, m, 400, "invalid_month")
	res, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/targets?month=2027-01", "")
	expectError(t, res, m, 400, "month_out_of_range")
	res, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/targets?scope=nope", "")
	expectError(t, res, m, 400, "invalid_scope")
	res, m, _ = f.doJSON(t, http.MethodDelete, "/api/v1/targets", "")
	expectError(t, res, m, 405, "method_not_allowed")
}

func TestTargets_EmptyDataIsHonest(t *testing.T) {
	f := targetsFixture(t, false)
	m := f.getTargets(t, "")
	acc := asMap(t, m["account"])
	if acc["status"] != "no_data" || acc["target"] != nil || num(acc["net"]) != 0 {
		t.Fatalf("account = %v", acc)
	}
	if len(asList(t, m["products"])) != 0 || len(asList(t, m["share_weeks"])) != 0 || m["covered_until"] != nil || len(asList(t, m["history"])) != 0 {
		t.Fatalf("body = %v", m)
	}
	// A target on an unfetched month shows the target with no_data, never a fake status.
	_, put, _ := f.doJSON(t, http.MethodPut, "/api/v1/targets", `{"targets":[{"product":"*","amount":100},{"product":"alpha","amount":10}]}`)
	if a := asMap(t, put["account"]); a["status"] != "no_data" || num(a["target"]) != 100 || a["pct"] != nil {
		t.Fatalf("account = %v", a)
	}
	if a := productRow(t, put, "alpha"); a["status"] != "no_data" || num(a["target"]) != 10 {
		t.Fatalf("alpha = %v", a)
	}
}

func TestTargets_NoRulesMeansUnattributed(t *testing.T) {
	f := targetsFixture(t, true)
	if _, err := store.ReplaceAttributionRules(context.Background(), f.store.DB, nil, phase1Now); err != nil {
		t.Fatal(err)
	}
	m := f.getTargets(t, "")
	if len(asList(t, m["products"])) != 0 || num(asMap(t, m["shared"])["net"]) != 0 {
		t.Fatalf("body = %v", m)
	}
	if u := asMap(t, m["unattributed"]); math.Abs(num(u["net"])-num(asMap(t, m["account"])["net"])) > 0.005 || math.Abs(num(u["share_pct"])-100) > 1e-9 {
		t.Fatalf("unattributed = %v", u)
	}
}

func TestTargets_ConcurrentPutsAreSafe(t *testing.T) {
	f := targetsFixture(t, true)
	const n = 12
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"month_from":"2026-10","targets":[{"product":"*","amount":%d},{"product":"alpha","amount":%d}]}`, 1000+i, 100+i)
			res, _, _ := f.doJSON(t, http.MethodPut, "/api/v1/targets", body)
			codes[i] = res.StatusCode
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("PUT %d -> %d", i, c)
		}
	}
	m := f.getTargets(t, "")
	acc := num(asMap(t, m["account"])["target"])
	alpha := num(productRow(t, m, "alpha")["target"])
	if acc < 1000 || acc >= 1000+n || alpha-100 != acc-1000 {
		t.Fatalf("torn write: account %v alpha %v", acc, alpha)
	}
	st, _ := store.GetMonthlyTarget(context.Background(), f.store.DB)
	if st == nil || *st != acc {
		t.Fatalf("setting %v != account target %v", st, acc)
	}
	if h := asList(t, m["history"]); len(h) != 2 {
		t.Fatalf("history = %v (one row per product/month)", h)
	}
}

func TestTargets_NotConfigured(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{}, &now)
	res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/targets", "")
	expectError(t, res, m, 503, "attribution_not_configured")
}

// TestLive_TargetsReconcile refreshes real data (month 2026-10, week 2026-W42),
// sets a target and checks the reconciliation invariants against the real
// Month figure. The names used are placeholders.
func TestLive_TargetsReconcile(t *testing.T) {
	testenv.RequireBigQuery(t)
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		t.Fatalf("config.Load from .env: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	src, err := billing.New(ctx, &cfg)
	if err != nil {
		t.Fatalf("billing.New: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	st, err := store.Open(ctx, testenv.TempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	logger := logging.NewWriter(&bytes.Buffer{}, "info")
	runner := &refresh.Runner{Store: st, Source: src, FirstWeek: cfg.FirstWeek, Settle: time.Duration(cfg.SettleHours) * time.Hour,
		MarginDays: cfg.PartitionMarginDays, Projects: cfg.Projects, Logger: logger,
		Month: refresh.BQMonthSource{Client: src.Client(), Table: cfg.BillingExportTable, MarginDays: cfg.PartitionMarginDays}}
	deps := Deps{Config: cfg, Store: st, Logger: logger, Version: "test", Refresh: runner,
		Attribution: &AttributionDeps{NewEngine: attribution.NewEngine}}
	f := &fixture{store: st, deps: deps}
	f.srv = httptest.NewServer(NewHandler(deps))
	t.Cleanup(f.srv.Close)
	rules := []store.AttributionRule{
		{Priority: 300, MatchField: "service", MatchOp: "equals", MatchValue: "Compute Engine", Product: "example-compute", Enabled: true, Source: "user"},
		{Priority: 900, MatchField: "service", MatchOp: "equals", MatchValue: "Networking", Shared: true, Enabled: true, Source: "user"},
	}
	if _, err := store.ReplaceAttributionRules(context.Background(), f.store.DB, rules, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Logf("month refresh: %v", f.refreshBody(t, `{"mode":"month","month":"2026-10"}`)["run"])
	f.refreshBody(t, `{"mode":"week","week":"2026-W42"}`)

	_, cm, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/month?from=2026-10&to=2026-10", "")
	monthNet := num(asMap(t, cm["summary"])["net"])
	t.Logf("summary: %v", cm["summary"])

	res, put, raw := f.doJSON(t, http.MethodPut, "/api/v1/targets?month=2026-10",
		`{"month_from":"2026-10","targets":[{"product":"*","amount":123456},{"product":"example-compute","amount":50000}]}`)
	if res.StatusCode != 200 {
		t.Fatalf("PUT: %d %s", res.StatusCode, raw)
	}
	got := f.getTargets(t, "?month=2026-10")
	// Wall clock: projections move between calls, so compare the stored facts.
	for _, k := range []string{"target", "month_from", "net"} {
		if !reflect.DeepEqual(asMap(t, put["account"])[k], asMap(t, got["account"])[k]) {
			t.Fatalf("PUT account.%s %v != GET %v", k, asMap(t, put["account"])[k], asMap(t, got["account"])[k])
		}
	}
	if !reflect.DeepEqual(put["history"], got["history"]) || len(asList(t, put["products"])) != len(asList(t, got["products"])) {
		t.Fatalf("PUT body differs from GET")
	}
	acc := asMap(t, got["account"])
	if monthNet <= 0 || math.Abs(num(acc["net"])-monthNet) > 0.005 {
		t.Fatalf("account net %v != Month tab net %v", acc["net"], monthNet)
	}
	if s := sumTargets(t, got); math.Abs(s-monthNet) > 0.005 {
		t.Fatalf("Σ rows %v != month net %v", s, monthNet)
	}
	setting, err := store.GetMonthlyTarget(context.Background(), f.store.DB)
	if err != nil || setting == nil || num(acc["target"]) != *setting || *setting != 123456 {
		t.Fatalf("account target %v != setting %v (%v)", acc["target"], setting, err)
	}
	if sw := asList(t, got["share_weeks"]); len(sw) == 0 {
		t.Fatalf("no share weeks: %v", got)
	}
	t.Logf("month net %.2f; account %v; products %v; shared %v; unattributed %v", monthNet, acc["status"], got["products"], got["shared"], got["unattributed"])
}
