package errorsrc

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/genproto/googleapis/api/metric"
	"google.golang.org/genproto/googleapis/api/monitoredres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/monitoring"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

var gapsW42 = gcpsource.Window{
	Start: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
}

func gapsNow() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }

type gapsFakeLister struct {
	series map[string][]*monitoringpb.TimeSeries
	errs   map[string]error
	reqs   []*monitoringpb.ListTimeSeriesRequest
}

func (f *gapsFakeLister) ListTimeSeries(_ context.Context, req *monitoringpb.ListTimeSeriesRequest) ([]*monitoringpb.TimeSeries, error) {
	f.reqs = append(f.reqs, req)
	p := strings.TrimPrefix(req.GetName(), "projects/")
	if err := f.errs[p]; err != nil {
		return nil, err
	}
	return f.series[p], nil
}

func lbSeries(urlMap, class string, v int64) *monitoringpb.TimeSeries {
	return &monitoringpb.TimeSeries{
		Metric:   &metric.Metric{Labels: map[string]string{"response_code_class": class}},
		Resource: &monitoredres.MonitoredResource{Labels: map[string]string{"url_map_name": urlMap}},
		Points: []*monitoringpb.Point{{Value: &monitoringpb.TypedValue{
			Value: &monitoringpb.TypedValue_Int64Value{Int64Value: v}}}},
	}
}

func deref(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

func TestFetchLoadBalancer_MapsSeriesOrGap(t *testing.T) {
	denied := status.Error(codes.PermissionDenied, "permission denied on project")
	tests := []struct {
		name       string
		projects   []string
		series     map[string][]*monitoringpb.TimeSeries
		errs       map[string]error
		wantStatus string
		wantRows   map[string][4]int64 // url map -> 2xx..5xx
		wantGap    bool
		wantMsg    string
	}{
		{
			name:     "series produce one row per url map with class counts",
			projects: []string{"p1"},
			series: map[string][]*monitoringpb.TimeSeries{"p1": {
				lbSeries("web-map", "200", 1000), lbSeries("web-map", "400", 40),
				lbSeries("web-map", "500", 5), lbSeries("api-map", "300", 7),
				lbSeries("api-map", "0", 3), // no-response class is ignored
			}},
			wantStatus: gcpsource.StatusOK,
			wantRows:   map[string][4]int64{"web-map": {1000, 0, 40, 5}, "api-map": {0, 7, 0, 0}},
		},
		{
			name:       "no series anywhere is a single gap row, status ok",
			projects:   []string{"p1", "p2"},
			series:     map[string][]*monitoringpb.TimeSeries{},
			wantStatus: gcpsource.StatusOK,
			wantGap:    true,
		},
		{
			name:       "every project failing is a classified failure",
			projects:   []string{"p1"},
			errs:       map[string]error{"p1": denied},
			wantStatus: gcpsource.StatusPermissionDenied,
		},
		{
			name:     "one failing project is skipped and noted",
			projects: []string{"p1", "p2"},
			series:   map[string][]*monitoringpb.TimeSeries{"p2": {lbSeries("m", "500", 2)}},
			errs:     map[string]error{"p1": denied},

			wantStatus: gcpsource.StatusOK,
			wantRows:   map[string][4]int64{"m": {0, 0, 0, 2}},
			wantMsg:    "skipped 1 project(s) p1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &gapsFakeLister{series: tc.series, errs: tc.errs}
			res := FetchLoadBalancer(context.Background(), f, tc.projects, gapsW42, gapsNow)
			if res.Source != "errors_load_balancer" {
				t.Fatalf("source = %q", res.Source)
			}
			if res.Status != tc.wantStatus {
				t.Fatalf("status = %q (%s), want %q", res.Status, res.Message, tc.wantStatus)
			}
			if len(f.reqs) != len(tc.projects) {
				t.Fatalf("requests = %d, want %d", len(f.reqs), len(tc.projects))
			}
			req := f.reqs[0]
			agg := req.GetAggregation()
			if req.GetFilter() != LBFilter || agg.GetPerSeriesAligner() != monitoringpb.Aggregation_ALIGN_DELTA ||
				agg.GetCrossSeriesReducer() != monitoringpb.Aggregation_REDUCE_SUM ||
				strings.Join(agg.GetGroupByFields(), ",") != "resource.labels.url_map_name,metric.labels.response_code_class" {
				t.Fatalf("unexpected request: %v", req)
			}
			if !strings.Contains(res.Message, tc.wantMsg) {
				t.Fatalf("message = %q, want contains %q", res.Message, tc.wantMsg)
			}
			if tc.wantStatus != gcpsource.StatusOK {
				if len(res.Errors) != 0 || res.Hint != "grant the service account roles/monitoring.viewer" {
					t.Fatalf("failure: rows=%d hint=%q", len(res.Errors), res.Hint)
				}
				return
			}
			if tc.wantGap {
				if len(res.Errors) != 1 {
					t.Fatalf("rows = %d, want 1 gap", len(res.Errors))
				}
				r := res.Errors[0]
				if r.Source != gcpsource.ErrSrcLB || r.Gap != "logging disabled" || r.Hint != LBHint ||
					r.C4xx != nil || r.C5xx != nil || r.Provenance.Query != LBFilter {
					t.Fatalf("gap row = %+v", r)
				}
				if !strings.Contains(r.Hint, "backend service") {
					t.Fatalf("hint lacks 'backend service': %q", r.Hint)
				}
				return
			}
			if len(res.Errors) != len(tc.wantRows) {
				t.Fatalf("rows = %d, want %d", len(res.Errors), len(tc.wantRows))
			}
			for _, r := range res.Errors {
				want, ok := tc.wantRows[r.Service]
				if !ok || r.Gap != "" || r.Source != gcpsource.ErrSrcLB {
					t.Fatalf("unexpected row %+v", r)
				}
				got := [4]int64{deref(r.C2xx), deref(r.C3xx), deref(r.C4xx), deref(r.C5xx)}
				if got != want {
					t.Fatalf("%s counts = %v, want %v", r.Service, got, want)
				}
			}
		})
	}
}

type gapsFakeCounter struct {
	n       map[string]int
	errs    map[string]error
	filters []string
	limits  []int
}

func (f *gapsFakeCounter) CountEntries(_ context.Context, project, filter string, limit int) (int, error) {
	f.filters = append(f.filters, filter)
	f.limits = append(f.limits, limit)
	if err := f.errs[project]; err != nil {
		return 0, err
	}
	return f.n[project], nil
}

func TestNginxFilter_IsExactText(t *testing.T) {
	want := `resource.type="gce_instance" AND (log_id("nginx_access") OR log_id("nginx-access") OR log_id("apache_access")) AND timestamp>="2026-10-12T00:00:00Z" AND timestamp<"2026-10-19T00:00:00Z"`
	if got := NginxFilter(gapsW42); got != want {
		t.Fatalf("filter =\n%s\nwant\n%s", got, want)
	}
}

func TestFetchNginx_GapUntilEntriesAppear(t *testing.T) {
	disabled := errors.New("googleapi: Error 403: Cloud Logging API has not been used in project p1 before or it is disabled. SERVICE_DISABLED")
	tests := []struct {
		name       string
		projects   []string
		n          map[string]int
		errs       map[string]error
		wantStatus string
		wantGaps   int
		wantFound  int
		wantMsg    string
		wantHint   string
	}{
		{name: "no entries is a not-collected gap per project", projects: []string{"p1", "p2"},
			wantStatus: gcpsource.StatusOK, wantGaps: 2},
		{name: "entries present give a row without gap", projects: []string{"p1"}, n: map[string]int{"p1": 1},
			wantStatus: gcpsource.StatusOK, wantFound: 1, wantMsg: NginxFound},
		{name: "logging api disabled everywhere is a classified failure", projects: []string{"p1"},
			errs: map[string]error{"p1": disabled}, wantStatus: gcpsource.StatusAPIDisabled,
			wantHint: "enable logging.googleapis.com on the project (gcloud services enable logging.googleapis.com)"},
		{name: "one failing project is skipped and noted", projects: []string{"p1", "p2"},
			errs: map[string]error{"p1": status.Error(codes.PermissionDenied, "denied")}, wantStatus: gcpsource.StatusOK,
			wantGaps: 1, wantMsg: "skipped 1 project(s) p1", wantHint: "grant the service account roles/logging.viewer"},
		{name: "blank project ids are ignored", projects: []string{" ", ""}, wantStatus: gcpsource.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &gapsFakeCounter{n: tc.n, errs: tc.errs}
			res := FetchNginx(context.Background(), f, tc.projects, gapsW42, gapsNow)
			if res.Source != "errors_nginx" || res.Status != tc.wantStatus {
				t.Fatalf("source/status = %q/%q (%s)", res.Source, res.Status, res.Message)
			}
			for i, fl := range f.filters {
				if fl != NginxFilter(gapsW42) || f.limits[i] != 1 {
					t.Fatalf("call %d filter=%q limit=%d", i, fl, f.limits[i])
				}
			}
			if !strings.Contains(res.Message, tc.wantMsg) || res.Hint != tc.wantHint {
				t.Fatalf("message=%q hint=%q", res.Message, res.Hint)
			}
			var gaps, found int
			for _, r := range res.Errors {
				if r.Source != gcpsource.ErrSrcNginx || r.Provenance.Query != NginxFilter(gapsW42) ||
					r.C2xx != nil || r.C4xx != nil || r.C5xx != nil {
					t.Fatalf("bad row %+v", r)
				}
				switch r.Gap {
				case "not collected":
					if r.Hint != "nginx access logs are not collected — add the Ops Agent nginx receiver to the VMs" {
						t.Fatalf("hint = %q", r.Hint)
					}
					gaps++
				case "":
					found++
				default:
					t.Fatalf("gap = %q", r.Gap)
				}
			}
			if gaps != tc.wantGaps || found != tc.wantFound {
				t.Fatalf("gaps=%d found=%d, want %d/%d", gaps, found, tc.wantGaps, tc.wantFound)
			}
		})
	}
}

// TestGapSources_RealAPI runs both sources against the real APIs for 2026-W42.
func TestGapSources_RealAPI(t *testing.T) {
	testenv.LoadDotEnv(t)
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" || os.Getenv("CLOUD0_GCP_PROJECTS") == "" {
		t.Skip("GOOGLE_APPLICATION_CREDENTIALS or CLOUD0_GCP_PROJECTS not set — fill .env (see .env.example) to run real GCP tests")
	}
	var projects []string
	for _, p := range strings.Split(os.Getenv("CLOUD0_GCP_PROJECTS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			projects = append(projects, p)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	l, closeL, err := monitoring.NewLister(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer closeL()
	lb := FetchLoadBalancer(ctx, l, projects, gapsW42, time.Now)
	t.Logf("LB: status=%s message=%q rows=%d", lb.Status, lb.Message, len(lb.Errors))
	if lb.Status != gcpsource.StatusOK || len(lb.Errors) == 0 {
		t.Fatalf("LB status=%s rows=%d hint=%q", lb.Status, len(lb.Errors), lb.Hint)
	}
	for i, r := range lb.Errors {
		if r.Gap != "" {
			t.Logf("LB row %d: GAP %q hint=%q", i, r.Gap, r.Hint)
			if !strings.Contains(r.Hint, "backend service") {
				t.Fatalf("gap hint lacks 'backend service'")
			}
			continue
		}
		t.Logf("LB row %d: url_map=%s 2xx=%d 3xx=%d 4xx=%d 5xx=%d", i, r.Service, *r.C2xx, *r.C3xx, *r.C4xx, *r.C5xx)
	}

	lc, closeLC, err := NewLogCounter(ctx, projects)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLC()
	ng := FetchNginx(ctx, lc, projects, gapsW42, time.Now)
	t.Logf("nginx: status=%s message=%q rows=%d", ng.Status, ng.Message, len(ng.Errors))
	if ng.Status != gcpsource.StatusOK || len(ng.Errors) == 0 {
		t.Fatalf("nginx status=%s message=%q hint=%q", ng.Status, ng.Message, ng.Hint)
	}
	for i, r := range ng.Errors {
		t.Logf("nginx row %d: gap=%q hint=%q", i, r.Gap, r.Hint)
	}

	// Sanity: the real counter sees ordinary VM syslog entries, so a zero
	// nginx count is a true absence rather than a broken query.
	n, err := lc.CountEntries(ctx, projects[0], `resource.type="gce_instance" AND timestamp>="2026-10-12T00:00:00Z" AND timestamp<"2026-10-19T00:00:00Z"`, 3)
	if err != nil {
		t.Fatalf("count gce_instance entries: %v", err)
	}
	t.Logf("gce_instance entries in first project (limit 3): %d", n)
	if n != 3 {
		t.Fatalf("limit not honoured or no VM logs: %d", n)
	}
}
