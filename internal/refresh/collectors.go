package refresh

import (
	"context"
	"errors"
	"time"

	"cloud.google.com/go/bigquery"

	"github.com/smallTechOrg/cloud0/internal/billingusage"
	"github.com/smallTechOrg/cloud0/internal/errorsrc"
	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/monitoring"
	"github.com/smallTechOrg/cloud0/internal/monthcost"
)

// MaxConcurrency bounds how many week-window sources run at once.
const MaxConcurrency = 4

// SourceTimeout bounds one week-window source (spec: 120 s per source).
const SourceTimeout = 120 * time.Second

// Owned names the Metrics-table columns of one group that a collector fills.
// When the collector fails, exactly these cells show "source failed".
type Owned struct {
	Group   string
	Columns []string
}

// Collector is one week-window source (a run_sources row of its own).
type Collector struct {
	Name string
	// Fetch runs the source for the window. It must not panic or return an
	// error: failures are classified into the Result (gcpsource.Result.Finish).
	Fetch func(ctx context.Context, w gcpsource.Window) gcpsource.Result
	// Owns lists the metric cells this source produces (empty for error sources).
	Owns []Owned
	// ErrorSource is the gcpsource.ErrSrc* id of an error source ("" for metrics).
	ErrorSource string
}

// MonthSource fetches the calendar-month cost rows for the inclusive range.
type MonthSource interface {
	FetchMonths(ctx context.Context, from, to monthcost.Month) ([]monthcost.Row, gcpsource.Provenance, error)
}

// BQMonthSource is the real month source over the billing export.
type BQMonthSource struct {
	Client     *bigquery.Client
	Table      string
	MarginDays int
	Now        func() time.Time
}

// FetchMonths implements MonthSource via monthcost.Fetch.
func (b BQMonthSource) FetchMonths(ctx context.Context, from, to monthcost.Month) ([]monthcost.Row, gcpsource.Provenance, error) {
	return monthcost.Fetch(ctx, b.Client, b.Table, from, to, b.MarginDays, b.Now)
}

// Sources are the real clients a StandardCollectors set is built from. Any
// client may be nil (with its construction error in the matching *Err field):
// that source then fails with the classified error instead of being omitted.
type Sources struct {
	Projects []string
	Now      func() time.Time

	Monitoring    monitoring.Lister
	MonitoringErr error
	ErrReporting  errorsrc.GroupStatsLister
	ErrReportErr  error
	Logs          errorsrc.LogCounter
	LogsErr       error
	BigQuery      *bigquery.Client
	BigQueryErr   error
	BillingTable  string
	MarginDays    int
}

func (s Sources) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func unavailable(name string, err error, fallback, roleHint, api string, now time.Time) gcpsource.Result {
	if err == nil {
		err = errors.New(fallback)
	}
	r := gcpsource.Result{Source: name, Started: now}
	r.Finish(now, err, roleHint, api)
	return r
}

func columns(group string, keys ...string) Owned { return Owned{Group: group, Columns: keys} }

// StandardCollectors returns the ten week-window sources in the contract
// order: monitoring_vm, monitoring_cloudsql, monitoring_cloudrun,
// monitoring_gcs, monitoring_logging, bigquery_usage, errors_cloudrun,
// errors_error_reporting, errors_load_balancer, errors_nginx.
func StandardCollectors(s Sources) []Collector {
	mon := func(name string, owns []Owned, f func(context.Context, monitoring.Lister, []string, gcpsource.Window, func() time.Time) gcpsource.Result) Collector {
		return Collector{Name: name, Owns: owns, Fetch: func(ctx context.Context, w gcpsource.Window) gcpsource.Result {
			if s.Monitoring == nil {
				return unavailable(name, s.MonitoringErr, "Cloud Monitoring client unavailable", monitoring.RoleHint, monitoring.API, s.now())
			}
			return f(ctx, s.Monitoring, s.Projects, w, s.now)
		}}
	}
	errMon := func(name, src string, f func(context.Context, monitoring.Lister, []string, gcpsource.Window, func() time.Time) gcpsource.Result) Collector {
		c := mon(name, nil, f)
		c.ErrorSource = src
		return c
	}
	util := []string{gcpsource.MCPU, gcpsource.MMemory, gcpsource.MDisk}
	return []Collector{
		mon("monitoring_vm", []Owned{columns(gcpsource.GroupVM, util...)}, monitoring.FetchVM),
		mon("monitoring_cloudsql", []Owned{columns(gcpsource.GroupCloudSQL, util...)}, monitoring.FetchCloudSQL),
		mon("monitoring_cloudrun", []Owned{columns(gcpsource.GroupCloudRun, gcpsource.MServiceMemGiBs, gcpsource.MRequests, gcpsource.MServiceCPUSec)}, monitoring.FetchCloudRun),
		mon("monitoring_gcs", []Owned{columns(gcpsource.GroupGCS, gcpsource.MClassAOps, gcpsource.MClassBOps, gcpsource.MSentGB, gcpsource.MStorageGBMonth)}, monitoring.FetchGCS),
		mon("monitoring_logging", []Owned{columns(gcpsource.GroupLogging, gcpsource.MLoggingGB)}, monitoring.FetchLogging),
		{Name: billingusage.Source, Owns: []Owned{
			columns(gcpsource.GroupNetworking, gcpsource.MLBHours, gcpsource.MOutboundGB, gcpsource.MInboundGB),
			columns(gcpsource.GroupCloudRun, gcpsource.MEgressInterGB),
			columns(gcpsource.GroupGCS, gcpsource.MDownloadAPACGB),
		}, Fetch: func(ctx context.Context, w gcpsource.Window) gcpsource.Result {
			if s.BigQuery == nil {
				return unavailable(billingusage.Source, s.BigQueryErr, "BigQuery client unavailable", billingusage.RoleHint, billingusage.API, s.now())
			}
			return billingusage.Fetch(ctx, s.BigQuery, s.BillingTable, w, s.MarginDays, s.now)
		}},
		errMon("errors_cloudrun", gcpsource.ErrSrcCloudRun, errorsrc.FetchCloudRun),
		{Name: "errors_error_reporting", ErrorSource: gcpsource.ErrSrcErrorReporting, Fetch: func(ctx context.Context, w gcpsource.Window) gcpsource.Result {
			if s.ErrReporting == nil {
				return unavailable("errors_error_reporting", s.ErrReportErr, "Error Reporting client unavailable", "roles/errorreporting.viewer", "clouderrorreporting.googleapis.com", s.now())
			}
			return errorsrc.FetchErrorReporting(ctx, s.ErrReporting, s.Projects, w, s.now)
		}},
		errMon("errors_load_balancer", gcpsource.ErrSrcLB, errorsrc.FetchLoadBalancer),
		{Name: "errors_nginx", ErrorSource: gcpsource.ErrSrcNginx, Fetch: func(ctx context.Context, w gcpsource.Window) gcpsource.Result {
			if s.Logs == nil {
				return unavailable("errors_nginx", s.LogsErr, "Cloud Logging client unavailable", "roles/logging.viewer", "logging.googleapis.com", s.now())
			}
			return errorsrc.FetchNginx(ctx, s.Logs, s.Projects, w, s.now)
		}},
	}
}
