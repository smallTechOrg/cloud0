package monitoring

import (
	"context"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

func glIngest(resourceType string, b float64) *monitoringpb.TimeSeries {
	return gcsLogSeries(map[string]string{"resource_type": resourceType}, nil, b, true)
}

func TestFetchLogging_CasesTable(t *testing.T) {
	denied := status.Error(codes.PermissionDenied, "Permission denied (or the resource may not exist).")
	disabled := status.Error(codes.PermissionDenied, "Cloud Monitoring API has not been used in project 1 before or it is disabled. SERVICE_DISABLED")
	cases := []struct {
		name       string
		fake       *gcsLogFake
		projects   []string
		wantStatus string
		wantHint   string
		want       map[string]any // project -> float64 GB or gap string
	}{
		{
			name: "sums every resource type",
			fake: &gcsLogFake{series: map[string][]*monitoringpb.TimeSeries{
				gcsLogKey("p1", loggingBytesIngestedMetric): {glIngest("gce_instance", 1.2e9), glIngest("cloud_run_revision", 2.6e8)},
			}},
			projects:   []string{"p1"},
			wantStatus: gcpsource.StatusOK,
			want:       map[string]any{"p1": 1.46},
		},
		{
			name:       "permission denied",
			fake:       &gcsLogFake{errs: map[string]error{gcsLogKey("p1", loggingBytesIngestedMetric): denied}},
			projects:   []string{"p1"},
			wantStatus: gcpsource.StatusPermissionDenied,
			wantHint:   RoleHint,
			want:       map[string]any{"p1": "source failed"},
		},
		{
			name: "partial: one project denied, one ok, one empty",
			fake: &gcsLogFake{
				series: map[string][]*monitoringpb.TimeSeries{gcsLogKey("ok", loggingBytesIngestedMetric): {glIngest("project", 5e8)}},
				errs:   map[string]error{gcsLogKey("bad", loggingBytesIngestedMetric): denied},
			},
			projects:   []string{"ok", "bad", "empty"},
			wantStatus: gcpsource.StatusPermissionDenied,
			wantHint:   RoleHint,
			want:       map[string]any{"ok": 0.5, "bad": "source failed", "empty": "no data"},
		},
		{
			name:       "api disabled",
			fake:       &gcsLogFake{errs: map[string]error{gcsLogKey("p1", loggingBytesIngestedMetric): disabled}},
			projects:   []string{"p1"},
			wantStatus: gcpsource.StatusAPIDisabled,
			wantHint:   API,
			want:       map[string]any{"p1": "source failed"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := FetchLogging(context.Background(), c.fake, c.projects, glW42, glFixedNow)
			if r.Source != "monitoring_logging" || r.Status != c.wantStatus {
				t.Fatalf("source/status %q/%q, want %q (msg %q)", r.Source, r.Status, c.wantStatus, r.Message)
			}
			if !strings.Contains(r.Hint, c.wantHint) {
				t.Fatalf("hint %q lacks %q", r.Hint, c.wantHint)
			}
			if len(r.Metrics) != len(c.want) {
				t.Fatalf("rows %d, want %d", len(r.Metrics), len(c.want))
			}
			for p, w := range c.want {
				row, ok := glFindRow(r.Metrics, p, p)
				if !ok || row.Group != gcpsource.GroupLogging {
					t.Fatalf("missing logging row for %s", p)
				}
				switch v := w.(type) {
				case float64:
					glWantValue(t, row, gcpsource.MLoggingGB, v, "GB")
					prov := row.Cells[gcpsource.MLoggingGB].Provenance
					if !strings.Contains(prov.Query, loggingBytesIngestedMetric) || prov.Window != glW42 || !strings.Contains(prov.Aggregation, "REDUCE_SUM") {
						t.Fatalf("provenance %+v", prov)
					}
				case string:
					glWantGap(t, row, gcpsource.MLoggingGB, v)
				}
			}
		})
	}
}

func TestFetchLogging_RealMonitoringW42(t *testing.T) {
	projects := glRealProjects(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	l, closeFn, err := NewLister(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	r := FetchLogging(ctx, l, projects, glW42, time.Now)
	t.Logf("status=%s hint=%q message=%q", r.Status, r.Hint, r.Message)
	if r.Status != gcpsource.StatusOK && !strings.Contains(r.Hint, RoleHint) {
		t.Fatalf("non-ok status %s without role hint", r.Status)
	}
	var total float64
	for i, row := range r.Metrics {
		c := row.Cells[gcpsource.MLoggingGB]
		if c.Value == nil {
			t.Logf("project#%d: gap %q", i, c.Gap)
			continue
		}
		t.Logf("project#%d: %.4f GB", i, *c.Value)
		total += *c.Value
	}
	t.Logf("W42 logging total = %.4f GB", total)
	if total <= 0 {
		t.Fatal("expected logging GB > 0 for W42")
	}
}
