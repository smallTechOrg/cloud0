package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/store"
)

// ---- fakes -----------------------------------------------------------------

type engFakePolicies struct {
	mu      sync.Mutex
	m       map[string]PolicyView
	n       int
	writes  int
	failErr error
}

func newEngFakePolicies() *engFakePolicies { return &engFakePolicies{m: map[string]PolicyView{}} }

func (f *engFakePolicies) put(name string, raw string, managed bool, enabled bool) {
	var m map[string]any
	_ = json.Unmarshal([]byte(raw), &m)
	m["name"] = name
	m["enabled"] = enabled
	m["etag"] = fmt.Sprint(f.n)
	f.n++
	b, _ := json.Marshal(m)
	var nc struct {
		C []string `json:"notificationChannels"`
	}
	_ = json.Unmarshal(b, &nc)
	v := PolicyView{Name: name, Project: engProjectOf(name), Enabled: enabled, Managed: managed, RawJSON: b}
	for _, c := range nc.C {
		v.Channels = append(v.Channels, ChannelRef{Name: c})
	}
	if managed {
		var l struct {
			UL map[string]string `json:"userLabels"`
		}
		_ = json.Unmarshal(b, &l)
		v.TemplateID, v.Instance = l.UL[LabelTemplate], l.UL[LabelInstance]
	}
	f.m[name] = v
}

func (f *engFakePolicies) List(_ context.Context, project string) ([]PolicyView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []PolicyView
	for _, v := range f.m {
		if v.Project == project {
			out = append(out, v)
		}
	}
	return out, nil
}
func (f *engFakePolicies) Get(_ context.Context, name string) (PolicyView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[name]
	if !ok {
		return PolicyView{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return v, nil
}
func (f *engFakePolicies) Create(_ context.Context, project string, policy json.RawMessage) (PolicyView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return PolicyView{}, f.failErr
	}
	f.writes++
	name := fmt.Sprintf("projects/%s/alertPolicies/%d", project, 900+f.n)
	var l struct {
		UL map[string]string `json:"userLabels"`
		En *bool             `json:"enabled"`
	}
	_ = json.Unmarshal(policy, &l)
	en := l.En == nil || *l.En
	f.put(name, string(policy), l.UL[LabelManagedBy] == LabelManagedByVal, en)
	return f.m[name], nil
}
func (f *engFakePolicies) Update(_ context.Context, name string, policy json.RawMessage) (PolicyView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return PolicyView{}, f.failErr
	}
	f.writes++
	old := f.m[name]
	f.put(name, string(policy), old.Managed, old.Enabled)
	return f.m[name], nil
}
func (f *engFakePolicies) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return f.failErr
	}
	f.writes++
	delete(f.m, name)
	return nil
}
func (f *engFakePolicies) SetEnabled(_ context.Context, name string, en bool) (PolicyView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	old := f.m[name]
	f.put(name, string(old.RawJSON), old.Managed, en)
	return f.m[name], nil
}

type engFakeChannels struct {
	list    []Channel
	created []ChannelDraft
	deleted []string
}

func (f *engFakeChannels) List(_ context.Context, project string) ([]Channel, error) {
	var out []Channel
	for _, c := range f.list {
		if c.Project == project {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *engFakeChannels) Descriptors(context.Context, string) ([]ChannelDescriptor, error) {
	return []ChannelDescriptor{
		{Type: "email", Creatable: true, Labels: []LabelSpec{{Key: "email_address", Required: true}}},
		{Type: "webhook_tokenauth", Creatable: true, Labels: []LabelSpec{{Key: "url", Required: true, Sensitive: true}}},
		{Type: "slack", Creatable: false, Hint: "Slack needs console OAuth"},
	}, nil
}
func (f *engFakeChannels) Create(_ context.Context, d ChannelDraft) (Channel, error) {
	f.created = append(f.created, d)
	c := Channel{Name: "projects/" + d.Project + "/notificationChannels/77", Project: d.Project, Type: d.Type, DisplayName: d.DisplayName, Enabled: true, VerificationStatus: "UNVERIFIED"}
	f.list = append(f.list, c)
	return c, nil
}
func (f *engFakeChannels) Delete(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}

type engFakeUptime struct{ m map[string]UptimeCheck }

func (f *engFakeUptime) List(_ context.Context, project string) ([]UptimeCheck, error) {
	var out []UptimeCheck
	for _, v := range f.m {
		if v.Project == project {
			out = append(out, v)
		}
	}
	return out, nil
}
func (f *engFakeUptime) Get(_ context.Context, name string) (UptimeCheck, error) {
	v, ok := f.m[name]
	if !ok {
		return v, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return v, nil
}
func (f *engFakeUptime) Create(_ context.Context, d UptimeDraft) (UptimeCheck, error) {
	name := "projects/" + d.Project + "/uptimeCheckConfigs/new1"
	v := UptimeCheck{Name: name, Project: d.Project, DisplayName: d.DisplayName, Host: d.Host, Managed: true}
	f.m[name] = v
	return v, nil
}
func (f *engFakeUptime) Update(_ context.Context, name string, d UptimeDraft) (UptimeCheck, error) {
	v := f.m[name]
	v.DisplayName, v.Host = d.DisplayName, d.Host
	v.RawJSON = engMust(map[string]any{"name": name, "displayName": d.DisplayName})
	f.m[name] = v
	return v, nil
}
func (f *engFakeUptime) Delete(_ context.Context, name string) error { delete(f.m, name); return nil }

type engFakeSnoozes struct {
	m   map[string]Snooze
	now time.Time
}

func (f *engFakeSnoozes) List(_ context.Context, project string) ([]Snooze, error) {
	var out []Snooze
	for _, v := range f.m {
		if v.Project == project {
			out = append(out, v)
		}
	}
	return out, nil
}
func (f *engFakeSnoozes) Create(_ context.Context, d SnoozeDraft) (Snooze, error) {
	s := Snooze{Name: "projects/" + d.Project + "/snoozes/s9", Project: d.Project, DisplayName: d.DisplayName, PolicyNames: d.PolicyNames, StartTime: *d.StartTime, EndTime: d.StartTime.Add(time.Duration(d.DurationSeconds) * time.Second)}
	f.m[s.Name] = s
	return s, nil
}
func (f *engFakeSnoozes) End(_ context.Context, name string, now time.Time) (Snooze, error) {
	s := f.m[name]
	s.EndTime = now
	f.m[name] = s
	return s, nil
}

type engFakeBudgets struct {
	m       map[string]BudgetView
	created []BudgetDraft
	chans   [][]string
}

func (f *engFakeBudgets) List(context.Context) ([]BudgetView, error) { return nil, nil }
func (f *engFakeBudgets) Get(_ context.Context, name string) (BudgetView, error) {
	v, ok := f.m[name]
	if !ok {
		return v, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return v, nil
}
func (f *engFakeBudgets) Create(_ context.Context, d BudgetDraft, ch []string) (BudgetView, error) {
	f.created = append(f.created, d)
	f.chans = append(f.chans, ch)
	v := BudgetView{Name: "billingAccounts/B/budgets/new", DisplayName: d.DisplayName, Managed: true}
	v.RawJSON, _ = f.Body(d, ch)
	f.m[v.Name] = v
	return v, nil
}
func (f *engFakeBudgets) Update(_ context.Context, name string, d BudgetDraft, ch []string) (BudgetView, error) {
	v := f.m[name]
	v.DisplayName = d.DisplayName
	v.RawJSON, _ = f.Body(d, ch)
	f.m[name] = v
	return v, nil
}
func (f *engFakeBudgets) Delete(_ context.Context, name string) error { delete(f.m, name); return nil }
func (f *engFakeBudgets) Body(d BudgetDraft, ch []string) (json.RawMessage, error) {
	return engMust(map[string]any{"displayName": d.DisplayName, "amount": d.Amount, "thresholds": d.Thresholds, "channels": ch}), nil
}

type engFakeRouter struct {
	chans   []string
	warns   []string
	routing map[string][]string
}

func (r *engFakeRouter) Get(context.Context) (map[string][]string, error) { return r.routing, nil }
func (r *engFakeRouter) Put(context.Context, map[string][]string) error   { return nil }
func (r *engFakeRouter) Resolve(_ context.Context, _, _ string, override []string) ([]string, []string, error) {
	if len(override) > 0 {
		return override, nil, nil
	}
	return r.chans, r.warns, nil
}

// ---- engHarness ---------------------------------------------------------------

type engHarness struct {
	e   Engine
	pol *engFakePolicies
	ch  *engFakeChannels
	up  *engFakeUptime
	sn  *engFakeSnoozes
	bu  *engFakeBudgets
	rt  *engFakeRouter
	st  *store.Store
	now time.Time
	clk *time.Time
}

const engChanOK = "projects/proj-a/notificationChannels/111"

func newEngHarness(t *testing.T) *engHarness {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	h := &engHarness{
		pol: newEngFakePolicies(),
		ch:  &engFakeChannels{list: []Channel{{Name: engChanOK, Project: "proj-a", Type: "email", DisplayName: "Alert", Enabled: true, VerificationStatus: "VERIFIED"}}},
		up:  &engFakeUptime{m: map[string]UptimeCheck{}},
		bu:  &engFakeBudgets{m: map[string]BudgetView{}},
		rt:  &engFakeRouter{chans: []string{engChanOK}, routing: map[string][]string{"ERROR": {engChanOK}}},
		st:  st, now: now,
	}
	h.sn = &engFakeSnoozes{m: map[string]Snooze{}}
	h.clk = &h.now
	h.e = NewEngine(EngineDeps{Builder: NewBuilder(), Policies: h.pol, Channels: h.ch, Uptime: h.up, Snoozes: h.sn, Budgets: h.bu, Router: h.rt, Store: st, Now: func() time.Time { return *h.clk }})
	return h
}

func (h *engHarness) history(t *testing.T) []store.ChangeEntry {
	t.Helper()
	l, err := store.ListChanges(context.Background(), h.st.DB, store.ChangeFilter{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func engPolicyCreateReq(params string) PreviewRequest {
	return PreviewRequest{Kind: TargetPolicy, Action: ActionCreate, Project: "proj-a", TemplateID: TplVMCPUHigh, Params: json.RawMessage(params)}
}

func engHasWarn(p Preview, sub string) bool {
	for _, w := range p.Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func engSeedManaged(h *engHarness) string {
	name := "projects/proj-a/alertPolicies/10"
	h.pol.put(name, `{"displayName":"cloud0: VM CPU high","severity":"WARNING","userLabels":{"managed-by":"cloud0","cloud0-template":"vm_cpu_high"},"notificationChannels":["`+engChanOK+`"],"conditions":[{"displayName":"c"}]}`, true, true)
	return name
}

func engSeedUnmanaged(h *engHarness) string {
	name := "projects/proj-a/alertPolicies/20"
	h.pol.put(name, `{"displayName":"Zero-Backend CPU Spike","notificationChannels":["`+engChanOK+`"],"conditions":[{"displayName":"c"}]}`, false, true)
	return name
}

// ---- policy ----------------------------------------------------------------

func TestPolicyCreatePreviewApplyHistory(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	pv, err := h.e.Preview(ctx, engPolicyCreateReq(`{"threshold":90,"duration_seconds":120}`))
	if err != nil {
		t.Fatal(err)
	}
	if h.pol.writes != 0 {
		t.Fatal("preview must not write")
	}
	if !strings.HasPrefix(pv.PreviewToken, "pv_") || string(pv.Current) != "null" || pv.RequiresConfirm {
		t.Fatalf("preview = %+v", pv)
	}
	if !pv.ExpiresAt.Equal(h.now.Add(10 * time.Minute)) {
		t.Errorf("expires = %v", pv.ExpiresAt)
	}
	if !strings.Contains(string(pv.Desired), `"managed-by":"cloud0"`) || !strings.Contains(string(pv.Desired), engChanOK) {
		t.Errorf("desired lacks labels/channels: %s", pv.Desired)
	}
	if len(pv.Diff.Changes) == 0 || !strings.Contains(pv.Diff.Unified, "+") || !strings.Contains(pv.Gcloud, "gcloud monitoring policies create --project=proj-a") {
		t.Errorf("diff/gcloud missing: %+v %q", pv.Diff.Changes, pv.Gcloud)
	}
	res, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !strings.HasPrefix(res.Target, "projects/proj-a/alertPolicies/") || res.ChangeHistoryID == 0 || h.pol.writes != 1 {
		t.Fatalf("result = %+v writes=%d", res, h.pol.writes)
	}
	hist := h.history(t)
	if len(hist) != 1 || hist[0].Kind != TargetPolicy || hist[0].Action != ActionCreate || hist[0].Target != res.Target ||
		string(hist[0].Before) != "null" || hist[0].PreviewHash != strings.TrimPrefix(pv.PreviewToken, "pv_") || hist[0].Actor != "local-user" {
		t.Fatalf("history = %+v", hist)
	}
	if !strings.Contains(string(hist[0].After), "VM CPU") {
		t.Errorf("history after = %s", hist[0].After)
	}
}

func TestTokenSingleUseAndUnknown(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	pv, _ := h.e.Preview(ctx, engPolicyCreateReq(`{}`))
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("second apply = %v, want ErrPreviewStale", err)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: "pv_nope"}); !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("unknown token = %v", err)
	}
	if h.pol.writes != 1 || len(h.history(t)) != 1 {
		t.Fatalf("writes=%d history=%d", h.pol.writes, len(h.history(t)))
	}
}

func TestTokenExpiry(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	pv, _ := h.e.Preview(ctx, engPolicyCreateReq(`{}`))
	h.now = h.now.Add(10*time.Minute + time.Second)
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("expired apply = %v", err)
	}
	if h.pol.writes != 0 {
		t.Fatal("expired token must not write")
	}
}

func TestIdempotentRePreview(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	a, _ := h.e.Preview(ctx, engPolicyCreateReq(`{"threshold":85}`))
	b, _ := h.e.Preview(ctx, engPolicyCreateReq(`{"threshold":85}`))
	c, _ := h.e.Preview(ctx, engPolicyCreateReq(`{"threshold":86}`))
	if a.PreviewToken != b.PreviewToken || a.PreviewToken == c.PreviewToken {
		t.Fatalf("tokens a=%s b=%s c=%s", a.PreviewToken, b.PreviewToken, c.PreviewToken)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: a.PreviewToken}); err != nil {
		t.Fatal(err)
	}
	if h.pol.writes != 1 {
		t.Fatalf("writes = %d", h.pol.writes)
	}
}

func TestStaleWhenLiveChanged(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	name := engSeedManaged(h)
	pv, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionUpdate, Target: name, Params: json.RawMessage(`{"threshold":95}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Diff.Changes) == 0 || string(pv.Current) == "null" {
		t.Fatalf("update preview = %+v", pv)
	}
	// someone edits the policy in the console
	h.pol.put(name, `{"displayName":"edited by hand","userLabels":{"managed-by":"cloud0","cloud0-template":"vm_cpu_high"}}`, true, true)
	before := h.pol.writes
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("apply = %v, want ErrPreviewStale", err)
	}
	if h.pol.writes != before || len(h.history(t)) != 0 {
		t.Fatal("stale apply must not write or log")
	}
	// the token is spent: must preview again
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("reuse = %v", err)
	}
}

func TestStaleWhenLiveDeleted(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	name := engSeedManaged(h)
	pv, _ := h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionDisable, Target: name})
	delete(h.pol.m, name)
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("apply = %v", err)
	}
}

func TestPolicyUpdateManagedKeepsLabels(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	name := engSeedManaged(h)
	pv, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionUpdate, Target: name, Params: json.RawMessage(`{"threshold":95,"duration_seconds":600}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pv.Gcloud, "policies update "+name) {
		t.Errorf("gcloud = %s", pv.Gcloud)
	}
	res, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken})
	if err != nil || res.Target != name {
		t.Fatalf("apply = %+v %v", res, err)
	}
	h2 := h.history(t)
	if h2[0].Action != ActionUpdate || string(h2[0].Before) == "null" {
		t.Fatalf("history = %+v", h2[0])
	}
}

func TestUnmanagedRules(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	name := engSeedUnmanaged(h)
	// never templated
	if _, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionUpdate, Target: name, Params: json.RawMessage(`{}`)}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("templated update of unmanaged = %v", err)
	}
	// raw edit allowed
	desired := `{"displayName":"Zero-Backend CPU Spike","enabled":true,"notificationChannels":["` + engChanOK + `"],"conditions":[{"displayName":"c2"}]}`
	pv, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionUpdate, Target: name, Desired: json.RawMessage(desired)})
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Diff.Changes) != 1 || pv.Diff.Changes[0].Path != "conditions[0].displayName" {
		t.Fatalf("diff = %+v", pv.Diff.Changes)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); err != nil {
		t.Fatal(err)
	}
	// both params and desired
	if _, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionUpdate, Target: name, Params: json.RawMessage(`{"threshold":1}`), Desired: json.RawMessage(desired)}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("both = %v", err)
	}
	// create from raw desired is refused
	if _, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionCreate, Project: "proj-a", Desired: json.RawMessage(desired)}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("raw create = %v", err)
	}
	// disable works on unmanaged
	pv, err = h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionDisable, Target: name})
	if err != nil || !strings.Contains(pv.Gcloud, "--no-enabled") {
		t.Fatalf("disable preview = %+v %v", pv, err)
	}
	res, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken})
	if err != nil || h.pol.m[name].Enabled {
		t.Fatalf("disable apply = %+v %v", res, err)
	}
	pv, _ = h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionDisable, Target: name})
	if !engHasWarn(pv, "already") {
		t.Errorf("expected already-disabled warning: %v", pv.Warnings)
	}
}

func TestRawDesiredDroppingLabelsWarns(t *testing.T) {
	h := newEngHarness(t)
	name := engSeedManaged(h)
	pv, err := h.e.Preview(context.Background(), PreviewRequest{Kind: TargetPolicy, Action: ActionUpdate, Target: name,
		Desired: json.RawMessage(`{"displayName":"x","notificationChannels":["` + engChanOK + `"]}`)})
	if err != nil || !engHasWarn(pv, "managed-by=cloud0") {
		t.Fatalf("warnings = %v err=%v", pv.Warnings, err)
	}
}

func TestEnableDisablePolicy(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	name := engSeedManaged(h)
	pv, _ := h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionDisable, Target: name})
	if len(pv.Diff.Changes) != 1 || pv.Diff.Changes[0].Path != "enabled" {
		t.Fatalf("diff = %+v", pv.Diff.Changes)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); err != nil || h.pol.m[name].Enabled {
		t.Fatalf("disable: %v", err)
	}
	pv, _ = h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionEnable, Target: name})
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); err != nil || !h.pol.m[name].Enabled {
		t.Fatalf("enable: %v", err)
	}
	if len(h.history(t)) != 2 {
		t.Fatalf("history = %d", len(h.history(t)))
	}
}

func TestDeleteNeedsConfirm(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	name := engSeedUnmanaged(h)
	pv, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionDelete, Target: name})
	if err != nil || !pv.RequiresConfirm || string(pv.Desired) != "null" || !engHasWarn(pv, "unmanaged") {
		t.Fatalf("preview = %+v %v", pv, err)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("no confirm = %v", err)
	}
	if _, ok := h.pol.m[name]; !ok {
		t.Fatal("deleted without confirmation")
	}
	res, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken, ConfirmDelete: true})
	if err != nil || string(res.Resource) != "null" {
		t.Fatalf("confirmed = %+v %v", res, err)
	}
	if _, ok := h.pol.m[name]; ok {
		t.Fatal("not deleted")
	}
	hist := h.history(t)
	if hist[0].Action != ActionDelete || string(hist[0].After) != "null" || string(hist[0].Before) == "null" {
		t.Fatalf("history = %+v", hist[0])
	}
}

func TestChannelWarnings(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	h.ch.list[0].VerificationStatus = "VERIFICATION_STATUS_UNSPECIFIED"
	pv, _ := h.e.Preview(ctx, engPolicyCreateReq(`{}`))
	if !engHasWarn(pv, "not verified") {
		t.Errorf("want unverified warning: %v", pv.Warnings)
	}
	h.rt.chans = nil
	pv, _ = h.e.Preview(ctx, engPolicyCreateReq(`{"severity":"ERROR"}`))
	if !engHasWarn(pv, "No notification channel resolved for severity ERROR in proj-a: the policy would notify nobody.") {
		t.Errorf("want nobody warning: %v", pv.Warnings)
	}
	h.rt.chans = []string{engChanOK}
	h.rt.warns = []string{"router says hi"}
	pv, _ = h.e.Preview(ctx, engPolicyCreateReq(`{"channel_override":["projects/proj-a/notificationChannels/999"]}`))
	if !engHasWarn(pv, "999") || !engHasWarn(pv, "not found") {
		t.Errorf("want missing-channel warning: %v", pv.Warnings)
	}
	h.rt.warns = []string{"router says hi"}
	pv, _ = h.e.Preview(ctx, engPolicyCreateReq(`{}`))
	if !engHasWarn(pv, "router says hi") {
		t.Errorf("router warnings must surface: %v", pv.Warnings)
	}
	req := engPolicyCreateReq(`{}`)
	req.TemplateID = TplVMMemoryHigh
	pv, _ = h.e.Preview(ctx, req)
	if !engHasWarn(pv, "Ops Agent") {
		t.Errorf("want Ops Agent warning: %v", pv.Warnings)
	}
}

func TestInvalidInputs(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	bad := []PreviewRequest{
		{Kind: "nope", Action: ActionCreate},
		engPolicyCreateReq(`{"threshold":500}`),
		engPolicyCreateReq(`{"bogus":1}`),
		{Kind: TargetPolicy, Action: ActionCreate, Project: "proj-a", TemplateID: "nope"},
		{Kind: TargetPolicy, Action: ActionEnd},
		{Kind: TargetPolicy, Action: ActionDelete},
		{Kind: TargetChannel, Action: ActionEnable},
	}
	for i, r := range bad {
		if _, err := h.e.Preview(ctx, r); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("case %d: err = %v", i, err)
		}
	}
	if _, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionDelete, Target: "projects/proj-a/alertPolicies/404"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing target = %v", err)
	}
}

func TestNotConfiguredPropagatesAndKeepsToken(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	h.pol.failErr = ErrNotConfigured
	pv, _ := h.e.Preview(ctx, engPolicyCreateReq(`{}`))
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("apply = %v", err)
	}
	if len(h.history(t)) != 0 {
		t.Fatal("no history on failure")
	}
	h.pol.failErr = nil
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); err != nil {
		t.Fatalf("retry after configuring = %v", err)
	}
}

func TestUpstreamFailureNoHistory(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	h.pol.failErr = fmt.Errorf("%w: boom", ErrPermissionDenied)
	pv, _ := h.e.Preview(ctx, engPolicyCreateReq(`{}`))
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("apply = %v", err)
	}
	if len(h.history(t)) != 0 {
		t.Fatal("history written on failure")
	}
}

func TestHistoryRedactsSecrets(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	name := engSeedUnmanaged(h)
	desired := `{"displayName":"Zero-Backend CPU Spike","documentation":{"webhook_url":"https://hooks.example.com/SECRET123","auth_token":"tok-abc"},"conditions":[{"displayName":"c"}]}`
	pv, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetPolicy, Action: ActionUpdate, Target: name, Desired: json.RawMessage(desired)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); err != nil {
		t.Fatal(err)
	}
	hist := h.history(t)
	all := string(hist[0].Before) + string(hist[0].After)
	if strings.Contains(all, "SECRET123") || strings.Contains(all, "tok-abc") || !strings.Contains(all, "[redacted]") {
		t.Fatalf("history not redacted: %s", all)
	}
}

// ---- other kinds -----------------------------------------------------------

func TestChannelCreateKeepsSecretsOut(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	params := `{"project":"proj-a","type":"webhook_tokenauth","display_name":"hook","labels":{"url":"https://hooks.example.com/SECRET123"},"sensitive_labels":{"auth_token":"tok-abc"}}`
	pv, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetChannel, Action: ActionCreate, Params: json.RawMessage(params)})
	if err != nil {
		t.Fatal(err)
	}
	blob := string(pv.Desired) + pv.Gcloud + pv.Diff.Unified + fmt.Sprint(pv.Warnings)
	for _, c := range pv.Diff.Changes {
		blob += string(c.After)
	}
	if strings.Contains(blob, "SECRET123") || strings.Contains(blob, "tok-abc") || !strings.Contains(string(pv.Desired), "[redacted]") {
		t.Fatalf("preview leaks secrets: %s", blob)
	}
	res, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.ch.created) != 1 || h.ch.created[0].SensitiveLabels["url"] != "https://hooks.example.com/SECRET123" || h.ch.created[0].SensitiveLabels["auth_token"] != "tok-abc" || len(h.ch.created[0].Labels) != 0 {
		t.Fatalf("secrets must reach GCP via sensitive labels: %+v", h.ch.created)
	}
	all := string(res.Resource)
	for _, he := range h.history(t) {
		all += string(he.Before) + string(he.After) + he.Target
	}
	if strings.Contains(all, "SECRET123") || strings.Contains(all, "tok-abc") {
		t.Fatalf("secret persisted: %s", all)
	}
}

func TestChannelRulesAndDelete(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	if _, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetChannel, Action: ActionCreate, Params: json.RawMessage(`{"project":"proj-a","type":"slack","display_name":"s"}`)}); !errors.Is(err, ErrInvalidParams) || !strings.Contains(err.Error(), "console OAuth") {
		t.Errorf("slack = %v", err)
	}
	if _, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetChannel, Action: ActionCreate, Params: json.RawMessage(`{"project":"proj-a","type":"email","display_name":"e"}`)}); !errors.Is(err, ErrInvalidParams) {
		t.Errorf("missing required label = %v", err)
	}
	pol := engSeedManaged(h)
	_ = pol
	pv, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetChannel, Action: ActionDelete, Target: engChanOK})
	if err != nil || !pv.RequiresConfirm || !engHasWarn(pv, "ERROR routing") || !engHasWarn(pv, "1 alert policy") {
		t.Fatalf("delete preview = %+v %v", pv, err)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken, ConfirmDelete: true}); err != nil || len(h.ch.deleted) != 1 {
		t.Fatalf("delete = %v", err)
	}
}

func TestUptimeLifecycle(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	params := `{"project":"proj-a","display_name":"api health","host":"api.example.com","path":"/health","use_ssl":true,"period_seconds":60,"timeout_seconds":10}`
	pv, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetUptimeCheck, Action: ActionCreate, Params: json.RawMessage(params)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pv.Desired), `"period":"60s"`) || !strings.Contains(string(pv.Desired), `"managed-by":"cloud0"`) || !strings.Contains(pv.Gcloud, "gcloud monitoring uptime create") {
		t.Fatalf("preview = %s / %s", pv.Desired, pv.Gcloud)
	}
	res, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken})
	if err != nil || res.Target != "projects/proj-a/uptimeCheckConfigs/new1" {
		t.Fatalf("create = %+v %v", res, err)
	}
	pv, err = h.e.Preview(ctx, PreviewRequest{Kind: TargetUptimeCheck, Action: ActionUpdate, Target: res.Target, Params: json.RawMessage(`{"display_name":"api health 2","host":"api.example.com"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); err != nil || h.up.m[res.Target].DisplayName != "api health 2" {
		t.Fatalf("update: %v", err)
	}
	pv, _ = h.e.Preview(ctx, PreviewRequest{Kind: TargetUptimeCheck, Action: ActionDelete, Target: res.Target})
	if !pv.RequiresConfirm {
		t.Fatal("delete needs confirm")
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("no confirm = %v", err)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken, ConfirmDelete: true}); err != nil || len(h.up.m) != 0 {
		t.Fatalf("delete: %v", err)
	}
	if len(h.history(t)) != 3 {
		t.Fatalf("history = %d", len(h.history(t)))
	}
	for _, bad := range []string{
		`{"project":"proj-a","display_name":"x","host":"https://api.example.com"}`,
		`{"project":"proj-a","display_name":"x","host":"a.example.com","period_seconds":120}`,
		`{"project":"proj-a","display_name":"x","host":"a.example.com","timeout_seconds":99}`,
		`{"project":"proj-a","host":"a.example.com"}`,
	} {
		if _, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetUptimeCheck, Action: ActionCreate, Params: json.RawMessage(bad)}); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestBudgetLifecycle(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	h.bu.m["billingAccounts/B/budgets/old"] = BudgetView{Name: "billingAccounts/B/budgets/old", DisplayName: "Existing", RawJSON: json.RawMessage(`{"displayName":"Existing","amount":5000}`)}
	pv, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetBudget, Action: ActionCreate, Project: "proj-a", Params: json.RawMessage(`{"display_name":"cloud0-account-monthly","amount":6480,"projects":["proj-a"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pv.Desired), engChanOK) || strings.Count(string(pv.Desired), "percent") != 4 || !strings.Contains(pv.Gcloud, "gcloud billing budgets create") || !strings.Contains(pv.Gcloud, "forecasted-spend") {
		t.Fatalf("preview = %s / %s", pv.Desired, pv.Gcloud)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); err != nil || len(h.bu.created) != 1 || h.bu.chans[0][0] != engChanOK {
		t.Fatalf("create: %v %+v", err, h.bu.created)
	}
	// unprefixed create refused, zero amount refused
	for _, bad := range []string{`{"display_name":"mine","amount":10}`, `{"display_name":"cloud0-x","amount":0}`} {
		if _, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetBudget, Action: ActionCreate, Params: json.RawMessage(bad)}); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	// explicit update of an unmanaged budget works with a warning
	pv, err = h.e.Preview(ctx, PreviewRequest{Kind: TargetBudget, Action: ActionUpdate, Target: "billingAccounts/B/budgets/old", Params: json.RawMessage(`{"display_name":"Existing","amount":7000}`)})
	if err != nil || !engHasWarn(pv, "unmanaged") || len(pv.Diff.Changes) == 0 {
		t.Fatalf("update = %+v %v", pv, err)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); err != nil {
		t.Fatal(err)
	}
	pv, _ = h.e.Preview(ctx, PreviewRequest{Kind: TargetBudget, Action: ActionDelete, Target: "billingAccounts/B/budgets/new"})
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken, ConfirmDelete: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.bu.m["billingAccounts/B/budgets/new"]; ok {
		t.Fatal("budget not deleted")
	}
	h.rt.chans = nil
	pv, _ = h.e.Preview(ctx, PreviewRequest{Kind: TargetBudget, Action: ActionCreate, Params: json.RawMessage(`{"display_name":"cloud0-y","amount":1}`)})
	if !engHasWarn(pv, "No monitoring notification channels") {
		t.Errorf("warnings = %v", pv.Warnings)
	}
}

func TestSnoozeCreateAndEnd(t *testing.T) {
	h := newEngHarness(t)
	ctx := context.Background()
	pol := engSeedManaged(h)
	params := fmt.Sprintf(`{"project":"proj-a","display_name":"deploy","policy_names":[%q],"duration_seconds":3600}`, pol)
	pv, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetSnooze, Action: ActionCreate, Params: json.RawMessage(params)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pv.Desired), "2026-10-27T10:00:00Z") || !strings.Contains(pv.Gcloud, "snoozes create") {
		t.Fatalf("preview = %s / %s", pv.Desired, pv.Gcloud)
	}
	res, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken})
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(30 * time.Minute)
	pv, err = h.e.Preview(ctx, PreviewRequest{Kind: TargetSnooze, Action: ActionEnd, Target: res.Target})
	if err != nil || len(pv.Diff.Changes) != 1 || pv.Diff.Changes[0].Path != "interval.endTime" {
		t.Fatalf("end preview = %+v %v", pv, err)
	}
	if _, err := h.e.Apply(ctx, ApplyRequest{PreviewToken: pv.PreviewToken}); err != nil || !h.sn.m[res.Target].EndTime.Equal(h.now) {
		t.Fatalf("end: %v", err)
	}
	// ended snooze cannot be ended again
	if _, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetSnooze, Action: ActionEnd, Target: res.Target}); !errors.Is(err, ErrInvalidParams) {
		t.Errorf("re-end = %v", err)
	}
	for _, bad := range []string{
		`{"project":"proj-a","display_name":"d","policy_names":[],"duration_seconds":3600}`,
		fmt.Sprintf(`{"project":"proj-a","display_name":"d","policy_names":[%q],"duration_seconds":10}`, pol),
		`{"project":"proj-a","display_name":"d","policy_names":["projects/other/alertPolicies/1"],"duration_seconds":3600}`,
	} {
		if _, err := h.e.Preview(ctx, PreviewRequest{Kind: TargetSnooze, Action: ActionCreate, Params: json.RawMessage(bad)}); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestNoStoreStillApplies(t *testing.T) {
	h := newEngHarness(t)
	e := NewEngine(EngineDeps{Builder: NewBuilder(), Policies: h.pol, Channels: h.ch, Uptime: h.up, Snoozes: h.sn, Budgets: h.bu, Router: h.rt, Now: func() time.Time { return h.now }})
	pv, _ := e.Preview(context.Background(), engPolicyCreateReq(`{}`))
	res, err := e.Apply(context.Background(), ApplyRequest{PreviewToken: pv.PreviewToken})
	if err != nil || len(res.Warnings) == 0 || res.ChangeHistoryID != 0 {
		t.Fatalf("res = %+v %v", res, err)
	}
}
