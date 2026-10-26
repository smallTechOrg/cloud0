package errorsrc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/logging/logadmin"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/api/iterator"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/monitoring"
)

// Source ids (run_sources) of the load-balancer and nginx error sources.
const (
	SourceLoadBalancer = "errors_load_balancer"
	SourceNginx        = "errors_nginx"
)

// LBFilter selects the HTTP(S) load-balancer request counts.
const LBFilter = `metric.type="loadbalancing.googleapis.com/https/request_count" AND resource.type="https_lb_rule"`

// Gap reasons and fix hints for the load-balancer and nginx sources.
const (
	LBGap      = "logging disabled"
	LBHint     = "HTTP(S) load-balancer backend service logging is disabled on every backend service — enable logging on the backend service (sample rate ≥ 0.1) to see 4xx/5xx here"
	NginxGap   = "not collected"
	NginxHint  = "nginx access logs are not collected — add the Ops Agent nginx receiver to the VMs"
	NginxFound = "nginx access logs present; per-status counts arrive in a later phase"
)

// Logging error classification.
const (
	LoggingRoleHint = "roles/logging.viewer"
	LoggingAPI      = "logging.googleapis.com"
)

// LBNoURLMap is the Service of requests that matched no URL map.
const LBNoURLMap = "(no url map)"

const (
	lbGroupURLMap = "resource.labels.url_map_name"
	lbGroupClass  = "metric.labels.response_code_class"
)

// LBQuery is the weekly per-URL-map, per-status-class request count query.
func LBQuery(project string, w gcpsource.Window) monitoring.Query {
	return monitoring.Query{
		Project: project,
		Filter:  LBFilter,
		Aligner: monitoringpb.Aggregation_ALIGN_DELTA,
		Reducer: monitoringpb.Aggregation_REDUCE_SUM,
		GroupBy: []string{lbGroupURLMap, lbGroupClass},
		Window:  w,
	}
}

// lbCounts accumulates status-class counts for one URL map. A class absent
// for a URL map that served traffic is a real zero, not a gap.
type lbCounts struct {
	c [4]int64 // 2xx, 3xx, 4xx, 5xx
}

func lbClassIndex(class string) int {
	switch strings.ToLower(strings.TrimSpace(class)) {
	case "200", "2xx":
		return 0
	case "300", "3xx":
		return 1
	case "400", "4xx":
		return 2
	case "500", "5xx":
		return 3
	}
	return -1
}

func lbPtr(v int64) *int64 { return &v }

// FetchLoadBalancer returns one ErrorRow per URL map with weekly 2xx–5xx
// counts. When no project has any series, it returns a single gap row
// explaining how to enable backend-service logging (Status ok — a gap is a
// result, not a failure). A failing project is skipped and noted in Message;
// the Result fails only when every project fails.
func FetchLoadBalancer(ctx context.Context, l monitoring.Lister, projects []string, w gcpsource.Window, now func() time.Time) gcpsource.Result {
	res := gcpsource.Result{Source: SourceLoadBalancer, Started: now().UTC()}
	var (
		firstErr   error
		failedProj []string
		okProjects int
		firstProv  *gcpsource.Provenance
	)
	for _, project := range projects {
		project = strings.TrimSpace(project)
		if project == "" {
			continue
		}
		q := LBQuery(project, w)
		series, err := q.Run(ctx, l)
		p := q.Provenance(SourceLoadBalancer, now())
		res.Provenance = append(res.Provenance, p)
		if firstProv == nil {
			firstProv = &p
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("errorsrc: load balancer in %s: %w", project, err)
			}
			failedProj = append(failedProj, project)
			continue
		}
		okProjects++
		res.Errors = append(res.Errors, lbRows(project, series, p)...)
	}

	if okProjects == 0 && firstErr != nil {
		res.Finish(now().UTC(), firstErr, monitoring.RoleHint, monitoring.API)
		return res
	}
	if len(res.Errors) == 0 {
		var p gcpsource.Provenance
		if firstProv != nil {
			p = *firstProv
		} else {
			p = gcpsource.Provenance{Source: SourceLoadBalancer, Query: LBFilter, Window: w, FetchedAt: now().UTC()}
		}
		project := ""
		if len(projects) == 1 {
			project = strings.TrimSpace(projects[0])
		}
		res.Errors = []gcpsource.ErrorRow{{
			Source: gcpsource.ErrSrcLB, Project: project,
			Gap: LBGap, Hint: LBHint, Provenance: p,
		}}
	}
	if len(failedProj) > 0 {
		_, msg, hint := gcpsource.Classify(firstErr, monitoring.RoleHint, monitoring.API)
		res.Message = fmt.Sprintf("skipped %d project(s) %s: %s", len(failedProj), strings.Join(failedProj, ","), msg)
		res.Hint = hint
	}
	res.Finish(now().UTC(), nil, "", "")
	return res
}

// lbRows folds the series of one project into one row per URL map, sorted.
func lbRows(project string, series []*monitoringpb.TimeSeries, p gcpsource.Provenance) []gcpsource.ErrorRow {
	acc := map[string]*lbCounts{}
	for _, ts := range series {
		// Class "0" (no response sent) has no 2xx–5xx column and is skipped.
		i := lbClassIndex(monitoring.Label(ts, "response_code_class"))
		if i < 0 {
			continue
		}
		name := monitoring.Label(ts, "url_map_name")
		if name == "" {
			// Requests the LB rejected before routing carry no url map /
			// forwarding rule; keep them visible as their own row.
			name = LBNoURLMap
		}
		c := acc[name]
		if c == nil {
			c = &lbCounts{}
			acc[name] = c
		}
		c.c[i] += int64(monitoring.Value(ts) + 0.5)
	}
	names := make([]string, 0, len(acc))
	for n := range acc {
		names = append(names, n)
	}
	sort.Strings(names)
	rows := make([]gcpsource.ErrorRow, 0, len(names))
	for _, n := range names {
		c := acc[n]
		rows = append(rows, gcpsource.ErrorRow{
			Source: gcpsource.ErrSrcLB, Project: project, Service: n,
			C2xx: lbPtr(c.c[0]), C3xx: lbPtr(c.c[1]), C4xx: lbPtr(c.c[2]), C5xx: lbPtr(c.c[3]),
			Provenance: p,
		})
	}
	return rows
}

// LogCounter counts Cloud Logging entries matching filter in project,
// stopping once limit entries have been seen.
type LogCounter interface {
	CountEntries(ctx context.Context, project, filter string, limit int) (int, error)
}

type gcpLogCounter struct{ clients map[string]*logadmin.Client }

// NewLogCounter builds a real LogCounter (one logadmin client per project)
// using Application Default Credentials. Call close when done.
func NewLogCounter(ctx context.Context, projects []string) (LogCounter, func() error, error) {
	g := gcpLogCounter{clients: map[string]*logadmin.Client{}}
	closeAll := func() error {
		var errs []error
		for _, c := range g.clients {
			errs = append(errs, c.Close())
		}
		return errors.Join(errs...)
	}
	for _, p := range projects {
		p = strings.TrimSpace(p)
		if p == "" || g.clients[p] != nil {
			continue
		}
		c, err := logadmin.NewClient(ctx, p)
		if err != nil {
			_ = closeAll()
			return nil, nil, fmt.Errorf("errorsrc: logadmin client for %s: %w", p, err)
		}
		g.clients[p] = c
	}
	return g, closeAll, nil
}

func (g gcpLogCounter) CountEntries(ctx context.Context, project, filter string, limit int) (int, error) {
	c := g.clients[project]
	if c == nil {
		return 0, fmt.Errorf("errorsrc: no logadmin client for project %q", project)
	}
	if limit <= 0 {
		limit = 1
	}
	ps := int32(limit)
	if ps > 1000 {
		ps = 1000
	}
	it := c.Entries(ctx, logadmin.Filter(filter), logadmin.PageSize(ps))
	n := 0
	for n < limit {
		_, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// NginxFilter is the Cloud Logging filter detecting VM web-server access logs in w.
func NginxFilter(w gcpsource.Window) string {
	return fmt.Sprintf(`resource.type="gce_instance" AND (log_id("nginx_access") OR log_id("nginx-access") OR log_id("apache_access")) AND timestamp>="%s" AND timestamp<"%s"`,
		w.Start.UTC().Format(time.RFC3339), w.End.UTC().Format(time.RFC3339))
}

// FetchNginx returns one ErrorRow per project: a "not collected" gap when no
// nginx/apache access log entry exists in the window, otherwise a row with
// nil counts (per-status counts arrive in a later phase). A failing project
// is skipped and noted; the Result fails only when every project fails.
func FetchNginx(ctx context.Context, lc LogCounter, projects []string, w gcpsource.Window, now func() time.Time) gcpsource.Result {
	res := gcpsource.Result{Source: SourceNginx, Started: now().UTC()}
	filter := NginxFilter(w)
	var (
		firstErr   error
		failedProj []string
		okProjects int
		present    int
	)
	for _, project := range projects {
		project = strings.TrimSpace(project)
		if project == "" {
			continue
		}
		n, err := lc.CountEntries(ctx, project, filter, 1)
		p := gcpsource.Provenance{
			Source: SourceNginx, Query: filter,
			Aggregation: fmt.Sprintf(`{"project":%q,"limit":1}`, project),
			Window:      w, FetchedAt: now().UTC(),
		}
		res.Provenance = append(res.Provenance, p)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("errorsrc: nginx logs in %s: %w", project, err)
			}
			failedProj = append(failedProj, project)
			continue
		}
		okProjects++
		row := gcpsource.ErrorRow{Source: gcpsource.ErrSrcNginx, Project: project, Provenance: p}
		if n == 0 {
			row.Gap, row.Hint = NginxGap, NginxHint
		} else {
			present++
		}
		res.Errors = append(res.Errors, row)
	}
	if okProjects == 0 && firstErr != nil {
		res.Finish(now().UTC(), firstErr, LoggingRoleHint, LoggingAPI)
		return res
	}
	var msgs []string
	if present > 0 {
		msgs = append(msgs, NginxFound)
	}
	if len(failedProj) > 0 {
		_, msg, hint := gcpsource.Classify(firstErr, LoggingRoleHint, LoggingAPI)
		msgs = append(msgs, fmt.Sprintf("skipped %d project(s) %s: %s", len(failedProj), strings.Join(failedProj, ","), msg))
		res.Hint = hint
	}
	res.Message = strings.Join(msgs, "; ")
	res.Finish(now().UTC(), nil, "", "")
	return res
}
