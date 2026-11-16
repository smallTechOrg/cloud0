package monitoring

import (
	"context"
	"fmt"
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
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

var crW42 = gcpsource.Window{
	Start: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
}

func crNow() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }

const (
	crMem = "run.googleapis.com/container/memory/allocation_time"
	crReq = "run.googleapis.com/request_count"
	crCPU = "run.googleapis.com/container/cpu/allocation_time"
)

// crFake answers by (project, metric type); errs takes precedence.
type crFake struct {
	series map[string]map[string][]*monitoringpb.TimeSeries
	errs   map[string]map[string]error
	reqs   []*monitoringpb.ListTimeSeriesRequest
}

func (f *crFake) ListTimeSeries(_ context.Context, req *monitoringpb.ListTimeSeriesRequest) ([]*monitoringpb.TimeSeries, error) {
	f.reqs = append(f.reqs, req)
	project := strings.TrimPrefix(req.GetName(), "projects/")
	for typ, err := range f.errs[project] {
		if strings.Contains(req.GetFilter(), `"`+typ+`"`) {
			return nil, err
		}
	}
	for typ, ss := range f.series[project] {
		if strings.Contains(req.GetFilter(), `"`+typ+`"`) {
			return ss, nil
		}
	}
	return nil, nil
}

func crDouble(svc string, v float64) *monitoringpb.TimeSeries {
	return &monitoringpb.TimeSeries{
		Metric:   &metric.Metric{},
		Resource: &monitoredres.MonitoredResource{Type: "cloud_run_revision", Labels: map[string]string{"service_name": svc}},
		Points:   []*monitoringpb.Point{{Value: &monitoringpb.TypedValue{Value: &monitoringpb.TypedValue_DoubleValue{DoubleValue: v}}}},
	}
}

func crInt(svc string, v int64) *monitoringpb.TimeSeries {
	return &monitoringpb.TimeSeries{
		Metric:   &metric.Metric{},
		Resource: &monitoredres.MonitoredResource{Type: "cloud_run_revision", Labels: map[string]string{"service_name": svc}},
		Points:   []*monitoringpb.Point{{Value: &monitoringpb.TypedValue{Value: &monitoringpb.TypedValue_Int64Value{Int64Value: v}}}},
	}
}

// want: resource -> metric key -> value (float) or gap (string).
type crWant map[string]map[string]any

func crCheck(t *testing.T, res gcpsource.Result, project string, want crWant) {
	t.Helper()
	got := map[string]gcpsource.MetricRow{}
	for _, r := range res.Metrics {
		if r.Project != project {
			continue
		}
		if r.Group != gcpsource.GroupCloudRun {
			t.Errorf("row %q group = %q", r.Resource, r.Group)
		}
		got[r.Resource] = r
	}
	if len(got) != len(want) {
		t.Fatalf("project %s: got %d rows, want %d (%v)", project, len(got), len(want), got)
	}
	for res, cells := range want {
		row, ok := got[res]
		if !ok {
			t.Fatalf("missing row %q", res)
		}
		if _, bad := row.Cells[gcpsource.MEgressInterGB]; bad {
			t.Errorf("row %q must not carry %s", res, gcpsource.MEgressInterGB)
		}
		if len(row.Cells) != 3 {
			t.Errorf("row %q has %d cells, want 3", res, len(row.Cells))
		}
		for key, w := range cells {
			c := row.Cells[key]
			if c.Provenance.Source != SourceCloudRun || c.Provenance.Query == "" || c.Provenance.Window != crW42 {
				t.Errorf("row %q %s provenance = %+v", res, key, c.Provenance)
			}
			switch w := w.(type) {
			case float64:
				if c.Value == nil || math.Abs(*c.Value-w) > 0.005 {
					t.Errorf("row %q %s = %+v, want %v", res, key, c, w)
				}
				if c.Gap != "" {
					t.Errorf("row %q %s has gap %q with value", res, key, c.Gap)
				}
			case string:
				if c.Value != nil || c.Gap != w {
					t.Errorf("row %q %s = value %v gap %q, want gap %q", res, key, c.Value, c.Gap, w)
				}
				if w == "source failed" && !strings.Contains(c.Hint, RoleHint) {
					t.Errorf("row %q %s hint %q lacks %s", res, key, c.Hint, RoleHint)
				}
			}
		}
	}
}

func TestFetchCloudRun_BuildsRowsAndTotals(t *testing.T) {
	denied := status.Error(codes.PermissionDenied, "Permission denied (or the resource may not exist).")
	units := map[string]string{gcpsource.MServiceMemGiBs: "GiB-s", gcpsource.MRequests: "count", gcpsource.MServiceCPUSec: "vCPU-s"}
	tests := []struct {
		name       string
		projects   []string
		fake       *crFake
		wantStatus string
		wantMsg    string
		want       map[string]crWant // project -> rows
	}{
		{
			name:     "two services sum into a total row",
			projects: []string{"p1"},
			fake: &crFake{series: map[string]map[string][]*monitoringpb.TimeSeries{"p1": {
				crMem: {crDouble("api", 100.5), crDouble("web", 200)},
				crReq: {crInt("api", 10), crInt("web", 30)},
				crCPU: {crDouble("api", 50), crDouble("web", 25.25)},
			}}},
			wantStatus: gcpsource.StatusOK,
			want: map[string]crWant{"p1": {
				"api": {gcpsource.MServiceMemGiBs: 100.5, gcpsource.MRequests: 10.0, gcpsource.MServiceCPUSec: 50.0},
				"web": {gcpsource.MServiceMemGiBs: 200.0, gcpsource.MRequests: 30.0, gcpsource.MServiceCPUSec: 25.25},
				"":    {gcpsource.MServiceMemGiBs: 300.5, gcpsource.MRequests: 40.0, gcpsource.MServiceCPUSec: 75.25},
			}},
		},
		{
			name:     "missing requests are zero but missing memory or cpu is a gap",
			projects: []string{"p1"},
			fake: &crFake{series: map[string]map[string][]*monitoringpb.TimeSeries{"p1": {
				crMem: {crDouble("idle", 7)},
				crReq: {crInt("busy", 5)},
				crCPU: {crDouble("idle", 3)},
			}}},
			wantStatus: gcpsource.StatusOK,
			want: map[string]crWant{"p1": {
				"idle": {gcpsource.MServiceMemGiBs: 7.0, gcpsource.MRequests: 0.0, gcpsource.MServiceCPUSec: 3.0},
				"busy": {gcpsource.MServiceMemGiBs: "no data", gcpsource.MRequests: 5.0, gcpsource.MServiceCPUSec: "no data"},
				"":     {gcpsource.MServiceMemGiBs: 7.0, gcpsource.MRequests: 5.0, gcpsource.MServiceCPUSec: 3.0},
			}},
		},
		{
			name:       "project without Cloud Run has only a no-data total",
			projects:   []string{"p1"},
			fake:       &crFake{},
			wantStatus: gcpsource.StatusOK,
			want: map[string]crWant{"p1": {
				"": {gcpsource.MServiceMemGiBs: "no data", gcpsource.MRequests: "no data", gcpsource.MServiceCPUSec: "no data"},
			}},
		},
		{
			name:     "one metric denied gives source-failed cells and keeps the rest",
			projects: []string{"p1"},
			fake: &crFake{
				series: map[string]map[string][]*monitoringpb.TimeSeries{"p1": {
					crMem: {crDouble("api", 4)},
					crReq: {crInt("api", 9)},
				}},
				errs: map[string]map[string]error{"p1": {crCPU: denied}},
			},
			wantStatus: gcpsource.StatusOK,
			wantMsg:    "Permission denied",
			want: map[string]crWant{"p1": {
				"api": {gcpsource.MServiceMemGiBs: 4.0, gcpsource.MRequests: 9.0, gcpsource.MServiceCPUSec: "source failed"},
				"":    {gcpsource.MServiceMemGiBs: 4.0, gcpsource.MRequests: 9.0, gcpsource.MServiceCPUSec: "source failed"},
			}},
		},
		{
			name:     "denied project is skipped when another project succeeds",
			projects: []string{"p1", " p2 ", ""},
			fake: &crFake{
				series: map[string]map[string][]*monitoringpb.TimeSeries{"p1": {
					crMem: {crDouble("api", 1)}, crReq: {crInt("api", 2)}, crCPU: {crDouble("api", 3)},
				}},
				errs: map[string]map[string]error{"p2": {crMem: denied, crReq: denied, crCPU: denied}},
			},
			wantStatus: gcpsource.StatusOK,
			wantMsg:    "skipped projects: p2 (permission_denied)",
			want: map[string]crWant{
				"p1": {
					"api": {gcpsource.MServiceMemGiBs: 1.0, gcpsource.MRequests: 2.0, gcpsource.MServiceCPUSec: 3.0},
					"":    {gcpsource.MServiceMemGiBs: 1.0, gcpsource.MRequests: 2.0, gcpsource.MServiceCPUSec: 3.0},
				},
				"p2": {},
			},
		},
		{
			name:       "all projects denied is permission_denied with the role hint",
			projects:   []string{"p1"},
			fake:       &crFake{errs: map[string]map[string]error{"p1": {crMem: denied, crReq: denied, crCPU: denied}}},
			wantStatus: gcpsource.StatusPermissionDenied,
			wantMsg:    "Permission denied",
			want:       map[string]crWant{"p1": {}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := FetchCloudRun(context.Background(), tc.fake, tc.projects, crW42, crNow)
			if res.Source != SourceCloudRun {
				t.Errorf("source = %q", res.Source)
			}
			if res.Status != tc.wantStatus {
				t.Fatalf("status = %q (%s), want %q", res.Status, res.Message, tc.wantStatus)
			}
			if !strings.Contains(res.Message, tc.wantMsg) {
				t.Errorf("message %q lacks %q", res.Message, tc.wantMsg)
			}
			if tc.wantMsg != "" && !strings.Contains(res.Hint, RoleHint) {
				t.Errorf("hint %q lacks %s", res.Hint, RoleHint)
			}
			if !res.Started.Equal(crNow()) || !res.Finished.Equal(crNow()) {
				t.Errorf("started/finished = %v/%v", res.Started, res.Finished)
			}
			for p, rows := range tc.want {
				crCheck(t, res, p, rows)
			}
			for _, r := range res.Metrics {
				for k, c := range r.Cells {
					if c.Unit != units[k] {
						t.Errorf("row %q %s unit = %q, want %q", r.Resource, k, c.Unit, units[k])
					}
				}
			}
			if last := res.Metrics; len(last) > 0 && last[len(last)-1].Resource != "" {
				t.Errorf("total row must close the group, got %q last", last[len(last)-1].Resource)
			}
		})
	}
}

func TestFetchCloudRun_SendsWeekAlignedDeltaSumByService(t *testing.T) {
	f := &crFake{}
	res := FetchCloudRun(context.Background(), f, []string{"p1"}, crW42, crNow)
	if len(f.reqs) != 3 || len(res.Provenance) != 3 {
		t.Fatalf("requests = %d, provenance = %d, want 3 each", len(f.reqs), len(res.Provenance))
	}
	for i, typ := range []string{crMem, crReq, crCPU} {
		r := f.reqs[i]
		wantFilter := `metric.type="` + typ + `" AND resource.type="cloud_run_revision"`
		if r.GetName() != "projects/p1" || r.GetFilter() != wantFilter {
			t.Errorf("req %d = %s %s", i, r.GetName(), r.GetFilter())
		}
		a := r.GetAggregation()
		if a.GetPerSeriesAligner() != monitoringpb.Aggregation_ALIGN_DELTA ||
			a.GetCrossSeriesReducer() != monitoringpb.Aggregation_REDUCE_SUM ||
			len(a.GetGroupByFields()) != 1 || a.GetGroupByFields()[0] != "resource.labels.service_name" ||
			a.GetAlignmentPeriod().AsDuration() != 7*24*time.Hour {
			t.Errorf("req %d aggregation = %v", i, a)
		}
		if !r.GetInterval().GetStartTime().AsTime().Equal(crW42.Start) || !r.GetInterval().GetEndTime().AsTime().Equal(crW42.End) {
			t.Errorf("req %d interval = %v", i, r.GetInterval())
		}
		p := res.Provenance[i]
		if p.Query != wantFilter || !strings.Contains(p.Aggregation, "ALIGN_DELTA") || !strings.Contains(p.Aggregation, "REDUCE_SUM") || !p.FetchedAt.Equal(crNow()) {
			t.Errorf("provenance %d = %+v", i, p)
		}
	}
}

func TestFetchCloudRun_RealWeek42HasRequests(t *testing.T) {
	testenv.LoadDotEnv(t)
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" || os.Getenv("CLOUD0_GCP_PROJECTS") == "" {
		t.Skip("GOOGLE_APPLICATION_CREDENTIALS or CLOUD0_GCP_PROJECTS not set — fill .env (see .env.example) to run real Monitoring tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	l, closeFn, err := NewLister(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	projects := strings.Split(os.Getenv("CLOUD0_GCP_PROJECTS"), ",")
	res := FetchCloudRun(ctx, l, projects, crW42, func() time.Time { return time.Now().UTC() })
	if res.Status != gcpsource.StatusOK {
		t.Fatalf("status = %s: %s (hint %s)", res.Status, res.Message, res.Hint)
	}
	if res.Message != "" {
		t.Logf("message: %s | hint: %s", res.Message, res.Hint)
	}
	totals := map[string]float64{}
	services, maxReq := 0, 0.0
	for _, r := range res.Metrics {
		if r.Group != gcpsource.GroupCloudRun {
			t.Errorf("group = %q", r.Group)
		}
		for k, c := range r.Cells {
			if (c.Value == nil) == (c.Gap == "") {
				t.Errorf("row %q %s: exactly one of value/gap must be set", r.Resource, k)
			}
			if c.Value != nil && *c.Value < 0 {
				t.Errorf("row %q %s negative %v", r.Resource, k, *c.Value)
			}
		}
		if r.Resource == "" {
			for k, c := range r.Cells {
				if c.Value != nil {
					totals[k] += *c.Value
				}
			}
			continue
		}
		services++
		if c := r.Cells[gcpsource.MRequests]; c.Value != nil && *c.Value > maxReq {
			maxReq = *c.Value
		}
		t.Logf("service #%d mem=%s req=%s cpu=%s", services,
			crFmt(r.Cells[gcpsource.MServiceMemGiBs]), crFmt(r.Cells[gcpsource.MRequests]), crFmt(r.Cells[gcpsource.MServiceCPUSec]))
	}
	t.Logf("W42 totals: memory %.1f GiB-s, requests %.0f, cpu %.1f vCPU-s across %d services",
		totals[gcpsource.MServiceMemGiBs], totals[gcpsource.MRequests], totals[gcpsource.MServiceCPUSec], services)
	if services == 0 || maxReq <= 0 {
		t.Fatalf("want at least one service with request_count > 0, got %d services, max %v", services, maxReq)
	}
	if totals[gcpsource.MServiceMemGiBs] <= 0 || totals[gcpsource.MServiceCPUSec] <= 0 {
		t.Errorf("want positive memory and cpu totals, got %v", totals)
	}
}

func crFmt(c gcpsource.Cell) string {
	if c.Value == nil {
		return "gap:" + c.Gap
	}
	return fmt.Sprintf("%.1f", *c.Value)
}
