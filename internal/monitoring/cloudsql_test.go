package monitoring

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/genproto/googleapis/api/monitoredres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

var sqlTestWindow = gcpsource.Window{
	Start: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
}

func sqlTestNow() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }

// sqlFakeResp is the canned answer for one (project, metric type) call.
type sqlFakeResp struct {
	series []*monitoringpb.TimeSeries
	err    error
}

// sqlFakeLister answers by project and metric short name
// ("cpu", "mem", "quota", "disk").
type sqlFakeLister struct {
	resp  map[string]sqlFakeResp // key "project/short"
	calls []*monitoringpb.ListTimeSeriesRequest
}

func (f *sqlFakeLister) ListTimeSeries(_ context.Context, req *monitoringpb.ListTimeSeriesRequest) ([]*monitoringpb.TimeSeries, error) {
	f.calls = append(f.calls, req)
	project := strings.TrimPrefix(req.GetName(), "projects/")
	short := map[string]string{
		"/database/cpu/utilization":    "cpu",
		"/database/memory/total_usage": "mem",
		"/database/memory/quota":       "quota",
		"/database/disk/utilization":   "disk",
	}
	for frag, k := range short {
		if strings.Contains(req.GetFilter(), frag) {
			r := f.resp[project+"/"+k]
			return r.series, r.err
		}
	}
	return nil, nil
}

func sqlSeries(dbID string, vals ...float64) *monitoringpb.TimeSeries {
	ts := &monitoringpb.TimeSeries{
		Resource: &monitoredres.MonitoredResource{
			Type:   "cloudsql_database",
			Labels: map[string]string{"database_id": dbID, "project_id": strings.Split(dbID, ":")[0]},
		},
	}
	for _, v := range vals {
		ts.Points = append(ts.Points, &monitoringpb.Point{
			Value: &monitoringpb.TypedValue{Value: &monitoringpb.TypedValue_DoubleValue{DoubleValue: v}},
		})
	}
	return ts
}

func sqlCellValue(t *testing.T, c gcpsource.Cell) float64 {
	t.Helper()
	if c.Value == nil {
		t.Fatalf("cell has no value (gap %q)", c.Gap)
	}
	return *c.Value
}

func sqlApprox(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestFetchCloudSQL_BuildsRowsAndGaps(t *testing.T) {
	denied := status.Error(codes.PermissionDenied, "Permission monitoring.timeSeries.list denied")
	disabled := status.Error(codes.PermissionDenied, "Cloud Monitoring API has not been used in project p2 before or it is disabled. SERVICE_DISABLED")

	type wantCell struct {
		value *float64
		gap   string
		hint  string
	}
	f := func(v float64) *float64 { return &v }
	type wantRow struct {
		project, resource string
		cells             map[string]wantCell
	}

	tests := []struct {
		name        string
		projects    []string
		resp        map[string]sqlFakeResp
		wantStatus  string
		wantHintSub string
		wantMsgSub  string
		rows        []wantRow
	}{
		{
			name:     "three metrics for one instance, x100",
			projects: []string{"p1"},
			resp: map[string]sqlFakeResp{
				"p1/cpu":   {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 0.125)}},
				"p1/mem":   {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 260)}},
				"p1/quota": {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 1000)}},
				"p1/disk":  {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 0.03)}},
			},
			wantStatus: gcpsource.StatusOK,
			rows: []wantRow{{"p1", "db-a", map[string]wantCell{
				gcpsource.MCPU: {value: f(12.5)}, gcpsource.MMemory: {value: f(26)}, gcpsource.MDisk: {value: f(3)},
			}}},
		},
		{
			name:     "missing memory series is a no-data gap, not zero",
			projects: []string{"p1"},
			resp: map[string]sqlFakeResp{
				"p1/cpu":   {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 0.1), sqlSeries("p1:db-b", 0.5)}},
				"p1/quota": {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 1000), sqlSeries("p1:db-b", 1000)}},
				"p1/mem":   {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-b", 100)}},
				"p1/disk":  {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 0.2)}},
			},
			wantStatus: gcpsource.StatusOK,
			rows: []wantRow{
				{"p1", "db-a", map[string]wantCell{
					gcpsource.MCPU: {value: f(10)}, gcpsource.MMemory: {gap: "no data"}, gcpsource.MDisk: {value: f(20)},
				}},
				{"p1", "db-b", map[string]wantCell{
					gcpsource.MCPU: {value: f(50)}, gcpsource.MMemory: {value: f(10)}, gcpsource.MDisk: {gap: "no data"},
				}},
			},
		},
		{
			name:     "domain-scoped database_id and multi-point series are averaged",
			projects: []string{"p1"},
			resp: map[string]sqlFakeResp{
				"p1/cpu":   {series: []*monitoringpb.TimeSeries{sqlSeries("example.com:p1:db-a", 0.1, 0.3)}},
				"p1/mem":   {series: []*monitoringpb.TimeSeries{sqlSeries("example.com:p1:db-a", 400)}},
				"p1/quota": {series: []*monitoringpb.TimeSeries{sqlSeries("example.com:p1:db-a", 1000)}},
				"p1/disk":  {series: []*monitoringpb.TimeSeries{sqlSeries("example.com:p1:db-a", 0.05)}},
			},
			wantStatus: gcpsource.StatusOK,
			rows: []wantRow{{"p1", "db-a", map[string]wantCell{
				gcpsource.MCPU: {value: f(20)}, gcpsource.MMemory: {value: f(40)}, gcpsource.MDisk: {value: f(5)},
			}}},
		},
		{
			name:     "one failed metric query marks only that cell source failed",
			projects: []string{"p1"},
			resp: map[string]sqlFakeResp{
				"p1/cpu":   {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 0.1)}},
				"p1/mem":   {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 100)}},
				"p1/quota": {err: status.Error(codes.ResourceExhausted, "quota exceeded")},
				"p1/disk":  {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 0.2)}},
			},
			wantStatus:  gcpsource.StatusOK,
			wantHintSub: "quota",
			rows: []wantRow{{"p1", "db-a", map[string]wantCell{
				gcpsource.MCPU: {value: f(10)}, gcpsource.MMemory: {gap: "source failed", hint: "quota"}, gcpsource.MDisk: {value: f(20)},
			}}},
		},
		{
			name:     "api disabled in one project does not fail the others",
			projects: []string{"p1", "p2"},
			resp: map[string]sqlFakeResp{
				"p1/cpu":   {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 0.1)}},
				"p1/mem":   {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 200)}},
				"p1/quota": {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 1000)}},
				"p1/disk":  {series: []*monitoringpb.TimeSeries{sqlSeries("p1:db-a", 0.3)}},
				"p2/cpu":   {err: disabled}, "p2/mem": {err: disabled}, "p2/quota": {err: disabled}, "p2/disk": {err: disabled},
			},
			wantStatus:  gcpsource.StatusOK,
			wantMsgSub:  "p2 (api_disabled)",
			wantHintSub: "enable " + API,
			rows: []wantRow{{"p1", "db-a", map[string]wantCell{
				gcpsource.MCPU: {value: f(10)}, gcpsource.MMemory: {value: f(20)}, gcpsource.MDisk: {value: f(30)},
			}}},
		},
		{
			name:     "permission denied everywhere fails with the role hint",
			projects: []string{"p1", "p2"},
			resp: map[string]sqlFakeResp{
				"p1/cpu": {err: denied}, "p1/mem": {err: denied}, "p1/quota": {err: denied}, "p1/disk": {err: denied},
				"p2/cpu": {err: denied}, "p2/mem": {err: denied}, "p2/quota": {err: denied}, "p2/disk": {err: denied},
			},
			wantStatus:  gcpsource.StatusPermissionDenied,
			wantHintSub: RoleHint,
		},
		{
			name:       "no instances anywhere is ok with no rows",
			projects:   []string{"p1", " ", "p2"},
			resp:       map[string]sqlFakeResp{},
			wantStatus: gcpsource.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fl := &sqlFakeLister{resp: tc.resp}
			res := FetchCloudSQL(context.Background(), fl, tc.projects, sqlTestWindow, sqlTestNow)

			if res.Source != "monitoring_cloudsql" {
				t.Errorf("Source = %q", res.Source)
			}
			if res.Status != tc.wantStatus {
				t.Fatalf("Status = %q (msg %q), want %q", res.Status, res.Message, tc.wantStatus)
			}
			if !strings.Contains(res.Hint, tc.wantHintSub) {
				t.Errorf("Hint = %q, want it to contain %q", res.Hint, tc.wantHintSub)
			}
			if !strings.Contains(res.Message, tc.wantMsgSub) {
				t.Errorf("Message = %q, want it to contain %q", res.Message, tc.wantMsgSub)
			}
			if res.Started.IsZero() || res.Finished.IsZero() {
				t.Errorf("Started/Finished not stamped: %v %v", res.Started, res.Finished)
			}
			if len(res.Metrics) != len(tc.rows) {
				t.Fatalf("got %d rows, want %d: %+v", len(res.Metrics), len(tc.rows), res.Metrics)
			}
			for i, wr := range tc.rows {
				row := res.Metrics[i]
				if row.Group != gcpsource.GroupCloudSQL || row.Project != wr.project || row.Resource != wr.resource {
					t.Errorf("row %d = %s/%s/%s, want cloudsql/%s/%s", i, row.Group, row.Project, row.Resource, wr.project, wr.resource)
				}
				for key, wc := range wr.cells {
					c, ok := row.Cells[key]
					if !ok {
						t.Fatalf("row %s missing cell %s", wr.resource, key)
					}
					if c.Unit != "%" {
						t.Errorf("%s/%s unit = %q", wr.resource, key, c.Unit)
					}
					if c.Provenance.Source != "monitoring_cloudsql" || !strings.Contains(c.Provenance.Query, "cloudsql.googleapis.com/database/") ||
						c.Provenance.Window != sqlTestWindow || !strings.Contains(c.Provenance.Aggregation, "ALIGN_MEAN") {
						t.Errorf("%s/%s provenance = %+v", wr.resource, key, c.Provenance)
					}
					if key == gcpsource.MMemory && !(strings.Contains(c.Provenance.Query, "memory/total_usage") && strings.Contains(c.Provenance.Query, "memory/quota")) {
						t.Errorf("memory provenance should name both series: %q", c.Provenance.Query)
					}
					if wc.value != nil {
						if got := sqlCellValue(t, c); !sqlApprox(got, *wc.value) {
							t.Errorf("%s/%s = %v, want %v", wr.resource, key, got, *wc.value)
						}
						continue
					}
					if c.Value != nil || c.Gap != wc.gap || !strings.Contains(c.Hint, wc.hint) {
						t.Errorf("%s/%s = value %v gap %q hint %q, want gap %q hint ~%q", wr.resource, key, c.Value, c.Gap, c.Hint, wc.gap, wc.hint)
					}
				}
			}
		})
	}
}

func TestFetchCloudSQL_RequestsAreWeeklyMeanPerProject(t *testing.T) {
	fl := &sqlFakeLister{resp: map[string]sqlFakeResp{}}
	res := FetchCloudSQL(context.Background(), fl, []string{"p1", "p2"}, sqlTestWindow, sqlTestNow)
	if len(fl.calls) != 8 || len(res.Provenance) != 8 {
		t.Fatalf("calls = %d, provenance = %d, want 8 each", len(fl.calls), len(res.Provenance))
	}
	for _, req := range fl.calls {
		agg := req.GetAggregation()
		if agg.GetPerSeriesAligner() != monitoringpb.Aggregation_ALIGN_MEAN {
			t.Errorf("aligner = %v", agg.GetPerSeriesAligner())
		}
		if agg.GetAlignmentPeriod().AsDuration() != 7*24*time.Hour {
			t.Errorf("alignment = %v", agg.GetAlignmentPeriod().AsDuration())
		}
		if !req.GetInterval().GetStartTime().AsTime().Equal(sqlTestWindow.Start) || !req.GetInterval().GetEndTime().AsTime().Equal(sqlTestWindow.End) {
			t.Errorf("interval = %v", req.GetInterval())
		}
		if !strings.Contains(req.GetFilter(), `resource.type = "cloudsql_database"`) {
			t.Errorf("filter = %q", req.GetFilter())
		}
	}
}

func TestFetchCloudSQL_ContextErrorFailsSource(t *testing.T) {
	fl := &sqlFakeLister{resp: map[string]sqlFakeResp{
		"p1/cpu": {err: context.DeadlineExceeded}, "p1/mem": {err: context.DeadlineExceeded},
		"p1/quota": {err: errors.New("boom")}, "p1/disk": {err: errors.New("boom")},
	}}
	res := FetchCloudSQL(context.Background(), fl, []string{"p1"}, sqlTestWindow, sqlTestNow)
	if res.Status != gcpsource.StatusFailed || len(res.Metrics) != 0 {
		t.Fatalf("Status = %q rows = %d, want failed/0", res.Status, len(res.Metrics))
	}
}

// TestFetchCloudSQL_RealMonitoring queries the real API for ISO week 2026-W42.
func TestFetchCloudSQL_RealMonitoring(t *testing.T) {
	testenv.LoadDotEnv(t)
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" || os.Getenv("CLOUD0_GCP_PROJECTS") == "" {
		t.Skip("GOOGLE_APPLICATION_CREDENTIALS or CLOUD0_GCP_PROJECTS not set — fill .env (see .env.example) to run real Monitoring tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	l, closeFn, err := NewLister(ctx)
	if err != nil {
		t.Fatalf("NewLister: %v", err)
	}
	defer closeFn()

	projects := strings.Split(os.Getenv("CLOUD0_GCP_PROJECTS"), ",")
	res := FetchCloudSQL(ctx, l, projects, sqlTestWindow, time.Now)
	t.Logf("status=%s message=%q hint=%q rows=%d queries=%d", res.Status, res.Message, res.Hint, len(res.Metrics), len(res.Provenance))
	if res.Status != gcpsource.StatusOK {
		t.Fatalf("Status = %q: %s (hint %s)", res.Status, res.Message, res.Hint)
	}
	if len(res.Metrics) == 0 {
		t.Fatal("no Cloud SQL instances found in W42")
	}
	full := 0
	for _, row := range res.Metrics {
		if strings.Contains(row.Resource, ":") || row.Resource == "" {
			t.Errorf("resource %q should be a bare instance name", row.Resource)
		}
		numeric := 0
		for _, key := range []string{gcpsource.MCPU, gcpsource.MMemory, gcpsource.MDisk} {
			c := row.Cells[key]
			if c.Value != nil {
				t.Logf("W42 %s %s = %.2f %s", row.Resource, key, *c.Value, c.Unit)
				if *c.Value <= 0 || *c.Value >= 100 {
					t.Errorf("%s %s = %v, want in (0,100)", row.Resource, key, *c.Value)
				}
				numeric++
			} else {
				t.Logf("W42 %s %s gap %q", row.Resource, key, c.Gap)
			}
		}
		if numeric == 3 {
			full++
		}
	}
	if full == 0 {
		t.Error("no instance has all three utilisations numeric")
	}
}
