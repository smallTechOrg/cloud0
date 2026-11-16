// Package limits compares month-to-date usage against the free-tier
// allowances and quotas tracked on the sheet's Limits tab. It is pure: no
// I/O, no clock — callers pass rows, settings and "now".
package limits

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// Status levels.
const (
	LevelOK     = "ok"
	LevelNear   = "near"
	LevelOver   = "over"
	LevelStub   = "stub"
	LevelNoData = "no data"
)

// Settings keys.
const (
	SettingPrefix  = "limit."
	WarnKey        = "warn_pct" // stored as setting "limit.warn_pct"
	DefaultWarnPct = 80.0
)

// ErrInvalidOverride is returned by ParseOverrides for unparseable or
// out-of-range setting values.
var ErrInvalidOverride = errors.New("limits: invalid override")

// Allowance is one monthly free-tier allowance or quota. ID is stable
// snake_case and doubles as the settings key suffix (limit.<id>).
type Allowance struct {
	ID          string
	Service     string
	Label       string
	Unit        string
	Monthly     float64
	MetricGroup string // gcpsource.Group*; empty for stubs without a metric
	MetricKey   string // gcpsource.M*; empty for stubs without a metric
	Stub        bool
	Reason      string // why a stub row has no usage yet; for a live row, a one-line note shown with it
}

// SettingKey returns the settings key for an allowance id.
func SettingKey(id string) string { return SettingPrefix + id }

const groqModel = "meta-llama/llama-4-scout-17b-16e-instruct"

var defaults = []Allowance{
	{ID: "gcs_storage_gb_month", Service: "Cloud Storage", Label: "Storage", Unit: "GB-month", Monthly: 5,
		MetricGroup: gcpsource.GroupGCS, MetricKey: gcpsource.MStorageGBMonth},
	{ID: "gcs_class_a_ops", Service: "Cloud Storage", Label: "Class A operations", Unit: "count", Monthly: 5000,
		MetricGroup: gcpsource.GroupGCS, MetricKey: gcpsource.MClassAOps},
	{ID: "gcs_class_b_ops", Service: "Cloud Storage", Label: "Class B operations", Unit: "count", Monthly: 50000,
		MetricGroup: gcpsource.GroupGCS, MetricKey: gcpsource.MClassBOps},
	// The 100 GB North-America egress allowance is measured with the
	// Networking block's outbound bytes, the closest collected metric.
	{ID: "gcs_egress_na_gb", Service: "Cloud Storage", Label: "Egress (North America)", Unit: "GB", Monthly: 100,
		MetricGroup: gcpsource.GroupNetworking, MetricKey: gcpsource.MOutboundGB},
	{ID: "cloudrun_requests", Service: "Cloud Run", Label: "Requests", Unit: "count", Monthly: 2000000,
		MetricGroup: gcpsource.GroupCloudRun, MetricKey: gcpsource.MRequests},
	{ID: "cloudrun_cpu_vcpu_s", Service: "Cloud Run", Label: "CPU", Unit: "vCPU-s", Monthly: 180000,
		MetricGroup: gcpsource.GroupCloudRun, MetricKey: gcpsource.MServiceCPUSec,
		Reason: "free tier is consumed by active + idle min-instance CPU"},
	{ID: "cloudrun_memory_gib_s", Service: "Cloud Run", Label: "Memory", Unit: "GiB-s", Monthly: 360000,
		MetricGroup: gcpsource.GroupCloudRun, MetricKey: gcpsource.MServiceMemGiBs,
		Reason: "free tier is consumed by active + idle min-instance memory"},
	{ID: "cloudrun_egress_gb", Service: "Cloud Run", Label: "Egress", Unit: "GB", Monthly: 1,
		MetricGroup: gcpsource.GroupCloudRun, MetricKey: gcpsource.MEgressInterGB},
	{ID: "logging_gb", Service: "Cloud Logging", Label: "Log ingestion", Unit: "GB", Monthly: 50,
		MetricGroup: gcpsource.GroupLogging, MetricKey: gcpsource.MLoggingGB},
	{ID: "disk_throughput_gib", Service: "Compute Engine", Label: "Disk throughput", Unit: "GiB", Monthly: 50,
		Stub: true, Reason: "no disk-throughput metric is collected yet"},
	{ID: "groq_llama4_scout_rpm", Service: "Groq", Label: groqModel + " requests/minute", Unit: "requests/min", Monthly: 30,
		Stub: true, Reason: "Groq usage arrives in Phase 4"},
	{ID: "groq_llama4_scout_rpd", Service: "Groq", Label: groqModel + " requests/day", Unit: "requests/day", Monthly: 1000,
		Stub: true, Reason: "Groq usage arrives in Phase 4"},
	{ID: "groq_llama4_scout_tpm", Service: "Groq", Label: groqModel + " tokens/minute", Unit: "tokens/min", Monthly: 30000,
		Stub: true, Reason: "Groq usage arrives in Phase 4"},
	{ID: "groq_llama4_scout_tpd", Service: "Groq", Label: groqModel + " tokens/day", Unit: "tokens/day", Monthly: 500000,
		Stub: true, Reason: "Groq usage arrives in Phase 4"},
}

// Defaults returns a fresh copy of the sheet's seeded allowances, in
// Limits-tab order. (Groq rows carry per-minute/per-day quotas in Monthly.)
func Defaults() []Allowance {
	out := make([]Allowance, len(defaults))
	copy(out, defaults)
	return out
}

// Apply returns a copy of as with Monthly replaced from overrides by ID.
func Apply(as []Allowance, overrides map[string]float64) []Allowance {
	out := make([]Allowance, len(as))
	copy(out, as)
	for i := range out {
		if v, ok := overrides[out[i].ID]; ok {
			out[i].Monthly = v
		}
	}
	return out
}

// Usage is the measured consumption for one allowance.
type Usage struct {
	MonthToDate float64
	Week        float64
}

// Status is the evaluated state of one allowance.
type Status struct {
	ID        string
	Used      float64 // month to date
	Projected float64 // linear projection to month end
	Allowance float64
	Pct       float64 // Projected / Allowance * 100
	Level     string  // ok | near | over | stub | no data
	Week      float64
}

// Evaluate projects u.MonthToDate linearly to the end of month (by elapsed
// fraction of the window, clamped to [0,1]; at or after month end, or with
// nothing elapsed, projected = used) and grades the projected percentage
// against warnPct (invalid values fall back to 80).
func Evaluate(a Allowance, u Usage, month gcpsource.Window, now time.Time, warnPct float64) Status {
	s := Status{ID: a.ID, Allowance: a.Monthly}
	if a.Stub {
		s.Level = LevelStub
		return s
	}
	if !(warnPct > 0) || math.IsInf(warnPct, 0) {
		warnPct = DefaultWarnPct
	}
	s.Used, s.Week = u.MonthToDate, u.Week
	s.Projected = project(u.MonthToDate, month, now)
	switch {
	case a.Monthly > 0:
		s.Pct = s.Projected / a.Monthly * 100
	case s.Projected > 0:
		s.Pct = 100 // any use of a zero allowance is over
	}
	switch {
	case s.Pct >= 100:
		s.Level = LevelOver
	case s.Pct >= warnPct:
		s.Level = LevelNear
	default:
		s.Level = LevelOK
	}
	return s
}

func project(used float64, month gcpsource.Window, now time.Time) float64 {
	total := month.End.Sub(month.Start)
	elapsed := now.Sub(month.Start)
	if total <= 0 || elapsed <= 0 || elapsed >= total {
		return used
	}
	return used * float64(total) / float64(elapsed)
}

// NoData is the status for an allowance whose metric has no value
// (UsageFromRows returned false). Stub rows stay stub.
func NoData(a Allowance) Status {
	s := Status{ID: a.ID, Allowance: a.Monthly, Level: LevelNoData}
	if a.Stub {
		s.Level = LevelStub
	}
	return s
}

// UsageFromRows sums a's metric over the group-total rows (Resource "") when
// any of them carries a value, otherwise over the resource rows. The Cloud Run
// memory and CPU allowances use the TOTAL columns (MServiceMemGiBs,
// MServiceCPUSec: active + idle min-instance), never the billing active/idle
// split columns. Gap cells are
// skipped. It returns false when no value exists (or a is a stub).
func UsageFromRows(rows []gcpsource.MetricRow, a Allowance) (float64, bool) {
	if a.Stub || a.MetricGroup == "" || a.MetricKey == "" {
		return 0, false
	}
	var totalSum, resSum float64
	var totalOK, resOK bool
	for _, r := range rows {
		if r.Group != a.MetricGroup {
			continue
		}
		c, ok := r.Cells[a.MetricKey]
		if !ok || c.Value == nil {
			continue
		}
		if r.Resource == "" {
			totalSum += *c.Value
			totalOK = true
		} else {
			resSum += *c.Value
			resOK = true
		}
	}
	switch {
	case totalOK:
		return totalSum, true
	case resOK:
		return resSum, true
	}
	return 0, false
}

// ParseOverrides reads limit.<id> (known ids only; finite, ≥ 0) and
// limit.warn_pct (0 < pct ≤ 100, default 80) from settings. The result is
// keyed by id, with the warn threshold under WarnKey (always present).
func ParseOverrides(settings map[string]string) (map[string]float64, error) {
	out := map[string]float64{WarnKey: DefaultWarnPct}
	known := make(map[string]bool, len(defaults))
	for _, a := range defaults {
		known[a.ID] = true
	}
	for k, raw := range settings {
		id, ok := strings.CutPrefix(k, SettingPrefix)
		if !ok || (id != WarnKey && !known[id]) {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("%w: %s=%q is not a number", ErrInvalidOverride, k, raw)
		}
		if id == WarnKey {
			if v <= 0 || v > 100 {
				return nil, fmt.Errorf("%w: %s=%q must be in (0, 100]", ErrInvalidOverride, k, raw)
			}
		} else if v < 0 {
			return nil, fmt.Errorf("%w: %s=%q must not be negative", ErrInvalidOverride, k, raw)
		}
		out[id] = v
	}
	return out, nil
}
