package alerting

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func roundTrip[T any](t *testing.T, in T) {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in  %+v\n out %+v", in, out)
	}
}

func TestJSONRoundTrips(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	thr, dur, on := 80.0, 300, true
	raw := json.RawMessage(`{"displayName":"x"}`)
	roundTrip(t, TemplateParams{Threshold: &thr, DurationSeconds: &dur, Enabled: &on, Instance: "warning"})
	roundTrip(t, Template{ID: TplVMCPUHigh, Kind: KindThreshold, Severity: SevWarning, Defaults: TemplateParams{Threshold: &thr},
		ParamSpecs: []ParamSpec{{Key: "threshold", Type: "number"}}, Installed: []string{}, CoveredResources: []CoveredResource{{Project: "proj-a", Name: "vm-a", Covered: true}}})
	roundTrip(t, PolicyView{Name: "projects/proj-a/alertPolicies/1", Managed: true, TemplateID: TplVMCPUHigh, UpdatedAt: &now,
		Conditions: []ConditionSummary{{Threshold: &thr, DurationSeconds: &dur}}, Channels: []ChannelRef{{Name: "c"}}, RawJSON: raw})
	roundTrip(t, Preview{PreviewToken: "pv_1", ExpiresAt: now, Current: raw, Desired: raw, Diff: Diff{Changes: []DiffChange{{Path: "a", Op: "change", Before: raw, After: raw}}}})
	roundTrip(t, ApplyResult{OK: true, Resource: raw, AppliedAt: now, ChangeHistoryID: 3})
	roundTrip(t, Incident{ID: "i1", OpenedAt: now, ClosedAt: &now})
	roundTrip(t, HistoryRow{Opened: 2, FirstOpenedAt: now, LastOpenedAt: now, Resources: []string{"vm-a"}})
	roundTrip(t, CoverageRow{Signals: []CoverageSignal{{Signal: "memory", Reason: "Ops Agent not installed"}}})
	roundTrip(t, UptimeCheck{Name: "n", Port: 443, UseSSL: true, RawJSON: raw})
	roundTrip(t, Channel{Name: "c", VerificationStatus: "UNVERIFIED"})
	amt := 5000.0
	roundTrip(t, BudgetView{Name: "b", Amount: &amt, Thresholds: []BudgetThreshold{{Percent: 50, Basis: "current_spend"}}, RawJSON: raw})
	roundTrip(t, BudgetDraft{DisplayName: "cloud0-account", Amount: 6000})
	roundTrip(t, Suggestion{Scope: "account", Amount: 6000, Basis: SuggestionBasis{Multiplier: 1.2}})
	roundTrip(t, ChangeRecord{ID: 1, At: now, Before: raw, After: raw})
	roundTrip(t, PreviewRequest{Kind: TargetPolicy, Action: ActionCreate, TemplateID: TplVMCPUHigh, Params: raw})
}

func TestChannelDraftSensitiveOmittedWhenEmpty(t *testing.T) {
	b, _ := json.Marshal(ChannelDraft{Type: "slack"})
	if strings.Contains(string(b), "sensitive_labels") {
		t.Fatalf("sensitive_labels must be omitted when empty: %s", b)
	}
}

func TestCodeFor(t *testing.T) {
	cases := map[error]struct {
		s int
		c string
	}{
		ErrNotConfigured: {503, "alerts_not_configured"}, ErrPreviewStale: {409, "preview_stale"},
		ErrInvalidParams: {400, "invalid_params"}, ErrPermissionDenied: {403, "permission_denied"},
		ErrNotFound: {404, "not_found"}, ErrConfirmRequired: {400, "confirm_required"},
		errors.New("x"): {http.StatusInternalServerError, "internal_error"},
	}
	for e, w := range cases {
		s, c := CodeFor(fmt.Errorf("wrap: %w", e))
		if s != w.s || c != w.c {
			t.Errorf("%v: got %d %s", e, s, c)
		}
	}
}
