package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/attribution"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/logging"
	"github.com/smallTechOrg/cloud0/internal/store"
	"github.com/smallTechOrg/cloud0/internal/webui"
)

// ---- fakes ------------------------------------------------------------------

type rtSeeder struct{ calls int }

func (s *rtSeeder) Seed(rows []attribution.CostRow, opts attribution.SeedOptions) (attribution.SeedResult, error) {
	s.calls++
	note := "fake"
	kept, replaced := 0, 0
	for _, r := range opts.Existing {
		if r.Source == attribution.SourceSeed {
			replaced++
		} else {
			kept++
		}
	}
	return attribution.SeedResult{
		Proposed: []attribution.Rule{
			{Priority: 310, Match: attribution.Match{Field: "resource_name", Op: "contains", Value: "example-web"},
				Action: attribution.Action{Product: "example-product"}, Enabled: true, Source: "seed", Note: &note},
			{Priority: 320, Match: attribution.Match{Field: "resource_name", Op: "contains", Value: "example-orphan"},
				Action: attribution.Action{Product: "example-orphan", Environment: "prod"}, Enabled: true, Source: "seed"},
		},
		Counts:   attribution.SeedCounts{Rules: 2, ResourceName: 2, Products: 2, KeptUserRules: kept, ReplacedSeedRules: replaced},
		Products: []attribution.ProductSummary{{Product: "example-product", Net: 120, Resources: 1}},
	}, nil
}

type rtLabels struct{}

func (rtLabels) Suggest(rows []attribution.CostRow, eng attribution.Engine) ([]attribution.LabelSuggestion, int) {
	return []attribution.LabelSuggestion{{ProjectID: "project-a", ResourceName: "example-web", ResourceType: "unknown",
		Labels: map[string]string{"product": "example-product"}, Missing: []string{"product"}, Risk: "label in the console"}}, 3
}

type rtProposer struct {
	enabled bool
	err     error
	calls   int
	in      attribution.ProposeInput
}

func (p *rtProposer) Enabled() bool { return p.enabled }
func (p *rtProposer) Model() string { return "test/model" }
func (p *rtProposer) Propose(_ context.Context, in attribution.ProposeInput) (attribution.ProposeOutput, error) {
	p.calls++
	p.in = in
	if p.err != nil {
		return attribution.ProposeOutput{}, p.err
	}
	return attribution.ProposeOutput{Model: "test/model", LatencyMS: 5, InputTokens: 10, OutputTokens: 4, Dropped: 1,
		Proposals: []attribution.Proposal{
			{Rule: attribution.Rule{Priority: 330, Match: attribution.Match{Field: "resource_name", Op: "contains", Value: "example-orphan"},
				Action: attribution.Action{Product: "example-orphan"}, Enabled: true, Source: "proposed"}, Confidence: "medium", Rationale: "token", Covers: 1},
			{Rule: attribution.Rule{Priority: 331, Match: attribution.Match{Field: "resource_name", Op: "regex", Value: "("},
				Action: attribution.Action{Product: "bad"}, Enabled: true, Source: "proposed"}, Confidence: "low", Rationale: "bad regex"},
		}}, nil
}

// ---- fixture ----------------------------------------------------------------

type rtFx struct {
	*fixture
	seeder   *rtSeeder
	proposer *rtProposer
}

func rtNew(t *testing.T, withDeps bool) *rtFx {
	t.Helper()
	f := newFixture(t, true, phase1Now)
	rf := &rtFx{fixture: f, seeder: &rtSeeder{}, proposer: &rtProposer{enabled: true}}
	if withDeps {
		f.deps.Attribution = &AttributionDeps{NewEngine: attribution.NewEngine, Seeder: rf.seeder, Labels: rtLabels{}, Proposer: rf.proposer}
	}
	f.srv.Close()
	f.deps.Logger = logging.NewWriter(f.logs, "info")
	f.deps.UI = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ui") })
	f.deps.UIMode = webui.ModeReal
	f.srv = httptest.NewServer(NewHandler(f.deps))
	t.Cleanup(f.srv.Close)
	return rf
}

func (f *rtFx) call(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, rd)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s %s: not JSON: %q", method, path, raw)
	}
	return res.StatusCode, m
}

func rtErrCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func (f *rtFx) seedRows(t *testing.T, week string) {
	t.Helper()
	w := isoweek.MustParse(week)
	ctx := context.Background()
	run, err := store.CreateRun(ctx, f.store.DB, store.NewRun{Trigger: store.TriggerRefreshWeek, From: w, To: w, StartedAt: phase1Now})
	if err != nil {
		t.Fatal(err)
	}
	job := "job-rt-1"
	src, err := store.AddSource(ctx, f.store.DB, store.NewRunSource{RunID: run.ID, Source: store.SourceBigQueryBilling, Status: store.SourceOK,
		StartedAt: phase1Now, FinishedAt: phase1Now.Add(time.Second), SQLText: "SELECT 1", Params: map[string]string{}, JobID: &job})
	if err != nil {
		t.Fatal(err)
	}
	s := func(v string) *string { return &v }
	rows := []store.SnapshotRow{
		{ProjectID: "project-a", ServiceID: "s1", Service: "Compute Engine", SKUID: "k1", SKU: "N1 Core", ResourceName: s("example-web"),
			ResourceGlobalName: s("//compute.googleapis.com/projects/project-a/zones/zone-a/instances/example-web"), LabelsJSON: s(`[{"key":"env","value":"staging"}]`), Gross: 100, LineItems: 5},
		{ProjectID: "project-a", ServiceID: "s2", Service: "Cloud Run", SKUID: "k2", SKU: "CPU", ResourceName: s("example-web"), LabelsJSON: s(`[]`), Gross: 20, LineItems: 2},
		{ProjectID: "project-a", ServiceID: "s1", Service: "Compute Engine", SKUID: "k3", SKU: "Disk", ResourceName: s("example-orphan"),
			ResourceGlobalName: s("//compute.googleapis.com/projects/project-a/zones/zone-a/instances/example-orphan"), LabelsJSON: s(`[]`), Gross: 30, LineItems: 3},
		{ProjectID: "project-a", ServiceID: "s3", Service: "Networking", SKUID: "k4", SKU: "Egress", LabelsJSON: s(`[]`), Gross: 10, LineItems: 1},
		{ProjectID: "project-b", ServiceID: "s2", Service: "Cloud Run", SKUID: "k2", SKU: "CPU", ResourceName: s("example-worker"), LabelsJSON: s(`[]`), Gross: 5, LineItems: 1},
	}
	err = f.store.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := store.InsertSnapshot(ctx, tx, store.NewSnapshot{Week: w, FetchedAt: phase1Now, StatusAtFetch: isoweek.StatusFinal, RunID: run.ID, RunSourceID: src.ID, Rows: rows})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *rtFx) changeCount(t *testing.T) int {
	t.Helper()
	ch, err := store.ListChanges(context.Background(), f.store.DB, store.ChangeFilter{Kind: "attribution"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	return len(ch)
}

func (f *rtFx) rulesBody(t *testing.T) map[string]any {
	t.Helper()
	st, m := f.call(t, "GET", "/api/v1/attribution/rules", "")
	if st != 200 {
		t.Fatalf("GET rules %d %v", st, m)
	}
	return m
}

const rtUserRule = `{"priority":900,"match":{"field":"service","op":"equals","value":"Networking"},"action":{"shared":true},"enabled":true,"source":"user","note":"net"}`

// ---- tests ------------------------------------------------------------------

func TestAttributionRules_503WhenNotConfigured(t *testing.T) {
	f := rtNew(t, false)
	for _, c := range [][3]string{
		{"GET", "/rules", ""}, {"PUT", "/rules", `{"rules":[]}`}, {"POST", "/seed/preview", "{}"}, {"POST", "/seed/apply", "{}"},
		{"GET", "/unattributed", ""}, {"POST", "/explain", `{"resource":"x"}`}, {"GET", "/label-suggestions", ""}, {"POST", "/propose", "{}"},
	} {
		st, m := f.call(t, c[0], "/api/v1/attribution"+c[1], c[2])
		if st != 503 || rtErrCode(m) != "attribution_not_configured" {
			t.Errorf("%s %s: %d %v", c[0], c[1], st, m)
		}
	}
}

func TestAttributionRules_GetEmptyThenPutRoundTrip(t *testing.T) {
	f := rtNew(t, true)
	m := f.rulesBody(t)
	if r := m["rules"].([]any); len(r) != 0 || m["updated_at"] != nil || m["allocate"] != "none" {
		t.Fatalf("empty body: %v", m)
	}
	if p := m["proposer"].(map[string]any); p["enabled"] != true || p["model"] != "test/model" {
		t.Fatalf("proposer: %v", p)
	}
	st, put := f.call(t, "PUT", "/api/v1/attribution/rules",
		`{"rules":[`+rtUserRule+`,{"priority":100,"match":{"field":"label","op":"equals","key":"app","value":"x"},"action":{"product":" Example-Product "}}],"allocate":"even","aliases":{"Alias":"example-product"}}`)
	if st != 200 {
		t.Fatalf("PUT: %d %v", st, put)
	}
	rules := put["rules"].([]any)
	if len(rules) != 2 || rules[0].(map[string]any)["priority"].(float64) != 100 {
		t.Fatalf("sorted rules: %v", rules)
	}
	r0 := rules[0].(map[string]any)
	if r0["enabled"] != true || r0["source"] != "user" || r0["action"].(map[string]any)["product"] != "example-product" || r0["id"].(float64) <= 0 {
		t.Fatalf("defaults/normalisation: %v", r0)
	}
	if put["allocate"] != "even" || put["aliases"].(map[string]any)["alias"] != "example-product" || put["updated_at"] == nil {
		t.Fatalf("settings: %v", put)
	}
	if n := f.changeCount(t); n != 1 {
		t.Fatalf("change rows = %d", n)
	}
	// Survives: GET equals the PUT answer.
	if got := f.rulesBody(t); len(got["rules"].([]any)) != 2 || got["allocate"] != "even" {
		t.Fatalf("GET after PUT: %v", got)
	}
	// Identical PUT (ids kept) is a no-op for history.
	id0 := rules[0].(map[string]any)["id"]
	id1 := rules[1].(map[string]any)["id"]
	body, _ := json.Marshal(map[string]any{"rules": []any{rules[0], rules[1]}})
	if st, m := f.call(t, "PUT", "/api/v1/attribution/rules", string(body)); st != 200 {
		t.Fatalf("PUT again: %d %v", st, m)
	}
	if n := f.changeCount(t); n != 1 {
		t.Fatalf("no-op PUT wrote history: %d", n)
	}
	// Omitting a stored id deletes it; omitted allocate/aliases stay.
	body, _ = json.Marshal(map[string]any{"rules": []any{rules[1]}})
	_, m = f.call(t, "PUT", "/api/v1/attribution/rules", string(body))
	left := m["rules"].([]any)
	if len(left) != 1 || left[0].(map[string]any)["id"] != id1 || m["allocate"] != "even" || id0 == id1 {
		t.Fatalf("delete by omission: %v", m)
	}
	if n := f.changeCount(t); n != 2 {
		t.Fatalf("change rows = %d", n)
	}
	ch, _ := store.ListChanges(context.Background(), f.store.DB, store.ChangeFilter{Kind: "attribution"}, 10)
	if ch[0].Action != "update" || ch[0].Target != "rules" || !bytes.Contains(ch[0].Before, []byte("rules")) || !bytes.Contains(ch[0].After, []byte("rules")) {
		t.Fatalf("change row: %+v", ch[0])
	}
}

func TestAttributionRules_InvalidRuleIsAtomicAndNamesIndex(t *testing.T) {
	f := rtNew(t, true)
	f.call(t, "PUT", "/api/v1/attribution/rules", `{"rules":[`+rtUserRule+`],"allocate":"none"}`)
	before := f.rulesBody(t)
	cases := map[string]string{
		"regex":   `{"rules":[` + rtUserRule + `,{"priority":1,"match":{"field":"resource_name","op":"regex","value":"("},"action":{"product":"p"}}]}`,
		"nothing": `{"rules":[{"priority":1,"match":{"field":"sku","op":"equals","value":"x"},"action":{}}]}`,
		"badop":   `{"rules":[{"priority":1,"match":{"field":"sku","op":"like","value":"x"},"action":{"product":"p"}}]}`,
		"unkid":   `{"rules":[{"id":9999,"priority":1,"match":{"field":"sku","op":"equals","value":"x"},"action":{"product":"p"}}]}`,
	}
	for name, body := range cases {
		st, m := f.call(t, "PUT", "/api/v1/attribution/rules", body)
		if st != 400 || rtErrCode(m) != "invalid_rule" {
			t.Errorf("%s: %d %v", name, st, m)
		}
		if name == "regex" && !strings.Contains(m["error"].(map[string]any)["message"].(string), "[1]") {
			t.Errorf("message lacks index: %v", m)
		}
		if m["error"].(map[string]any)["hint"] == nil {
			t.Errorf("%s: no hint", name)
		}
	}
	after := f.rulesBody(t)
	if a, b := rtJSON(after["rules"]), rtJSON(before["rules"]); a != b {
		t.Fatalf("rules changed after invalid PUT:\n%s\n%s", a, b)
	}
	if n := f.changeCount(t); n != 1 {
		t.Fatalf("history after failures: %d", n)
	}
}

func rtJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestAttributionRules_PutRequestErrors(t *testing.T) {
	f := rtNew(t, true)
	for body, want := range map[string]string{
		`{"rules":[],"allocate":"weird"}`:          "invalid_allocate",
		`{"rules":[],"bogus":1}`:                   "invalid_request",
		`{"allocate":"none"}`:                      "invalid_request",
		`not json`:                                 "invalid_request",
		`{"rules":[],"aliases":{"a":"BAD NAME!"}}`: "invalid_request",
		`{"rules":[{"priority":1,"extra":true}]}`:  "invalid_request",
	} {
		st, m := f.call(t, "PUT", "/api/v1/attribution/rules", body)
		if st != 400 || rtErrCode(m) != want {
			t.Errorf("%s: %d %v (want %s)", body, st, m, want)
		}
	}
	big := `{"rules":[],"aliases":{"a":"` + strings.Repeat("x", 300<<10) + `"}}`
	if st, m := f.call(t, "PUT", "/api/v1/attribution/rules", big); st != 400 || rtErrCode(m) != "invalid_request" {
		t.Errorf("oversize: %d %v", st, m)
	}
	// 501 rules.
	var sb strings.Builder
	sb.WriteString(`{"rules":[`)
	for i := 0; i < 501; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"priority":1,"match":{"field":"sku","op":"equals","value":"x"},"action":{"product":"p"}}`)
	}
	sb.WriteString(`]}`)
	if st, m := f.call(t, "PUT", "/api/v1/attribution/rules", sb.String()); st != 400 || rtErrCode(m) != "invalid_rule" {
		t.Errorf("501 rules: %d %v", st, m)
	}
}

func TestAttributionSeed_PreviewPersistsNothing_ApplyIsIdempotent(t *testing.T) {
	f := rtNew(t, true)
	f.seedRows(t, "2026-W43")
	f.call(t, "PUT", "/api/v1/attribution/rules", `{"rules":[`+rtUserRule+`]}`)
	baseChanges := f.changeCount(t)

	st, pv := f.call(t, "POST", "/api/v1/attribution/seed/preview", `{"week":"2026-W43"}`)
	if st != 200 {
		t.Fatalf("preview: %d %v", st, pv)
	}
	if len(pv["proposed"].([]any)) != 2 || pv["snapshot"] == nil || pv["labels_captured"] != true || len(pv["warnings"].([]any)) != 0 {
		t.Fatalf("preview body: %v", pv)
	}
	cov, before := pv["coverage"].(map[string]any), pv["coverage_before"].(map[string]any)
	// before: only the shared Networking user rule; after: web + orphan attributed too.
	if before["shared_net"].(float64) != 10 || before["attributed_net"].(float64) != 0 || cov["attributed_net"].(float64) != 150 || cov["unattributed_net"].(float64) != 5 {
		t.Fatalf("coverage: %v / %v", cov, before)
	}
	if got := f.rulesBody(t)["rules"].([]any); len(got) != 1 {
		t.Fatalf("preview persisted: %v", got)
	}

	st, ap := f.call(t, "POST", "/api/v1/attribution/seed/apply", `{"week":"2026-W43","aliases":{"Alias":"example-product"}}`)
	if st != 200 || ap["applied"] != true || len(ap["rules"].([]any)) != 3 {
		t.Fatalf("apply: %d %v", st, ap)
	}
	ids1 := rtIDs(ap["rules"])
	if f.changeCount(t) != baseChanges+1 {
		t.Fatalf("apply history rows: %d", f.changeCount(t))
	}
	if f.rulesBody(t)["aliases"].(map[string]any)["alias"] != "example-product" {
		t.Fatal("aliases not persisted")
	}
	// Second apply: same rule set, same ids, no history.
	_, ap2 := f.call(t, "POST", "/api/v1/attribution/seed/apply", `{"week":"2026-W43"}`)
	if len(ap2["rules"].([]any)) != 3 || rtIDs(ap2["rules"]) != ids1 {
		t.Fatalf("second apply changed rules: %v vs %v", rtIDs(ap2["rules"]), ids1)
	}
	if f.changeCount(t) != baseChanges+1 {
		t.Fatal("idempotent apply wrote history")
	}
	// The user rule survived; seed rules counted as replaced on the second run.
	if c := ap2["counts"].(map[string]any); c["kept_user_rules"].(float64) != 1 || c["replaced_seed_rules"].(float64) != 2 {
		t.Fatalf("counts: %v", c)
	}
}

func rtIDs(v any) string {
	var ids []any
	for _, r := range v.([]any) {
		ids = append(ids, r.(map[string]any)["id"])
	}
	return rtJSON(ids)
}

func TestAttributionSeed_NoSnapshotAndBadInput(t *testing.T) {
	f := rtNew(t, true)
	st, pv := f.call(t, "POST", "/api/v1/attribution/seed/preview", `{"week":"2026-W40"}`)
	if st != 200 || pv["snapshot"] != nil || len(pv["proposed"].([]any)) != 0 || len(pv["warnings"].([]any)) != 1 {
		t.Fatalf("preview no snapshot: %d %v", st, pv)
	}
	st, ap := f.call(t, "POST", "/api/v1/attribution/seed/apply", `{"week":"2026-W40","aliases":{"a":"b"}}`)
	if st != 200 || ap["applied"] != false {
		t.Fatalf("apply no snapshot: %d %v", st, ap)
	}
	if len(f.rulesBody(t)["aliases"].(map[string]any)) != 0 {
		t.Fatal("aliases persisted without snapshot")
	}
	for body, want := range map[string]string{
		`{"week":"bad"}`:              "invalid_week",
		`{"week":"2020-W01"}`:         "week_out_of_range",
		`{"scope":"nope"}`:            "invalid_scope",
		`{"mystery":1}`:               "invalid_request",
		`{"aliases":{"a":"NOT OK!"}}`: "invalid_request",
	} {
		st, m := f.call(t, "POST", "/api/v1/attribution/seed/preview", body)
		if st != 400 || rtErrCode(m) != want {
			t.Errorf("%s: %d %v", body, st, m)
		}
	}
	// Empty body is allowed (all optional).
	if st, _ := f.call(t, "POST", "/api/v1/attribution/seed/preview", ""); st != 200 {
		t.Errorf("empty body: %d", st)
	}
}

func TestAttributionUnattributed(t *testing.T) {
	f := rtNew(t, true)
	f.seedRows(t, "2026-W43")
	f.call(t, "PUT", "/api/v1/attribution/rules", `{"rules":[{"priority":310,"match":{"field":"resource_name","op":"contains","value":"example-web"},"action":{"product":"example-product"}}]}`)
	st, m := f.call(t, "GET", "/api/v1/attribution/unattributed?week=2026-W43", "")
	if st != 200 {
		t.Fatalf("%d %v", st, m)
	}
	if m["total_net"].(float64) != 165 || m["unattributed_net"].(float64) != 45 || m["item_count"].(float64) != 3 || m["truncated"] != false {
		t.Fatalf("totals: %v", m)
	}
	items := m["items"].([]any)
	first := items[0].(map[string]any)
	if first["key"] != "example-orphan" || first["kind"] != "resource" || first["reason"] != "no_matching_rule" || first["net"].(float64) != 30 {
		t.Fatalf("first item: %v", first)
	}
	if sg, _ := first["suggested"].(map[string]any); sg == nil || sg["product"] != "example-orphan" || sg["environment"] != "prod" {
		t.Fatalf("suggested: %v", first["suggested"])
	}
	var sku map[string]any
	for _, it := range items {
		if it.(map[string]any)["kind"] == "sku" {
			sku = it.(map[string]any)
		}
	}
	if sku == nil || sku["reason"] != "no_resource_name" || sku["key"] != "Egress" || sku["resource_name"] != nil {
		t.Fatalf("sku item: %v", sku)
	}
	// limit
	_, m = f.call(t, "GET", "/api/v1/attribution/unattributed?week=2026-W43&limit=1", "")
	if len(m["items"].([]any)) != 1 || m["truncated"] != true || m["item_count"].(float64) != 3 {
		t.Fatalf("limit: %v", m)
	}
	// scope filter
	_, m = f.call(t, "GET", "/api/v1/attribution/unattributed?week=2026-W43&scope=project-b", "")
	if m["unattributed_net"].(float64) != 5 {
		t.Fatalf("scope: %v", m)
	}
	// no snapshot, bad params
	_, m = f.call(t, "GET", "/api/v1/attribution/unattributed?week=2026-W40", "")
	if m["snapshot"] != nil || len(m["items"].([]any)) != 0 || m["unattributed_net"].(float64) != 0 {
		t.Fatalf("no snapshot: %v", m)
	}
	for _, q := range []string{"limit=0", "limit=1001", "limit=x", "week=zzz", "scope=nope"} {
		if st, _ := f.call(t, "GET", "/api/v1/attribution/unattributed?"+q, ""); st != 400 {
			t.Errorf("%s: %d", q, st)
		}
	}
}

func TestAttributionExplain(t *testing.T) {
	f := rtNew(t, true)
	f.seedRows(t, "2026-W43")
	f.call(t, "PUT", "/api/v1/attribution/rules", `{"rules":[{"priority":310,"match":{"field":"resource_name","op":"contains","value":"example-web"},"action":{"product":"example-product"}}]}`)
	st, m := f.call(t, "POST", "/api/v1/attribution/explain", `{"resource":"EXAMPLE-WEB","week":"2026-W43"}`)
	if st != 200 || m["matches"].(float64) != 2 {
		t.Fatalf("explain: %d %v", st, m)
	}
	res := m["results"].([]any)
	r0 := res[0].(map[string]any)
	ev := r0["evaluation"].(map[string]any)
	if r0["service"] != "Compute Engine" || r0["net"].(float64) != 100 || ev["product"] != "example-product" || ev["environment"] != "staging" || len(r0["trail"].([]any)) == 0 {
		t.Fatalf("result 0: %v", r0)
	}
	// unique suffix match on the global name
	if st, m := f.call(t, "POST", "/api/v1/attribution/explain", `{"resource":"zone-a/instances/example-orphan"}`); st != 200 || m["matches"].(float64) != 1 {
		t.Fatalf("suffix: %d %v", st, m)
	}
	if st, m := f.call(t, "POST", "/api/v1/attribution/explain", `{"resource":"nope"}`); st != 404 || rtErrCode(m) != "not_found" {
		t.Errorf("no match: %d %v", st, m)
	}
	if st, m := f.call(t, "POST", "/api/v1/attribution/explain", `{"resource":"example-web","week":"2026-W40"}`); st != 404 {
		t.Errorf("no snapshot: %d %v", st, m)
	}
	if st, m := f.call(t, "POST", "/api/v1/attribution/explain", `{"resource":"  "}`); st != 400 || rtErrCode(m) != "invalid_request" {
		t.Errorf("empty: %d %v", st, m)
	}
	if st, m := f.call(t, "POST", "/api/v1/attribution/explain", `{"resource":"x","nope":1}`); st != 400 || rtErrCode(m) != "invalid_request" {
		t.Errorf("unknown field: %d %v", st, m)
	}
}

func TestAttributionLabelSuggestions(t *testing.T) {
	f := rtNew(t, true)
	f.seedRows(t, "2026-W43")
	st, m := f.call(t, "GET", "/api/v1/attribution/label-suggestions?week=2026-W43", "")
	if st != 200 || m["count"].(float64) != 1 || m["done_count"].(float64) != 3 || len(m["items"].([]any)) != 1 || m["snapshot"] == nil {
		t.Fatalf("labels: %d %v", st, m)
	}
	_, m = f.call(t, "GET", "/api/v1/attribution/label-suggestions?week=2026-W40", "")
	if m["snapshot"] != nil || len(m["items"].([]any)) != 0 {
		t.Fatalf("no snapshot: %v", m)
	}
	if st, _ := f.call(t, "GET", "/api/v1/attribution/label-suggestions?limit=0", ""); st != 400 {
		t.Errorf("limit: %d", st)
	}
}

func TestAttributionPropose(t *testing.T) {
	f := rtNew(t, true)
	f.seedRows(t, "2026-W43")

	st, m := f.call(t, "POST", "/api/v1/attribution/propose", `{"week":"2026-W43","max_rules":5}`)
	if st != 200 {
		t.Fatalf("propose: %d %v", st, m)
	}
	props := m["proposals"].([]any)
	if len(props) != 1 || m["dropped"].(float64) != 2 || m["model"] != "test/model" || m["input_tokens"].(float64) != 10 {
		t.Fatalf("body: %v", m)
	}
	if rule := props[0].(map[string]any)["rule"].(map[string]any); rule["id"] != nil || rule["source"] != "proposed" {
		t.Fatalf("rule: %v", rule)
	}
	if c := m["coverage_if_applied"].(map[string]any); c["attributed_net"].(float64) != 30 || c["unattributed_net"].(float64) != 135 {
		t.Fatalf("coverage_if_applied: %v", c)
	}
	if f.proposer.in.MaxRules != 5 || len(f.proposer.in.Items) != 4 {
		t.Fatalf("proposer input: max=%d items=%d", f.proposer.in.MaxRules, len(f.proposer.in.Items))
	}
	if got := f.rulesBody(t)["rules"].([]any); len(got) != 0 {
		t.Fatalf("propose persisted rules: %v", got)
	}
	if f.changeCount(t) != 0 {
		t.Fatal("propose wrote history")
	}

	// bad max_rules
	for _, b := range []string{`{"max_rules":51}`, `{"max_rules":-1}`, `{"x":1}`} {
		if st, m := f.call(t, "POST", "/api/v1/attribution/propose", b); st != 400 {
			t.Errorf("%s: %d %v", b, st, m)
		}
	}
	// provider failure -> 502, no rules
	f.proposer.err = errors.New("upstream said no")
	st, m = f.call(t, "POST", "/api/v1/attribution/propose", `{}`)
	if st != 502 || rtErrCode(m) != "proposer_failed" || !strings.Contains(m["error"].(map[string]any)["hint"].(string), "upstream said no") {
		t.Fatalf("failed: %d %v", st, m)
	}
	// disabled -> 503 with no call
	f.proposer.err, f.proposer.enabled = nil, false
	calls := f.proposer.calls
	st, m = f.call(t, "POST", "/api/v1/attribution/propose", `{}`)
	if st != 503 || rtErrCode(m) != "proposer_not_configured" || f.proposer.calls != calls {
		t.Fatalf("disabled: %d %v", st, m)
	}
	if h := m["error"].(map[string]any)["hint"].(string); !strings.Contains(h, "CLOUD0_OPENROUTER_API_KEY") {
		t.Errorf("hint: %q", h)
	}
	if _, m := f.call(t, "GET", "/api/v1/attribution/rules", ""); m["proposer"].(map[string]any)["enabled"] != false {
		t.Errorf("proposer info: %v", m["proposer"])
	}
	// Noop proposer behaves the same.
	f.deps.Attribution.Proposer = attribution.NoopProposer{}
	f.srv.Close()
	f.srv = httptest.NewServer(NewHandler(f.deps))
	if st, m := f.call(t, "POST", "/api/v1/attribution/propose", `{}`); st != 503 || rtErrCode(m) != "proposer_not_configured" {
		t.Errorf("noop: %d %v", st, m)
	}
}

func TestAttributionRules_MethodNotAllowed(t *testing.T) {
	f := rtNew(t, true)
	st, m := f.call(t, "DELETE", "/api/v1/attribution/rules", "")
	if st != 405 || rtErrCode(m) != "method_not_allowed" {
		t.Fatalf("%d %v", st, m)
	}
}
