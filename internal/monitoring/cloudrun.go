package monitoring

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// SourceCloudRun is the run_sources id of the Cloud Run family.
const SourceCloudRun = "monitoring_cloudrun"

// cloudRunMetric is one Cloud Run column. zeroIfMissing marks a counter that
// is genuinely 0 for a service seen in another metric of the same project.
type cloudRunMetric struct {
	Key           string
	Type          string
	Unit          string
	zeroIfMissing bool
}

// cloudRunMetrics are the Cloud Run columns in the sheet's order (the
// intercontinental-egress column comes from billing usage, not from here).
// Monitoring already reports memory allocation in GiB·s (GiBy.s) and CPU
// allocation in vCPU·s (s{CPU}), so values are used as-is.
var cloudRunMetrics = []cloudRunMetric{
	{Key: gcpsource.MServiceMemGiBs, Type: "run.googleapis.com/container/memory/allocation_time", Unit: "GiB-s"},
	{Key: gcpsource.MRequests, Type: "run.googleapis.com/request_count", Unit: "count", zeroIfMissing: true},
	{Key: gcpsource.MServiceCPUSec, Type: "run.googleapis.com/container/cpu/allocation_time", Unit: "vCPU-s"},
}

func cloudRunQuery(project, metricType string, w gcpsource.Window) Query {
	return Query{
		Project: project,
		Filter:  `metric.type="` + metricType + `" AND resource.type="cloud_run_revision"`,
		Aligner: monitoringpb.Aggregation_ALIGN_DELTA,
		Reducer: monitoringpb.Aggregation_REDUCE_SUM,
		GroupBy: []string{"resource.labels.service_name"},
		Window:  w,
	}
}

// FetchCloudRun returns, per project, one MetricRow per Cloud Run service
// (sorted by name) followed by a group-total row (Resource "") whose cells
// are sums across that project's services. A project where every query
// fails is skipped and named in Message; if all projects fail the Result
// carries the classified status (e.g. permission_denied + role hint).
func FetchCloudRun(ctx context.Context, l Lister, projects []string, w gcpsource.Window, now func() time.Time) gcpsource.Result {
	res := gcpsource.Result{Source: SourceCloudRun, Started: now().UTC()}

	var (
		firstErr   error
		failedProj []string
		okProjects int
	)
	for _, project := range projects {
		project = strings.TrimSpace(project)
		if project == "" {
			continue
		}
		provs := make(map[string]gcpsource.Provenance, len(cloudRunMetrics))
		errs := make(map[string]error, len(cloudRunMetrics))
		vals := map[string]map[string]float64{} // service -> metric key -> value
		for _, m := range cloudRunMetrics {
			q := cloudRunQuery(project, m.Type, w)
			series, err := q.Run(ctx, l)
			p := q.Provenance(SourceCloudRun, now())
			provs[m.Key] = p
			res.Provenance = append(res.Provenance, p)
			if err != nil {
				errs[m.Key] = fmt.Errorf("monitoring: cloudrun %s in %s: %w", m.Type, project, err)
				continue
			}
			for _, ts := range series {
				if len(ts.GetPoints()) == 0 {
					continue
				}
				svc := Label(ts, "service_name")
				if svc == "" {
					continue
				}
				if vals[svc] == nil {
					vals[svc] = map[string]float64{}
				}
				vals[svc][m.Key] += Value(ts)
			}
		}

		if len(errs) == len(cloudRunMetrics) {
			err := errs[cloudRunMetrics[0].Key]
			if firstErr == nil {
				firstErr = err
			}
			st, _, _ := gcpsource.Classify(err, RoleHint, API)
			failedProj = append(failedProj, project+" ("+st+")")
			continue
		}
		okProjects++
		if firstErr == nil {
			for _, m := range cloudRunMetrics {
				if errs[m.Key] != nil {
					firstErr = errs[m.Key]
					break
				}
			}
		}

		services := make([]string, 0, len(vals))
		for s := range vals {
			services = append(services, s)
		}
		sort.Strings(services)

		sums := map[string]float64{}
		have := map[string]bool{}
		for _, svc := range services {
			row := gcpsource.MetricRow{
				Group:    gcpsource.GroupCloudRun,
				Project:  project,
				Resource: svc,
				Cells:    make(map[string]gcpsource.Cell, len(cloudRunMetrics)),
			}
			for _, m := range cloudRunMetrics {
				v, ok := vals[svc][m.Key]
				switch {
				case errs[m.Key] != nil:
					_, _, hint := gcpsource.Classify(errs[m.Key], RoleHint, API)
					row.Cells[m.Key] = gcpsource.G("source failed", hint, m.Unit, provs[m.Key])
				case ok || m.zeroIfMissing:
					row.Cells[m.Key] = gcpsource.V(v, m.Unit, provs[m.Key])
					sums[m.Key] += v
					have[m.Key] = true
				default:
					row.Cells[m.Key] = gcpsource.G("no data", "", m.Unit, provs[m.Key])
				}
			}
			res.Metrics = append(res.Metrics, row)
		}

		total := gcpsource.MetricRow{
			Group:   gcpsource.GroupCloudRun,
			Project: project,
			Cells:   make(map[string]gcpsource.Cell, len(cloudRunMetrics)),
		}
		for _, m := range cloudRunMetrics {
			switch {
			case errs[m.Key] != nil:
				_, _, hint := gcpsource.Classify(errs[m.Key], RoleHint, API)
				total.Cells[m.Key] = gcpsource.G("source failed", hint, m.Unit, provs[m.Key])
			case have[m.Key]:
				total.Cells[m.Key] = gcpsource.V(sums[m.Key], m.Unit, provs[m.Key])
			default:
				total.Cells[m.Key] = gcpsource.G("no data", "", m.Unit, provs[m.Key])
			}
		}
		res.Metrics = append(res.Metrics, total)
	}

	if okProjects == 0 && firstErr != nil {
		res.Finish(now().UTC(), firstErr, RoleHint, API)
		return res
	}
	if firstErr != nil {
		_, msg, hint := gcpsource.Classify(firstErr, RoleHint, API)
		if len(failedProj) > 0 {
			res.Message = "skipped projects: " + strings.Join(failedProj, ", ") + "; " + msg
		} else {
			res.Message = msg
		}
		res.Hint = hint
	}
	res.Finish(now().UTC(), nil, RoleHint, API)
	return res
}
