// Package attribution holds the FROZEN contract of Phase 3 "Attribution +
// Insights" (spec/api.md → "Attribution additions"): the JSON types shared by
// the HTTP layer, the store and the pure engines, plus the interfaces the
// parallel workers implement (rule Engine, Seeder, Proposer, Allocator,
// LabelSuggester, rightsizing Detector). It has no I/O, no clock and no
// dependency on store/httpapi: callers convert store rows into Row/CostRow.
//
// Conventions: JSON field names are the spec's snake_case; fields the spec
// shows as `null` are pointers without omitempty; money is float64 in
// config.currency; every list that the spec shows as `[]` must be marshalled
// as `[]` (implementations initialise slices, never leave them nil).
package attribution

import (
	"context"
	"errors"
	"time"
)

// ---- Constants --------------------------------------------------------------

// Match fields (Match.Field).
const (
	FieldLabel        = "label"
	FieldProject      = "project"
	FieldResourceName = "resource_name"
	FieldSKU          = "sku"
	FieldService      = "service"
)

// Match operators (Match.Op). All comparisons are case-insensitive; regex is
// RE2, compiled with (?i), unanchored.
const (
	OpEquals   = "equals"
	OpContains = "contains"
	OpPrefix   = "prefix"
	OpRegex    = "regex"
)

// Rule sources (Rule.Source).
const (
	SourceSeed     = "seed"
	SourceUser     = "user"
	SourceProposed = "proposed"
)

// Group kinds (Group.Kind).
const (
	KindProduct      = "product"
	KindEnvironment  = "environment"
	KindShared       = "shared"
	KindUnattributed = "unattributed"
)

// Breakdown dimensions (Breakdown.Dimension).
const (
	DimensionProduct     = "product"
	DimensionEnvironment = "environment"
)

// Bucket keys/labels. Products use KeyShared/KeyUnattributed; the unresolved
// environment bucket is KeyNoEnvironment (kind unattributed).
const (
	KeyShared        = "(shared)"
	KeyUnattributed  = "(unattributed)"
	KeyNoEnvironment = "(no environment)"
	// BuiltinRuleID is the rule id of the built-in label rules (`product`,
	// `env`/`environment`) that run before every stored rule.
	BuiltinRuleID int64 = 0
	// ProductLabelKey / EnvLabelKeys are the user-label keys cloud0 itself
	// suggests and the engine honours without a rule. A product label value of
	// SharedLabelValue means "shared".
	ProductLabelKey  = "product"
	SharedLabelValue = "shared"
	// ColumnTotal is the total column key in Breakdown.Columns.
	ColumnTotal = "total"
)

// EnvLabelKeys are the built-in environment label keys, in priority order.
var EnvLabelKeys = []string{"env", "environment"}

// Seeder priority tiers (inclusive ranges) — precedence is data, not code.
const (
	TierLabelMin       = 100
	TierLabelMax       = 199
	TierProjectMin     = 200
	TierProjectMax     = 299
	TierNameMin        = 300
	TierNameMax        = 599
	TierSKUServiceMin  = 600
	TierSKUServiceMax  = 899
	TierSharedMin      = 900
	TierSharedMax      = 999
	MaxRules           = 500
	DefaultUnattrLimit = 100
)

// Allocation modes (query param `allocate`, setting attribution.allocate).
type AllocateMode string

const (
	AllocateNone         AllocateMode = "none"
	AllocateEven         AllocateMode = "even"
	AllocateProportional AllocateMode = "proportional"
)

// Valid reports whether m is one of the three modes.
func (m AllocateMode) Valid() bool {
	return m == AllocateNone || m == AllocateEven || m == AllocateProportional
}

// Unattributed reasons (UnattributedItem.Reason).
const (
	ReasonNoResourceName  = "no_resource_name"
	ReasonNoMatchingRule  = "no_matching_rule"
	ReasonEnvironmentOnly = "environment_only"
)

// Confidence levels (Suggested.Confidence, Proposal.Confidence, Candidate.Confidence).
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Resource types parsed from resource.global_name (LabelSuggestion.ResourceType).
const (
	TypeComputeInstance    = "compute_instance"
	TypeComputeDisk        = "compute_disk"
	TypeComputeAddress     = "compute_address"
	TypeCloudRunService    = "cloud_run_service"
	TypeCloudRunJob        = "cloud_run_job"
	TypeGCSBucket          = "gcs_bucket"
	TypeCloudSQLInstance   = "cloud_sql_instance"
	TypeArtifactRepository = "artifact_repository"
	TypeUnknown            = "unknown"
)

// Rightsizing candidate kinds (Candidate.Kind).
const (
	KindLowCPUVM        = "low_cpu_vm"
	KindIdleDisk        = "idle_disk"
	KindUnusedIP        = "unused_ip"
	KindStaleSnapshot   = "stale_snapshot"
	KindCloudRunMinInst = "cloud_run_min_instances"
)

// Rightsizing defaults and the weeks→month factor (365.25 / 84).
const (
	DefaultCPUPct            = 15.0
	DefaultWeeks             = 3
	DefaultStaleSnapshotDays = 30
	DefaultIdleRunMinNet     = 1.0
	MonthlyFactor            = 365.25 / 84.0
	// VMDownsizeSavingFraction is the documented assumption behind
	// low_cpu_vm savings: one machine size down halves core+RAM cost.
	VMDownsizeSavingFraction = 0.5
)

// Target statuses (TargetRow.Status) and the at-risk band.
const (
	StatusOnTrack  = "on_track"
	StatusAtRisk   = "at_risk"
	StatusOver     = "over"
	StatusNoTarget = "no_target"
	StatusNoData   = "no_data"
	// AtRiskFactor: projected <= target*AtRiskFactor is at_risk, above is over.
	AtRiskFactor = 1.10
	// AccountProduct is the `product` of the account-wide target.
	AccountProduct = "*"
)

// Settings keys (mirror internal/store; kept here for the pure packages).
const (
	SettingAllocate = "attribution.allocate"
	SettingAliases  = "attribution.seed.aliases"
)

// API error codes introduced by Phase 3 (HTTP status in the comment).
const (
	CodeInvalidRule           = "invalid_rule"            // 400
	CodeInvalidAllocate       = "invalid_allocate"        // 400
	CodeInvalidTarget         = "invalid_target"          // 400
	CodeInvalidDimension      = "invalid_dimension"       // 400
	CodeProposerNotConfigured = "proposer_not_configured" // 503
	CodeProposerFailed        = "proposer_failed"         // 502
)

// ---- Sentinel errors --------------------------------------------------------

var (
	// ErrInvalidRule wraps every rule validation failure (HTTP 400 invalid_rule).
	ErrInvalidRule = errors.New("attribution: invalid rule")
	// ErrInvalidAllocate means an unknown allocation mode (400 invalid_allocate).
	ErrInvalidAllocate = errors.New("attribution: invalid allocate mode")
	// ErrProposerNotConfigured is returned by NoopProposer and by any proposer
	// without credentials; the HTTP layer maps it to 503 proposer_not_configured
	// and makes no outbound call.
	ErrProposerNotConfigured = errors.New("attribution: rule proposer not configured")
	// ErrProposerFailed wraps provider errors and schema-invalid output (502).
	ErrProposerFailed = errors.New("attribution: rule proposer failed")
	// ErrNoSnapshot means the requested week has no snapshot (explain → 404).
	ErrNoSnapshot = errors.New("attribution: no snapshot for week")
)

// ---- Rules ------------------------------------------------------------------

// Match is what a rule tests. Key is required for (and only for) Field label.
type Match struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Key   string `json:"key,omitempty"`
	Value string `json:"value"`
}

// Action is what a matching rule decides. Shared excludes Product; at least
// one of the three must be set.
type Action struct {
	Product     string `json:"product,omitempty"`
	Environment string `json:"environment,omitempty"`
	Shared      bool   `json:"shared,omitempty"`
}

// Rule is one attribution rule. ID is nil for rules not yet stored (seed
// previews, proposals, new rules in a PUT).
type Rule struct {
	ID       *int64  `json:"id"`
	Priority int     `json:"priority"`
	Match    Match   `json:"match"`
	Action   Action  `json:"action"`
	Enabled  bool    `json:"enabled"`
	Source   string  `json:"source"`
	Note     *string `json:"note"`
}

// ProposerInfo is the `proposer` object of GET /attribution/rules.
type ProposerInfo struct {
	Enabled bool    `json:"enabled"`
	Model   *string `json:"model"`
}

// RulesResponse is the body of GET and PUT /api/v1/attribution/rules.
type RulesResponse struct {
	Rules     []Rule            `json:"rules"`
	Allocate  AllocateMode      `json:"allocate"`
	Aliases   map[string]string `json:"aliases"`
	Proposer  ProposerInfo      `json:"proposer"`
	UpdatedAt *time.Time        `json:"updated_at"`
}

// RulesUpdate is the PUT /api/v1/attribution/rules body. Nil Allocate/Aliases
// leave the stored values unchanged.
type RulesUpdate struct {
	Rules    []Rule            `json:"rules"`
	Allocate *AllocateMode     `json:"allocate,omitempty"`
	Aliases  map[string]string `json:"aliases,omitempty"`
}

// ---- Engine -----------------------------------------------------------------

// Row is the engine's input: one snapshot row's identity. Empty strings mean
// "absent" (resource_name NULL). Labels are the parsed user labels (nil when
// the snapshot predates label capture); keys are matched case-insensitively.
type Row struct {
	ProjectID          string
	Service            string
	SKU                string
	ResourceName       string
	ResourceGlobalName string
	Labels             map[string]string
}

// CostRow is a Row with its money, for seeding, breakdowns and suggestions.
type CostRow struct {
	Row
	Gross     float64
	Credits   float64
	Net       float64
	LineItems int64
}

// Evaluation is the engine's verdict for one Row (JSON = explain's
// `evaluation`). Product is "" when shared or unattributed. RuleID is the
// deciding product/shared rule (BuiltinRuleID for built-in labels, nil for no
// decision); EnvironmentRuleID likewise for the environment.
type Evaluation struct {
	Product           string   `json:"product"`
	Environment       string   `json:"environment"`
	Shared            bool     `json:"shared"`
	RuleID            *int64   `json:"rule_id"`
	EnvironmentRuleID *int64   `json:"environment_rule_id"`
	Evidence          []string `json:"evidence"`
}

// TrailStep is one entry of explain's `trail`: a rule considered in order.
// Applied is "product", "environment", "shared" or nil (matched but its
// dimension was already decided, or did not match).
type TrailStep struct {
	RuleID      int64   `json:"rule_id"`
	Builtin     bool    `json:"builtin"`
	Priority    *int    `json:"priority,omitempty"`
	Description string  `json:"description"`
	Matched     bool    `json:"matched"`
	Applied     *string `json:"applied"`
	Reason      string  `json:"reason"`
}

// Engine evaluates rows against a compiled, ordered rule set. Implementations
// are pure and safe for concurrent use. Evaluation order: built-in label rules
// first, then enabled rules by (priority, id); product/shared and environment
// resolve independently, first decision wins (spec/api.md → Semantics).
type Engine interface {
	// Evaluate returns the verdict for one row.
	Evaluate(r Row) Evaluation
	// Explain returns every enabled rule in evaluation order up to and
	// including the last deciding rule, with matched/applied flags.
	Explain(r Row) []TrailStep
	// Rules returns the compiled rules in evaluation order.
	Rules() []Rule
}

// EngineFactory compiles rules (validating each: wraps ErrInvalidRule with the
// 0-based index) into an Engine.
type EngineFactory func(rules []Rule) (Engine, error)

// ---- Seeder -----------------------------------------------------------------

// SeedOptions parameterise the deterministic seeder.
type SeedOptions struct {
	// Aliases map a label value or name token to a product (e.g. "0" and
	// "alias-b" → "example-product"); seeded as label-tier / name-tier rules.
	Aliases map[string]string
	// SharedTokens are resource-name substrings whose cost is shared.
	SharedTokens []string
	// Existing are the stored rules (used for coverage_before and to avoid
	// duplicating user rules); seed rules among them are replaced.
	Existing []Rule
}

// SeedCounts is the `counts` object of seed preview/apply.
type SeedCounts struct {
	Rules             int `json:"rules"`
	Label             int `json:"label"`
	Project           int `json:"project"`
	ResourceName      int `json:"resource_name"`
	SKUService        int `json:"sku_service"`
	Products          int `json:"products"`
	Environments      int `json:"environments"`
	KeptUserRules     int `json:"kept_user_rules"`
	ReplacedSeedRules int `json:"replaced_seed_rules"`
}

// ProductSummary is one element of seed preview `products`.
type ProductSummary struct {
	Product   string  `json:"product"`
	Net       float64 `json:"net"`
	Resources int     `json:"resources"`
}

// Coverage summarises how a week's net splits across attribution outcomes.
// The three percentages sum to 100 (± 0.01); all zero when TotalNet is 0.
type Coverage struct {
	TotalNet        float64 `json:"total_net"`
	AttributedNet   float64 `json:"attributed_net"`
	SharedNet       float64 `json:"shared_net"`
	UnattributedNet float64 `json:"unattributed_net"`
	AttributedPct   float64 `json:"attributed_pct"`
	SharedPct       float64 `json:"shared_pct"`
	UnattributedPct float64 `json:"unattributed_pct"`
}

// SeedResult is what a Seeder returns; the HTTP layer adds snapshot/coverage.
type SeedResult struct {
	// Proposed is the complete seed rule set (source "seed", ID nil), already
	// ordered by (priority, text) and deterministic for equal input.
	Proposed []Rule
	Counts   SeedCounts
	Products []ProductSummary
}

// Seeder derives a rule set from the resources, labels and projects actually
// seen. It is deterministic (no clock, no randomness, no network).
type Seeder interface {
	Seed(rows []CostRow, opts SeedOptions) (SeedResult, error)
}

// SnapshotRef is the `snapshot` object shared by the Phase 3 responses
// (identical to costtable.SnapshotRef).
type SnapshotRef struct {
	ID            int64     `json:"id"`
	FetchedAt     time.Time `json:"fetched_at"`
	StatusAtFetch string    `json:"status_at_fetch"`
	RunID         int64     `json:"run_id"`
	RowCount      int       `json:"row_count"`
	Stale         bool      `json:"stale"`
}

// SeedRequest is the body of POST /attribution/seed/preview and /apply.
type SeedRequest struct {
	Week         string            `json:"week,omitempty"`
	Scope        string            `json:"scope,omitempty"`
	Aliases      map[string]string `json:"aliases,omitempty"`
	SharedTokens []string          `json:"shared_tokens,omitempty"`
}

// SeedPreview is the 200 body of POST /attribution/seed/preview.
type SeedPreview struct {
	Week           string           `json:"week"`
	Snapshot       *SnapshotRef     `json:"snapshot"`
	LabelsCaptured bool             `json:"labels_captured"`
	Proposed       []Rule           `json:"proposed"`
	Counts         SeedCounts       `json:"counts"`
	Coverage       Coverage         `json:"coverage"`
	CoverageBefore Coverage         `json:"coverage_before"`
	Products       []ProductSummary `json:"products"`
	Warnings       []string         `json:"warnings"`
}

// SeedApplied is the 200 body of POST /attribution/seed/apply.
type SeedApplied struct {
	Applied  bool       `json:"applied"`
	Rules    []Rule     `json:"rules"`
	Counts   SeedCounts `json:"counts"`
	Coverage Coverage   `json:"coverage"`
	Warnings []string   `json:"warnings"`
}

// ---- Unattributed / explain -------------------------------------------------

// Suggested is a seeder-heuristic guess for an unattributed item.
type Suggested struct {
	Product     string  `json:"product"`
	Environment *string `json:"environment"`
	Confidence  string  `json:"confidence"`
	Basis       string  `json:"basis"`
}

// UnattributedItem is one grouped entry of GET /attribution/unattributed.
type UnattributedItem struct {
	Kind               string     `json:"kind"` // "resource" | "sku"
	Key                string     `json:"key"`
	ProjectID          string     `json:"project_id"`
	Service            string     `json:"service"`
	ResourceName       *string    `json:"resource_name"`
	ResourceGlobalName *string    `json:"resource_global_name"`
	SKU                *string    `json:"sku"`
	Net                float64    `json:"net"`
	SharePct           float64    `json:"share_pct"`
	LineItems          int64      `json:"line_items"`
	Reason             string     `json:"reason"`
	Suggested          *Suggested `json:"suggested"`
	Evidence           []string   `json:"evidence"`
}

// UnattributedResponse is the body of GET /api/v1/attribution/unattributed.
type UnattributedResponse struct {
	Week            string             `json:"week"`
	Scope           string             `json:"scope"`
	Snapshot        *SnapshotRef       `json:"snapshot"`
	LabelsCaptured  bool               `json:"labels_captured"`
	TotalNet        float64            `json:"total_net"`
	UnattributedNet float64            `json:"unattributed_net"`
	UnattributedPct float64            `json:"unattributed_pct"`
	ItemCount       int                `json:"item_count"`
	Truncated       bool               `json:"truncated"`
	Items           []UnattributedItem `json:"items"`
}

// ExplainRequest is the body of POST /attribution/explain.
type ExplainRequest struct {
	Resource string `json:"resource"`
	Week     string `json:"week,omitempty"`
	Scope    string `json:"scope,omitempty"`
}

// ExplainResult is one (project, service) entry of an explain response.
type ExplainResult struct {
	ProjectID          string            `json:"project_id"`
	Service            string            `json:"service"`
	ResourceName       *string           `json:"resource_name"`
	ResourceGlobalName *string           `json:"resource_global_name"`
	Labels             map[string]string `json:"labels"`
	Net                float64           `json:"net"`
	Evaluation         Evaluation        `json:"evaluation"`
	Trail              []TrailStep       `json:"trail"`
}

// ExplainResponse is the 200 body of POST /attribution/explain.
type ExplainResponse struct {
	Week     string          `json:"week"`
	Resource string          `json:"resource"`
	Matches  int             `json:"matches"`
	Results  []ExplainResult `json:"results"`
}

// ---- Breakdowns -------------------------------------------------------------

// Column is a breakdown column (a Cost-table service/total column).
type Column struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Kind     string   `json:"kind"` // "service" | "total"
	Services []string `json:"services,omitempty"`
	Extra    bool     `json:"extra,omitempty"`
}

// BreakdownCell is a money cell without week-over-week deltas.
type BreakdownCell struct {
	Net     float64 `json:"net"`
	Gross   float64 `json:"gross"`
	Credits float64 `json:"credits"`
}

// Group is one product/environment/shared/unattributed bucket.
type Group struct {
	Key          string                   `json:"key"`
	Label        string                   `json:"label"`
	Kind         string                   `json:"kind"`
	Cells        map[string]BreakdownCell `json:"cells"`
	DirectNet    float64                  `json:"direct_net"`
	AllocatedNet float64                  `json:"allocated_net"`
	Net          float64                  `json:"net"`
	SharePct     float64                  `json:"share_pct"`
	RuleIDs      []int64                  `json:"rule_ids"`
}

// BreakdownTotal is the `total` object of a breakdown response.
type BreakdownTotal struct {
	Cells map[string]BreakdownCell `json:"cells"`
}

// Reconciliation proves Σ groups = the Cost table total (OK: |Diff| <= 0.005).
type Reconciliation struct {
	CostTableTotal float64 `json:"cost_table_total"`
	BreakdownTotal float64 `json:"breakdown_total"`
	Diff           float64 `json:"diff"`
	OK             bool    `json:"ok"`
}

// ReconcileTolerance is the allowed |diff| between breakdown and Cost table.
const ReconcileTolerance = 0.005

// BreakdownFilter echoes the active filter (both null when none).
type BreakdownFilter struct {
	Env     *string `json:"env"`
	Product *string `json:"product"`
}

// Breakdown is the body of GET /cost/by-product and /cost/by-environment.
// Snapshot is nil (and Groups empty) when the week was never fetched.
type Breakdown struct {
	Dimension      string          `json:"dimension"`
	Week           string          `json:"week"`
	WeekStart      string          `json:"week_start"`
	WeekEnd        string          `json:"week_end"`
	WeekStatus     string          `json:"week_status"`
	Scope          string          `json:"scope"`
	Allocate       AllocateMode    `json:"allocate"`
	Filter         BreakdownFilter `json:"filter"`
	GeneratedAt    time.Time       `json:"generated_at"`
	Snapshot       *SnapshotRef    `json:"snapshot"`
	LabelsCaptured bool            `json:"labels_captured"`
	Columns        []Column        `json:"columns"`
	Groups         []Group         `json:"groups"`
	Total          BreakdownTotal  `json:"total"`
	Coverage       Coverage        `json:"coverage"`
	Reconciliation Reconciliation  `json:"reconciliation"`
	Notes          []string        `json:"notes"`
}

// Allocator redistributes the shared group over the product/environment
// groups. Contract (spec/api.md → Allocation): per service column and for
// gross/credits; AllocateNone returns groups unchanged; even = equal split
// over groups with DirectNet != 0, proportional = by DirectNet share; the
// shared group keeps DirectNet, gets AllocatedNet = -DirectNet, Net 0 and zero
// cells; unattributed is never allocated; Σ Net is preserved. With no eligible
// group nothing moves and the note "No products to allocate shared cost to."
// is returned. Pure; must not mutate its input.
type Allocator interface {
	Allocate(groups []Group, mode AllocateMode) (out []Group, notes []string, err error)
}

// ---- Label suggestions ------------------------------------------------------

// LabelBasis names the non-label rules behind a suggestion.
type LabelBasis struct {
	ProductRuleID     *int64 `json:"product_rule_id"`
	EnvironmentRuleID *int64 `json:"environment_rule_id"`
}

// LabelSuggestion is one resource whose attribution came from a non-label rule.
// Command is nil for unknown resource types or when the zone/region could not
// be parsed; cloud0 never executes it.
type LabelSuggestion struct {
	ProjectID          string            `json:"project_id"`
	ResourceName       string            `json:"resource_name"`
	ResourceGlobalName *string           `json:"resource_global_name"`
	ResourceType       string            `json:"resource_type"`
	RegionOrZone       *string           `json:"region_or_zone"`
	Net                float64           `json:"net"`
	Labels             map[string]string `json:"labels"`
	Missing            []string          `json:"missing"`
	Basis              LabelBasis        `json:"basis"`
	Command            *string           `json:"command"`
	Risk               string            `json:"risk"`
}

// LabelSuggestionsResponse is the body of GET /attribution/label-suggestions.
type LabelSuggestionsResponse struct {
	Week           string            `json:"week"`
	Scope          string            `json:"scope"`
	Snapshot       *SnapshotRef      `json:"snapshot"`
	LabelsCaptured bool              `json:"labels_captured"`
	Count          int               `json:"count"`
	DoneCount      int               `json:"done_count"`
	Items          []LabelSuggestion `json:"items"`
}

// LabelSuggester builds suggestions for a snapshot's rows under an engine.
// Pure: one item per distinct (project, resource_name), sorted by Net desc
// then resource name; resources already labelled (built-in label decided both
// dimensions) are counted in the second return value, not listed.
type LabelSuggester interface {
	Suggest(rows []CostRow, eng Engine) (items []LabelSuggestion, done int)
}

// ---- Targets ----------------------------------------------------------------

// TargetRow is one product's (or the account's) progress for a month.
type TargetRow struct {
	Product   string   `json:"product"`
	Target    *float64 `json:"target"`
	MonthFrom *string  `json:"month_from"`
	Net       float64  `json:"net"`
	Projected float64  `json:"projected"`
	Pct       *float64 `json:"pct"`
	Remaining *float64 `json:"remaining"`
	Status    string   `json:"status"`
	SharePct  *float64 `json:"share_pct,omitempty"`
}

// BucketProgress is the shared / unattributed remainder of the month.
type BucketProgress struct {
	Net       float64 `json:"net"`
	Projected float64 `json:"projected"`
	SharePct  float64 `json:"share_pct"`
}

// TargetHistory is one stored `targets` row.
type TargetHistory struct {
	Product   string    `json:"product"`
	MonthFrom string    `json:"month_from"`
	Amount    *float64  `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
}

// TargetsResponse is the body of GET and PUT /api/v1/targets.
type TargetsResponse struct {
	Month        string          `json:"month"`
	MonthStart   string          `json:"month_start"`
	MonthEnd     string          `json:"month_end"`
	Currency     string          `json:"currency"`
	GeneratedAt  time.Time       `json:"generated_at"`
	MonthStatus  string          `json:"month_status"`
	CoveredUntil *string         `json:"covered_until"`
	ShareWeeks   []string        `json:"share_weeks"`
	Account      TargetRow       `json:"account"`
	Products     []TargetRow     `json:"products"`
	Shared       BucketProgress  `json:"shared"`
	Unattributed BucketProgress  `json:"unattributed"`
	History      []TargetHistory `json:"history"`
}

// TargetEntry is one element of a PUT /targets body; nil Amount clears.
type TargetEntry struct {
	Product string   `json:"product"`
	Amount  *float64 `json:"amount"`
}

// TargetsUpdate is the PUT /api/v1/targets body.
type TargetsUpdate struct {
	MonthFrom string        `json:"month_from,omitempty"`
	Targets   []TargetEntry `json:"targets"`
}

// TargetStatus classifies projected against target: on_track (<= target),
// at_risk (<= AtRiskFactor × target), over; no_target when target is nil.
func TargetStatus(projected float64, target *float64) string {
	switch {
	case target == nil:
		return StatusNoTarget
	case projected <= *target:
		return StatusOnTrack
	case projected <= *target*AtRiskFactor:
		return StatusAtRisk
	default:
		return StatusOver
	}
}

// ---- Rightsizing ------------------------------------------------------------

// RightsizingThresholds echo the effective settings.
type RightsizingThresholds struct {
	CPUPct            float64 `json:"cpu_pct"`
	Weeks             int     `json:"weeks"`
	StaleSnapshotDays int     `json:"stale_snapshot_days"`
	IdleRunMinNet     float64 `json:"idle_run_min_net"`
}

// DefaultRightsizingThresholds are used when the settings keys are absent.
func DefaultRightsizingThresholds() RightsizingThresholds {
	return RightsizingThresholds{CPUPct: DefaultCPUPct, Weeks: DefaultWeeks, StaleSnapshotDays: DefaultStaleSnapshotDays, IdleRunMinNet: DefaultIdleRunMinNet}
}

// EvidencePoint is one per-week value of a candidate's evidence. Value is nil
// for a week with no stored data (a gap).
type EvidencePoint struct {
	Week  string   `json:"week"`
	Value *float64 `json:"value"`
	Unit  string   `json:"unit"`
}

// Evidence is the window and per-week data behind a candidate.
type Evidence struct {
	WindowStart string          `json:"window_start"`
	WindowEnd   string          `json:"window_end"`
	Points      []EvidencePoint `json:"points"`
	Notes       []string        `json:"notes"`
}

// Candidate is one rightsizing hint. EstimatedMonthlySaving and NetLastWeek
// are nil when no billing row matches (SavingBasis then says so).
type Candidate struct {
	ID                     string   `json:"id"`
	Kind                   string   `json:"kind"`
	ProjectID              string   `json:"project_id"`
	ResourceName           string   `json:"resource_name"`
	ResourceType           string   `json:"resource_type"`
	Location               *string  `json:"location"`
	Confidence             string   `json:"confidence"`
	Evidence               Evidence `json:"evidence"`
	NetLastWeek            *float64 `json:"net_last_week"`
	EstimatedMonthlySaving *float64 `json:"estimated_monthly_saving"`
	SavingBasis            string   `json:"saving_basis"`
	Action                 string   `json:"action"`
}

// SourceStatus reports one data source's outcome inside a rightsizing response.
type SourceStatus struct {
	Source    string  `json:"source"`
	Status    string  `json:"status"` // "ok" | "failed" | "skipped"
	ErrorCode *string `json:"error_code"`
	Message   *string `json:"message"`
	FixHint   *string `json:"fix_hint"`
}

// RightsizingTotals sums the candidates.
type RightsizingTotals struct {
	Count                  int     `json:"count"`
	EstimatedMonthlySaving float64 `json:"estimated_monthly_saving"`
}

// RightsizingResponse is the body of GET /api/v1/rightsizing.
type RightsizingResponse struct {
	Week          string                `json:"week"`
	Scope         string                `json:"scope"`
	Currency      string                `json:"currency"`
	GeneratedAt   time.Time             `json:"generated_at"`
	MonthlyFactor float64               `json:"monthly_factor"`
	Thresholds    RightsizingThresholds `json:"thresholds"`
	Sources       []SourceStatus        `json:"sources"`
	Candidates    []Candidate           `json:"candidates"`
	Totals        RightsizingTotals     `json:"totals"`
}

// VMCPUSeries is one VM's stored weekly mean CPU %, oldest first, one point
// per window week (nil Value = no snapshot or a gap cell → breaks the streak).
type VMCPUSeries struct {
	ProjectID string
	Instance  string
	Zone      string
	Points    []EvidencePoint
}

// Disk, Address and Snapshot are Compute API inventory entries (read-only,
// roles/compute.viewer). Users is the attached-to list ("users" in the API).
type Disk struct {
	ProjectID string
	Name      string
	Zone      string
	Status    string
	Users     []string
	SizeGB    int64
	Type      string
}

// Address is a reserved IP (status RESERVED + no users = unused).
type Address struct {
	ProjectID string
	Name      string
	Region    string
	Status    string
	Users     []string
	Address   string
}

// Snapshot is a disk snapshot with its creation time and source disk name.
type Snapshot struct {
	ProjectID  string
	Name       string
	CreatedAt  time.Time
	SourceDisk string
	SizeGB     int64
}

// Inventory is the Compute API view fetched by an InventoryProvider.
type Inventory struct {
	FetchedAt time.Time
	Disks     []Disk
	Addresses []Address
	Snapshots []Snapshot
}

// InventoryProvider fetches the Compute inventory for the configured
// projects. On failure it returns a SourceStatus with Status "failed" and the
// fix hint (roles/compute.viewer, compute.googleapis.com) — never an error that
// aborts the rightsizing response.
type InventoryProvider interface {
	Inventory(ctx context.Context, projects []string) (Inventory, SourceStatus)
}

// RightsizingInput is everything the pure Detector needs.
type RightsizingInput struct {
	Week       string // window ends at this ISO week
	Thresholds RightsizingThresholds
	Now        time.Time
	VMCPU      []VMCPUSeries
	// BillingRows are the latest snapshot rows of the window weeks, keyed by
	// ISO week (WeekID); "last week" = Week.
	BillingRows map[string][]CostRow
	// Inventory is nil when the inventory source failed or is unconfigured:
	// disk / IP / snapshot kinds are then skipped.
	Inventory *Inventory
}

// Detector turns evidence into candidates. Rules in spec/api.md → rightsizing:
// low_cpu_vm needs a numeric value below CPUPct in each of Weeks consecutive
// stored weeks ending at Week (a gap breaks the streak, never counted as 0);
// idle_disk/unused_ip need status READY/RESERVED with no users; stale_snapshot
// older than StaleSnapshotDays; cloud_run_min_instances from idle-SKU net >=
// IdleRunMinNet. Savings use MonthlyFactor. Pure and deterministic; sorted by
// saving desc (nil last) then ID.
type Detector interface {
	Detect(in RightsizingInput) []Candidate
}

// ---- Proposer ---------------------------------------------------------------

// ProposeRequest is the body of POST /attribution/propose.
type ProposeRequest struct {
	Week     string `json:"week,omitempty"`
	Scope    string `json:"scope,omitempty"`
	MaxRules int    `json:"max_rules,omitempty"`
}

// Proposal is one proposed rule; it is never applied automatically.
type Proposal struct {
	Rule       Rule   `json:"rule"`
	Confidence string `json:"confidence"`
	Rationale  string `json:"rationale"`
	Covers     int    `json:"covers"`
}

// ProposeResponse is the 200 body of POST /attribution/propose.
type ProposeResponse struct {
	Week              string     `json:"week"`
	Model             string     `json:"model"`
	LatencyMS         int64      `json:"latency_ms"`
	InputTokens       int64      `json:"input_tokens"`
	OutputTokens      int64      `json:"output_tokens"`
	Proposals         []Proposal `json:"proposals"`
	Dropped           int        `json:"dropped"`
	CoverageIfApplied Coverage   `json:"coverage_if_applied"`
}

// ProposeInput is what a Proposer sees: resource names, labels, project ids
// and service names only — never costs (Net is zeroed by the caller).
type ProposeInput struct {
	Items    []UnattributedItem
	Existing []Rule
	MaxRules int
}

// ProposeOutput is a Proposer's raw result before coverage is computed.
type ProposeOutput struct {
	Model        string
	LatencyMS    int64
	InputTokens  int64
	OutputTokens int64
	Proposals    []Proposal
	Dropped      int
}

// Proposer is the optional, off-by-default LLM rule proposer. Implementations
// make one batched call, validate the output against the rule schema, and
// never write rules. A proposer without credentials returns
// ErrProposerNotConfigured and performs no network call.
type Proposer interface {
	// Enabled reports whether Propose can run (credentials present).
	Enabled() bool
	// Model is the configured model slug, "" when disabled.
	Model() string
	Propose(ctx context.Context, in ProposeInput) (ProposeOutput, error)
}

// NoopProposer is the default Proposer: disabled, no network.
type NoopProposer struct{}

var _ Proposer = NoopProposer{}

// Enabled is always false.
func (NoopProposer) Enabled() bool { return false }

// Model is always empty.
func (NoopProposer) Model() string { return "" }

// Propose always returns ErrProposerNotConfigured.
func (NoopProposer) Propose(context.Context, ProposeInput) (ProposeOutput, error) {
	return ProposeOutput{}, ErrProposerNotConfigured
}
