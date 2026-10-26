package monitoring

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
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

// gcsLogFake answers ListTimeSeries by "project|filter"; it records requests.
type gcsLogFake struct {
	mu     sync.Mutex
	series map[string][]*monitoringpb.TimeSeries
	errs   map[string]error
	reqs   []*monitoringpb.ListTimeSeriesRequest
}

func (f *gcsLogFake) ListTimeSeries(_ context.Context, req *monitoringpb.ListTimeSeriesRequest) ([]*monitoringpb.TimeSeries, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	key := strings.TrimPrefix(req.GetName(), "projects/") + "|" + req.GetFilter()
	if err := f.errs[key]; err != nil {
		return nil, err
	}
	return f.series[key], nil
}

func gcsLogKey(project, metricType string) string { return project + "|" + glMetricFilter(metricType) }

func gcsLogSeries(metricLabels, resLabels map[string]string, v float64, int64Typed bool) *monitoringpb.TimeSeries {
	tv := &monitoringpb.TypedValue{Value: &monitoringpb.TypedValue_DoubleValue{DoubleValue: v}}
	if int64Typed {
		tv = &monitoringpb.TypedValue{Value: &monitoringpb.TypedValue_Int64Value{Int64Value: int64(v)}}
	}
	return &monitoringpb.TimeSeries{
		Metric:   &metric.Metric{Labels: metricLabels},
		Resource: &monitoredres.MonitoredResource{Labels: resLabels},
		Points:   []*monitoringpb.Point{{Value: tv}},
	}
}

func glReqSeries(bucket, method string, n float64) *monitoringpb.TimeSeries {
	return gcsLogSeries(map[string]string{"method": method}, map[string]string{"bucket_name": bucket}, n, true)
}

func glBucketSeries(bucket string, v float64) *monitoringpb.TimeSeries {
	return gcsLogSeries(nil, map[string]string{"bucket_name": bucket}, v, false)
}

var glW42 = gcpsource.Window{
	Start: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
}

func glFixedNow() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }

func glNear(a, b float64) bool { return math.Abs(a-b) < 1e-9*math.Max(1, math.Abs(b)) }

func glFindRow(rows []gcpsource.MetricRow, project, resource string) (gcpsource.MetricRow, bool) {
	for _, r := range rows {
		if r.Project == project && r.Resource == resource {
			return r, true
		}
	}
	return gcpsource.MetricRow{}, false
}

func glWantValue(t *testing.T, row gcpsource.MetricRow, key string, want float64, unit string) {
	t.Helper()
	c, ok := row.Cells[key]
	if !ok {
		t.Fatalf("row %s/%s: no cell %s", row.Project, row.Resource, key)
	}
	if c.Value == nil {
		t.Fatalf("row %s/%s cell %s: gap %q, want value %v", row.Project, row.Resource, key, c.Gap, want)
	}
	if !glNear(*c.Value, want) {
		t.Fatalf("row %s/%s cell %s = %v, want %v", row.Project, row.Resource, key, *c.Value, want)
	}
	if c.Unit != unit {
		t.Fatalf("row %s/%s cell %s unit %q, want %q", row.Project, row.Resource, key, c.Unit, unit)
	}
}

func glWantGap(t *testing.T, row gcpsource.MetricRow, key, gap string) gcpsource.Cell {
	t.Helper()
	c := row.Cells[key]
	if c.Value != nil || c.Gap != gap {
		t.Fatalf("row %s/%s cell %s: value=%v gap=%q, want gap %q", row.Project, row.Resource, key, c.Value, c.Gap, gap)
	}
	return c
}

func TestGCSOperationClass_MapsPublishedMethodsToTheirClass(t *testing.T) {
	cases := []struct{ method, class string }{
		{"WriteObject", GCSClassA},
		{"ListObjects", GCSClassA},
		{"ListBuckets", GCSClassA},
		{"ComposeObject", GCSClassA},
		{"CopyObject", GCSClassA},
		{"RewriteObject", GCSClassA},
		{"SetBucketIamPolicy", GCSClassA},
		{"ReadObject", GCSClassB},
		{"GetObjectMetadata", GCSClassB},
		{"GetBucketMetadata", GCSClassB},
		{"GetBucketIamPolicy", GCSClassB},
		{"GetServiceAccount", GCSClassB},
		{"DeleteObject", GCSClassFree},
		{"DeleteBucket", GCSClassFree},
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			if got := GCSOperationClass[c.method]; got != c.class {
				t.Fatalf("GCSOperationClass[%q] = %q, want %q", c.method, got, c.class)
			}
		})
	}
	if _, ok := GCSOperationClass["MadeUpMethod"]; ok {
		t.Fatal("unknown method must not be classified")
	}
	for m, cl := range GCSOperationClass {
		if cl != GCSClassA && cl != GCSClassB && cl != GCSClassFree {
			t.Fatalf("method %s has invalid class %q", m, cl)
		}
	}
}

func TestFetchGCS_SplitsClassesSumsBytesAndComputesGBMonth(t *testing.T) {
	f := &gcsLogFake{series: map[string][]*monitoringpb.TimeSeries{
		gcsLogKey("p1", gcsRequestCountMetric): {
			glReqSeries("b1", "WriteObject", 70),
			glReqSeries("b1", "ReadObject", 1000),
			glReqSeries("b1", "DeleteObject", 4),
			glReqSeries("b1", "MadeUpMethod", 3),
			glReqSeries("b2", "ListObjects", 1),
			glReqSeries("b2", "GetObjectMetadata", 500),
		},
		gcsLogKey("p1", gcsSentBytesMetric):  {glBucketSeries("b1", 2e9), glBucketSeries("b2", 5e8)},
		gcsLogKey("p1", gcsTotalBytesMetric): {glBucketSeries("b1", 1e9), glBucketSeries("b2", 3e9), glBucketSeries("b3", 7.3e9)},
	}}
	r := FetchGCS(context.Background(), f, []string{" p1 ", ""}, glW42, glFixedNow)

	if r.Source != "monitoring_gcs" || r.Status != gcpsource.StatusOK {
		t.Fatalf("source/status = %q/%q, want monitoring_gcs/ok (msg %q)", r.Source, r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "MadeUpMethod=3") {
		t.Fatalf("unknown method not listed in Message: %q", r.Message)
	}
	if len(r.Metrics) != 4 {
		t.Fatalf("got %d rows, want 3 buckets + 1 total", len(r.Metrics))
	}
	share := 168.0 / 730.0
	b1, _ := glFindRow(r.Metrics, "p1", "b1")
	if b1.Group != gcpsource.GroupGCS {
		t.Fatalf("group %q", b1.Group)
	}
	glWantValue(t, b1, gcpsource.MClassAOps, 70, "count")
	glWantValue(t, b1, gcpsource.MClassBOps, 1000, "count")
	glWantValue(t, b1, gcpsource.MSentGB, 2, "GB")
	glWantValue(t, b1, gcpsource.MStorageGBMonth, 1*share, "GB-month")
	if _, ok := b1.Cells[gcpsource.MDownloadAPACGB]; ok {
		t.Fatal("download_apac_gb belongs to the billing-usage slice")
	}

	b3, _ := glFindRow(r.Metrics, "p1", "b3")
	glWantGap(t, b3, gcpsource.MClassAOps, "no data")
	glWantGap(t, b3, gcpsource.MSentGB, "no data")
	glWantValue(t, b3, gcpsource.MStorageGBMonth, 7.3*share, "GB-month")

	tot, ok := glFindRow(r.Metrics, "p1", "")
	if !ok {
		t.Fatal("no total row")
	}
	glWantValue(t, tot, gcpsource.MClassAOps, 71, "count")
	glWantValue(t, tot, gcpsource.MClassBOps, 1500, "count")
	glWantValue(t, tot, gcpsource.MSentGB, 2.5, "GB")
	glWantValue(t, tot, gcpsource.MStorageGBMonth, 11.3*share, "GB-month")

	prov := tot.Cells[gcpsource.MClassAOps].Provenance
	if prov.Source != SourceGCS || !strings.Contains(prov.Query, gcsRequestCountMetric) || prov.Window != glW42 || !prov.FetchedAt.Equal(glFixedNow()) {
		t.Fatalf("bad provenance %+v", prov)
	}
	if !strings.Contains(prov.Aggregation, "ALIGN_SUM") || !strings.Contains(prov.Aggregation, "metric.labels.method") {
		t.Fatalf("aggregation %s", prov.Aggregation)
	}
	if len(r.Provenance) != 3 {
		t.Fatalf("want 3 provenance entries, got %d", len(r.Provenance))
	}
	for _, req := range f.reqs {
		if req.GetAggregation().GetAlignmentPeriod().AsDuration() != 168*time.Hour {
			t.Fatalf("alignment period %v", req.GetAggregation().GetAlignmentPeriod().AsDuration())
		}
		if strings.Contains(req.GetFilter(), "total_bytes") && req.GetAggregation().GetPerSeriesAligner() != monitoringpb.Aggregation_ALIGN_MEAN {
			t.Fatal("total_bytes must be ALIGN_MEAN")
		}
	}
}

func TestFetchGCS_ErrorsAndPartialData(t *testing.T) {
	denied := status.Error(codes.PermissionDenied, "Permission denied (or the resource may not exist).")
	cases := []struct {
		name       string
		fake       *gcsLogFake
		projects   []string
		wantStatus string
		check      func(t *testing.T, r gcpsource.Result)
	}{
		{
			name:       "all queries denied",
			fake:       &gcsLogFake{errs: map[string]error{gcsLogKey("p1", gcsRequestCountMetric): denied, gcsLogKey("p1", gcsSentBytesMetric): denied, gcsLogKey("p1", gcsTotalBytesMetric): denied}},
			projects:   []string{"p1"},
			wantStatus: gcpsource.StatusPermissionDenied,
			check: func(t *testing.T, r gcpsource.Result) {
				if !strings.Contains(r.Hint, RoleHint) {
					t.Fatalf("hint %q lacks %s", r.Hint, RoleHint)
				}
				if len(r.Metrics) != 1 {
					t.Fatalf("want only the total row, got %d", len(r.Metrics))
				}
				c := glWantGap(t, r.Metrics[0], gcpsource.MStorageGBMonth, "source failed")
				if !strings.Contains(c.Hint, RoleHint) {
					t.Fatalf("cell hint %q", c.Hint)
				}
			},
		},
		{
			name: "one project denied, other project kept",
			fake: &gcsLogFake{
				series: map[string][]*monitoringpb.TimeSeries{gcsLogKey("ok", gcsRequestCountMetric): {glReqSeries("b", "ReadObject", 9)}},
				errs:   map[string]error{gcsLogKey("bad", gcsRequestCountMetric): denied, gcsLogKey("bad", gcsSentBytesMetric): denied, gcsLogKey("bad", gcsTotalBytesMetric): denied},
			},
			projects:   []string{"ok", "bad"},
			wantStatus: gcpsource.StatusPermissionDenied,
			check: func(t *testing.T, r gcpsource.Result) {
				b, ok := glFindRow(r.Metrics, "ok", "b")
				if !ok {
					t.Fatal("healthy project's bucket row missing")
				}
				glWantValue(t, b, gcpsource.MClassBOps, 9, "count")
				glWantGap(t, b, gcpsource.MStorageGBMonth, "no data")
				bad, _ := glFindRow(r.Metrics, "bad", "")
				glWantGap(t, bad, gcpsource.MClassAOps, "source failed")
				if !strings.Contains(r.Message, "project bad") || strings.Contains(r.Message, "project ok") {
					t.Fatalf("message %q", r.Message)
				}
			},
		},
		{
			name: "storage query fails, requests still filled",
			fake: &gcsLogFake{
				series: map[string][]*monitoringpb.TimeSeries{gcsLogKey("p1", gcsRequestCountMetric): {glReqSeries("b", "WriteObject", 2)}},
				errs:   map[string]error{gcsLogKey("p1", gcsTotalBytesMetric): errors.New("boom")},
			},
			projects:   []string{"p1"},
			wantStatus: gcpsource.StatusFailed,
			check: func(t *testing.T, r gcpsource.Result) {
				b, _ := glFindRow(r.Metrics, "p1", "b")
				glWantValue(t, b, gcpsource.MClassAOps, 2, "count")
				glWantValue(t, b, gcpsource.MClassBOps, 0, "count")
				glWantGap(t, b, gcpsource.MStorageGBMonth, "source failed")
				glWantGap(t, b, gcpsource.MSentGB, "no data")
			},
		},
		{
			name:       "no buckets at all",
			fake:       &gcsLogFake{},
			projects:   []string{"p1"},
			wantStatus: gcpsource.StatusOK,
			check: func(t *testing.T, r gcpsource.Result) {
				if len(r.Metrics) != 1 {
					t.Fatalf("want total row only, got %d", len(r.Metrics))
				}
				glWantGap(t, r.Metrics[0], gcpsource.MClassAOps, "no data")
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := FetchGCS(context.Background(), c.fake, c.projects, glW42, glFixedNow)
			if r.Status != c.wantStatus {
				t.Fatalf("status %q, want %q (msg %q)", r.Status, c.wantStatus, r.Message)
			}
			if r.Finished.IsZero() || r.Started.IsZero() {
				t.Fatal("result not stamped")
			}
			c.check(t, r)
		})
	}
}

func glRealProjects(t *testing.T) []string {
	t.Helper()
	testenv.LoadDotEnv(t)
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" || os.Getenv("CLOUD0_GCP_PROJECTS") == "" {
		t.Skip("GOOGLE_APPLICATION_CREDENTIALS or CLOUD0_GCP_PROJECTS not set — fill .env (see .env.example) to run real Monitoring tests")
	}
	return strings.Split(os.Getenv("CLOUD0_GCP_PROJECTS"), ",")
}

func TestFetchGCS_RealMonitoringW42(t *testing.T) {
	projects := glRealProjects(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	l, closeFn, err := NewLister(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	r := FetchGCS(ctx, l, projects, glW42, time.Now)
	t.Logf("status=%s hint=%q message=%q", r.Status, r.Hint, r.Message)
	if r.Status != gcpsource.StatusOK && !strings.Contains(r.Hint, RoleHint) {
		t.Fatalf("non-ok status %s without role hint", r.Status)
	}
	var a, b, sent, stor float64
	var buckets, i int
	for _, row := range r.Metrics {
		if row.Resource != "" {
			buckets++
			continue
		}
		i++
		for _, k := range []string{gcpsource.MClassAOps, gcpsource.MClassBOps, gcpsource.MSentGB, gcpsource.MStorageGBMonth} {
			c := row.Cells[k]
			if c.Value == nil {
				t.Logf("project#%d total %s: gap %q", i, k, c.Gap)
				continue
			}
			switch k {
			case gcpsource.MClassAOps:
				a += *c.Value
			case gcpsource.MClassBOps:
				b += *c.Value
			case gcpsource.MSentGB:
				sent += *c.Value
			case gcpsource.MStorageGBMonth:
				stor += *c.Value
			}
		}
	}
	t.Logf("W42 GCS: buckets=%d classA=%.0f classB=%.0f sent=%.3f GB storage=%.3f GB-month", buckets, a, b, sent, stor)
	if buckets == 0 || b <= 0 || stor <= 0 {
		t.Fatalf("expected real bucket rows with class B ops and storage > 0")
	}
}
