// Package costtable is the pure assembly layer between the store and the
// HTTP API: it maps billing service descriptions onto the reference sheet's
// columns, builds the Cost table (totals, % change, anomaly flags, scope
// filter, statuses, stale flag) and shapes drill-down / provenance responses
// exactly as spec/api.md describes. No I/O, no clock — callers inject `now`.
package costtable

import "strings"

// ColumnKind tells the UI how to render a column (spec/api.md → columns[].kind).
type ColumnKind string

const (
	KindWeek      ColumnKind = "week"
	KindService   ColumnKind = "service"
	KindTotal     ColumnKind = "total"
	KindPctChange ColumnKind = "pct_change"
	KindStub      ColumnKind = "stub"
)

// Fixed column keys. Service columns either come from the configured layout
// (CLOUD0_COST_COLUMNS) or are ExtraKeyPrefix + description; vendor stub
// columns (CLOUD0_VENDOR_COLUMNS) are StubKeyPrefix + their id.
const (
	KeyWeek        = "week"
	KeyTotal       = "total"
	KeyPctChange   = "pct_change"
	ExtraKeyPrefix = "svc:"
	StubKeyPrefix  = "stub:"
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

var weekColumn = Column{Key: KeyWeek, Label: "Week", Kind: KindWeek}

var totalColumns = []Column{
	{Key: KeyTotal, Label: "Total GCP", Kind: KindTotal},
	{Key: KeyPctChange, Label: "% change", Kind: KindPctChange},
}

// normalizeService is the match rule: exact after trimming and case-folding.
func normalizeService(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ColumnKeyFor returns the Cost-table column key for a billing
// service.description under the active layout: the pinned column's key when
// CLOUD0_COST_COLUMNS lists the service, otherwise ExtraKeyPrefix + the
// trimmed description.
func ColumnKeyFor(service string) string { return ActiveLayout().KeyFor(service) }

// IsExtraKey reports whether a column key denotes an extra (non-sheet) service.
func IsExtraKey(key string) bool { return strings.HasPrefix(key, ExtraKeyPrefix) }

// ServiceTotal is the range total of one unmapped service; it decides whether
// the service gets an extra column and, in the dynamic layout, its position.
type ServiceTotal struct{ Net, Gross float64 }

// Columns builds the header under the active layout for the given unmapped
// service descriptions, all treated as present with equal weight (so extras
// sort by label). Callers with real totals use ColumnsFor.
func Columns(extraServices []string) []Column {
	totals := make(map[string]ServiceTotal, len(extraServices))
	for _, s := range extraServices {
		totals[s] = ServiceTotal{Net: 1}
	}
	return ActiveLayout().Columns(totals)
}

// ColumnsFor builds the header under the active layout from the range totals
// of every unmapped service (see Layout.Columns for the presence rule).
func ColumnsFor(totals map[string]ServiceTotal) []Column { return ActiveLayout().Columns(totals) }
