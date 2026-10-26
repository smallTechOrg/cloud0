package attribution

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func engTID(v int64) *int64 { return &v }

func engTRule(i int64, prio int, field, op, key, val string, a Action) Rule {
	r := Rule{Priority: prio, Match: Match{Field: field, Op: op, Key: key, Value: val}, Action: a, Enabled: true, Source: SourceUser}
	if i != 0 {
		r.ID = engTID(i)
	}
	return r
}

func engTMust(t *testing.T, rules []Rule) Engine {
	t.Helper()
	e, err := NewEngine(rules)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func engTRuleID(p *int64) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprint(*p)
}

func TestBuiltinLabelPrecedence(t *testing.T) {
	e := engTMust(t, []Rule{
		engTRule(1, 10, FieldResourceName, OpContains, "", "vm-a", Action{Product: "product-b", Environment: "dev"}),
	})
	ev := e.Evaluate(Row{ResourceName: "vm-a", Labels: map[string]string{"Product": " Product-A ", "ENV": "Prod"}})
	if ev.Product != "product-a" || ev.Environment != "prod" || engTRuleID(ev.RuleID) != "0" || engTRuleID(ev.EnvironmentRuleID) != "0" {
		t.Fatalf("builtin should win: %+v", ev)
	}
	if len(ev.Evidence) != 2 || ev.Evidence[0] != "label product= Product-A " || ev.Evidence[1] != "label env=Prod" {
		t.Fatalf("evidence: %v", ev.Evidence)
	}
	// environment label key fallback and independence of dimensions
	ev = e.Evaluate(Row{ResourceName: "vm-a", Labels: map[string]string{"environment": "staging"}})
	if ev.Product != "product-b" || ev.Environment != "staging" || engTRuleID(ev.RuleID) != "1" || engTRuleID(ev.EnvironmentRuleID) != "0" {
		t.Fatalf("independent: %+v", ev)
	}
	// env wins over environment
	ev = e.Evaluate(Row{Labels: map[string]string{"environment": "b", "env": "a"}})
	if ev.Environment != "a" {
		t.Fatalf("env first: %+v", ev)
	}
	// invalid label value is ignored; rule decides
	ev = e.Evaluate(Row{ResourceName: "vm-a", Labels: map[string]string{"product": "Bad Name!", "env": ""}})
	if ev.Product != "product-b" || engTRuleID(ev.RuleID) != "1" || ev.Environment != "dev" {
		t.Fatalf("invalid label: %+v", ev)
	}
}

func TestBuiltinSharedLabel(t *testing.T) {
	e := engTMust(t, nil)
	ev := e.Evaluate(Row{Labels: map[string]string{"product": "Shared"}})
	if !ev.Shared || ev.Product != "" || engTRuleID(ev.RuleID) != "0" {
		t.Fatalf("%+v", ev)
	}
}

func TestSharedAction(t *testing.T) {
	e := engTMust(t, []Rule{
		engTRule(1, 900, FieldService, OpEquals, "", "VM Manager", Action{Shared: true}),
		engTRule(2, 950, FieldService, OpEquals, "", "VM Manager", Action{Product: "product-a"}),
	})
	ev := e.Evaluate(Row{Service: "vm manager"})
	if !ev.Shared || ev.Product != "" || engTRuleID(ev.RuleID) != "1" || ev.EnvironmentRuleID != nil {
		t.Fatalf("%+v", ev)
	}
	if ev.Evidence[0] != `service equals "VM Manager"` {
		t.Fatalf("%v", ev.Evidence)
	}
}

func TestFieldOpTable(t *testing.T) {
	row := Row{
		ProjectID: "Proj-A", Service: "Compute Engine", SKU: "N2 Instance Core running in Americas",
		ResourceName: "projects/123/instances/VM-A", ResourceGlobalName: "//compute.googleapis.com/projects/123/zones/z/instances/vm-a",
		Labels: map[string]string{"App": "Svc-A"},
	}
	cases := []struct {
		name                string
		field, op, key, val string
		want                bool
	}{
		{"label equals", FieldLabel, OpEquals, "app", "SVC-a", true},
		{"label equals miss", FieldLabel, OpEquals, "app", "svc", false},
		{"label contains", FieldLabel, OpContains, "APP", "vc-", true},
		{"label prefix", FieldLabel, OpPrefix, "app", "svc", true},
		{"label prefix miss", FieldLabel, OpPrefix, "app", "vc", false},
		{"label regex", FieldLabel, OpRegex, "app", "^svc-[a-z]$", true},
		{"label missing key", FieldLabel, OpEquals, "other", "svc-a", false},
		{"project equals", FieldProject, OpEquals, "", "proj-a", true},
		{"project contains", FieldProject, OpContains, "", "oj-", true},
		{"project prefix", FieldProject, OpPrefix, "", "PROJ", true},
		{"project regex", FieldProject, OpRegex, "", `proj-\w`, true},
		{"project regex miss", FieldProject, OpRegex, "", `^roj`, false},
		{"sku contains", FieldSKU, OpContains, "", "n2 instance core", true},
		{"sku prefix", FieldSKU, OpPrefix, "", "n2 instance", true},
		{"sku equals miss", FieldSKU, OpEquals, "", "n2 instance", false},
		{"sku regex", FieldSKU, OpRegex, "", `core running in (americas|europe)`, true},
		{"service equals", FieldService, OpEquals, "", "compute engine", true},
		{"service contains", FieldService, OpContains, "", "engine", true},
		{"service prefix", FieldService, OpPrefix, "", "compute", true},
		{"service regex", FieldService, OpRegex, "", "^compute", true},
		{"name equals last segment", FieldResourceName, OpEquals, "", "vm-a", true},
		{"name equals full", FieldResourceName, OpEquals, "", "projects/123/instances/vm-a", true},
		{"name contains", FieldResourceName, OpContains, "", "instances/vm", true},
		{"name prefix full", FieldResourceName, OpPrefix, "", "projects/123", true},
		{"name prefix last segment", FieldResourceName, OpPrefix, "", "vm-", true},
		{"name prefix global", FieldResourceName, OpPrefix, "", "//compute.googleapis.com", true},
		{"name regex anchored last segment", FieldResourceName, OpRegex, "", "^vm-a$", true},
		{"name regex miss", FieldResourceName, OpRegex, "", "^vm-b$", false},
		{"name contains miss", FieldResourceName, OpContains, "", "vm-b", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := engTMust(t, []Rule{engTRule(1, 1, tc.field, tc.op, tc.key, tc.val, Action{Product: "product-a"})})
			got := e.Evaluate(row).Product == "product-a"
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestResourceNameAbsent(t *testing.T) {
	e := engTMust(t, []Rule{engTRule(1, 1, FieldResourceName, OpRegex, "", ".*", Action{Product: "product-a"})})
	if ev := e.Evaluate(Row{ProjectID: "p"}); ev.RuleID != nil || ev.Product != "" {
		t.Fatalf("row without resource must not match: %+v", ev)
	}
	// only global name present
	if ev := e.Evaluate(Row{ResourceGlobalName: "//x/projects/1/instances/vm-a"}); ev.Product != "product-a" {
		t.Fatalf("%+v", ev)
	}
}

func TestOldRowNilLabelsFallback(t *testing.T) {
	e := engTMust(t, []Rule{
		engTRule(1, 100, FieldLabel, OpEquals, "app", "svc-a", Action{Product: "product-a"}),
		engTRule(2, 300, FieldResourceName, OpContains, "", "svc-b", Action{Product: "product-b"}),
		engTRule(3, 310, FieldResourceName, OpRegex, "", `(^|[-_/])prod($|[-_/])`, Action{Environment: "prod"}),
	})
	ev := e.Evaluate(Row{ResourceName: "projects/1/instances/svc-b-prod", Labels: nil})
	if ev.Product != "product-b" || ev.Environment != "prod" || engTRuleID(ev.RuleID) != "2" || engTRuleID(ev.EnvironmentRuleID) != "3" {
		t.Fatalf("%+v", ev)
	}
	ev = e.Evaluate(Row{ResourceName: "projects/1/instances/svc-b-prod", Labels: map[string]string{}})
	if ev.Product != "product-b" {
		t.Fatalf("empty labels: %+v", ev)
	}
}

func TestPriorityTiesAndDisabled(t *testing.T) {
	rules := []Rule{
		engTRule(5, 10, FieldProject, OpEquals, "", "p", Action{Product: "product-5"}),
		engTRule(3, 10, FieldProject, OpEquals, "", "p", Action{Product: "product-3"}),
		engTRule(4, 5, FieldProject, OpEquals, "", "p", Action{Product: "product-4"}),
	}
	e := engTMust(t, rules)
	var order []string
	for _, r := range e.Rules() {
		order = append(order, engTRuleID(r.ID))
	}
	if strings.Join(order, ",") != "4,3,5" {
		t.Fatalf("order %v", order)
	}
	if ev := e.Evaluate(Row{ProjectID: "p"}); ev.Product != "product-4" {
		t.Fatalf("%+v", ev)
	}
	rules[2].Enabled = false
	e = engTMust(t, rules)
	if ev := e.Evaluate(Row{ProjectID: "p"}); ev.Product != "product-3" || engTRuleID(ev.RuleID) != "3" {
		t.Fatalf("tie by id: %+v", ev)
	}
	if len(e.Rules()) != 2 {
		t.Fatalf("disabled rules must not be listed: %d", len(e.Rules()))
	}
	for _, s := range e.Explain(Row{ProjectID: "p"}) {
		if s.RuleID == 4 {
			t.Fatal("disabled rule in trail")
		}
	}
}

func TestIndependentDimensions(t *testing.T) {
	e := engTMust(t, []Rule{
		engTRule(1, 1, FieldResourceName, OpContains, "", "web", Action{Environment: "prod"}),
		engTRule(2, 2, FieldResourceName, OpContains, "", "web", Action{Product: "product-a", Environment: "dev"}),
		engTRule(3, 3, FieldResourceName, OpContains, "", "web", Action{Product: "product-b"}),
	})
	ev := e.Evaluate(Row{ResourceName: "web"})
	if ev.Product != "product-a" || ev.Environment != "prod" || engTRuleID(ev.RuleID) != "2" || engTRuleID(ev.EnvironmentRuleID) != "1" {
		t.Fatalf("%+v", ev)
	}
	if len(ev.Evidence) != 2 {
		t.Fatalf("%v", ev.Evidence)
	}
}

func TestNoDecision(t *testing.T) {
	e := engTMust(t, []Rule{engTRule(1, 1, FieldProject, OpEquals, "", "zzz", Action{Product: "product-a"})})
	ev := e.Evaluate(Row{ProjectID: "p"})
	if ev.Product != "" || ev.Environment != "" || ev.Shared || ev.RuleID != nil || ev.EnvironmentRuleID != nil || ev.Evidence == nil || len(ev.Evidence) != 0 {
		t.Fatalf("%+v", ev)
	}
}

func TestEnvironmentOnly(t *testing.T) {
	e := engTMust(t, []Rule{engTRule(1, 1, FieldResourceName, OpContains, "", "stage", Action{Environment: "staging"})})
	ev := e.Evaluate(Row{ResourceName: "x-stage"})
	if ev.Product != "" || ev.Environment != "staging" || ev.RuleID != nil || engTRuleID(ev.EnvironmentRuleID) != "1" {
		t.Fatalf("%+v", ev)
	}
}

func TestUnsavedRulesGetSyntheticIDs(t *testing.T) {
	e := engTMust(t, []Rule{
		engTRule(0, 5, FieldProject, OpEquals, "", "p", Action{Product: "product-a"}),
		engTRule(0, 5, FieldProject, OpEquals, "", "p", Action{Product: "product-b"}),
	})
	ev := e.Evaluate(Row{ProjectID: "p"})
	if ev.Product != "product-a" || ev.RuleID == nil || *ev.RuleID != -1 {
		t.Fatalf("%+v", ev)
	}
	if e.Rules()[0].ID != nil {
		t.Fatal("Rules must keep nil ids")
	}
}

func TestInvalidRules(t *testing.T) {
	good := engTRule(1, 1, FieldProject, OpEquals, "", "p", Action{Product: "product-a"})
	bad := map[string]func(r *Rule){
		"negative priority": func(r *Rule) { r.Priority = -1 },
		"unknown field":     func(r *Rule) { r.Match.Field = "zone" },
		"unknown op":        func(r *Rule) { r.Match.Op = "glob" },
		"empty value":       func(r *Rule) { r.Match.Value = "" },
		"label no key":      func(r *Rule) { r.Match.Field = FieldLabel },
		"key on non-label":  func(r *Rule) { r.Match.Key = "app" },
		"no action":         func(r *Rule) { r.Action = Action{} },
		"shared+product":    func(r *Rule) { r.Action = Action{Shared: true, Product: "product-a"} },
		"bad product name":  func(r *Rule) { r.Action.Product = "Bad Name" },
		"bad env name":      func(r *Rule) { r.Action.Environment = "-x" },
		"long name":         func(r *Rule) { r.Action.Product = strings.Repeat("a", 41) },
		"bad regex":         func(r *Rule) { r.Match.Op = OpRegex; r.Match.Value = "(" },
		"regex backref":     func(r *Rule) { r.Match.Op = OpRegex; r.Match.Value = `(a)\1` },
		"regex too long":    func(r *Rule) { r.Match.Op = OpRegex; r.Match.Value = strings.Repeat("a", 201) },
		"regex huge repeat": func(r *Rule) { r.Match.Op = OpRegex; r.Match.Value = `a{1001}` },
	}
	for name, mut := range bad {
		t.Run(name, func(t *testing.T) {
			r := good
			mut(&r)
			_, err := NewEngine([]Rule{good, r})
			if !errors.Is(err, ErrInvalidRule) {
				t.Fatalf("want ErrInvalidRule, got %v", err)
			}
			if !strings.Contains(err.Error(), "rules[1]") {
				t.Fatalf("missing index: %v", err)
			}
		})
	}
	// a disabled rule is still validated
	r := good
	r.Enabled = false
	r.Match.Op = "nope"
	if _, err := NewEngine([]Rule{r}); !errors.Is(err, ErrInvalidRule) || !strings.Contains(err.Error(), "rules[0]") {
		t.Fatalf("%v", err)
	}
	// 200-char regex is accepted
	r = good
	r.Match.Op, r.Match.Value = OpRegex, strings.Repeat("a", 200)
	if _, err := NewEngine([]Rule{r}); err != nil {
		t.Fatal(err)
	}
}

func TestRulesNormalised(t *testing.T) {
	e := engTMust(t, []Rule{engTRule(1, 1, FieldProject, OpEquals, "", "p", Action{Product: " Product-A ", Environment: "PROD"})})
	a := e.Rules()[0].Action
	if a.Product != "product-a" || a.Environment != "prod" {
		t.Fatalf("%+v", a)
	}
	// mutating the returned slice must not affect the engine
	e.Rules()[0].Action.Product = "x"
	if e.Evaluate(Row{ProjectID: "p"}).Product != "product-a" {
		t.Fatal("engine state leaked")
	}
}

func TestExplainTrail(t *testing.T) {
	e := engTMust(t, []Rule{
		engTRule(1, 100, FieldLabel, OpEquals, "app", "svc-a", Action{Product: "product-a"}),
		engTRule(2, 310, FieldResourceName, OpContains, "", "web", Action{Product: "product-b"}),
		engTRule(3, 320, FieldResourceName, OpContains, "", "web", Action{Product: "product-c", Environment: "prod"}),
		engTRule(4, 330, FieldProject, OpEquals, "", "p", Action{Environment: "dev"}),
		engTRule(5, 340, FieldProject, OpEquals, "", "p", Action{Product: "product-z"}),
	})
	row := Row{ProjectID: "p", ResourceName: "projects/1/instances/web-1", Labels: map[string]string{"env": "staging"}}
	tr := e.Explain(row)
	// builtin product, builtin env, rule1, rule2, rule3 (last deciding? env already decided -> no)
	if len(tr) != 4 {
		t.Fatalf("trail len %d: %+v", len(tr), tr)
	}
	if !tr[0].Builtin || tr[0].Matched || tr[0].Applied != nil || tr[0].Description != "label product" {
		t.Fatalf("%+v", tr[0])
	}
	if !tr[1].Builtin || !tr[1].Matched || tr[1].Applied == nil || *tr[1].Applied != KindEnvironment || tr[1].Reason != "label env=staging" {
		t.Fatalf("%+v", tr[1])
	}
	if tr[2].RuleID != 1 || tr[2].Matched || tr[2].Applied != nil || tr[2].Reason != `no label "app"` || tr[2].Priority == nil || *tr[2].Priority != 100 {
		t.Fatalf("%+v", tr[2])
	}
	if tr[3].RuleID != 2 || !tr[3].Matched || *tr[3].Applied != KindProduct {
		t.Fatalf("%+v", tr[3])
	}
	// the evaluation agrees
	ev := e.Evaluate(row)
	if ev.Product != "product-b" || engTRuleID(ev.RuleID) != "2" || engTRuleID(ev.EnvironmentRuleID) != "0" {
		t.Fatalf("%+v", ev)
	}
}

func TestExplainMatchedNotApplied(t *testing.T) {
	e := engTMust(t, []Rule{
		engTRule(1, 1, FieldProject, OpEquals, "", "p", Action{Product: "product-a"}),
		engTRule(2, 2, FieldProject, OpEquals, "", "p", Action{Product: "product-b"}),
		engTRule(3, 3, FieldProject, OpEquals, "", "p", Action{Environment: "prod"}),
		engTRule(4, 4, FieldProject, OpEquals, "", "p", Action{Environment: "dev"}),
	})
	tr := e.Explain(Row{ProjectID: "p"})
	// builtins (2), rule1 applied, rule2 matched/not applied, rule3 applied -> stop
	if len(tr) != 5 {
		t.Fatalf("len %d %+v", len(tr), tr)
	}
	if !tr[3].Matched || tr[3].Applied != nil {
		t.Fatalf("%+v", tr[3])
	}
	if tr[4].RuleID != 3 || *tr[4].Applied != KindEnvironment {
		t.Fatalf("%+v", tr[4])
	}
}

func TestExplainNothingDecides(t *testing.T) {
	e := engTMust(t, []Rule{engTRule(1, 1, FieldProject, OpEquals, "", "zzz", Action{Product: "product-a"})})
	tr := e.Explain(Row{ProjectID: "p"})
	if len(tr) != 3 {
		t.Fatalf("%+v", tr)
	}
	if tr[2].Matched || tr[2].Applied != nil || tr[2].Reason == "" {
		t.Fatalf("%+v", tr[2])
	}
	if tr := engTMust(t, nil).Explain(Row{}); len(tr) != 2 {
		t.Fatalf("builtins always listed: %+v", tr)
	}
}

func TestExplainSharedApplied(t *testing.T) {
	e := engTMust(t, []Rule{engTRule(1, 1, FieldService, OpEquals, "", "VM Manager", Action{Shared: true})})
	tr := e.Explain(Row{Service: "VM Manager"})
	last := tr[len(tr)-1]
	if last.RuleID != 1 || last.Applied == nil || *last.Applied != KindShared {
		t.Fatalf("%+v", last)
	}
}

// Explain and Evaluate must agree on every row.
func TestExplainAgreesWithEvaluate(t *testing.T) {
	e := engTMust(t, engTSampleRules())
	for _, row := range engTSampleRows(500) {
		ev := e.Evaluate(row)
		var pRule, eRule *int64
		for _, st := range e.Explain(row) {
			if st.Applied == nil {
				continue
			}
			switch *st.Applied {
			case KindProduct, KindShared:
				pRule = engPtr(st.RuleID)
				if strings.Contains(st.Reason, "also decides environment") {
					eRule = engPtr(st.RuleID)
				}
			case KindEnvironment:
				eRule = engPtr(st.RuleID)
			}
		}
		if engTRuleID(pRule) != engTRuleID(ev.RuleID) || engTRuleID(eRule) != engTRuleID(ev.EnvironmentRuleID) {
			t.Fatalf("mismatch trail (%s,%s) eval (%s,%s) for %+v", engTRuleID(pRule), engTRuleID(eRule), engTRuleID(ev.RuleID), engTRuleID(ev.EnvironmentRuleID), row)
		}
	}
}

func TestEvaluateIsPure(t *testing.T) {
	e := engTMust(t, engTSampleRules())
	rows := engTSampleRows(300)
	first := make([]Evaluation, len(rows))
	for i, r := range rows {
		first[i] = e.Evaluate(r)
	}
	// repeated, reversed and via a freshly built engine from shuffled input
	rules := engTSampleRules()
	for i, j := 0, len(rules)-1; i < j; i, j = i+1, j-1 {
		rules[i], rules[j] = rules[j], rules[i]
	}
	e2 := engTMust(t, rules)
	for k := 0; k < 3; k++ {
		for i := len(rows) - 1; i >= 0; i-- {
			if got := e.Evaluate(rows[i]); !reflect.DeepEqual(got, first[i]) {
				t.Fatalf("not pure at %d: %+v vs %+v", i, got, first[i])
			}
			if got := e2.Evaluate(rows[i]); !reflect.DeepEqual(got, first[i]) {
				t.Fatalf("input order changed result at %d: %+v vs %+v", i, got, first[i])
			}
		}
	}
	// case-variant duplicate label keys resolve deterministically
	row := Row{Labels: map[string]string{"ENV": "b", "Env": "a", "eNv": "c"}}
	want := e.Evaluate(row)
	for i := 0; i < 50; i++ {
		if got := e.Evaluate(row); !reflect.DeepEqual(got, want) {
			t.Fatalf("label key tie nondeterministic: %+v vs %+v", got, want)
		}
	}
}

func TestEvaluateDoesNotMutateRow(t *testing.T) {
	e := engTMust(t, engTSampleRules())
	labels := map[string]string{"App": "Svc-A", "Env": "PROD"}
	row := Row{ProjectID: "P", ResourceName: "A/b", Labels: labels}
	e.Evaluate(row)
	e.Explain(row)
	if len(labels) != 2 || labels["App"] != "Svc-A" || labels["Env"] != "PROD" || row.ProjectID != "P" {
		t.Fatal("row mutated")
	}
}

func engTSampleRules() []Rule {
	return []Rule{
		engTRule(1, 100, FieldLabel, OpEquals, "app", "svc-a", Action{Product: "product-a"}),
		engTRule(2, 200, FieldProject, OpEquals, "", "proj-b", Action{Product: "product-b"}),
		engTRule(3, 310, FieldResourceName, OpContains, "", "svc-c", Action{Product: "product-c"}),
		engTRule(4, 320, FieldResourceName, OpRegex, "", `(^|[-_/])prod($|[-_/])`, Action{Environment: "prod"}),
		engTRule(5, 330, FieldResourceName, OpPrefix, "", "stg-", Action{Environment: "staging"}),
		engTRule(6, 700, FieldSKU, OpContains, "", "network", Action{Shared: true}),
		engTRule(7, 900, FieldService, OpEquals, "", "VM Manager", Action{Shared: true}),
		engTRule(8, 910, FieldService, OpPrefix, "", "cloud", Action{Product: "product-d", Environment: "dev"}),
	}
}

func engTSampleRows(n int) []Row {
	names := []string{"", "svc-c-prod", "projects/1/instances/svc-c-web", "stg-db", "projects/9/instances/vm-a-prod", "other"}
	svcs := []string{"Compute Engine", "VM Manager", "Cloud Run", "Cloud SQL"}
	skus := []string{"Core", "Network egress", "RAM"}
	projects := []string{"proj-a", "proj-b", "proj-c"}
	rows := make([]Row, n)
	for i := range rows {
		r := Row{
			ProjectID: projects[i%len(projects)], Service: svcs[i%len(svcs)], SKU: skus[(i/2)%len(skus)],
			ResourceName: names[(i/3)%len(names)],
		}
		if r.ResourceName != "" && i%2 == 0 {
			r.ResourceGlobalName = "//compute.googleapis.com/" + r.ResourceName
		}
		switch i % 5 {
		case 0:
			r.Labels = map[string]string{"app": "svc-a"}
		case 1:
			r.Labels = map[string]string{"Env": "prod", "product": "product-l"}
		case 2:
			r.Labels = map[string]string{}
		}
		rows[i] = r
	}
	return rows
}

func TestLargeRunUnderBudget(t *testing.T) {
	e := engTMust(t, engTSampleRules())
	rows := engTSampleRows(1000)
	start := time.Now()
	n := 0
	for i := 0; i < 100_000; i++ {
		if e.Evaluate(rows[i%len(rows)]).RuleID != nil {
			n++
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("100k evaluations took %v", d)
	}
	if n == 0 {
		t.Fatal("nothing attributed")
	}
}

func BenchmarkEvaluate(b *testing.B) {
	e, _ := NewEngine(engTSampleRules())
	rows := engTSampleRows(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Evaluate(rows[i%len(rows)])
	}
}

func BenchmarkExplain(b *testing.B) {
	e, _ := NewEngine(engTSampleRules())
	rows := engTSampleRows(1000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		e.Explain(rows[i%len(rows)])
	}
}
