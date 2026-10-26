package errorsrc

import (
	"context"
	"errors"
	"math"
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

var crW42 = gcpsource.Window{
	Start: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
}

var crNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func crClock() time.Time { return crNow }

// fakeCR answers per project with canned series or an error and records requests.
type fakeCR struct {
	series map[string][]*monitoringpb.TimeSeries
	errs   map[string]error
	reqs   []*monitoringpb.ListTimeSeriesRequest
}

func (f *fakeCR) ListTimeSeries(_ context.Context, req *monitoringpb.ListTimeSeriesRequest) ([]*monitoringpb.TimeSeries, error) {
	f.reqs = append(f.reqs, req)
	p := strings.TrimPrefix(req.GetName(), "projects/")
	if err := f.errs[p]; err != nil {
		return nil, err
	}
	return f.series[p], nil
}

func crSeries(service, class string, v int64) *monitoringpb.TimeSeries {
	return &monitoringpb.TimeSeries{
		Metric:   &metric.Metric{Type: "run.googleapis.com/request_count", Labels: map[string]string{"response_code_class": class}},
		Resource: &monitoredres.MonitoredResource{Type: "cloud_run_revision", Labels: map[string]string{"service_name": service}},
		Points: []*monitoringpb.Point{{Value: &monitoringpb.TypedValue{
			Value: &monitoringpb.TypedValue_Int64Value{Int64Value: v},
		}}},
	}
}

type crCounts [4]int64 // 2xx, 3xx, 4xx, 5xx

func crRowCounts(t *testing.T, r gcpsource.ErrorRow) crCounts {
	t.Helper()
	if r.C2xx == nil || r.C3xx == nil || r.C4xx == nil || r.C5xx == nil {
		t.Fatalf("row %s/%q has nil counts: %+v", r.Project, r.Service, r)
	}
	return crCounts{*r.C2xx, *r.C3xx, *r.C4xx, *r.C5xx}
}

type crWantRow struct {
	project, service string
	c                crCounts // ignored when gap != ""
	gap              string
	hintHas          string
}

func TestFetchCloudRun_BuildsRowsAndClassifiesErrors(t *testing.T) {
	cases := []struct {
		name       string
		projects   []string
		series     map[string][]*monitoringpb.TimeSeries
		errs       map[string]error
		wantStatus string
		wantHint   string
		wantMsg    string
		want       []crWantRow
	}{
		{
			name:     "two services, missing classes are zero, totals row last",
			projects: []string{"p1"},
			series: map[string][]*monitoringpb.TimeSeries{"p1": {
				crSeries("storefront", "2xx", 1000),
				crSeries("storefront", "4xx", 10492),
				crSeries("storefront", "5xx", 162),
				crSeries("api", "2xx", 50),
				crSeries("api", "3xx", 7),
			}},
			wantStatus: gcpsource.StatusOK,
			want: []crWantRow{
				{project: "p1", service: "api", c: crCounts{50, 7, 0, 0}},
				{project: "p1", service: "storefront", c: crCounts{1000, 0, 10492, 162}},
				{project: "p1", service: "", c: crCounts{1050, 7, 10492, 162}},
			},
		},
		{
			name:       "project without Cloud Run data yields no rows and ok",
			projects:   []string{"empty"},
			wantStatus: gcpsource.StatusOK,
		},
		{
			name:     "unknown class is not counted in any column",
			projects: []string{"p1"},
			series: map[string][]*monitoringpb.TimeSeries{"p1": {
				crSeries("svc", "0xx", 3), crSeries("svc", "4xx", 2),
			}},
			wantStatus: gcpsource.StatusOK,
			want: []crWantRow{
				{project: "p1", service: "svc", c: crCounts{0, 0, 2, 0}},
				{project: "p1", service: "", c: crCounts{0, 0, 2, 0}},
			},
		},
		{
			name:       "permission denied names the monitoring viewer role",
			projects:   []string{"p1"},
			errs:       map[string]error{"p1": status.Error(codes.PermissionDenied, "caller lacks monitoring.timeSeries.list")},
			wantStatus: gcpsource.StatusPermissionDenied,
			wantHint:   monitoring.RoleHint,
			want:       []crWantRow{{project: "p1", gap: "source failed", hintHas: monitoring.RoleHint}},
		},
		{
			name:       "api disabled hints to enable monitoring",
			projects:   []string{"p1"},
			errs:       map[string]error{"p1": status.Error(codes.PermissionDenied, "SERVICE_DISABLED: Cloud Monitoring API has not been used in project")},
			wantStatus: gcpsource.StatusAPIDisabled,
			wantHint:   monitoring.API,
			want:       []crWantRow{{project: "p1", gap: "source failed", hintHas: monitoring.API}},
		},
		{
			name:       "quota exhausted",
			projects:   []string{"p1"},
			errs:       map[string]error{"p1": status.Error(codes.ResourceExhausted, "rate limited")},
			wantStatus: gcpsource.StatusQuota,
			want:       []crWantRow{{project: "p1", gap: "source failed", hintHas: "quota"}},
		},
		{
			name:       "every project failing with an unclassified error is failed",
			projects:   []string{"a", "b"},
			errs:       map[string]error{"a": errors.New("connection reset"), "b": errors.New("connection reset")},
			wantStatus: gcpsource.StatusFailed,
			wantMsg:    "errorsrc: cloud run request_count for project a",
			want:       []crWantRow{{project: "a", gap: "source failed"}, {project: "b", gap: "source failed"}},
		},
		{
			name:     "one failing project keeps the other project's rows",
			projects: []string{"bad", "good"},
			series: map[string][]*monitoringpb.TimeSeries{"good": {
				crSeries("web", "4xx", 7832),
			}},
			errs:       map[string]error{"bad": status.Error(codes.PermissionDenied, "Permission denied (or the resource may not exist).")},
			wantStatus: gcpsource.StatusOK,
			wantMsg:    "skipped projects: bad",
			wantHint:   monitoring.RoleHint,
			want: []crWantRow{
				{project: "bad", gap: "source failed", hintHas: monitoring.RoleHint},
				{project: "good", service: "web", c: crCounts{0, 0, 7832, 0}},
				{project: "good", service: "", c: crCounts{0, 0, 7832, 0}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeCR{series: tc.series, errs: tc.errs}
			res := FetchCloudRun(context.Background(), f, tc.projects, crW42, crClock)
			if res.Source != "errors_cloudrun" {
				t.Fatalf("Source = %q", res.Source)
			}
			if res.Status != tc.wantStatus {
				t.Fatalf("Status = %q (msg %q), want %q", res.Status, res.Message, tc.wantStatus)
			}
			if tc.wantMsg != "" && !strings.HasPrefix(res.Message, tc.wantMsg) {
				t.Fatalf("Message = %q, want prefix %q", res.Message, tc.wantMsg)
			}
			if tc.wantHint != "" && !strings.Contains(res.Hint, tc.wantHint) {
				t.Fatalf("Hint = %q, want it to contain %q", res.Hint, tc.wantHint)
			}
			if !res.Started.Equal(crNow) || !res.Finished.Equal(crNow) {
				t.Fatalf("Started/Finished = %v/%v", res.Started, res.Finished)
			}
			if len(res.Provenance) != len(tc.projects) {
				t.Fatalf("Provenance len = %d, want %d", len(res.Provenance), len(tc.projects))
			}
			if len(res.Errors) != len(tc.want) {
				t.Fatalf("got %d rows, want %d: %+v", len(res.Errors), len(tc.want), res.Errors)
			}
			for i, wr := range tc.want {
				r := res.Errors[i]
				if r.Source != gcpsource.ErrSrcCloudRun || r.Project != wr.project || r.Service != wr.service {
					t.Fatalf("row %d = %s/%s/%q, want cloudrun/%s/%q", i, r.Source, r.Project, r.Service, wr.project, wr.service)
				}
				if r.Provenance.Query != CloudRunFilter || r.Provenance.Source != SourceCloudRun || r.Provenance.Window != crW42 {
					t.Fatalf("row %d provenance = %+v", i, r.Provenance)
				}
				if wr.gap != "" {
					if r.Gap != wr.gap || r.C2xx != nil || r.C4xx != nil {
						t.Fatalf("row %d = %+v, want gap %q with nil crCounts", i, r, wr.gap)
					}
					if !strings.Contains(r.Hint, wr.hintHas) {
						t.Fatalf("row %d hint = %q, want %q", i, r.Hint, wr.hintHas)
					}
					continue
				}
				if r.Gap != "" {
					t.Fatalf("row %d unexpected gap %q", i, r.Gap)
				}
				if got := crRowCounts(t, r); got != wr.c {
					t.Fatalf("row %d (%q) crCounts = %v, want %v", i, r.Service, got, wr.c)
				}
			}
		})
	}
}

func TestFetchCloudRun_SendsWindowAlignedGroupedQuery(t *testing.T) {
	f := &fakeCR{}
	FetchCloudRun(context.Background(), f, []string{"p1", "p2"}, crW42, crClock)
	if len(f.reqs) != 2 {
		t.Fatalf("got %d requests, want one per project", len(f.reqs))
	}
	r := f.reqs[1]
	agg := r.GetAggregation()
	switch {
	case r.GetName() != "projects/p2":
		t.Fatalf("Name = %q", r.GetName())
	case r.GetFilter() != CloudRunFilter:
		t.Fatalf("Filter = %q", r.GetFilter())
	case agg.GetPerSeriesAligner() != monitoringpb.Aggregation_ALIGN_DELTA:
		t.Fatalf("aligner = %v", agg.GetPerSeriesAligner())
	case agg.GetCrossSeriesReducer() != monitoringpb.Aggregation_REDUCE_SUM:
		t.Fatalf("reducer = %v", agg.GetCrossSeriesReducer())
	case strings.Join(agg.GetGroupByFields(), ",") != "resource.labels.service_name,metric.labels.response_code_class,metric.labels.response_code":
		t.Fatalf("group by = %v", agg.GetGroupByFields())
	case agg.GetAlignmentPeriod().AsDuration() != 7*24*time.Hour:
		t.Fatalf("alignment = %v", agg.GetAlignmentPeriod().AsDuration())
	case !r.GetInterval().GetStartTime().AsTime().Equal(crW42.Start) || !r.GetInterval().GetEndTime().AsTime().Equal(crW42.End):
		t.Fatalf("interval = %v", r.GetInterval())
	}
}

// TestFetchCloudRun_RealAPI_ClassSumEqualsUngroupedCount runs against the
// real Monitoring API for ISO week 2026-W42 and checks the spec criterion:
// Σ over code classes equals request_count without the class grouping.
func TestFetchCloudRun_RealAPI_ClassSumEqualsUngroupedCount(t *testing.T) {
	testenv.LoadDotEnv(t)
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" || os.Getenv("CLOUD0_GCP_PROJECTS") == "" {
		t.Skip("GOOGLE_APPLICATION_CREDENTIALS or CLOUD0_GCP_PROJECTS not set — fill .env (see .env.example) to run real Monitoring tests")
	}
	var projects []string
	for _, p := range strings.Split(os.Getenv("CLOUD0_GCP_PROJECTS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			projects = append(projects, p)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	l, closeFn, err := monitoring.NewLister(ctx)
	if err != nil {
		t.Fatalf("NewLister: %v", err)
	}
	defer closeFn()

	res := FetchCloudRun(ctx, l, projects, crW42, time.Now)
	if res.Status != gcpsource.StatusOK {
		t.Fatalf("Status = %q: %s (hint %q)", res.Status, res.Message, res.Hint)
	}
	if res.Message != "" {
		t.Logf("partial: %s (hint %q)", res.Message, res.Hint)
	}
	totals := map[string]crCounts{}
	failed := map[string]bool{}
	any4xx := false
	for _, r := range res.Errors {
		if r.Gap != "" {
			if r.Gap != "source failed" || r.Hint == "" {
				t.Fatalf("gap row %+v lacks a classified fix hint", r)
			}
			failed[r.Project] = true
			t.Logf("GAP project#%d %s: %s", crIndexOf(projects, r.Project), r.Gap, r.Hint)
			continue
		}
		c := crRowCounts(t, r)
		if r.Service == "" {
			totals[r.Project] = c
			t.Logf("TOTAL project#%d 2xx=%d 3xx=%d 4xx=%d 5xx=%d", crIndexOf(projects, r.Project), c[0], c[1], c[2], c[3])
			continue
		}
		if c[2] > 0 {
			any4xx = true
		}
		t.Logf("service %-32s 2xx=%d 3xx=%d 4xx=%d 5xx=%d", r.Service, c[0], c[1], c[2], c[3])
	}
	if !any4xx {
		t.Fatalf("no Cloud Run service has 4xx > 0 in W42 (spec success criterion)")
	}
	for i, p := range projects {
		if failed[p] {
			continue
		}
		ts, err := CloudRunQuery(p, crW42, nil).Run(ctx, l)
		if err != nil {
			t.Fatalf("ungrouped query project#%d: %v", i, err)
		}
		var ungrouped int64
		for _, s := range ts {
			ungrouped += int64(math.Round(monitoring.Value(s)))
		}
		c := totals[p]
		sum := c[0] + c[1] + c[2] + c[3]
		t.Logf("project#%d Σclasses=%d ungrouped=%d", i, sum, ungrouped)
		if sum != ungrouped {
			t.Fatalf("project#%d: Σ over classes %d != ungrouped request_count %d", i, sum, ungrouped)
		}
	}
}

func crIndexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}
