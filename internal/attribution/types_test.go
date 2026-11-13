package attribution

import (
	"context"
	"encoding/json"
	"errors"
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
		t.Fatalf("round trip mismatch:\n in  %+v\n out %+v\n json %s", in, out, b)
	}
}

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

var now = time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)

func TestRuleJSONShapeMatchesSpec(t *testing.T) {
	const spec = `{"id":1,"priority":100,"match":{"field":"label","op":"equals","key":"app","value":"example-alias"},"action":{"product":"example-product"},"enabled":true,"source":"seed","note":null}`
	var r Rule
	if err := json.Unmarshal([]byte(spec), &r); err != nil {
		t.Fatal(err)
	}
	if r.ID == nil || *r.ID != 1 || r.Match.Key != "app" || r.Action.Product != "example-product" || r.Action.Shared || r.Note != nil {
		t.Fatalf("decoded %+v", r)
	}
	b, _ := json.Marshal(r)
	if string(b) != spec {
		t.Fatalf("re-encoded\n got  %s\n want %s", b, spec)
	}
	// A new/proposed rule marshals id as null and omits empty match.key / action fields.
	b, _ = json.Marshal(Rule{Priority: 900, Match: Match{Field: FieldService, Op: OpEquals, Value: "VM Manager"}, Action: Action{Shared: true}, Enabled: true, Source: SourceUser})
	if !strings.Contains(string(b), `"id":null`) || strings.Contains(string(b), `"key"`) || strings.Contains(string(b), `"product"`) || !strings.Contains(string(b), `"shared":true`) {
		t.Fatalf("new rule json: %s", b)
	}
}

func TestJSONRoundTrips(t *testing.T) {
	rule := Rule{ID: i64(2), Priority: 310, Match: Match{Field: FieldResourceName, Op: OpContains, Value: "example-product"}, Action: Action{Product: "example-product", Environment: "prod"}, Enabled: true, Source: SourceSeed, Note: str("7 resources")}
	cov := Coverage{TotalNet: 100, AttributedNet: 90, SharedNet: 4, UnattributedNet: 6, AttributedPct: 90, SharedPct: 4, UnattributedPct: 6}
	snap := &SnapshotRef{ID: 13, FetchedAt: now, StatusAtFetch: "settling", RunID: 8, RowCount: 2345}
	ev := Evaluation{Product: "example-product", Environment: "prod", RuleID: i64(2), EnvironmentRuleID: i64(0), Evidence: []string{`label env=prod`}}
	cell := BreakdownCell{Net: 1, Gross: 2, Credits: -1}
	roundTrip(t, rule)
	roundTrip(t, RulesResponse{Rules: []Rule{rule}, Allocate: AllocateNone, Aliases: map[string]string{"a": "b"}, Proposer: ProposerInfo{Model: str("m")}, UpdatedAt: &now})
	allocate := AllocateProportional
	roundTrip(t, RulesUpdate{Rules: []Rule{rule}, Allocate: &allocate, Aliases: map[string]string{"a": "b"}})
	roundTrip(t, SeedPreview{Week: "2026-W43", Snapshot: snap, LabelsCaptured: true, Proposed: []Rule{rule}, Counts: SeedCounts{Rules: 1, Products: 1}, Coverage: cov, CoverageBefore: cov, Products: []ProductSummary{{Product: "p", Net: 1, Resources: 2}}, Warnings: []string{"w"}})
	roundTrip(t, SeedApplied{Applied: true, Rules: []Rule{rule}, Counts: SeedCounts{Rules: 1}, Coverage: cov, Warnings: []string{}})
	roundTrip(t, UnattributedResponse{Week: "2026-W43", Scope: "all", Snapshot: snap, Items: []UnattributedItem{{
		Kind: "resource", Key: "example-orphan-vm", ProjectID: "proj-a", Service: "Compute Engine", ResourceName: str("example-orphan-vm"),
		Net: 3, Reason: ReasonNoMatchingRule, Suggested: &Suggested{Product: "example-orphan", Confidence: ConfidenceLow, Basis: "name token"}, Evidence: []string{"x"}}}})
	roundTrip(t, ExplainResponse{Week: "2026-W43", Resource: "r", Matches: 1, Results: []ExplainResult{{ProjectID: "p", Service: "s", Labels: map[string]string{"env": "prod"}, Net: 1, Evaluation: ev,
		Trail: []TrailStep{{RuleID: 0, Builtin: true, Description: "label env", Matched: true, Applied: str("environment"), Reason: "label env=prod"}, {RuleID: 2, Priority: func() *int { v := 310; return &v }(), Description: "d", Reason: "r"}}}}})
	roundTrip(t, Breakdown{Dimension: DimensionProduct, Week: "2026-W43", Scope: "all", Allocate: AllocateEven, GeneratedAt: now, Snapshot: snap, LabelsCaptured: true,
		Columns: []Column{{Key: "cloud_run", Label: "Cloud Run", Kind: "service", Services: []string{"Cloud Run"}}, {Key: ColumnTotal, Label: "Total GCP", Kind: "total"}},
		Groups:  []Group{{Key: "p", Label: "p", Kind: KindProduct, Cells: map[string]BreakdownCell{"total": cell}, DirectNet: 1, Net: 1, RuleIDs: []int64{2, 0}}},
		Total:   BreakdownTotal{Cells: map[string]BreakdownCell{"total": cell}}, Coverage: cov, Reconciliation: Reconciliation{CostTableTotal: 1, BreakdownTotal: 1, OK: true}, Notes: []string{}})
	roundTrip(t, LabelSuggestionsResponse{Week: "2026-W43", Scope: "all", Snapshot: snap, Count: 1, Items: []LabelSuggestion{{ProjectID: "p", ResourceName: "example-vm", ResourceGlobalName: str("//g"), ResourceType: TypeComputeInstance, RegionOrZone: str("zone-a"), Net: 1,
		Labels: map[string]string{"product": "p"}, Missing: []string{"product"}, Basis: LabelBasis{ProductRuleID: i64(2)}, Command: str("gcloud …"), Risk: "none"}}})
	roundTrip(t, TargetsResponse{Month: "2026-10", GeneratedAt: now, CoveredUntil: str("2026-10-25"), ShareWeeks: []string{"2026-W41"},
		Account:  TargetRow{Product: "*", Target: f64(25000), MonthFrom: str("2026-09"), Net: 1, Projected: 2, Pct: f64(4), Remaining: f64(3), Status: StatusOnTrack},
		Products: []TargetRow{{Product: "p", Status: StatusNoTarget, SharePct: f64(22.5)}}, Shared: BucketProgress{Net: 1}, History: []TargetHistory{{Product: "p", MonthFrom: "2026-10", Amount: f64(6000), CreatedAt: now}}})
	roundTrip(t, TargetsUpdate{MonthFrom: "2026-11", Targets: []TargetEntry{{Product: "p", Amount: f64(6000)}, {Product: "q"}}})
	roundTrip(t, RightsizingResponse{Week: "2026-W43", Scope: "all", Currency: "INR", GeneratedAt: now, MonthlyFactor: MonthlyFactor, Thresholds: DefaultRightsizingThresholds(),
		Sources: []SourceStatus{{Source: "monitoring_vm", Status: "ok"}, {Source: "compute_inventory", Status: "failed", ErrorCode: str("permission_denied"), Message: str("m"), FixHint: str("h")}},
		Candidates: []Candidate{{ID: "low_cpu_vm:p:vm", Kind: KindLowCPUVM, ProjectID: "p", ResourceName: "vm", ResourceType: TypeComputeInstance, Location: str("zone-a"), Confidence: ConfidenceMedium,
			Evidence:    Evidence{WindowStart: "2026-10-05", WindowEnd: "2026-10-25", Points: []EvidencePoint{{Week: "2026-W41", Value: f64(6.2), Unit: "%"}, {Week: "2026-W42", Unit: "%"}}, Notes: []string{"n"}},
			NetLastWeek: f64(410), EstimatedMonthlySaving: f64(891.5), SavingBasis: "b", Action: "a"}}, Totals: RightsizingTotals{Count: 1, EstimatedMonthlySaving: 891.5}})
	roundTrip(t, ProposeResponse{Week: "2026-W43", Model: "m", LatencyMS: 1, InputTokens: 2, OutputTokens: 3, Proposals: []Proposal{{Rule: rule, Confidence: ConfidenceMedium, Rationale: "r", Covers: 3}}, CoverageIfApplied: cov})
}

func TestNilCollectionsMarshalAsSpecNullsOnlyWhereDocumented(t *testing.T) {
	b, _ := json.Marshal(Breakdown{})
	// Snapshot null is documented; the rest are initialised by implementations.
	if !strings.Contains(string(b), `"snapshot":null`) {
		t.Fatalf("snapshot must be null: %s", b)
	}
	b, _ = json.Marshal(Candidate{})
	for _, k := range []string{`"location":null`, `"net_last_week":null`, `"estimated_monthly_saving":null`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("candidate lacks %s: %s", k, b)
		}
	}
}

func TestTargetStatusThresholds(t *testing.T) {
	cases := []struct {
		projected float64
		target    *float64
		want      string
	}{
		{100, f64(100), StatusOnTrack},
		{100.01, f64(100), StatusAtRisk},
		{110, f64(100), StatusAtRisk},
		{110.5, f64(100), StatusOver},
		{50, nil, StatusNoTarget},
		{0, f64(0), StatusOnTrack},
	}
	for _, c := range cases {
		if got := TargetStatus(c.projected, c.target); got != c.want {
			t.Errorf("TargetStatus(%v, %v) = %s, want %s", c.projected, c.target, got, c.want)
		}
	}
}

func TestNoopProposerIsDisabledAndMakesNoCall(t *testing.T) {
	var p Proposer = NoopProposer{}
	if p.Enabled() || p.Model() != "" {
		t.Fatal("noop must be disabled")
	}
	out, err := p.Propose(context.Background(), ProposeInput{MaxRules: 5})
	if !errors.Is(err, ErrProposerNotConfigured) || len(out.Proposals) != 0 {
		t.Fatalf("Propose = %+v, %v", out, err)
	}
}

func TestAllocateModeValid(t *testing.T) {
	for _, m := range []AllocateMode{AllocateNone, AllocateEven, AllocateProportional} {
		if !m.Valid() {
			t.Errorf("%q should be valid", m)
		}
	}
	for _, m := range []AllocateMode{"", "equal", "NONE"} {
		if m.Valid() {
			t.Errorf("%q should be invalid", m)
		}
	}
}

func TestConstantsMatchSpec(t *testing.T) {
	if MonthlyFactor < 4.3482 || MonthlyFactor > 4.3483 {
		t.Errorf("MonthlyFactor = %v", MonthlyFactor)
	}
	if DefaultWeeks != 3 || DefaultCPUPct != 15 || AtRiskFactor != 1.10 || ReconcileTolerance != 0.005 {
		t.Error("defaults drifted from spec/api.md")
	}
	if TierLabelMax >= TierProjectMin || TierProjectMax >= TierNameMin || TierNameMax >= TierSKUServiceMin || TierSKUServiceMax >= TierSharedMin {
		t.Error("seeder tiers must be disjoint and ascending")
	}
}
