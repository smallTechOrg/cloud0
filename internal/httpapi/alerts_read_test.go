package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/alerting"
	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/logging"
	"github.com/smallTechOrg/cloud0/internal/store"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

// ---- fakes (rd prefix: private to the read-endpoint tests) --------------------

type rdPolicies struct {
	byProject map[string][]alerting.PolicyView
	errs      map[string]error
}

func (f *rdPolicies) List(_ context.Context, project string) ([]alerting.PolicyView, error) {
	if err := f.errs[project]; err != nil {
		return nil, err
	}
	return f.byProject[project], nil
}
func (f *rdPolicies) Get(_ context.Context, name string) (alerting.PolicyView, error) {
	for _, ps := range f.byProject {
		for _, p := range ps {
			if p.Name == name {
				return p, nil
			}
		}
	}
	return alerting.PolicyView{}, fmt.Errorf("%w: %s", alerting.ErrNotFound, name)
}
func (f *rdPolicies) Create(context.Context, string, json.RawMessage) (alerting.PolicyView, error) {
	panic("write on read test")
}
func (f *rdPolicies) Update(context.Context, string, json.RawMessage) (alerting.PolicyView, error) {
	panic("write on read test")
}
func (f *rdPolicies) Delete(context.Context, string) error { panic("write on read test") }
func (f *rdPolicies) SetEnabled(context.Context, string, bool) (alerting.PolicyView, error) {
	panic("write on read test")
}

type rdChannels struct {
	byProject map[string][]alerting.Channel
	descs     map[string][]alerting.ChannelDescriptor
	err       error
}

func (f *rdChannels) List(_ context.Context, p string) ([]alerting.Channel, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byProject[p], nil
}
func (f *rdChannels) Descriptors(_ context.Context, p string) ([]alerting.ChannelDescriptor, error) {
	return f.descs[p], f.err
}
func (f *rdChannels) Create(context.Context, alerting.ChannelDraft) (alerting.Channel, error) {
	panic("write")
}
func (f *rdChannels) Delete(context.Context, string) error { panic("write") }

type rdUptime struct {
	byProject map[string][]alerting.UptimeCheck
}

func (f *rdUptime) List(_ context.Context, p string) ([]alerting.UptimeCheck, error) {
	return f.byProject[p], nil
}
func (f *rdUptime) Get(context.Context, string) (alerting.UptimeCheck, error) { panic("unused") }
func (f *rdUptime) Create(context.Context, alerting.UptimeDraft) (alerting.UptimeCheck, error) {
	panic("write")
}
func (f *rdUptime) Update(context.Context, string, alerting.UptimeDraft) (alerting.UptimeCheck, error) {
	panic("write")
}
func (f *rdUptime) Delete(context.Context, string) error { panic("write") }

type rdIncidents struct {
	byProject map[string][]alerting.Incident
	errs      map[string]error
	gotFilter []alerting.IncidentFilter
}

func (f *rdIncidents) List(_ context.Context, p string, flt alerting.IncidentFilter) ([]alerting.Incident, error) {
	f.gotFilter = append(f.gotFilter, flt)
	if err := f.errs[p]; err != nil {
		return nil, err
	}
	return f.byProject[p], nil
}

type rdSnoozes struct{ byProject map[string][]alerting.Snooze }

func (f *rdSnoozes) List(_ context.Context, p string) ([]alerting.Snooze, error) {
	return f.byProject[p], nil
}
func (f *rdSnoozes) Create(context.Context, alerting.SnoozeDraft) (alerting.Snooze, error) {
	panic("write")
}
func (f *rdSnoozes) End(context.Context, string, time.Time) (alerting.Snooze, error) { panic("write") }

type rdBudgets struct {
	list []alerting.BudgetView
	err  error
}

func (f *rdBudgets) List(context.Context) ([]alerting.BudgetView, error) { return f.list, f.err }
func (f *rdBudgets) Get(context.Context, string) (alerting.BudgetView, error) {
	panic("unused")
}
func (f *rdBudgets) Create(context.Context, alerting.BudgetDraft, []string) (alerting.BudgetView, error) {
	panic("write")
}
func (f *rdBudgets) Update(context.Context, string, alerting.BudgetDraft, []string) (alerting.BudgetView, error) {
	panic("write")
}
func (f *rdBudgets) Delete(context.Context, string) error { panic("write") }
func (f *rdBudgets) Body(alerting.BudgetDraft, []string) (json.RawMessage, error) {
	panic("unused")
}

type rdRouter struct {
	routing map[string][]string
	put     map[string][]string
	putErr  error
}

func (f *rdRouter) Get(context.Context) (map[string][]string, error) { return f.routing, nil }
func (f *rdRouter) Put(_ context.Context, m map[string][]string) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.put = m
	f.routing = m
	return nil
}
func (f *rdRouter) Resolve(context.Context, string, string, []string) ([]string, []string, error) {
	panic("unused")
}

type rdCoverage struct {
	rows []alerting.CoverageRow
	err  error
}

func (f rdCoverage) Coverage(context.Context) ([]alerting.CoverageRow, error) { return f.rows, f.err }

type rdHistory struct {
	rows []alerting.HistoryRow
	week string
	err  error
}

func (f *rdHistory) History(_ context.Context, week string) ([]alerting.HistoryRow, error) {
	f.week = week
	return f.rows, f.err
}

type rdSuggestions struct {
	list []alerting.Suggestion
	err  error
}

func (f rdSuggestions) Suggestions(context.Context) ([]alerting.Suggestion, error) {
	return f.list, f.err
}

// ---- harness ------------------------------------------------------------------

type rdEnv struct {
	t         *testing.T
	srv       *httptest.Server
	store     *store.Store
	policies  *rdPolicies
	channels  *rdChannels
	uptime    *rdUptime
	incidents *rdIncidents
	snoozes   *rdSnoozes
	budgets   *rdBudgets
	router    *rdRouter
	history   *rdHistory
}

var rdNow = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

func rdT(h, m int) time.Time { return time.Date(2026, 10, 27, h, m, 0, 0, time.UTC) }
func rdF(v float64) *float64 { return &v }
func rdI(v int) *int         { return &v }

const (
	chA   = "projects/proj-a/notificationChannels/111"
	chB   = "projects/proj-b/notificationChannels/222"
	polA1 = "projects/proj-a/alertPolicies/222"
	polA2 = "projects/proj-a/alertPolicies/333"
)

func newRDEnv(t *testing.T, writable bool) *rdEnv {
	t.Helper()
	cfg, err := config.Load(func(k string) string {
		return map[string]string{
			"CLOUD0_GCP_PROJECTS":         "proj-a,proj-b",
			"CLOUD0_BILLING_EXPORT_TABLE": "my-project.my_dataset.my_table",
			"CLOUD0_BIGQUERY_PROJECT":     "my-project",
			"CLOUD0_FIRST_WEEK":           "2026-W18",
			"CLOUD0_SETTLE_HOURS":         "48",
			"CLOUD0_BILLING_ACCOUNT_ID":   "BILLING_ACCOUNT_ID",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, testenv.TempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	e := &rdEnv{t: t, store: st,
		policies:  &rdPolicies{byProject: map[string][]alerting.PolicyView{}, errs: map[string]error{}},
		channels:  &rdChannels{byProject: map[string][]alerting.Channel{}, descs: map[string][]alerting.ChannelDescriptor{}},
		uptime:    &rdUptime{byProject: map[string][]alerting.UptimeCheck{}},
		incidents: &rdIncidents{byProject: map[string][]alerting.Incident{}, errs: map[string]error{}},
		snoozes:   &rdSnoozes{byProject: map[string][]alerting.Snooze{}},
		budgets:   &rdBudgets{},
		router:    &rdRouter{routing: map[string][]string{}},
		history:   &rdHistory{},
	}
	deps := Deps{Config: cfg, Store: st, Logger: logging.NewWriter(io.Discard, "info"), Version: "test",
		Now: func() time.Time { return rdNow },
		UI:  http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ui") }),
		Alerts: &AlertsDeps{Writable: writable, Builder: alerting.NewBuilder(),
			Policies: e.policies, Channels: e.channels, Uptime: e.uptime, Incidents: e.incidents,
			Snoozes: e.snoozes, Budgets: e.budgets, Router: e.router,
			Coverage: rdCoverage{}, History: e.history, Suggestions: rdSuggestions{}}}
	e.srv = httptest.NewServer(NewHandler(deps))
	t.Cleanup(e.srv.Close)
	return e
}

// rebuild swaps the Alerts deps (to test nil dependencies).
func (e *rdEnv) withDeps(t *testing.T, mutate func(*Deps)) *httptest.Server {
	t.Helper()
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{
			"CLOUD0_GCP_PROJECTS": "proj-a", "CLOUD0_BILLING_EXPORT_TABLE": "my-project.my_dataset.my_table",
			"CLOUD0_BIGQUERY_PROJECT": "my-project", "CLOUD0_FIRST_WEEK": "2026-W18"}[k]
	})
	d := Deps{Config: cfg, Store: e.store, Logger: logging.NewWriter(io.Discard, "info"), Now: func() time.Time { return rdNow },
		UI: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})}
	mutate(&d)
	s := httptest.NewServer(NewHandler(d))
	t.Cleanup(s.Close)
	return s
}

func rdDo(t *testing.T, base, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	raw, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s %s: non-JSON body %q", method, path, raw)
	}
	return res.StatusCode, m
}

func (e *rdEnv) get(path string) (int, map[string]any) {
	return rdDo(e.t, e.srv.URL, http.MethodGet, path, "")
}

func keysOf(m map[string]any) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

func wantKeys(t *testing.T, what string, m map[string]any, keys string) {
	t.Helper()
	want := strings.Split(keys, ",")
	sort.Strings(want)
	if got := keysOf(m); got != strings.Join(want, ",") {
		t.Fatalf("%s keys:\n got %s\nwant %s", what, got, strings.Join(want, ","))
	}
}

func list(t *testing.T, m map[string]any, key string) []any {
	t.Helper()
	v, ok := m[key].([]any)
	if !ok {
		t.Fatalf("%s is %T (%v), want a JSON array", key, m[key], m[key])
	}
	return v
}

func wantErr(t *testing.T, status int, m map[string]any, wantStatus int, code string) map[string]any {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status = %d, want %d; body %v", status, wantStatus, m)
	}
	e, _ := m["error"].(map[string]any)
	if e == nil || e["code"] != code {
		t.Fatalf("error = %v, want code %s", m["error"], code)
	}
	return e
}

func zeroCPUPolicy() alerting.PolicyView {
	return alerting.PolicyView{Name: polA1, Project: "proj-a", DisplayName: "Zero-Backend CPU Spike", Enabled: true,
		Conditions: []alerting.ConditionSummary{{DisplayName: "CPU > 80%", Type: "threshold", Summary: "CPU utilization > 80 % for 5 min",
			Filter: "metric.type=\"x\"", Comparison: "COMPARISON_GT", Threshold: rdF(0.8), DurationSeconds: rdI(300)}},
		Channels:   []alerting.ChannelRef{{Name: chA, DisplayName: "Alert", Type: "email", VerificationStatus: "UNVERIFIED"}},
		AdoptShape: alerting.ShapeZeroCPUSpike, Warnings: []string{"Covers one VM only."}, RawJSON: json.RawMessage(`{"displayName":"Zero-Backend CPU Spike"}`)}
}

func managedCPUPolicy() alerting.PolicyView {
	return alerting.PolicyView{Name: polA2, Project: "proj-a", DisplayName: "cloud0: VM CPU high", Enabled: true, Severity: "WARNING",
		Managed: true, TemplateID: alerting.TplVMCPUHigh,
		Conditions: []alerting.ConditionSummary{{Type: "threshold", Threshold: rdF(0.9), DurationSeconds: rdI(600)}},
		RawJSON:    json.RawMessage(`{"displayName":"cloud0: VM CPU high"}`)}
}

// ---- tests ----------------------------------------------------------------------

func TestAlertsReadStatusGolden(t *testing.T) {
	e := newRDEnv(t, true)
	st, m := e.get("/api/v1/alerts/status")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "status", m, "alerts_configured,billing_account_configured,projects,generated_at")
	if m["alerts_configured"] != true || m["billing_account_configured"] != true {
		t.Fatalf("flags: %v", m)
	}
	if got := list(t, m, "projects"); len(got) != 2 || got[0] != "proj-a" {
		t.Fatalf("projects: %v", got)
	}

	// Read-only wiring and no wiring at all: still 200, alerts_configured=false.
	e2 := newRDEnv(t, false)
	if _, m := e2.get("/api/v1/alerts/status"); m["alerts_configured"] != false {
		t.Fatalf("reader-only: %v", m)
	}
	s := e.withDeps(t, func(*Deps) {})
	st, m = rdDo(t, s.URL, "GET", "/api/v1/alerts/status", "")
	if st != 200 || m["alerts_configured"] != false || m["billing_account_configured"] != false {
		t.Fatalf("nil Alerts: %d %v", st, m)
	}
}

func TestAlertsReadPoliciesFiltersAndGolden(t *testing.T) {
	e := newRDEnv(t, false)
	e.policies.byProject["proj-a"] = []alerting.PolicyView{managedCPUPolicy(), zeroCPUPolicy()}
	bare := alerting.PolicyView{Name: "projects/proj-b/alertPolicies/9", Project: "proj-b", DisplayName: "B policy", Enabled: true}
	e.policies.byProject["proj-b"] = []alerting.PolicyView{bare}

	st, m := e.get("/api/v1/alerts/policies")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "policies", m, "policies,projects_failed,generated_at")
	ps := list(t, m, "policies")
	if len(ps) != 3 {
		t.Fatalf("policies = %d", len(ps))
	}
	first := ps[0].(map[string]any)
	wantKeys(t, "policy", first, "name,project,display_name,enabled,severity,managed,template_id,instance,conditions,channels,adopt_shape,warnings,updated_at,raw_json")
	if first["display_name"] != "cloud0: VM CPU high" { // sorted by project then display name
		t.Fatalf("order: %v", first["display_name"])
	}
	wantKeys(t, "condition", list(t, zeroMap(ps, "Zero-Backend CPU Spike"), "conditions")[0].(map[string]any),
		"display_name,type,summary,filter,comparison,threshold,duration_seconds")
	wantKeys(t, "channel ref", list(t, zeroMap(ps, "Zero-Backend CPU Spike"), "channels")[0].(map[string]any),
		"name,display_name,type,verification_status")
	// nil slices of a bare policy become [] and raw_json null
	b := zeroMap(ps, "B policy")
	for _, k := range []string{"conditions", "channels", "warnings"} {
		_ = list(t, b, k)
	}
	if b["raw_json"] != nil {
		t.Fatalf("raw_json = %v", b["raw_json"])
	}
	if len(list(t, m, "projects_failed")) != 0 {
		t.Fatal("projects_failed not empty")
	}

	_, m = e.get("/api/v1/alerts/policies?project=proj-a")
	if n := len(list(t, m, "policies")); n != 2 {
		t.Fatalf("project filter = %d", n)
	}
	_, m = e.get("/api/v1/alerts/policies?managed=true")
	if n := len(list(t, m, "policies")); n != 1 {
		t.Fatalf("managed=true = %d", n)
	}
	_, m = e.get("/api/v1/alerts/policies?managed=false&project=proj-a")
	ps = list(t, m, "policies")
	if len(ps) != 1 || ps[0].(map[string]any)["adopt_shape"] != "zero_cpu_spike" {
		t.Fatalf("managed=false: %v", ps)
	}
	st, m = e.get("/api/v1/alerts/policies?managed=maybe")
	wantErr(t, st, m, 400, "invalid_request")
	st, m = e.get("/api/v1/alerts/policies?project=nope")
	wantErr(t, st, m, 400, "invalid_scope")
}

func zeroMap(ps []any, name string) map[string]any {
	for _, p := range ps {
		if pm := p.(map[string]any); pm["display_name"] == name {
			return pm
		}
	}
	return nil
}

func TestAlertsReadPoliciesPartialFailure(t *testing.T) {
	e := newRDEnv(t, false)
	e.policies.byProject["proj-a"] = []alerting.PolicyView{zeroCPUPolicy()}
	e.policies.errs["proj-b"] = fmt.Errorf("%w: need monitoring.viewer", alerting.ErrPermissionDenied)
	st, m := e.get("/api/v1/alerts/policies")
	if st != 200 {
		t.Fatalf("partial failure must not fail the request: %d %v", st, m)
	}
	pf := list(t, m, "projects_failed")
	if len(pf) != 1 {
		t.Fatalf("projects_failed = %v", pf)
	}
	f := pf[0].(map[string]any)
	wantKeys(t, "project failure", f, "project,code,message,hint")
	if f["project"] != "proj-b" || f["code"] != "permission_denied" || f["hint"] == nil {
		t.Fatalf("failure: %v", f)
	}
	if len(list(t, m, "policies")) != 1 {
		t.Fatal("proj-a policies lost")
	}
}

func TestAlertsReadPolicyGet(t *testing.T) {
	e := newRDEnv(t, false)
	e.policies.byProject["proj-a"] = []alerting.PolicyView{zeroCPUPolicy()}
	st, m := e.get("/api/v1/alerts/policies/proj-a/222")
	if st != 200 || m["name"] != polA1 {
		t.Fatalf("%d %v", st, m)
	}
	st, m = e.get("/api/v1/alerts/policies/proj-a/404")
	wantErr(t, st, m, 404, "not_found")
	st, m = e.get("/api/v1/alerts/policies/other/222")
	wantErr(t, st, m, 404, "not_found")
}

func TestAlertsReadTemplatesGolden(t *testing.T) {
	e := newRDEnv(t, false)
	e.policies.byProject["proj-a"] = []alerting.PolicyView{managedCPUPolicy(), zeroCPUPolicy()}
	e.channels.byProject["proj-a"] = nil
	cov := rdCoverage{rows: []alerting.CoverageRow{{Project: "proj-a", ResourceType: "vm", Resource: "vm-a", Signals: []alerting.CoverageSignal{
		{Signal: "cpu", Covered: true, Policies: []string{polA2}},
		{Signal: "memory", Covered: false, Reason: "Ops Agent not installed"}}}}}
	srv := e.withDeps(t, func(d *Deps) {
		d.Alerts = &AlertsDeps{Builder: alerting.NewBuilder(), Policies: e.policies, Coverage: cov}
	})
	st, m := rdDo(t, srv.URL, "GET", "/api/v1/alerts/templates", "")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "templates", m, "templates,generated_at")
	ts := list(t, m, "templates")
	if len(ts) != len(alerting.NewBuilder().Templates()) {
		t.Fatalf("templates = %d", len(ts))
	}
	var cpu, mem map[string]any
	for _, x := range ts {
		tm := x.(map[string]any)
		wantKeys(t, "template", tm, "id,kind,title,description,severity,default_enabled,param_specs,defaults,params,installed,covered_resources")
		switch tm["id"] {
		case alerting.TplVMCPUHigh:
			cpu = tm
		case alerting.TplVMMemoryHigh:
			mem = tm
		}
	}
	if cpu == nil || mem == nil {
		t.Fatal("vm templates missing")
	}
	if mem["params"] != nil || len(list(t, mem, "installed")) != 0 {
		t.Fatalf("uninstalled template: %v %v", mem["params"], mem["installed"])
	}
	inst := list(t, cpu, "installed")
	if len(inst) != 1 || inst[0] != "cloud0: VM CPU high" {
		t.Fatalf("installed = %v", inst)
	}
	p := cpu["params"].(map[string]any)
	if p["threshold"] != 90.0 || p["duration_seconds"] != 600.0 || p["severity"] != "WARNING" || p["enabled"] != true {
		t.Fatalf("effective params = %v", p)
	}
	cr := list(t, cpu, "covered_resources")
	if len(cr) != 1 {
		t.Fatalf("covered_resources = %v", cr)
	}
	wantKeys(t, "covered resource", cr[0].(map[string]any), "project,resource_type,name,covered,note")
	mr := list(t, mem, "covered_resources")
	if len(mr) != 1 || mr[0].(map[string]any)["covered"] != false || mr[0].(map[string]any)["note"] != "Ops Agent not installed" {
		t.Fatalf("memory covered_resources = %v", mr)
	}
	for _, sp := range list(t, cpu, "param_specs") {
		wantKeys(t, "param spec", sp.(map[string]any), "key,label,type,unit,min,max,options,help")
	}
}

func TestAlertsReadChannelsGolden(t *testing.T) {
	e := newRDEnv(t, false)
	e.channels.byProject["proj-a"] = []alerting.Channel{{Name: chA, Project: "proj-a", Type: "email", DisplayName: "Alert", Enabled: true,
		VerificationStatus: "UNVERIFIED", Address: "alerts@example.com"}}
	e.channels.byProject["proj-b"] = []alerting.Channel{{Name: chB, Project: "proj-b", Type: "email", DisplayName: "Verified", Enabled: true,
		VerificationStatus: "VERIFIED", Address: "a***@example.com"}}
	e.channels.descs["proj-a"] = []alerting.ChannelDescriptor{{Type: "slack", DisplayName: "Slack", Creatable: false, Hint: "console OAuth"},
		{Type: "email", DisplayName: "Email", Creatable: true, Labels: []alerting.LabelSpec{{Key: "email_address", Required: true}}}}
	e.channels.descs["proj-b"] = []alerting.ChannelDescriptor{{Type: "email", DisplayName: "Email", Creatable: true}}
	st, m := e.get("/api/v1/alerts/channels")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "channels", m, "channels,descriptors,generated_at")
	chs := list(t, m, "channels")
	c0 := chs[0].(map[string]any)
	wantKeys(t, "channel", c0, "name,project,type,display_name,enabled,verification_status,address,warnings")
	if c0["address"] != "a***@example.com" {
		t.Fatalf("address not masked: %v", c0["address"])
	}
	if w := list(t, c0, "warnings"); len(w) != 1 || !strings.Contains(w[0].(string), "not verified") {
		t.Fatalf("warnings = %v", w)
	}
	if w := list(t, chs[1].(map[string]any), "warnings"); len(w) != 0 {
		t.Fatalf("verified channel warned: %v", w)
	}
	ds := list(t, m, "descriptors")
	if len(ds) != 2 || ds[0].(map[string]any)["type"] != "email" || ds[1].(map[string]any)["creatable"] != false {
		t.Fatalf("descriptors (union by type, sorted): %v", ds)
	}
	wantKeys(t, "descriptor", ds[0].(map[string]any), "type,display_name,creatable,hint,labels")
	wantKeys(t, "label", list(t, ds[0].(map[string]any), "labels")[0].(map[string]any), "key,description,required,sensitive")
	if strings.Contains(fmt.Sprint(m), "alerts@example.com") {
		t.Fatal("unmasked address leaked")
	}
}

func TestAlertsReadUptimeChecksGolden(t *testing.T) {
	e := newRDEnv(t, false)
	e.uptime.byProject["proj-a"] = []alerting.UptimeCheck{{Name: "projects/proj-a/uptimeCheckConfigs/abc", Project: "proj-a", DisplayName: "api health",
		Host: "api.example.com", Path: "/health", Port: 443, UseSSL: true, PeriodSeconds: 60, TimeoutSeconds: 10, RawJSON: json.RawMessage(`{"a":1}`)}}
	st, m := e.get("/api/v1/alerts/uptime-checks")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "uptime", m, "checks,generated_at")
	wantKeys(t, "check", list(t, m, "checks")[0].(map[string]any),
		"name,project,display_name,host,path,port,use_ssl,period_seconds,timeout_seconds,content_match,managed,raw_json")
	e.uptime.byProject = nil
	_, m = e.get("/api/v1/alerts/uptime-checks")
	if len(list(t, m, "checks")) != 0 {
		t.Fatal("expected empty list")
	}
}

func TestAlertsReadIncidentsStatesAndGolden(t *testing.T) {
	e := newRDEnv(t, false)
	closed := rdT(8, 0)
	e.incidents.byProject["proj-a"] = []alerting.Incident{
		{ID: "old", Project: "proj-a", State: "open", OpenedAt: rdT(6, 0), PolicyDisplayName: "p"},
		{ID: "new", Project: "proj-a", State: "open", OpenedAt: rdT(7, 40), PolicyDisplayName: "p", URL: "https://example.com/x"},
		{ID: "gone", Project: "proj-a", State: "closed", OpenedAt: rdT(5, 0), ClosedAt: &closed},
	}
	e.incidents.byProject["proj-b"] = []alerting.Incident{{ID: "b1", Project: "proj-b", State: "open", OpenedAt: rdT(7, 0)}}
	e.incidents.errs["proj-c"] = nil

	st, m := e.get("/api/v1/alerts/incidents")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "incidents", m, "incidents,total,projects_failed,generated_at")
	incs := list(t, m, "incidents")
	if len(incs) != 3 || m["total"] != 3.0 {
		t.Fatalf("default state=open: %v", incs)
	}
	ids := []string{}
	for _, i := range incs {
		ids = append(ids, i.(map[string]any)["id"].(string))
	}
	if strings.Join(ids, ",") != "new,b1,old" {
		t.Fatalf("order = %v", ids)
	}
	wantKeys(t, "incident", incs[0].(map[string]any),
		"id,project,policy_name,policy_display_name,severity,state,opened_at,closed_at,resource_type,resource_name,summary,url")

	_, m = e.get("/api/v1/alerts/incidents?state=closed")
	if incs = list(t, m, "incidents"); len(incs) != 1 || incs[0].(map[string]any)["id"] != "gone" {
		t.Fatalf("closed: %v", incs)
	}
	_, m = e.get("/api/v1/alerts/incidents?state=all")
	if len(list(t, m, "incidents")) != 4 {
		t.Fatalf("all: %v", m["incidents"])
	}
	_, m = e.get("/api/v1/alerts/incidents?project=proj-b")
	if len(list(t, m, "incidents")) != 1 {
		t.Fatalf("project filter: %v", m["incidents"])
	}
	st, m = e.get("/api/v1/alerts/incidents?state=weird")
	wantErr(t, st, m, 400, "invalid_request")

	e.incidents.errs["proj-b"] = fmt.Errorf("%w: boom", alerting.ErrUpstream)
	_, m = e.get("/api/v1/alerts/incidents")
	pf := list(t, m, "projects_failed")
	if len(pf) != 1 || pf[0].(map[string]any)["code"] != "upstream_error" || len(list(t, m, "incidents")) != 2 {
		t.Fatalf("partial failure: %v", m)
	}
}

func TestAlertsReadHistoryWeek(t *testing.T) {
	e := newRDEnv(t, false)
	e.history.rows = []alerting.HistoryRow{
		{Project: "proj-a", PolicyName: polA1, PolicyDisplayName: "B", Severity: "WARNING", Opened: 1, StillOpen: 1, FirstOpenedAt: rdT(1, 0), LastOpenedAt: rdT(1, 0)},
		{Project: "proj-a", PolicyName: polA2, PolicyDisplayName: "A", Severity: "ERROR", Opened: 3, Resources: []string{"vm-a"}, FirstOpenedAt: rdT(1, 0), LastOpenedAt: rdT(2, 0)},
	}
	st, m := e.get("/api/v1/alerts/history?week=2026-W43")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "history", m, "week,total_opened,still_open,rows,projects_failed,generated_at")
	if e.history.week != "2026-W43" || m["week"] != "2026-W43" || m["total_opened"] != 4.0 || m["still_open"] != 1.0 {
		t.Fatalf("history: week=%s %v", e.history.week, m)
	}
	rows := list(t, m, "rows")
	wantKeys(t, "row", rows[0].(map[string]any), "project,policy_name,policy_display_name,severity,opened,still_open,resources,first_opened_at,last_opened_at")
	if rows[0].(map[string]any)["opened"] != 3.0 {
		t.Fatalf("rows not ordered by opened desc: %v", rows)
	}
	if len(list(t, rows[1].(map[string]any), "resources")) != 0 {
		t.Fatal("resources must be []")
	}
	_, m = e.get("/api/v1/alerts/history")
	if m["week"] == "" || m["week"] == nil {
		t.Fatalf("default week: %v", m)
	}
	st, m = e.get("/api/v1/alerts/history?week=garbage")
	wantErr(t, st, m, 400, "invalid_week")
	st, m = e.get("/api/v1/alerts/history?week=2030-W01")
	wantErr(t, st, m, 400, "week_out_of_range")
	e.history.err = fmt.Errorf("%w: x", alerting.ErrUpstream)
	st, m = e.get("/api/v1/alerts/history?week=2026-W43")
	wantErr(t, st, m, 502, "upstream_error")
}

func TestAlertsReadCoverageGolden(t *testing.T) {
	e := newRDEnv(t, false)
	cov := rdCoverage{rows: []alerting.CoverageRow{{Project: "proj-a", ResourceType: "vm", Resource: "vm-a",
		Signals: []alerting.CoverageSignal{{Signal: "cpu", Covered: true, Policies: []string{polA2}}, {Signal: "memory", Reason: "Ops Agent not installed"}},
		Hint:    "install the Ops Agent."}}}
	srv := e.withDeps(t, func(d *Deps) { d.Alerts = &AlertsDeps{Coverage: cov} })
	st, m := rdDo(t, srv.URL, "GET", "/api/v1/alerts/coverage", "")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "coverage", m, "rows,generated_at")
	row := list(t, m, "rows")[0].(map[string]any)
	wantKeys(t, "coverage row", row, "project,resource_type,resource,signals,hint")
	sig := list(t, row, "signals")
	wantKeys(t, "signal", sig[0].(map[string]any), "signal,covered,policies,reason")
	if len(list(t, sig[1].(map[string]any), "policies")) != 0 {
		t.Fatal("policies must be []")
	}
	srv2 := e.withDeps(t, func(d *Deps) {
		d.Alerts = &AlertsDeps{Coverage: rdCoverage{err: fmt.Errorf("%w: no", alerting.ErrAPIDisabled)}}
	})
	st, m = rdDo(t, srv2.URL, "GET", "/api/v1/alerts/coverage", "")
	er := wantErr(t, st, m, 503, "api_disabled")
	if er["hint"] == nil || !strings.Contains(er["hint"].(string), "gcloud services enable") {
		t.Fatalf("hint: %v", er["hint"])
	}
}

func TestAlertsReadSnoozesGolden(t *testing.T) {
	e := newRDEnv(t, false)
	e.snoozes.byProject["proj-a"] = []alerting.Snooze{
		{Name: "projects/proj-a/snoozes/s1", Project: "proj-a", DisplayName: "old", StartTime: rdT(1, 0), EndTime: rdT(2, 0), State: "expired"},
		{Name: "projects/proj-a/snoozes/s2", Project: "proj-a", DisplayName: "deploy", PolicyNames: []string{polA2}, StartTime: rdT(8, 0), EndTime: rdT(12, 0), State: "active"}}
	st, m := e.get("/api/v1/alerts/snoozes")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "snoozes", m, "snoozes,generated_at")
	sn := list(t, m, "snoozes")
	if sn[0].(map[string]any)["display_name"] != "deploy" {
		t.Fatalf("not newest first: %v", sn)
	}
	wantKeys(t, "snooze", sn[0].(map[string]any), "name,project,display_name,policy_names,start_time,end_time,state")
	if len(list(t, sn[1].(map[string]any), "policy_names")) != 0 {
		t.Fatal("policy_names must be []")
	}
}

func TestAlertsReadRoutingGetAndPut(t *testing.T) {
	e := newRDEnv(t, false) // PUT needs no write credentials
	e.channels.byProject["proj-a"] = []alerting.Channel{{Name: chA, Project: "proj-a", Type: "email", DisplayName: "Alert", VerificationStatus: "UNVERIFIED", Address: "a***@example.com"}}
	e.channels.byProject["proj-b"] = []alerting.Channel{{Name: chB, Project: "proj-b", Type: "email", DisplayName: "Ok", VerificationStatus: "VERIFIED"}}
	e.router.routing = map[string][]string{"CRITICAL": {chA}, "ERROR": {chA}, "WARNING": {chA}}

	st, m := e.get("/api/v1/alerts/routing")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "routing", m, "routing,defaulted,updated_at,warnings,channels")
	if m["defaulted"] != true || m["updated_at"] != nil {
		t.Fatalf("defaulted/updated_at: %v %v", m["defaulted"], m["updated_at"])
	}
	wantKeys(t, "routing map", m["routing"].(map[string]any), "CRITICAL,ERROR,WARNING")
	if len(list(t, m, "warnings")) != 3 || !strings.Contains(list(t, m, "warnings")[0].(string), "UNVERIFIED") {
		t.Fatalf("warnings: %v", m["warnings"])
	}

	put := fmt.Sprintf(`{"routing":{"CRITICAL":[%q,%q],"ERROR":[%q],"WARNING":[%q]}}`, chA, chB, chB, chB)
	st, m = rdDo(t, e.srv.URL, "PUT", "/api/v1/alerts/routing", put)
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "put response", m, "routing,defaulted,updated_at,warnings,channels")
	if len(e.router.put["CRITICAL"]) != 2 {
		t.Fatalf("Router.Put got %v", e.router.put)
	}
	// Router.Put owns the audit row: the handler must not add a second one.
	ctx := context.Background()
	if rows, err := store.ListChanges(ctx, e.store.DB, store.ChangeFilter{Kind: "routing"}, 10); err != nil || len(rows) != 0 {
		t.Fatalf("handler wrote history: %v %v", rows, err)
	}

	bad := map[string]string{
		"unknown channel":  fmt.Sprintf(`{"routing":{"CRITICAL":["projects/x/notificationChannels/1"],"ERROR":[%q],"WARNING":[%q]}}`, chA, chA),
		"missing severity": fmt.Sprintf(`{"routing":{"CRITICAL":[%q],"ERROR":[%q]}}`, chA, chA),
		"unknown severity": fmt.Sprintf(`{"routing":{"CRITICAL":[%q],"ERROR":[%q],"WARNING":[%q],"INFO":[%q]}}`, chA, chA, chA, chA),
		"empty list":       fmt.Sprintf(`{"routing":{"CRITICAL":[],"ERROR":[%q],"WARNING":[%q]}}`, chA, chA),
		"duplicate":        fmt.Sprintf(`{"routing":{"CRITICAL":[%q,%q],"ERROR":[%q],"WARNING":[%q]}}`, chA, chA, chA, chA),
	}
	before := e.router.put
	for name, body := range bad {
		st, m := rdDo(t, e.srv.URL, "PUT", "/api/v1/alerts/routing", body)
		er := wantErr(t, st, m, 400, "invalid_params")
		if er["message"] == "" {
			t.Fatalf("%s: empty message", name)
		}
	}
	if fmt.Sprint(e.router.put) != fmt.Sprint(before) {
		t.Fatal("invalid PUT must not save")
	}
	st, m = rdDo(t, e.srv.URL, "PUT", "/api/v1/alerts/routing", `{not json`)
	wantErr(t, st, m, 400, "invalid_request")

	e.router.putErr = fmt.Errorf("%w: router says no", alerting.ErrInvalidParams)
	st, m = rdDo(t, e.srv.URL, "PUT", "/api/v1/alerts/routing", put)
	wantErr(t, st, m, 400, "invalid_params")
}

func TestAlertsReadBudgetsGoldenAndGate(t *testing.T) {
	e := newRDEnv(t, true)
	e.budgets.list = []alerting.BudgetView{
		{Name: "billingAccounts/BILLING_ACCOUNT_ID/budgets/u1", DisplayName: "Example-budget", AmountKind: "specified", Amount: rdF(5000), Currency: "INR",
			Projects: []string{"proj-a"}, Thresholds: []alerting.BudgetThreshold{{Percent: 50, Basis: "current_spend"}}, DefaultIAMRecipients: true},
		{Name: "billingAccounts/BILLING_ACCOUNT_ID/budgets/u2", DisplayName: "cloud0-account-monthly", AmountKind: "specified", Amount: rdF(6000), ChannelNames: []string{chA}},
	}
	st, m := e.get("/api/v1/budgets")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "budgets", m, "billing_account,currency,budgets,generated_at")
	if m["billing_account"] != "BILLING_ACCOUNT_ID" || m["currency"] != "INR" {
		t.Fatalf("%v", m)
	}
	bs := list(t, m, "budgets")
	b0 := bs[0].(map[string]any) // sorted by display name: cloud0-... first
	wantKeys(t, "budget", b0, "name,display_name,managed,amount_kind,amount,currency,projects,service_ids,thresholds,channel_names,default_iam_recipients,adopt_shape,warnings,raw_json")
	if b0["managed"] != true || b0["adopt_shape"] != "" || len(list(t, b0, "warnings")) != 0 {
		t.Fatalf("managed budget: %v", b0)
	}
	b1 := bs[1].(map[string]any)
	if b1["managed"] != false || b1["adopt_shape"] != "budget_account" || len(list(t, b1, "warnings")) != 1 {
		t.Fatalf("unmanaged budget: %v", b1)
	}
	wantKeys(t, "threshold", list(t, b1, "thresholds")[0].(map[string]any), "percent,basis")
	_ = list(t, b1, "service_ids")

	e.budgets.err = fmt.Errorf("%w: grant roles/billing.costsManager", alerting.ErrPermissionDenied)
	st, m = e.get("/api/v1/budgets")
	er := wantErr(t, st, m, 403, "permission_denied")
	if er["hint"] == nil || !strings.Contains(er["hint"].(string), "billing.costsManager") {
		t.Fatalf("hint: %v", er["hint"])
	}

	// Reader-only wiring: budgets need the alerts credentials.
	ro := newRDEnv(t, false)
	for _, p := range []string{"/api/v1/budgets", "/api/v1/budgets/suggestions"} {
		st, m := ro.get(p)
		er := wantErr(t, st, m, 503, "alerts_not_configured")
		if er["hint"] == nil || !strings.Contains(er["hint"].(string), "CLOUD0_ALERTS_CREDENTIALS") {
			t.Fatalf("%s hint: %v", p, er["hint"])
		}
	}
}

func TestAlertsReadSuggestionsGolden(t *testing.T) {
	e := newRDEnv(t, true)
	srv := e.withDeps(t, func(d *Deps) {
		d.Alerts = &AlertsDeps{Writable: true, Suggestions: rdSuggestions{list: []alerting.Suggestion{{Scope: "account", ServiceName: "All services", Amount: 6480,
			Basis: alerting.SuggestionBasis{Method: "snapshots_avg", WeeksUsed: 4, AvgWeekly: 1260, Multiplier: 1.2, Explanation: "x"},
			Draft: alerting.BudgetDraft{DisplayName: "cloud0-account-monthly", Amount: 6480, Severity: "ERROR"}}}}}
	})
	st, m := rdDo(t, srv.URL, "GET", "/api/v1/budgets/suggestions", "")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "suggestions", m, "currency,suggestions")
	s := list(t, m, "suggestions")[0].(map[string]any)
	wantKeys(t, "suggestion", s, "scope,service_id,service_name,amount,basis,draft,exists")
	wantKeys(t, "basis", s["basis"].(map[string]any), "method,weeks_used,avg_weekly,multiplier,explanation")
	d := s["draft"].(map[string]any)
	wantKeys(t, "draft", d, "display_name,amount,projects,service_ids,thresholds,severity")
	if len(list(t, d, "projects")) != 0 || len(list(t, d, "thresholds")) != 0 {
		t.Fatal("draft lists must be []")
	}
}

func TestAlertsReadChangeHistory(t *testing.T) {
	e := newRDEnv(t, false)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		kind := alerting.TargetPolicy
		if i == 1 {
			kind = alerting.TargetBudget
		}
		_, err := store.InsertChange(ctx, e.store.DB, store.NewChange{At: rdNow.Add(time.Duration(i) * time.Minute), Kind: kind, Action: "create",
			Target: fmt.Sprintf("t%d", i), After: json.RawMessage(`{"displayName":"x","webhook_url":"https://secret"}`), PreviewHash: "pv_x"})
		if err != nil {
			t.Fatal(err)
		}
	}
	st, m := e.get("/api/v1/history")
	if st != 200 {
		t.Fatal(st, m)
	}
	wantKeys(t, "history", m, "entries,limit")
	if m["limit"] != 50.0 {
		t.Fatalf("limit = %v", m["limit"])
	}
	en := list(t, m, "entries")
	if len(en) != 3 || en[0].(map[string]any)["target"] != "t2" {
		t.Fatalf("entries: %v", en)
	}
	wantKeys(t, "entry", en[0].(map[string]any), "id,at,actor,kind,action,target,before,after,preview_hash")
	if strings.Contains(fmt.Sprint(m), "https://secret") {
		t.Fatal("credential-like key not redacted")
	}
	_, m = e.get("/api/v1/history?kind=budget&limit=5")
	if en = list(t, m, "entries"); len(en) != 1 || m["limit"] != 5.0 {
		t.Fatalf("kind filter: %v", m)
	}
	_, m = e.get("/api/v1/history?limit=2")
	if len(list(t, m, "entries")) != 2 {
		t.Fatal("limit not applied")
	}
	for _, q := range []string{"kind=nope", "limit=0", "limit=501", "limit=abc"} {
		st, m := e.get("/api/v1/history?" + q)
		wantErr(t, st, m, 400, "invalid_request")
	}
	// local data: works with no Alerts wiring
	srv := e.withDeps(t, func(*Deps) {})
	if st, _ := rdDo(t, srv.URL, "GET", "/api/v1/history", ""); st != 200 {
		t.Fatalf("history without Alerts = %d", st)
	}
	_, m = rdDo(t, e.srv.URL, "GET", "/api/v1/history?kind=snooze", "")
	if en := list(t, m, "entries"); len(en) != 0 {
		t.Fatal("empty must be []")
	}
}

func TestAlertsReadNotConfigured503(t *testing.T) {
	e := newRDEnv(t, true)
	srv := e.withDeps(t, func(*Deps) {}) // Alerts == nil
	paths := []string{
		"/api/v1/alerts/templates", "/api/v1/alerts/policies", "/api/v1/alerts/policies/proj-a/1", "/api/v1/alerts/channels",
		"/api/v1/alerts/uptime-checks", "/api/v1/alerts/incidents", "/api/v1/alerts/history", "/api/v1/alerts/coverage",
		"/api/v1/alerts/snoozes", "/api/v1/alerts/routing", "/api/v1/budgets", "/api/v1/budgets/suggestions"}
	for _, p := range paths {
		st, m := rdDo(t, srv.URL, "GET", p, "")
		er := wantErr(t, st, m, 503, "alerts_not_configured")
		if er["hint"] == nil {
			t.Fatalf("%s: no hint", p)
		}
	}
	st, m := rdDo(t, srv.URL, "PUT", "/api/v1/alerts/routing", `{"routing":{}}`)
	wantErr(t, st, m, 503, "alerts_not_configured")

	// Wired but with the specific dependency nil.
	srv = e.withDeps(t, func(d *Deps) { d.Alerts = &AlertsDeps{Writable: true} })
	for _, p := range paths[:1] {
		st, m := rdDo(t, srv.URL, "GET", p, "")
		wantErr(t, st, m, 503, "alerts_not_configured")
	}
	st, m = rdDo(t, srv.URL, "GET", "/api/v1/budgets", "")
	wantErr(t, st, m, 503, "alerts_not_configured")
}

func TestAlertsReadErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
		hint   bool
	}{
		{alerting.ErrPermissionDenied, 403, "permission_denied", true},
		{alerting.ErrNotFound, 404, "not_found", false},
		{alerting.ErrUpstream, 502, "upstream_error", false},
		{alerting.ErrAPIDisabled, 503, "api_disabled", true},
		{alerting.ErrNotConfigured, 503, "alerts_not_configured", true},
		{alerting.ErrPreviewStale, 409, "preview_stale", true},
		{errors.New("boom secret detail"), 500, "internal_error", false},
	}
	for _, c := range cases {
		e := newRDEnv(t, true)
		e.budgets.err = fmt.Errorf("%w: detail", c.err)
		st, m := e.get("/api/v1/budgets")
		er := wantErr(t, st, m, c.status, c.code)
		if (er["hint"] != nil) != c.hint {
			t.Fatalf("%s hint = %v", c.code, er["hint"])
		}
		if c.status == 500 && strings.Contains(fmt.Sprint(er["message"]), "secret detail") {
			t.Fatal("internal error leaked its cause")
		}
	}
}

func TestAlertsReadMethodNotAllowed(t *testing.T) {
	e := newRDEnv(t, true)
	st, m := rdDo(t, e.srv.URL, "DELETE", "/api/v1/alerts/policies", "")
	wantErr(t, st, m, 405, "method_not_allowed")
}
