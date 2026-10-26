// Package gcpsource is the frozen Phase 2 contract shared by every data
// source (Cloud Monitoring families, error sources, billing usage, month
// cost). Sources return these types; the store/API slice persists them.
// Do not change these types inside a source slice — raise it instead.
package gcpsource

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Window is a half-open UTC interval [Start, End).
type Window struct {
	Start time.Time
	End   time.Time
}

// Hours returns the window length in hours.
func (w Window) Hours() float64 { return w.End.Sub(w.Start).Hours() }

// Provenance records exactly what produced a value, for cell inspection.
type Provenance struct {
	Source      string // e.g. "monitoring_vm", "bigquery_usage"
	Query       string // exact Monitoring filter string or SQL text
	Aggregation string // JSON of the aggregation (Monitoring) or params (SQL)
	Window      Window
	FetchedAt   time.Time
}

// Cell is one value in a metrics/errors table. Exactly one of Value or Gap
// is set: a missing measurement is a Gap with a reason, never a zero.
type Cell struct {
	Value      *float64
	Unit       string // "%", "GB", "GiB-s", "vCPU-s", "count", "GB-month", "hours"
	Gap        string // e.g. "no agent", "no data", "source failed", "not collected"
	Hint       string // actionable fix, shown with the gap
	Provenance Provenance
}

// V returns a Cell holding v.
func V(v float64, unit string, p Provenance) Cell { return Cell{Value: &v, Unit: unit, Provenance: p} }

// G returns a gap Cell.
func G(gap, hint, unit string, p Provenance) Cell {
	return Cell{Gap: gap, Hint: hint, Unit: unit, Provenance: p}
}

// Row groups, in the sheet's Metrics-tab order.
const (
	GroupVM         = "vm"
	GroupCloudSQL   = "cloudsql"
	GroupNetworking = "networking"
	GroupCloudRun   = "cloudrun"
	GroupGCS        = "gcs"
	GroupLogging    = "logging"
	GroupErrors     = "errors"
)

// Metric keys (column ids). The UI maps them to the sheet's headings.
const (
	MCPU            = "cpu_pct"
	MMemory         = "memory_pct"
	MDisk           = "disk_pct"
	MLBHours        = "lb_hours"
	MOutboundGB     = "outbound_gb"
	MInboundGB      = "inbound_gb"
	MServiceMemGiBs = "service_memory_gib_s"
	MRequests       = "requests"
	MServiceCPUSec  = "service_cpu_sec"
	MEgressInterGB  = "egress_intercontinental_gb"
	MDownloadAPACGB = "download_apac_gb"
	MClassAOps      = "class_a_ops"
	MClassBOps      = "class_b_ops"
	MStorageGBMonth = "storage_gb_month"
	MSentGB         = "sent_gb"
	MLoggingGB      = "logging_gb"
)

// MetricRow is one resource row of the Metrics block.
type MetricRow struct {
	Group    string // Group* constant
	Project  string // GCP project id
	Resource string // VM name, Cloud SQL instance, Cloud Run service, bucket, or "" for a group total
	Cells    map[string]Cell
}

// Error source ids.
const (
	ErrSrcCloudRun       = "cloudrun"
	ErrSrcErrorReporting = "error_reporting"
	ErrSrcLB             = "load_balancer"
	ErrSrcNginx          = "nginx"
)

// ErrorRow is one row of the Errors table. Counts are nil when Gap is set.
type ErrorRow struct {
	Source      string // ErrSrc* constant
	Project     string
	Service     string // Cloud Run service, Error Reporting service, URL map, VM
	C2xx        *int64
	C3xx        *int64
	C4xx        *int64
	C5xx        *int64
	Groups      *int64 // Error Reporting: distinct groups
	Occurrences *int64 // Error Reporting: total occurrences
	Gap         string
	Hint        string
	Provenance  Provenance
}

// Status of one source run, recorded as a run_sources row.
const (
	StatusOK               = "ok"
	StatusFailed           = "failed"
	StatusPermissionDenied = "permission_denied"
	StatusAPIDisabled      = "api_disabled"
	StatusQuota            = "quota"
)

// Result is what every source fetch returns, success or not.
type Result struct {
	Source     string
	Status     string
	Message    string
	Hint       string
	Started    time.Time
	Finished   time.Time
	Provenance []Provenance
	Metrics    []MetricRow
	Errors     []ErrorRow
}

// Finish stamps the result; if err != nil it is classified with roleHint and api.
func (r *Result) Finish(now time.Time, err error, roleHint, api string) {
	r.Finished = now
	if err == nil {
		if r.Status == "" {
			r.Status = StatusOK
		}
		return
	}
	r.Status, r.Message, r.Hint = Classify(err, roleHint, api)
}

// Classify maps a Google API error to (status, message, hint).
// roleHint e.g. "roles/monitoring.viewer"; api e.g. "monitoring.googleapis.com".
func Classify(err error, roleHint, api string) (string, string, string) {
	if err == nil {
		return StatusOK, "", ""
	}
	msg := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusFailed, msg, "the request timed out; retry the refresh"
	}
	code := codes.Unknown
	if s, ok := status.FromError(err); ok {
		code = s.Code()
	}
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(msg, "SERVICE_DISABLED") || strings.Contains(low, "has not been used in project") || strings.Contains(low, "is disabled"):
		return StatusAPIDisabled, msg, "enable " + api + " on the project (gcloud services enable " + api + ")"
	case code == codes.PermissionDenied || strings.Contains(low, "permission") || strings.Contains(msg, "403"):
		return StatusPermissionDenied, msg, "grant the service account " + roleHint
	case code == codes.ResourceExhausted || strings.Contains(msg, "429") || strings.Contains(low, "quota"):
		return StatusQuota, msg, "quota exceeded; wait a minute and retry"
	}
	return StatusFailed, msg, ""
}
