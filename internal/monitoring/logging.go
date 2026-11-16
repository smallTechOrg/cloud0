package monitoring

import (
	"context"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// SourceLogging is the run_sources id of the Cloud Logging family.
const SourceLogging = "monitoring_logging"

const loggingBytesIngestedMetric = "logging.googleapis.com/billing/bytes_ingested"

// FetchLogging reads Cloud Logging ingestion for window w: one row per
// project (Group logging, Resource = project id) whose logging_gb cell is
// logging.googleapis.com/billing/bytes_ingested, ALIGN_SUM over the window
// and REDUCE_SUM across resource types, bytes / 1e9 (unit "GB"). A project
// whose query fails gets a "source failed" cell; one with no series gets
// "no data". Status is the classification of the first error.
func FetchLogging(ctx context.Context, l Lister, projects []string, w gcpsource.Window, now func() time.Time) gcpsource.Result {
	r := gcpsource.Result{Source: SourceLogging, Started: now().UTC()}
	var failures []glProjectErr
	for _, p := range glCleanProjects(projects) {
		q := Query{Project: p, Filter: glMetricFilter(loggingBytesIngestedMetric), Window: w,
			Aligner: monitoringpb.Aggregation_ALIGN_SUM, Reducer: monitoringpb.Aggregation_REDUCE_SUM}
		prov := q.Provenance(SourceLogging, now())
		r.Provenance = append(r.Provenance, prov)
		ts, err := q.Run(ctx, l)
		var cell gcpsource.Cell
		switch {
		case err != nil:
			failures = append(failures, glProjectErr{project: p, metric: q.Filter, err: err})
			_, _, hint := gcpsource.Classify(err, RoleHint, API)
			cell = gcpsource.G("source failed", hint, "GB", prov)
		case len(ts) == 0:
			cell = gcpsource.G("no data", "no log ingestion recorded for this project in the window", "GB", prov)
		default:
			var bytes float64
			for _, s := range ts {
				bytes += Value(s)
			}
			cell = gcpsource.V(bytes/1e9, "GB", prov)
		}
		r.Metrics = append(r.Metrics, gcpsource.MetricRow{Group: gcpsource.GroupLogging, Project: p, Resource: p,
			Cells: map[string]gcpsource.Cell{gcpsource.MLoggingGB: cell}})
	}
	glFinishWithProjectErrors(&r, now(), failures)
	return r
}
