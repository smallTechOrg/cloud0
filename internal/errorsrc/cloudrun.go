package errorsrc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/monitoring"
)

// SourceCloudRun is the run_sources id of the Cloud Run status-class source.
const SourceCloudRun = "errors_cloudrun"

// CloudRunFilter selects Cloud Run request counts (all revisions).
const CloudRunFilter = `metric.type="run.googleapis.com/request_count" AND resource.type="cloud_run_revision"`

// Group-by fields of the per-service, per-class query.
const (
	crGroupService = "resource.labels.service_name"
	crGroupClass   = "metric.labels.response_code_class"
)

// CloudRunQuery is the window-aligned request_count query for one project,
// grouped by groupBy (nil = one ungrouped total series).
func CloudRunQuery(project string, w gcpsource.Window, groupBy []string) monitoring.Query {
	return monitoring.Query{
		Project: project,
		Filter:  CloudRunFilter,
		Aligner: monitoringpb.Aggregation_ALIGN_DELTA,
		Reducer: monitoringpb.Aggregation_REDUCE_SUM,
		GroupBy: groupBy,
		Window:  w,
	}
}

type crClassCounts struct{ c2, c3, c4, c5 int64 }

func (c *crClassCounts) add(class string, n int64) bool {
	switch class {
	case "2xx":
		c.c2 += n
	case "3xx":
		c.c3 += n
	case "4xx":
		c.c4 += n
	case "5xx":
		c.c5 += n
	default:
		return false
	}
	return true
}

func crI64(v int64) *int64 { return &v }

func (c crClassCounts) row(project, service string, p gcpsource.Provenance) gcpsource.ErrorRow {
	return gcpsource.ErrorRow{
		Source: gcpsource.ErrSrcCloudRun, Project: project, Service: service,
		C2xx: crI64(c.c2), C3xx: crI64(c.c3), C4xx: crI64(c.c4), C5xx: crI64(c.c5),
		Provenance: p,
	}
}

// FetchCloudRun returns one ErrorRow per Cloud Run service with its 2xx/3xx/
// 4xx/5xx request counts for w, plus a per-project totals row (Service "").
// A class with no data is 0 for a service that has any data; a project with
// no Cloud Run data yields no rows. A project whose query fails yields a
// "source failed" gap row (Service ""); the Result fails (classified with the
// Monitoring role/API) only when every project fails, otherwise it is ok and
// Message/Hint name the skipped projects — matching the sibling sources.
func FetchCloudRun(ctx context.Context, l monitoring.Lister, projects []string, w gcpsource.Window, now func() time.Time) gcpsource.Result {
	res := gcpsource.Result{Source: SourceCloudRun, Started: now().UTC()}
	var firstErr error
	var failed []string
	ok := 0
	for _, project := range projects {
		q := CloudRunQuery(project, w, []string{crGroupService, crGroupClass})
		series, err := q.Run(ctx, l)
		prov := q.Provenance(SourceCloudRun, now())
		res.Provenance = append(res.Provenance, prov)
		if err != nil {
			err = fmt.Errorf("errorsrc: cloud run request_count for project %s: %w", project, err)
			_, _, hint := gcpsource.Classify(err, monitoring.RoleHint, monitoring.API)
			res.Errors = append(res.Errors, gcpsource.ErrorRow{
				Source: gcpsource.ErrSrcCloudRun, Project: project,
				Gap: "source failed", Hint: hint, Provenance: prov,
			})
			if firstErr == nil {
				firstErr = err
			}
			failed = append(failed, project)
			continue
		}
		ok++
		res.Errors = append(res.Errors, cloudRunRows(project, series, prov)...)
	}
	if ok == 0 && firstErr != nil {
		res.Finish(now().UTC(), firstErr, monitoring.RoleHint, monitoring.API)
		return res
	}
	if firstErr != nil {
		_, msg, hint := gcpsource.Classify(firstErr, monitoring.RoleHint, monitoring.API)
		res.Message = "skipped projects: " + strings.Join(failed, ", ") + "; " + msg
		res.Hint = hint
	}
	res.Finish(now().UTC(), nil, monitoring.RoleHint, monitoring.API)
	return res
}

// cloudRunRows folds grouped series into per-service rows (sorted by service)
// followed by the project totals row. Unknown classes are counted nowhere.
func cloudRunRows(project string, series []*monitoringpb.TimeSeries, prov gcpsource.Provenance) []gcpsource.ErrorRow {
	byService := map[string]*crClassCounts{}
	for _, ts := range series {
		svc := monitoring.Label(ts, "service_name")
		c, ok := byService[svc]
		if !ok {
			c = &crClassCounts{}
			byService[svc] = c
		}
		c.add(monitoring.Label(ts, "response_code_class"), int64(math.Round(monitoring.Value(ts))))
	}
	if len(byService) == 0 {
		return nil
	}
	names := make([]string, 0, len(byService))
	for s := range byService {
		names = append(names, s)
	}
	sort.Strings(names)
	var total crClassCounts
	rows := make([]gcpsource.ErrorRow, 0, len(names)+1)
	for _, s := range names {
		c := *byService[s]
		total.c2 += c.c2
		total.c3 += c.c3
		total.c4 += c.c4
		total.c5 += c.c5
		rows = append(rows, c.row(project, s, prov))
	}
	return append(rows, total.row(project, "", prov))
}
