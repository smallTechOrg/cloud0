// Package alerting holds the FROZEN contract of the Alerts feature
// (spec/api.md → "Alerts additions"): the JSON types shared by the HTTP layer,
// the GCP clients and the pure policy builder, plus the interfaces the
// parallel workers implement. No GCP or HTTP code lives here.
//
// Conventions: Policy/uptime/budget bodies that GCP owns are carried as
// json.RawMessage in the Monitoring/Billing-Budgets REST (camelCase) shape so
// preview, diff and gcloud rendering are lossless. Secrets (webhook URLs,
// tokens) never appear in any type here except ChannelDraft.SensitiveLabels,
// which is passed through to GCP and never stored or logged.
package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// ---- Template ids, severities, labels --------------------------------------

// Template ids (Template.ID). One template → one managed policy per project.
const (
	TplVMCPUHigh      = "vm_cpu_high"
	TplVMMemoryHigh   = "vm_memory_high"
	TplVMDiskHigh     = "vm_disk_high"
	TplRun5xxBurst    = "run_5xx_burst"
	TplRun5xxRate     = "run_5xx_rate"
	TplRunErrorLogged = "run_error_logged"
	TplLB5xxBurst     = "lb_5xx_burst"
	TplSQLDown        = "sql_down"
	TplSQLCPUHigh     = "sql_cpu_high"
	TplSQLMemoryHigh  = "sql_memory_high"
	TplSQLDiskHigh    = "sql_disk_high"
	TplUptimeDown     = "uptime_down"
	TplRun401403Spike = "run_401_403_spike"
)

// AllTemplateIDs is the canonical order of GET /alerts/templates.
var AllTemplateIDs = []string{
	TplVMCPUHigh, TplVMMemoryHigh, TplVMDiskHigh,
	TplRun5xxBurst, TplRun5xxRate, TplRunErrorLogged, TplLB5xxBurst,
	TplSQLDown, TplSQLCPUHigh, TplSQLMemoryHigh, TplSQLDiskHigh,
	TplUptimeDown, TplRun401403Spike,
}

// Severities map 1:1 to the Monitoring policy `severity` field.
const (
	SevCritical = "CRITICAL"
	SevError    = "ERROR"
	SevWarning  = "WARNING"
)

// Severities in descending order.
var Severities = []string{SevCritical, SevError, SevWarning}

// Template kinds (Template.Kind) = which condition family the builder emits.
const (
	KindThreshold = "metric_threshold" // conditionThreshold, absolute
	KindRatio     = "metric_ratio"     // conditionThreshold with denominatorFilter
	KindLogMatch  = "log_match"        // conditionMatchedLog
	KindUptime    = "uptime"           // conditionThreshold on uptime_check/check_passed
	KindAbsent    = "metric_absent"    // reserved
)

// Labels cloud0 writes on every managed policy / uptime check (user_labels).
const (
	LabelManagedBy    = "managed-by"      // value LabelManagedByValue
	LabelTemplate     = "cloud0-template" // value = a Tpl* id
	LabelInstance     = "cloud0-instance" // optional discriminator, e.g. "warning" | "critical"
	LabelManagedByVal = "cloud0"
)

// BudgetNamePrefix marks cloud0-managed budgets (the Budgets API has no
// labels). A budget is Managed iff its display name starts with this.
const BudgetNamePrefix = "cloud0-"

// TestPrefix is mandatory in the display name of anything a real-GCP test creates.
const TestPrefix = "cloud0-test-"

// Change-history / preview kinds.
const (
	TargetPolicy      = "policy"
	TargetUptimeCheck = "uptime_check"
	TargetBudget      = "budget"
	TargetSnooze      = "snooze"
	TargetChannel     = "channel"
	TargetRouting     = "routing" // history only
)

// Actions.
const (
	ActionCreate  = "create"
	ActionUpdate  = "update"
	ActionDelete  = "delete"
	ActionEnable  = "enable"
	ActionDisable = "disable"
	ActionEnd     = "end" // snooze: end now
)

// Adopt shapes (POST /alerts/adopt/preview).
const (
	ShapeZeroCPUSpike      = "zero_cpu_spike"
	ShapeHighMemory        = "high_memory_load"
	ShapeUptimeBackend     = "uptime_backend_down"
	ShapeUptimeTwoHours    = "uptime_down_2h"
	ShapeBudgetBoilerplate = "budget_account"
)

// ---- Errors ----------------------------------------------------------------

// Sentinels the HTTP layer maps with CodeFor.
var (
	ErrNotConfigured    = errors.New("alerting: CLOUD0_ALERTS_CREDENTIALS not set")
	ErrPreviewStale     = errors.New("alerting: preview is stale or unknown")
	ErrInvalidParams    = errors.New("alerting: invalid params")
	ErrPermissionDenied = errors.New("alerting: permission denied")
	ErrNotFound         = errors.New("alerting: not found")
	ErrConfirmRequired  = errors.New("alerting: confirmation required")
	ErrUpstream         = errors.New("alerting: upstream GCP error")
	ErrAPIDisabled      = errors.New("alerting: API not enabled")
)

// CodeFor maps an error (errors.Is) to the HTTP status and envelope code of
// spec/api.md. Unknown errors → 500 internal_error.
func CodeFor(err error) (status int, code string) {
	switch {
	case errors.Is(err, ErrNotConfigured):
		return http.StatusServiceUnavailable, "alerts_not_configured"
	case errors.Is(err, ErrPreviewStale):
		return http.StatusConflict, "preview_stale"
	case errors.Is(err, ErrInvalidParams):
		return http.StatusBadRequest, "invalid_params"
	case errors.Is(err, ErrPermissionDenied):
		return http.StatusForbidden, "permission_denied"
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, ErrConfirmRequired):
		return http.StatusBadRequest, "confirm_required"
	case errors.Is(err, ErrAPIDisabled):
		return http.StatusServiceUnavailable, "api_disabled"
	case errors.Is(err, ErrUpstream):
		return http.StatusBadGateway, "upstream_error"
	}
	return http.StatusInternalServerError, "internal_error"
}

// ---- Templates -------------------------------------------------------------

// ParamSpec describes one editable template parameter (drives the UI form and
// server validation).
type ParamSpec struct {
	Key     string   `json:"key"` // JSON key in TemplateParams
	Label   string   `json:"label"`
	Type    string   `json:"type"` // "number" | "int" | "bool" | "enum"
	Unit    string   `json:"unit"` // "%", "seconds", "count", "" ...
	Min     *float64 `json:"min"`
	Max     *float64 `json:"max"`
	Options []string `json:"options"` // for enum
	Help    string   `json:"help"`
}

// TemplateParams are the user-editable values of a template. Nil/zero = use
// the template default. Thresholds are percents (0–100) or counts exactly as
// the ParamSpec unit says; the builder converts to ratios where the metric needs.
type TemplateParams struct {
	Threshold        *float64 `json:"threshold,omitempty"`
	DurationSeconds  *int     `json:"duration_seconds,omitempty"`
	WindowSeconds    *int     `json:"window_seconds,omitempty"`     // burst alignment window
	RateLimitSeconds *int     `json:"rate_limit_seconds,omitempty"` // log-match notification rate limit
	Severity         string   `json:"severity,omitempty"`
	Enabled          *bool    `json:"enabled,omitempty"`
	// ChannelOverride replaces the severity routing for this policy (full
	// notification channel resource names). Empty = use routing.
	ChannelOverride []string `json:"channel_override,omitempty"`
	// Instance discriminates several managed policies of one template
	// (uptime_down: "warning" | "critical").
	Instance string `json:"instance,omitempty"`
	// UptimeCheckIDs restricts uptime_down to these check ids (empty = all checks in the project).
	UptimeCheckIDs []string `json:"uptime_check_ids,omitempty"`
}

// CoveredResource is one resource a template covers (or silently skips).
type CoveredResource struct {
	Project      string `json:"project"`
	ResourceType string `json:"resource_type"` // vm | cloudsql | cloud_run | lb | uptime_check
	Name         string `json:"name"`
	Covered      bool   `json:"covered"`
	Note         string `json:"note"` // e.g. "no Ops Agent: memory/disk metrics unavailable"
}

// Template = parameters + builder metadata.
type Template struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Severity    string         `json:"severity"` // default severity
	ParamSpecs  []ParamSpec    `json:"param_specs"`
	Defaults    TemplateParams `json:"defaults"`
	// DefaultEnabled is false only for run_401_403_spike.
	DefaultEnabled bool `json:"default_enabled"`
	// Params is the effective params of the installed managed policy (nil when not installed).
	Params *TemplateParams `json:"params"`
	// Installed lists names of managed policies currently built from this template.
	Installed []string `json:"installed"`
	// CoveredResources is filled by GET /alerts/templates (live inventory).
	CoveredResources []CoveredResource `json:"covered_resources"`
}

// ---- Channels --------------------------------------------------------------

// Channel is a Monitoring notification channel (no secrets).
type Channel struct {
	Name               string   `json:"name"` // projects/proj-a/notificationChannels/123
	Project            string   `json:"project"`
	Type               string   `json:"type"` // email | slack | webhook_tokenauth | ...
	DisplayName        string   `json:"display_name"`
	Enabled            bool     `json:"enabled"`
	VerificationStatus string   `json:"verification_status"` // VERIFIED | UNVERIFIED | VERIFICATION_STATUS_UNSPECIFIED
	Address            string   `json:"address"`             // masked: email "a***@example.com"; others ""
	Warnings           []string `json:"warnings"`
}

// LabelSpec is one required label of a channel type.
type LabelSpec struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Sensitive   bool   `json:"sensitive"`
}

// ChannelDescriptor mirrors notificationChannelDescriptors with a creatable flag.
type ChannelDescriptor struct {
	Type        string      `json:"type"`
	DisplayName string      `json:"display_name"`
	Creatable   bool        `json:"creatable"` // false when console OAuth is required (Slack)
	Hint        string      `json:"hint"`
	Labels      []LabelSpec `json:"labels"`
}

// ChannelDraft creates a channel. SensitiveLabels (webhook URL/token) are passed
// through to GCP only: never stored, logged, echoed or put in change_history.
type ChannelDraft struct {
	Project         string            `json:"project"`
	Type            string            `json:"type"`
	DisplayName     string            `json:"display_name"`
	Labels          map[string]string `json:"labels"`
	SensitiveLabels map[string]string `json:"sensitive_labels,omitempty"`
}

// ChannelRef is a channel as referenced from a policy.
type ChannelRef struct {
	Name               string `json:"name"`
	DisplayName        string `json:"display_name"`
	Type               string `json:"type"`
	VerificationStatus string `json:"verification_status"`
}

// ---- Policies --------------------------------------------------------------

// ConditionSummary is a human-readable digest of one policy condition.
type ConditionSummary struct {
	DisplayName     string   `json:"display_name"`
	Type            string   `json:"type"`    // threshold | absent | log_match | other
	Summary         string   `json:"summary"` // "CPU utilization > 80 % for 5 min"
	Filter          string   `json:"filter"`
	Comparison      string   `json:"comparison"`
	Threshold       *float64 `json:"threshold"`
	DurationSeconds *int     `json:"duration_seconds"`
}

// PolicyView is one alert policy as the UI sees it.
type PolicyView struct {
	Name        string             `json:"name"` // projects/proj-a/alertPolicies/123
	Project     string             `json:"project"`
	DisplayName string             `json:"display_name"`
	Enabled     bool               `json:"enabled"`
	Severity    string             `json:"severity"` // "" when unset on the live policy
	Managed     bool               `json:"managed"`
	TemplateID  string             `json:"template_id"` // "" when unmanaged
	Instance    string             `json:"instance"`
	Conditions  []ConditionSummary `json:"conditions"`
	Channels    []ChannelRef       `json:"channels"`
	// AdoptShape is one of the Shape* constants when this unmanaged policy matches a known shape, else "".
	AdoptShape string          `json:"adopt_shape"`
	Warnings   []string        `json:"warnings"` // e.g. "duration is 0 s: fires on a single sample"
	UpdatedAt  *time.Time      `json:"updated_at"`
	RawJSON    json.RawMessage `json:"raw_json"`
}

// ---- Preview / apply -------------------------------------------------------

// PreviewRequest is the body of POST /alerts/preview.
type PreviewRequest struct {
	Kind       string          `json:"kind"`   // policy | uptime_check | budget | snooze | channel
	Action     string          `json:"action"` // create | update | delete | enable | disable | end
	Target     string          `json:"target"` // resource name; "" for create
	Project    string          `json:"project,omitempty"`
	TemplateID string          `json:"template_id,omitempty"` // policy create/update from a template
	Params     json.RawMessage `json:"params,omitempty"`      // TemplateParams | UptimeDraft | BudgetDraft | SnoozeDraft | ChannelDraft
	Desired    json.RawMessage `json:"desired,omitempty"`     // raw REST body (editing unmanaged resources)
}

// DiffChange is one differing JSON path (dotted, e.g. "conditions[0].conditionThreshold.thresholdValue").
type DiffChange struct {
	Path   string          `json:"path"`
	Op     string          `json:"op"` // add | remove | change
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

// Diff of live vs desired; Unified is a pretty-printed unified diff for display.
type Diff struct {
	Changes []DiffChange `json:"changes"`
	Unified string       `json:"unified"`
}

// Preview is the response of POST /alerts/preview. Nothing has changed in GCP.
type Preview struct {
	PreviewToken    string          `json:"preview_token"`
	ExpiresAt       time.Time       `json:"expires_at"`
	Kind            string          `json:"kind"`
	Action          string          `json:"action"`
	Target          string          `json:"target"`
	Current         json.RawMessage `json:"current"` // null on create
	Desired         json.RawMessage `json:"desired"` // null on delete
	Diff            Diff            `json:"diff"`
	Gcloud          string          `json:"gcloud"`
	Warnings        []string        `json:"warnings"`
	RequiresConfirm bool            `json:"requires_confirm"` // delete: apply needs confirm_delete=true
}

// ApplyRequest is the body of POST /alerts/apply.
type ApplyRequest struct {
	PreviewToken  string `json:"preview_token"`
	ConfirmDelete bool   `json:"confirm_delete,omitempty"`
}

// ApplyResult is the response of POST /alerts/apply.
type ApplyResult struct {
	OK              bool            `json:"ok"`
	Kind            string          `json:"kind"`
	Action          string          `json:"action"`
	Target          string          `json:"target"`   // resulting resource name
	Resource        json.RawMessage `json:"resource"` // resulting resource (null after delete)
	ChangeHistoryID int64           `json:"change_history_id"`
	AppliedAt       time.Time       `json:"applied_at"`
	Warnings        []string        `json:"warnings"`
}

// AdoptRequest is the body of POST /alerts/adopt/preview.
type AdoptRequest struct {
	Shape  string `json:"shape"`  // Shape* constant
	Target string `json:"target"` // name of the unmanaged policy/budget (optional if unambiguous)
}

// AdoptFollowUp is the non-automatic second step: disable (never delete) the old resource.
type AdoptFollowUp struct {
	Action string `json:"action"` // disable
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Note   string `json:"note"`
}

// AdoptPreview is the response of POST /alerts/adopt/preview: apply every
// preview first (each has its own token), only then offer FollowUp via a normal
// preview/apply.
type AdoptPreview struct {
	Shape    string          `json:"shape"`
	Replaces []string        `json:"replaces"`
	Previews []Preview       `json:"previews"`
	FollowUp []AdoptFollowUp `json:"follow_up"`
	Notes    []string        `json:"notes"`
}

// ---- Incidents, history, coverage -------------------------------------------

// Incident is one Monitoring alert (open or closed).
type Incident struct {
	ID                string     `json:"id"`
	Project           string     `json:"project"`
	PolicyName        string     `json:"policy_name"`
	PolicyDisplayName string     `json:"policy_display_name"`
	Severity          string     `json:"severity"`
	State             string     `json:"state"` // open | closed
	OpenedAt          time.Time  `json:"opened_at"`
	ClosedAt          *time.Time `json:"closed_at"`
	ResourceType      string     `json:"resource_type"`
	ResourceName      string     `json:"resource_name"`
	Summary           string     `json:"summary"`
	URL               string     `json:"url"` // GCP console incident link
}

// IncidentFilter for IncidentClient.
type IncidentFilter struct {
	State string    // "open" | "closed" | "" (all)
	From  time.Time // opened >= From (zero = no bound)
	To    time.Time // opened < To (zero = no bound)
}

// HistoryRow counts alerts opened in an ISO week for one policy.
type HistoryRow struct {
	Project           string    `json:"project"`
	PolicyName        string    `json:"policy_name"`
	PolicyDisplayName string    `json:"policy_display_name"`
	Severity          string    `json:"severity"`
	Opened            int       `json:"opened"`
	StillOpen         int       `json:"still_open"`
	Resources         []string  `json:"resources"`
	FirstOpenedAt     time.Time `json:"first_opened_at"`
	LastOpenedAt      time.Time `json:"last_opened_at"`
}

// CoverageSignal is one alertable signal of a resource.
type CoverageSignal struct {
	Signal   string   `json:"signal"` // cpu | memory | disk | errors | down | uptime
	Covered  bool     `json:"covered"`
	Policies []string `json:"policies"` // names of policies covering it
	Reason   string   `json:"reason"`   // why not (e.g. "Ops Agent not installed")
}

// CoverageRow is one resource and the alert signals covering it.
type CoverageRow struct {
	Project      string           `json:"project"`
	ResourceType string           `json:"resource_type"` // vm | cloudsql | cloud_run | lb | uptime_check
	Resource     string           `json:"resource"`
	Signals      []CoverageSignal `json:"signals"`
	Hint         string           `json:"hint"`
}

// ---- Uptime checks, snoozes -------------------------------------------------

// UptimeCheck is a Monitoring HTTP(S) uptime check.
type UptimeCheck struct {
	Name           string          `json:"name"` // projects/proj-a/uptimeCheckConfigs/xyz
	Project        string          `json:"project"`
	DisplayName    string          `json:"display_name"`
	Host           string          `json:"host"`
	Path           string          `json:"path"`
	Port           int             `json:"port"`
	UseSSL         bool            `json:"use_ssl"`
	PeriodSeconds  int             `json:"period_seconds"`
	TimeoutSeconds int             `json:"timeout_seconds"`
	ContentMatch   string          `json:"content_match"`
	Managed        bool            `json:"managed"`
	RawJSON        json.RawMessage `json:"raw_json"`
}

// UptimeDraft is the params of an uptime_check preview.
type UptimeDraft struct {
	Project        string `json:"project"`
	DisplayName    string `json:"display_name"`
	Host           string `json:"host"`
	Path           string `json:"path"`
	Port           int    `json:"port"`
	UseSSL         bool   `json:"use_ssl"`
	PeriodSeconds  int    `json:"period_seconds"`  // 60 | 300 | 600 | 900
	TimeoutSeconds int    `json:"timeout_seconds"` // 1–60
	ContentMatch   string `json:"content_match"`
}

// Snooze mutes policies for a time window.
type Snooze struct {
	Name        string    `json:"name"`
	Project     string    `json:"project"`
	DisplayName string    `json:"display_name"`
	PolicyNames []string  `json:"policy_names"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	State       string    `json:"state"` // active | scheduled | expired
}

// SnoozeDraft is the params of a snooze create preview.
type SnoozeDraft struct {
	Project         string     `json:"project"`
	DisplayName     string     `json:"display_name"`
	PolicyNames     []string   `json:"policy_names"`
	StartTime       *time.Time `json:"start_time,omitempty"` // nil = now
	DurationSeconds int        `json:"duration_seconds"`     // 300 .. 30 days
}

// ---- Budgets ---------------------------------------------------------------

// BudgetThreshold is one notification rule.
type BudgetThreshold struct {
	Percent float64 `json:"percent"` // 50, 80, 100 (of amount)
	Basis   string  `json:"basis"`   // current_spend | forecasted_spend
}

// BudgetView is one Cloud Billing budget.
type BudgetView struct {
	Name                 string            `json:"name"` // billingAccounts/BILLING_ACCOUNT_ID/budgets/uuid
	DisplayName          string            `json:"display_name"`
	Managed              bool              `json:"managed"`
	AmountKind           string            `json:"amount_kind"` // specified | last_period
	Amount               *float64          `json:"amount"`
	Currency             string            `json:"currency"`
	Projects             []string          `json:"projects"`    // project ids; empty = whole billing account
	ServiceIDs           []string          `json:"service_ids"` // services/6F81-5844-456A
	Thresholds           []BudgetThreshold `json:"thresholds"`
	ChannelNames         []string          `json:"channel_names"`
	DefaultIAMRecipients bool              `json:"default_iam_recipients"`
	AdoptShape           string            `json:"adopt_shape"`
	Warnings             []string          `json:"warnings"`
	RawJSON              json.RawMessage   `json:"raw_json"`
}

// BudgetDraft is the params of a budget preview (create/update).
type BudgetDraft struct {
	DisplayName          string            `json:"display_name"` // must start with BudgetNamePrefix for managed
	Amount               float64           `json:"amount"`       // > 0, in the billing currency (INR)
	Projects             []string          `json:"projects"`
	ServiceIDs           []string          `json:"service_ids"`
	Thresholds           []BudgetThreshold `json:"thresholds"`         // empty = defaults 50/80/100 current + 100 forecast
	Severity             string            `json:"severity,omitempty"` // routes via alerts.routing.<sev>; default ERROR
	ChannelOverride      []string          `json:"channel_override,omitempty"`
	DefaultIAMRecipients *bool             `json:"default_iam_recipients,omitempty"` // nil = true
}

// SuggestionBasis explains how a suggested amount was computed.
type SuggestionBasis struct {
	Method      string  `json:"method"` // snapshots_avg | monthly_total_setting | none
	WeeksUsed   int     `json:"weeks_used"`
	AvgWeekly   float64 `json:"avg_weekly"`
	Multiplier  float64 `json:"multiplier"` // 1.2
	Explanation string  `json:"explanation"`
}

// Suggestion is a prefilled, editable budget draft.
type Suggestion struct {
	Scope       string          `json:"scope"` // account | service
	ServiceID   string          `json:"service_id"`
	ServiceName string          `json:"service_name"`
	Amount      float64         `json:"amount"`
	Basis       SuggestionBasis `json:"basis"`
	Draft       BudgetDraft     `json:"draft"`
	Exists      bool            `json:"exists"` // a managed budget with this scope already exists
}

// ---- Change history --------------------------------------------------------

// ChangeRecord mirrors a change_history row.
type ChangeRecord struct {
	ID          int64           `json:"id"`
	At          time.Time       `json:"at"`
	Actor       string          `json:"actor"` // "local-user"
	Kind        string          `json:"kind"`
	Action      string          `json:"action"`
	Target      string          `json:"target"`
	Before      json.RawMessage `json:"before"`
	After       json.RawMessage `json:"after"`
	PreviewHash string          `json:"preview_hash"`
}

// ---- Interfaces other workers implement ------------------------------------

// BuildContext is the pure input of Builder.Build besides template and params.
type BuildContext struct {
	Project  string
	Channels []string // resolved channel resource names for Project (severity routing / override)
	Now      time.Time
	// UptimeChecks are the project's uptime checks (uptime_down).
	UptimeChecks []UptimeCheck
	// Existing is the live managed policy being updated, or nil.
	Existing *PolicyView
}

// Built is a desired policy: REST AlertPolicy JSON (no name on create) + warnings.
type Built struct {
	Policy   json.RawMessage
	Warnings []string
}

// Builder is pure (no I/O): template + merged params + context → AlertPolicy JSON.
// It must set severity, userLabels (managed-by, cloud0-template, cloud0-instance),
// enabled, notificationChannels from ctx.Channels, and conditions whose filters
// cover every resource of the type (including future ones).
type Builder interface {
	Templates() []Template
	Template(id string) (Template, bool)
	// Merge applies defaults and validates; errors wrap ErrInvalidParams.
	Merge(t Template, p TemplateParams) (TemplateParams, error)
	Build(t Template, p TemplateParams, ctx BuildContext) (Built, error)
	// Gcloud renders the equivalent gcloud command for create (name=="") or update.
	Gcloud(project, name string, policy json.RawMessage) string
}

// PolicyClient reads/writes alert policies (writes need alerts credentials).
type PolicyClient interface {
	List(ctx context.Context, project string) ([]PolicyView, error)
	Get(ctx context.Context, name string) (PolicyView, error)
	Create(ctx context.Context, project string, policy json.RawMessage) (PolicyView, error)
	Update(ctx context.Context, name string, policy json.RawMessage) (PolicyView, error)
	Delete(ctx context.Context, name string) error
	SetEnabled(ctx context.Context, name string, enabled bool) (PolicyView, error)
}

// ChannelClient reads channels and (alerts creds) creates/deletes them.
type ChannelClient interface {
	List(ctx context.Context, project string) ([]Channel, error)
	Descriptors(ctx context.Context, project string) ([]ChannelDescriptor, error)
	Create(ctx context.Context, d ChannelDraft) (Channel, error)
	Delete(ctx context.Context, name string) error
}

// UptimeClient manages uptime check configs.
type UptimeClient interface {
	List(ctx context.Context, project string) ([]UptimeCheck, error)
	Get(ctx context.Context, name string) (UptimeCheck, error)
	Create(ctx context.Context, d UptimeDraft) (UptimeCheck, error)
	Update(ctx context.Context, name string, d UptimeDraft) (UptimeCheck, error)
	Delete(ctx context.Context, name string) error
}

// IncidentClient reads the Monitoring alerts API.
type IncidentClient interface {
	List(ctx context.Context, project string, f IncidentFilter) ([]Incident, error)
}

// SnoozeClient manages snoozes. There is no delete: End sets end time to now.
type SnoozeClient interface {
	List(ctx context.Context, project string) ([]Snooze, error)
	Create(ctx context.Context, d SnoozeDraft) (Snooze, error)
	End(ctx context.Context, name string, now time.Time) (Snooze, error)
}

// BudgetClient manages Cloud Billing budgets of one billing account.
// channelNames in the draft are already resolved by the caller.
type BudgetClient interface {
	List(ctx context.Context) ([]BudgetView, error)
	Get(ctx context.Context, name string) (BudgetView, error)
	Create(ctx context.Context, d BudgetDraft, channelNames []string) (BudgetView, error)
	Update(ctx context.Context, name string, d BudgetDraft, channelNames []string) (BudgetView, error)
	Delete(ctx context.Context, name string) error
	// Body renders the REST budget body (for preview/diff/gcloud) without calling GCP.
	Body(d BudgetDraft, channelNames []string) (json.RawMessage, error)
}
