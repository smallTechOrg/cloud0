// Package costtable is the pure assembly layer between the store and the
// HTTP API: it maps billing service descriptions onto the reference sheet's
// columns, builds the Cost table (totals, % change, anomaly flags, scope
// filter, statuses, stale flag) and shapes drill-down / provenance responses
// exactly as spec/api.md describes. No I/O, no clock — callers inject `now`.
package costtable

import (
	"sort"
	"strings"
)

// ColumnKind tells the UI how to render a column (spec/api.md → columns[].kind).
type ColumnKind string

const (
	KindWeek      ColumnKind = "week"
	KindService   ColumnKind = "service"
	KindTotal     ColumnKind = "total"
	KindPctChange ColumnKind = "pct_change"
	KindStub      ColumnKind = "stub"
)

// Fixed column keys. Extra service columns use ExtraKeyPrefix + description.
const (
	KeyWeek             = "week"
	KeyComputeEngine    = "compute_engine"
	KeyNetworking       = "networking"
	KeyCloudSQL         = "cloud_sql"
	KeyGeminiAPI        = "gemini_api"
	KeyCloudRun         = "cloud_run"
	KeyGCS              = "gcs"
	KeyVMManager        = "vm_manager"
	KeyCloudBuild       = "cloud_build"
	KeyCloudLogging     = "cloud_logging"
	KeySecretManager    = "secret_manager"
	KeyArtifactRegistry = "artifact_registry"
	KeyTotal            = "total"
	KeyPctChange        = "pct_change"
	KeyStubGroq         = "stub:groq"
	KeyStubClaude       = "stub:claude"
	ExtraKeyPrefix      = "svc:"
)

// StubPhase is the phase in which the vendor columns become real.
const StubPhase = 4

// Column is one header cell of the Cost table.
type Column struct {
	Key      string     `json:"key"`
	Label    string     `json:"label"`
	Kind     ColumnKind `json:"kind"`
	Services []string   `json:"services,omitempty"` // raw service.description values aggregated
	Extra    bool       `json:"extra,omitempty"`    // not in the sheet; present only when non-zero in the range
	Phase    int        `json:"phase,omitempty"`    // stub columns: the phase that fills them
}

// fixedServiceColumns is the sheet's service column order with the
// service.description values each aggregates (spec/capabilities/cost-table.md).
var fixedServiceColumns = []Column{
	{Key: KeyComputeEngine, Label: "Compute Engine", Kind: KindService, Services: []string{"Compute Engine"}},
	{Key: KeyNetworking, Label: "Networking", Kind: KindService, Services: []string{"Networking"}},
	{Key: KeyCloudSQL, Label: "CloudSQL", Kind: KindService, Services: []string{"Cloud SQL"}},
	{Key: KeyGeminiAPI, Label: "Gemini API", Kind: KindService, Services: []string{"Gemini API", "Generative Language API"}},
	{Key: KeyCloudRun, Label: "Cloud Run", Kind: KindService, Services: []string{"Cloud Run"}},
	{Key: KeyGCS, Label: "GCS", Kind: KindService, Services: []string{"Cloud Storage"}},
	{Key: KeyVMManager, Label: "VM manager", Kind: KindService, Services: []string{"VM Manager"}},
	{Key: KeyCloudBuild, Label: "Cloud Build", Kind: KindService, Services: []string{"Cloud Build"}},
	{Key: KeyCloudLogging, Label: "Cloud Logging", Kind: KindService, Services: []string{"Cloud Logging"}},
	{Key: KeySecretManager, Label: "Secret Manager", Kind: KindService, Services: []string{"Secret Manager"}},
	{Key: KeyArtifactRegistry, Label: "Artifact Registry", Kind: KindService, Services: []string{"Artifact Registry"}},
}

var weekColumn = Column{Key: KeyWeek, Label: "Week", Kind: KindWeek}

var trailingColumns = []Column{
	{Key: KeyTotal, Label: "Total GCP", Kind: KindTotal},
	{Key: KeyPctChange, Label: "% change", Kind: KindPctChange},
	{Key: KeyStubGroq, Label: "AI Cost (Groq)", Kind: KindStub, Phase: StubPhase},
	{Key: KeyStubClaude, Label: "claude (usage)", Kind: KindStub, Phase: StubPhase},
}

// serviceKeys maps a normalised service.description to its fixed column key.
var serviceKeys = func() map[string]string {
	m := make(map[string]string)
	for _, c := range fixedServiceColumns {
		for _, s := range c.Services {
			m[normalizeService(s)] = c.Key
		}
	}
	return m
}()

// normalizeService is the match rule: exact after trimming and case-folding.
func normalizeService(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ColumnKeyFor returns the Cost-table column key for a billing
// service.description: a fixed key when the sheet has a column for it,
// otherwise ExtraKeyPrefix + the trimmed description.
func ColumnKeyFor(service string) string {
	if key, ok := serviceKeys[normalizeService(service)]; ok {
		return key
	}
	return ExtraKeyPrefix + strings.TrimSpace(service)
}

// IsExtraKey reports whether a column key denotes an extra (non-sheet) service.
func IsExtraKey(key string) bool { return strings.HasPrefix(key, ExtraKeyPrefix) }

// Columns builds the complete ordered header: week, the fixed service
// columns, one extra column per given raw service description (deduplicated
// after trimming, sorted by label), then total, % change and the two stubs.
func Columns(extraServices []string) []Column {
	seen := make(map[string]bool, len(extraServices))
	extras := make([]string, 0, len(extraServices))
	for _, s := range extraServices {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		extras = append(extras, s)
	}
	sort.Strings(extras)

	out := make([]Column, 0, 1+len(fixedServiceColumns)+len(extras)+len(trailingColumns))
	out = append(out, weekColumn)
	for _, c := range fixedServiceColumns {
		c.Services = append([]string(nil), c.Services...)
		out = append(out, c)
	}
	for _, s := range extras {
		out = append(out, Column{Key: ExtraKeyPrefix + s, Label: s, Kind: KindService, Services: []string{s}, Extra: true})
	}
	return append(out, trailingColumns...)
}
