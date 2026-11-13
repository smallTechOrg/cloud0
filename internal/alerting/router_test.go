package alerting

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallTechOrg/cloud0/internal/store"
)

type fakeChannels struct {
	byProject map[string][]Channel
	err       error
}

func (f *fakeChannels) List(_ context.Context, p string) ([]Channel, error) {
	return f.byProject[p], f.err
}
func (f *fakeChannels) Descriptors(context.Context, string) ([]ChannelDescriptor, error) {
	return nil, nil
}
func (f *fakeChannels) Create(context.Context, ChannelDraft) (Channel, error) { return Channel{}, nil }
func (f *fakeChannels) Delete(context.Context, string) error                  { return nil }

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	chA1 = "projects/proj-a/notificationChannels/1"
	chA2 = "projects/proj-a/notificationChannels/2"
	chB1 = "projects/proj-b/notificationChannels/9"
)

func fixtureChannels() *fakeChannels {
	return &fakeChannels{byProject: map[string][]Channel{
		"proj-a": {
			{Name: chA1, Project: "proj-a", Type: "email", DisplayName: "Alert", VerificationStatus: "VERIFICATION_STATUS_UNSPECIFIED"},
			{Name: chA2, Project: "proj-a", Type: "slack", DisplayName: "Ops", VerificationStatus: "VERIFIED"},
		},
		"proj-b": {{Name: chB1, Project: "proj-b", Type: "email", DisplayName: "B", VerificationStatus: "VERIFIED"}},
	}}
}

func TestRouterDefaults(t *testing.T) {
	ctx := context.Background()
	r := NewRouter(newTestStore(t), fixtureChannels(), []string{"proj-a", "proj-b"})
	got, err := r.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range Severities {
		if len(got[s]) != 1 || got[s][0] != chA1 {
			t.Fatalf("%s default = %v", s, got[s])
		}
	}
	// two "Alert" email channels -> no default
	fc := fixtureChannels()
	fc.byProject["proj-a"] = append(fc.byProject["proj-a"], Channel{Name: chA2 + "x", Type: "email", DisplayName: "Alert"})
	got, _ = NewRouter(newTestStore(t), fc, []string{"proj-a"}).Get(ctx)
	if len(got[SevError]) != 0 {
		t.Fatalf("ambiguous default should be empty: %v", got)
	}
}

func TestRouterPutGetPersistsAndAudits(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	r := NewRouter(st, fixtureChannels(), []string{"proj-a", "proj-b"})
	in := map[string][]string{SevCritical: {chA1, chA2}, SevError: {chA1}, SevWarning: {chB1}}
	if err := r.Put(ctx, in); err != nil {
		t.Fatal(err)
	}
	// survives a new router on the same store
	got, err := NewRouter(st, fixtureChannels(), []string{"proj-a", "proj-b"}).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got[SevCritical], ",") != chA1+","+chA2 || got[SevWarning][0] != chB1 {
		t.Fatalf("got %v", got)
	}
	ch, err := store.ListChanges(ctx, st.DB, store.ChangeFilter{Kind: TargetRouting}, 10)
	if err != nil || len(ch) != 1 || ch[0].Target != "alerts.routing" {
		t.Fatalf("history = %v %v", ch, err)
	}
}

func TestRouterPutValidation(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	r := NewRouter(st, fixtureChannels(), []string{"proj-a"})
	ok := func() map[string][]string {
		return map[string][]string{SevCritical: {chA1}, SevError: {chA1}, SevWarning: {chA1}}
	}
	cases := map[string]func(m map[string][]string){
		"unknown severity": func(m map[string][]string) { m["PANIC"] = []string{chA1} },
		"unknown channel":  func(m map[string][]string) { m[SevError] = []string{"projects/proj-a/notificationChannels/404"} },
		"other project":    func(m map[string][]string) { m[SevError] = []string{chB1} },
		"duplicate":        func(m map[string][]string) { m[SevError] = []string{chA1, chA1} },
		"empty":            func(m map[string][]string) { m[SevWarning] = nil },
		"missing":          func(m map[string][]string) { delete(m, SevWarning) },
	}
	for name, mut := range cases {
		m := ok()
		mut(m)
		if err := r.Put(ctx, m); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// nothing saved
	kv, _ := store.GetSettingsWithPrefix(ctx, st.DB, "alerts.routing.")
	if len(kv) != 0 {
		t.Fatalf("saved despite errors: %v", kv)
	}
	if err := r.Put(ctx, ok()); err != nil {
		t.Fatal(err)
	}
}

func TestRouterResolve(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	r := NewRouter(st, fixtureChannels(), []string{"proj-a", "proj-b"})
	if err := r.Put(ctx, map[string][]string{SevCritical: {chA1, chB1}, SevError: {chA2}, SevWarning: {chA2}}); err != nil {
		t.Fatal(err)
	}
	// cross-project skip + unverified warning
	chans, warns, err := r.Resolve(ctx, "proj-a", SevCritical, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(chans) != 1 || chans[0] != chA1 {
		t.Fatalf("chans = %v", chans)
	}
	joined := strings.Join(warns, "|")
	if !strings.Contains(joined, "channel "+chA1+" is unverified — Google sends a verification email") || !strings.Contains(joined, chB1) {
		t.Fatalf("warns = %v", warns)
	}
	// verified slack: no warning
	chans, warns, _ = r.Resolve(ctx, "proj-a", SevError, nil)
	if len(chans) != 1 || len(warns) != 0 {
		t.Fatalf("%v %v", chans, warns)
	}
	// override wins
	chans, _, _ = r.Resolve(ctx, "proj-b", SevError, []string{chB1})
	if len(chans) != 1 || chans[0] != chB1 {
		t.Fatalf("override: %v", chans)
	}
	// project with nothing routed
	chans, warns, _ = r.Resolve(ctx, "proj-b", SevError, nil)
	if len(chans) != 0 || len(warns) != 1 {
		t.Fatalf("%v %v", chans, warns)
	}
	if _, _, err := r.Resolve(ctx, "proj-a", "BOGUS", nil); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("err = %v", err)
	}
	// upstream failure surfaces
	bad := &fakeChannels{err: errors.New("boom")}
	if _, _, err := NewRouter(st, bad, []string{"proj-a"}).Resolve(ctx, "proj-a", SevError, nil); err == nil {
		t.Fatal("want error")
	}
}
