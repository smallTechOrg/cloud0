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

// SourceCloudSQL is the run_sources id of the Cloud SQL family.
const SourceCloudSQL = "monitoring_cloudsql"

const (
	sqlCPUUtil     = "cloudsql.googleapis.com/database/cpu/utilization"
	sqlMemUsage    = "cloudsql.googleapis.com/database/memory/total_usage"
	sqlMemQuota    = "cloudsql.googleapis.com/database/memory/quota"
	sqlDiskUtil    = "cloudsql.googleapis.com/database/disk/utilization"
	sqlResourceTyp = "cloudsql_database"
)

// cloudSQLMetric is one cell: Num (a 0..1 ratio) or Num/Denom, ×100.
//
// Memory is total_usage ÷ quota, not database/memory/utilization: on
// shared-core tiers that metric reports ~1.0 all week (usage == quota), while
// total_usage/quota tracks the instance's real memory use as the sheet does.
type cloudSQLMetric struct {
	Key   string
	Num   string
	Denom string // "" when Num is already a ratio
}

// cloudSQLMetrics is the column order of the Cloud SQL rows.
var cloudSQLMetrics = []cloudSQLMetric{
	{Key: gcpsource.MCPU, Num: sqlCPUUtil},
	{Key: gcpsource.MMemory, Num: sqlMemUsage, Denom: sqlMemQuota},
	{Key: gcpsource.MDisk, Num: sqlDiskUtil},
}

// cloudSQLTypes is every metric type queried per project, in call order.
var cloudSQLTypes = []string{sqlCPUUtil, sqlMemUsage, sqlMemQuota, sqlDiskUtil}

// cloudSQLQuery builds the weekly ALIGN_MEAN query for one metric in one project.
func cloudSQLQuery(project, metricType string, w gcpsource.Window) Query {
	return Query{
		Project: project,
		Filter:  fmt.Sprintf(`metric.type = %q AND resource.type = %q`, metricType, sqlResourceTyp),
		Aligner: monitoringpb.Aggregation_ALIGN_MEAN,
		Reducer: monitoringpb.Aggregation_REDUCE_NONE,
		Window:  w,
	}
}

// cloudSQLInstance returns the instance name from a series: resource label
// database_id is "<project>:<instance>" (domain-scoped projects carry an
// extra "domain:" prefix), so the name is everything after the last colon.
func cloudSQLInstance(ts *monitoringpb.TimeSeries) string {
	id := Label(ts, "database_id")
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// sqlMeanOfPoints returns the mean of a series' point values and whether any
// point exists. With the window-wide alignment there is exactly one point.
func sqlMeanOfPoints(ts *monitoringpb.TimeSeries) (float64, bool) {
	n := len(ts.GetPoints())
	if n == 0 {
		return 0, false
	}
	return Value(ts) / float64(n), true
}

// sqlMean accumulates the per-instance mean of one metric type.
type sqlMean struct {
	sum float64
	n   int
}

// FetchCloudSQL returns one MetricRow per Cloud SQL instance with weekly mean
// CPU, memory and disk utilisation in percent. A metric with no series for an
// instance is a "no data" gap; a failed query is a "source failed" gap. A
// project whose queries all fail is skipped (noted in Message/Hint) without
// failing the others; the Result fails only when every project fails.
func FetchCloudSQL(ctx context.Context, l Lister, projects []string, w gcpsource.Window, now func() time.Time) gcpsource.Result {
	res := gcpsource.Result{Source: SourceCloudSQL, Started: now().UTC()}

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
		provs := make(map[string]gcpsource.Provenance, len(cloudSQLTypes))
		errs := make(map[string]error, len(cloudSQLTypes))
		means := map[string]map[string]*sqlMean{} // instance → metric type → mean
		var instances []string
		for _, mt := range cloudSQLTypes {
			q := cloudSQLQuery(project, mt, w)
			series, err := q.Run(ctx, l)
			p := q.Provenance(SourceCloudSQL, now())
			provs[mt] = p
			res.Provenance = append(res.Provenance, p)
			if err != nil {
				errs[mt] = fmt.Errorf("monitoring: cloudsql %s in %s: %w", mt, project, err)
				continue
			}
			for _, ts := range series {
				name := cloudSQLInstance(ts)
				v, ok := sqlMeanOfPoints(ts)
				if name == "" || !ok {
					continue
				}
				if means[name] == nil {
					means[name] = map[string]*sqlMean{}
					instances = append(instances, name)
				}
				m := means[name][mt]
				if m == nil {
					m = &sqlMean{}
					means[name][mt] = m
				}
				m.sum += v
				m.n++
			}
		}

		if len(errs) == len(cloudSQLTypes) {
			err := errs[cloudSQLTypes[0]]
			if firstErr == nil {
				firstErr = err
			}
			st, _, _ := gcpsource.Classify(err, RoleHint, API)
			failedProj = append(failedProj, project+" ("+st+")")
			continue
		}
		okProjects++
		if firstErr == nil {
			for _, mt := range cloudSQLTypes {
				if errs[mt] != nil {
					firstErr = errs[mt]
					break
				}
			}
		}

		sort.Strings(instances)
		for _, name := range instances {
			row := gcpsource.MetricRow{
				Group:    gcpsource.GroupCloudSQL,
				Project:  project,
				Resource: name,
				Cells:    make(map[string]gcpsource.Cell, len(cloudSQLMetrics)),
			}
			for _, m := range cloudSQLMetrics {
				row.Cells[m.Key] = cloudSQLCell(m, means[name], errs, provs)
			}
			res.Metrics = append(res.Metrics, row)
		}
	}

	if okProjects == 0 && firstErr != nil {
		res.Finish(now().UTC(), firstErr, RoleHint, API)
		return res
	}
	if firstErr != nil {
		_, msg, hint := gcpsource.Classify(firstErr, RoleHint, API)
		if len(failedProj) > 0 {
			msg = "skipped projects: " + strings.Join(failedProj, ", ") + "; " + msg
		}
		res.Message, res.Hint = msg, hint
	}
	res.Finish(now().UTC(), nil, RoleHint, API)
	return res
}

// cloudSQLCell computes one cell from the per-type means of an instance.
func cloudSQLCell(m cloudSQLMetric, means map[string]*sqlMean, errs map[string]error, provs map[string]gcpsource.Provenance) gcpsource.Cell {
	prov := provs[m.Num]
	if m.Denom != "" {
		prov.Query = prov.Query + " / " + provs[m.Denom].Query
	}
	for _, mt := range []string{m.Num, m.Denom} {
		if mt != "" && errs[mt] != nil {
			_, _, hint := gcpsource.Classify(errs[mt], RoleHint, API)
			return gcpsource.G("source failed", hint, "%", prov)
		}
	}
	num := means[m.Num]
	if num == nil || num.n == 0 {
		return gcpsource.G("no data", "", "%", prov)
	}
	v := num.sum / float64(num.n)
	if m.Denom != "" {
		den := means[m.Denom]
		if den == nil || den.n == 0 || den.sum == 0 {
			return gcpsource.G("no data", "", "%", prov)
		}
		v /= den.sum / float64(den.n)
	}
	return gcpsource.V(v*100, "%", prov)
}
