package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakePolicies struct{ list []PolicyView }

func (f *fakePolicies) List(_ context.Context, project string) ([]PolicyView, error) {
	var out []PolicyView
	for _, p := range f.list {
		if p.Project == project {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakePolicies) Get(_ context.Context, name string) (PolicyView, error) {
	for _, p := range f.list {
		if p.Name == name {
			return p, nil
		}
	}
	return PolicyView{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}
func (f *fakePolicies) Create(context.Context, string, json.RawMessage) (PolicyView, error) {
	return PolicyView{}, nil
}
func (f *fakePolicies) Update(context.Context, string, json.RawMessage) (PolicyView, error) {
	return PolicyView{}, nil
}
func (f *fakePolicies) Delete(context.Context, string) error { return nil }
func (f *fakePolicies) SetEnabled(context.Context, string, bool) (PolicyView, error) {
	return PolicyView{}, nil
}

type fakeBudgets struct{ list []BudgetView }

func (f *fakeBudgets) List(context.Context) ([]BudgetView, error) { return f.list, nil }
func (f *fakeBudgets) Get(_ context.Context, name string) (BudgetView, error) {
	for _, b := range f.list {
		if b.Name == name {
			return b, nil
		}
	}
	return BudgetView{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}
func (f *fakeBudgets) Create(context.Context, BudgetDraft, []string) (BudgetView, error) {
	return BudgetView{}, nil
}
func (f *fakeBudgets) Update(context.Context, string, BudgetDraft, []string) (BudgetView, error) {
	return BudgetView{}, nil
}
func (f *fakeBudgets) Delete(context.Context, string) error { return nil }
func (f *fakeBudgets) Body(BudgetDraft, []string) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

type fakeEngine struct{ reqs []PreviewRequest }

func (f *fakeEngine) Preview(_ context.Context, r PreviewRequest) (Preview, error) {
	f.reqs = append(f.reqs, r)
	return Preview{PreviewToken: fmt.Sprintf("pv_%d", len(f.reqs)), Kind: r.Kind, Action: r.Action, Target: r.Target}, nil
}
func (f *fakeEngine) Apply(context.Context, ApplyRequest) (ApplyResult, error) {
	return ApplyResult{}, nil
}

type fakeRouter struct{}

func (fakeRouter) Get(context.Context) (map[string][]string, error) { return nil, nil }
func (fakeRouter) Put(context.Context, map[string][]string) error   { return nil }
func (fakeRouter) Resolve(_ context.Context, _, _ string, _ []string) ([]string, []string, error) {
	return []string{chA1}, []string{"channel " + chA1 + " is unverified — Google sends a verification email"}, nil
}

func ip(i int) *int { return &i }

func pol(id, name, filter string, dur int) PolicyView {
	return PolicyView{Name: "projects/proj-a/alertPolicies/" + id, Project: "proj-a", DisplayName: name, Enabled: true,
		Conditions: []ConditionSummary{{Filter: filter, DurationSeconds: ip(dur)}}}
}

func fixturePolicies() []PolicyView {
	return []PolicyView{
		pol("1", "Zero-Backend CPU Spike", `metric.type="compute.googleapis.com/instance/cpu/utilization" AND resource.label.instance_id="111"`, 300),
		pol("2", "High memory load", `metric.type="agent.googleapis.com/memory/percent_used"`, 0),
		pol("3", "smalltech backend down uptime failure", `metric.type="monitoring.googleapis.com/uptime_check/check_passed" AND metric.label.check_id="api-health"`, 600),
		pol("4", "smalltech down for 2 hours", `metric.type="monitoring.googleapis.com/uptime_check/check_passed" AND metric.label.check_id="api-health"`, 7200),
		{Name: "projects/proj-a/alertPolicies/9", Project: "proj-a", DisplayName: "Other", Conditions: []ConditionSummary{{Filter: `metric.type="x"`}}},
	}
}

func amt(v float64) *float64 { return &v }

func fixtureBudgets() []BudgetView {
	return []BudgetView{{Name: "billingAccounts/000/budgets/b1", DisplayName: "Ai-agent-boilerplate", AmountKind: "specified",
		Amount: amt(5000), Currency: "INR", Projects: []string{"proj-a"},
		Thresholds: []BudgetThreshold{{50, "current_spend"}, {90, "current_spend"}, {100, "current_spend"}}}}
}

func newAdopt(pols []PolicyView, buds []BudgetView) (Adopter, *fakeEngine) {
	eng := &fakeEngine{}
	return NewAdopter(AdoptDeps{Builder: NewBuilder(), Policies: &fakePolicies{pols}, Budgets: &fakeBudgets{buds},
		Engine: eng, Router: fakeRouter{}, Projects: []string{"proj-a"}, Now: time.Now}), eng
}

func params(t *testing.T, r PreviewRequest) TemplateParams {
	t.Helper()
	var p TemplateParams
	if err := json.Unmarshal(r.Params, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAdoptVMShapes(t *testing.T) {
	for _, c := range []struct {
		shape, tpl, old string
		thr             float64
		dur             int
	}{
		{ShapeZeroCPUSpike, TplVMCPUHigh, "projects/proj-a/alertPolicies/1", 80, 300},
		{ShapeHighMemory, TplVMMemoryHigh, "projects/proj-a/alertPolicies/2", 85, 900},
	} {
		a, eng := newAdopt(fixturePolicies(), nil)
		got, err := a.Preview(context.Background(), AdoptRequest{Shape: c.shape})
		if err != nil {
			t.Fatal(c.shape, err)
		}
		if len(eng.reqs) != 1 || len(got.Previews) != 1 || got.Previews[0].PreviewToken != "pv_1" {
			t.Fatalf("%s: reqs=%v", c.shape, eng.reqs)
		}
		r := eng.reqs[0]
		p := params(t, r)
		if r.Kind != TargetPolicy || r.Action != ActionCreate || r.TemplateID != c.tpl || r.Project != "proj-a" ||
			*p.Threshold != c.thr || *p.DurationSeconds != c.dur {
			t.Fatalf("%s: bad request %+v %+v", c.shape, r, p)
		}
		if len(got.Replaces) != 1 || got.Replaces[0] != c.old {
			t.Fatalf("replaces = %v", got.Replaces)
		}
		if len(got.FollowUp) != 1 || got.FollowUp[0].Action != ActionDisable || got.FollowUp[0].Target != c.old {
			t.Fatalf("follow_up = %v", got.FollowUp)
		}
	}
}

func TestAdoptMemoryNotesZeroDuration(t *testing.T) {
	a, _ := newAdopt(fixturePolicies(), nil)
	got, _ := a.Preview(context.Background(), AdoptRequest{Shape: ShapeHighMemory})
	if !strings.Contains(strings.Join(got.Notes, "|"), "duration 0 s") {
		t.Fatalf("notes = %v", got.Notes)
	}
}

func TestAdoptUptimeBothShapes(t *testing.T) {
	for _, shape := range []string{ShapeUptimeBackend, ShapeUptimeTwoHours} {
		a, eng := newAdopt(fixturePolicies(), nil)
		got, err := a.Preview(context.Background(), AdoptRequest{Shape: shape})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Previews) != 2 || len(eng.reqs) != 2 {
			t.Fatalf("%s: %d previews", shape, len(got.Previews))
		}
		w, c := params(t, eng.reqs[0]), params(t, eng.reqs[1])
		if eng.reqs[0].TemplateID != TplUptimeDown || w.Instance != "warning" || w.Severity != SevWarning || *w.DurationSeconds != 600 ||
			c.Instance != "critical" || c.Severity != SevCritical || *c.DurationSeconds != 7200 {
			t.Fatalf("%s: %+v %+v", shape, w, c)
		}
		if len(got.Replaces) != 2 || len(got.FollowUp) != 2 {
			t.Fatalf("replaces = %v follow = %v", got.Replaces, got.FollowUp)
		}
		set := strings.Join(got.Replaces, ",")
		if !strings.Contains(set, "alertPolicies/3") || !strings.Contains(set, "alertPolicies/4") {
			t.Fatalf("replaces = %v", got.Replaces)
		}
	}
}

func TestAdoptUptimeOnlyOneExists(t *testing.T) {
	pols := fixturePolicies()
	pols = append(pols[:2], pols[3:]...) // drop #3
	a, _ := newAdopt(pols, nil)
	got, err := a.Preview(context.Background(), AdoptRequest{Shape: ShapeUptimeTwoHours})
	if err != nil || len(got.Replaces) != 1 || !strings.HasSuffix(got.Replaces[0], "/4") {
		t.Fatalf("%v %v", got.Replaces, err)
	}
}

func TestAdoptAlreadyManagedYieldsUpdate(t *testing.T) {
	pols := fixturePolicies()
	pols = append(pols, PolicyView{Name: "projects/proj-a/alertPolicies/50", Project: "proj-a", Managed: true, TemplateID: TplUptimeDown, Instance: "critical"},
		PolicyView{Name: "projects/proj-a/alertPolicies/51", Project: "proj-a", Managed: true, TemplateID: TplVMCPUHigh})
	a, eng := newAdopt(pols, nil)
	if _, err := a.Preview(context.Background(), AdoptRequest{Shape: ShapeUptimeBackend}); err != nil {
		t.Fatal(err)
	}
	if eng.reqs[0].Action != ActionCreate || eng.reqs[1].Action != ActionUpdate || eng.reqs[1].Target != "projects/proj-a/alertPolicies/50" {
		t.Fatalf("%+v", eng.reqs)
	}
	if _, err := a.Preview(context.Background(), AdoptRequest{Shape: ShapeZeroCPUSpike}); err != nil {
		t.Fatal(err)
	}
	if last := eng.reqs[len(eng.reqs)-1]; last.Action != ActionUpdate || last.Target != "projects/proj-a/alertPolicies/51" {
		t.Fatalf("%+v", last)
	}
}

func TestAdoptBudget(t *testing.T) {
	a, eng := newAdopt(nil, fixtureBudgets())
	got, err := a.Preview(context.Background(), AdoptRequest{Shape: ShapeBudgetBoilerplate})
	if err != nil {
		t.Fatal(err)
	}
	r := eng.reqs[0]
	var d BudgetDraft
	if err := json.Unmarshal(r.Params, &d); err != nil {
		t.Fatal(err)
	}
	if r.Kind != TargetBudget || r.Action != ActionCreate || d.DisplayName != "cloud0-ai-agent-boilerplate" || d.Amount != 5000 ||
		len(d.Projects) != 1 || d.Projects[0] != "proj-a" || len(d.Thresholds) != 3 || d.Thresholds[1].Percent != 90 ||
		len(d.ChannelOverride) != 1 || d.ChannelOverride[0] != chA1 {
		t.Fatalf("draft = %+v req=%+v", d, r)
	}
	if len(got.Replaces) != 1 || len(got.FollowUp) != 0 || !strings.Contains(strings.Join(got.Notes, "|"), "unverified") {
		t.Fatalf("%+v", got)
	}
	// managed copy exists -> update
	buds := append(fixtureBudgets(), BudgetView{Name: "billingAccounts/000/budgets/b2", DisplayName: "cloud0-ai-agent-boilerplate", Managed: true})
	a, eng = newAdopt(nil, buds)
	if _, err := a.Preview(context.Background(), AdoptRequest{Shape: ShapeBudgetBoilerplate}); err != nil {
		t.Fatal(err)
	}
	if eng.reqs[0].Action != ActionUpdate || eng.reqs[0].Target != "billingAccounts/000/budgets/b2" {
		t.Fatalf("%+v", eng.reqs[0])
	}
}

func TestAdoptErrors(t *testing.T) {
	ctx := context.Background()
	a, _ := newAdopt(fixturePolicies(), fixtureBudgets())
	if _, err := a.Preview(ctx, AdoptRequest{Shape: "bogus"}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("unknown shape: %v", err)
	}
	if _, err := a.Preview(ctx, AdoptRequest{Shape: ShapeZeroCPUSpike, Target: "projects/proj-a/alertPolicies/2"}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("mismatched target: %v", err)
	}
	if _, err := a.Preview(ctx, AdoptRequest{Shape: ShapeZeroCPUSpike, Target: "projects/proj-a/alertPolicies/404"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing target: %v", err)
	}
	empty, _ := newAdopt(nil, nil)
	for _, s := range []string{ShapeZeroCPUSpike, ShapeHighMemory, ShapeUptimeBackend, ShapeUptimeTwoHours, ShapeBudgetBoilerplate} {
		if _, err := empty.Preview(ctx, AdoptRequest{Shape: s}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: %v", s, err)
		}
	}
	// two matching CPU policies and no target -> ambiguous
	pols := append(fixturePolicies(), pol("7", "Zero-Backend CPU Spike", `metric.type="compute.googleapis.com/instance/cpu/utilization"`, 300))
	amb, _ := newAdopt(pols, nil)
	if _, err := amb.Preview(ctx, AdoptRequest{Shape: ShapeZeroCPUSpike}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("ambiguous: %v", err)
	}
	// managed target refused
	mp := []PolicyView{{Name: "projects/proj-a/alertPolicies/8", Project: "proj-a", Managed: true, AdoptShape: ShapeZeroCPUSpike}}
	m, _ := newAdopt(mp, nil)
	if _, err := m.Preview(ctx, AdoptRequest{Shape: ShapeZeroCPUSpike, Target: mp[0].Name}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("managed: %v", err)
	}
}
