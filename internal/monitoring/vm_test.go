package monitoring

import (
	"context"
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

var vmTestWindow = gcpsource.Window{
	Start: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
}

func vmTestNow() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }

// vmFakeLister answers by (project, filter); unknown pairs return no series.
type vmFakeLister struct {
	series map[string][]*monitoringpb.TimeSeries
	errs   map[string]error
	calls  []*monitoringpb.ListTimeSeriesRequest
}

func vmKeyOf(project, filter string) string { return project + "|" + filter }

func (f *vmFakeLister) ListTimeSeries(_ context.Context, req *monitoringpb.ListTimeSeriesRequest) ([]*monitoringpb.TimeSeries, error) {
	f.calls = append(f.calls, req)
	k := vmKeyOf(strings.TrimPrefix(req.GetName(), "projects/"), req.GetFilter())
	if err, ok := f.errs[k]; ok {
		return nil, err
	}
	return f.series[k], nil
}

func vmTS(metricLabels, resLabels map[string]string, v float64) *monitoringpb.TimeSeries {
	return &monitoringpb.TimeSeries{
		Metric:   &metric.Metric{Labels: metricLabels},
		Resource: &monitoredres.MonitoredResource{Type: "gce_instance", Labels: resLabels},
		Points: []*monitoringpb.Point{{
			Value: &monitoringpb.TypedValue{Value: &monitoringpb.TypedValue_DoubleValue{DoubleValue: v}},
		}},
	}
}

func vmCPU(name, id string, frac float64) *monitoringpb.TimeSeries {
	return vmTS(map[string]string{"instance_name": name}, map[string]string{"instance_id": id}, frac)
}

func vmMem(id string, pct float64) *monitoringpb.TimeSeries {
	return vmTS(map[string]string{"state": "used"}, map[string]string{"instance_id": id}, pct)
}

func vmBytes(id, dev, state string, b float64) *monitoringpb.TimeSeries {
	return vmTS(map[string]string{"device": dev, "state": state}, map[string]string{"instance_id": id}, b)
}

func vmPct(id, dev string, pct float64) *monitoringpb.TimeSeries {
	return vmTS(map[string]string{"device": dev, "state": "used"}, map[string]string{"instance_id": id}, pct)
}

// rootDisk returns bytes_used series for a VM: root sda1 at used/(total),
// plus an EFI partition and a full snap loop device that must be ignored.
func vmRootDisk(id string, used, free, reserved float64) []*monitoringpb.TimeSeries {
	return []*monitoringpb.TimeSeries{
		vmBytes(id, "/dev/sda1", "used", used),
		vmBytes(id, "/dev/sda1", "free", free),
		vmBytes(id, "/dev/sda1", "reserved", reserved),
		vmBytes(id, "/dev/sda15", "used", 6e6),
		vmBytes(id, "/dev/sda15", "free", 100e6),
		vmBytes(id, "/dev/loop0", "used", 80e6),
		vmBytes(id, "/dev/loop0", "free", 0),
	}
}

func vmCellValue(t *testing.T, c gcpsource.Cell) float64 {
	t.Helper()
	if c.Value == nil {
		t.Fatalf("cell has no value (gap %q, hint %q)", c.Gap, c.Hint)
	}
	return *c.Value
}

func vmRowsByName(rows []gcpsource.MetricRow) map[string]gcpsource.MetricRow {
	out := map[string]gcpsource.MetricRow{}
	for _, r := range rows {
		out[r.Project+"/"+r.Resource] = r
	}
	return out
}

func vmNear(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestFetchVM_MapsAgentSeriesByInstanceIDAndGapsVMsWithoutAgent(t *testing.T) {
	f := &vmFakeLister{series: map[string][]*monitoringpb.TimeSeries{
		vmKeyOf("proj-a", vmCPUFilter): {
			vmCPU("vm-agent", "111", 0.25),
			vmCPU("vm-noagent", "222", 0.0645),
			vmCPU("vm-memonly", "333", 0.10),
		},
		vmKeyOf("proj-a", vmMemFilter):       {vmMem("111", 63.5), vmMem("333", 41.0), vmMem("999-unknown", 50)},
		vmKeyOf("proj-a", vmDiskBytesFilter): vmRootDisk("111", 30, 60, 10),
	}}
	res := FetchVM(context.Background(), f, []string{"proj-a"}, vmTestWindow, vmTestNow)
	if res.Status != gcpsource.StatusOK || res.Source != "monitoring_vm" {
		t.Fatalf("status %q source %q message %q", res.Status, res.Source, res.Message)
	}
	if len(res.Metrics) != 3 {
		t.Fatalf("want 3 rows, got %d", len(res.Metrics))
	}
	rows := vmRowsByName(res.Metrics)

	a := rows["proj-a/vm-agent"]
	if a.Group != gcpsource.GroupVM {
		t.Fatalf("group %q", a.Group)
	}
	if v := vmCellValue(t, a.Cells[gcpsource.MCPU]); !vmNear(v, 25) {
		t.Errorf("cpu = %v, want 25", v)
	}
	if v := vmCellValue(t, a.Cells[gcpsource.MMemory]); !vmNear(v, 63.5) {
		t.Errorf("memory = %v, want 63.5", v)
	}
	if v := vmCellValue(t, a.Cells[gcpsource.MDisk]); !vmNear(v, 30) {
		t.Errorf("disk = %v, want 30 (root sda1 used/(used+free+reserved))", v)
	}
	for k, c := range a.Cells {
		if c.Unit != "%" {
			t.Errorf("%s unit %q", k, c.Unit)
		}
	}
	if p := a.Cells[gcpsource.MCPU].Provenance; p.Query != vmCPUFilter || p.Source != "monitoring_vm" || !strings.Contains(p.Aggregation, "REDUCE_MEAN") || !strings.Contains(p.Aggregation, "resource.labels.instance_id") || p.Window != vmTestWindow {
		t.Errorf("cpu provenance %+v", p)
	}
	if p := a.Cells[gcpsource.MMemory].Provenance; p.Query != vmMemFilter {
		t.Errorf("memory provenance query %q", p.Query)
	}
	if p := a.Cells[gcpsource.MDisk].Provenance; p.Query != vmDiskBytesFilter {
		t.Errorf("disk provenance query %q (bytes_used fallback expected)", p.Query)
	}

	n := rows["proj-a/vm-noagent"]
	if v := vmCellValue(t, n.Cells[gcpsource.MCPU]); !vmNear(v, 6.45) {
		t.Errorf("cpu = %v", v)
	}
	for _, k := range []string{gcpsource.MMemory, gcpsource.MDisk} {
		c := n.Cells[k]
		if c.Value != nil || c.Gap != "no agent" || !strings.Contains(c.Hint, "Ops Agent") || c.Unit != "%" || c.Provenance.Query == "" {
			t.Errorf("%s on agentless VM = %+v, want no-agent gap", k, c)
		}
	}

	m := rows["proj-a/vm-memonly"]
	if v := vmCellValue(t, m.Cells[gcpsource.MMemory]); !vmNear(v, 41) {
		t.Errorf("memory = %v", v)
	}
	if c := m.Cells[gcpsource.MDisk]; c.Value != nil || c.Gap != "no data" {
		t.Errorf("disk on agent VM without disk series = %+v, want no-data gap", c)
	}
	if len(res.Provenance) != 4 {
		t.Errorf("want 4 provenance entries (cpu, mem, disk pct, disk bytes), got %d", len(res.Provenance))
	}
	if !res.Started.Equal(vmTestNow()) || !res.Finished.Equal(vmTestNow()) {
		t.Errorf("timestamps %v %v", res.Started, res.Finished)
	}
}

func TestFetchVM_PrefersDiskPercentUsedWhenReported(t *testing.T) {
	f := &vmFakeLister{series: map[string][]*monitoringpb.TimeSeries{
		vmKeyOf("p", vmCPUFilter):     {vmCPU("vm1", "1", 0.5)},
		vmKeyOf("p", vmMemFilter):     {vmMem("1", 20)},
		vmKeyOf("p", vmDiskPctFilter): {vmPct("1", "/dev/loop3", 100), vmPct("1", "/dev/sda15", 5.4), vmPct("1", "/dev/sda1", 72.5)},
	}}
	res := FetchVM(context.Background(), f, []string{"p"}, vmTestWindow, vmTestNow)
	c := res.Metrics[0].Cells[gcpsource.MDisk]
	if v := vmCellValue(t, c); !vmNear(v, 72.5) {
		t.Errorf("disk = %v, want 72.5 from /dev/sda1", v)
	}
	if c.Provenance.Query != vmDiskPctFilter {
		t.Errorf("disk provenance %q", c.Provenance.Query)
	}
	for _, call := range f.calls {
		if call.GetFilter() == vmDiskBytesFilter {
			t.Errorf("bytes_used fallback queried although percent_used had data")
		}
	}
}

func TestFetchVM_ClassifiesErrors(t *testing.T) {
	denied := status.Error(codes.PermissionDenied, "Permission denied (or the resource may not exist).")
	disabled := status.Error(codes.PermissionDenied, "Cloud Monitoring API has not been used in project 123 before or it is disabled. SERVICE_DISABLED")
	cases := []struct {
		name       string
		projects   []string
		errs       map[string]error
		series     map[string][]*monitoringpb.TimeSeries
		wantStatus string
		wantHint   string
		wantRows   int
		wantMsg    []string
	}{
		{
			name:       "permission denied on the only project",
			projects:   []string{"p1"},
			errs:       map[string]error{vmKeyOf("p1", vmCPUFilter): denied},
			wantStatus: gcpsource.StatusPermissionDenied,
			wantHint:   RoleHint,
		},
		{
			name:       "api disabled on every project",
			projects:   []string{"p1", "p2"},
			errs:       map[string]error{vmKeyOf("p1", vmCPUFilter): disabled, vmKeyOf("p2", vmCPUFilter): disabled},
			wantStatus: gcpsource.StatusAPIDisabled,
			wantHint:   API,
			wantMsg:    []string{"p1:", "p2:"},
		},
		{
			name:     "one project fails, the other still yields rows",
			projects: []string{"p-bad", " p-good "},
			errs:     map[string]error{vmKeyOf("p-bad", vmCPUFilter): disabled},
			series: map[string][]*monitoringpb.TimeSeries{
				vmKeyOf("p-good", vmCPUFilter): {vmCPU("vm1", "1", 0.1), vmCPU("vm2", "2", 0.2)},
			},
			wantStatus: gcpsource.StatusOK,
			wantRows:   2,
			wantMsg:    []string{"p-bad", "api_disabled"},
		},
		{
			name:       "project with no VMs is ok and empty",
			projects:   []string{"p1", ""},
			wantStatus: gcpsource.StatusOK,
		},
		{
			name:       "context deadline on every project",
			projects:   []string{"p1"},
			errs:       map[string]error{vmKeyOf("p1", vmCPUFilter): context.DeadlineExceeded},
			wantStatus: gcpsource.StatusFailed,
			wantHint:   "timed out",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &vmFakeLister{series: tc.series, errs: tc.errs}
			res := FetchVM(context.Background(), f, tc.projects, vmTestWindow, vmTestNow)
			if res.Status != tc.wantStatus {
				t.Fatalf("status %q, want %q (msg %q)", res.Status, tc.wantStatus, res.Message)
			}
			if tc.wantHint != "" && !strings.Contains(res.Hint, tc.wantHint) {
				t.Errorf("hint %q lacks %q", res.Hint, tc.wantHint)
			}
			if len(res.Metrics) != tc.wantRows {
				t.Errorf("rows %d, want %d", len(res.Metrics), tc.wantRows)
			}
			for _, m := range tc.wantMsg {
				if !strings.Contains(res.Message, m) {
					t.Errorf("message %q lacks %q", res.Message, m)
				}
			}
			for _, r := range res.Metrics {
				if r.Project != "p-good" {
					t.Errorf("row from failed project: %+v", r)
				}
				if c := r.Cells[gcpsource.MMemory]; c.Gap != "no agent" {
					t.Errorf("memory %+v, want no agent", c)
				}
			}
		})
	}
}

func TestFetchVM_AgentQueryFailureMarksOnlyThatColumn(t *testing.T) {
	denied := status.Error(codes.PermissionDenied, "permission denied")
	f := &vmFakeLister{
		series: map[string][]*monitoringpb.TimeSeries{
			vmKeyOf("p", vmCPUFilter):       {vmCPU("vm1", "1", 0.3)},
			vmKeyOf("p", vmDiskBytesFilter): vmRootDisk("1", 1, 3, 0),
		},
		errs: map[string]error{vmKeyOf("p", vmMemFilter): denied},
	}
	res := FetchVM(context.Background(), f, []string{"p"}, vmTestWindow, vmTestNow)
	if res.Status != gcpsource.StatusOK || len(res.Metrics) != 1 {
		t.Fatalf("status %q rows %d", res.Status, len(res.Metrics))
	}
	cells := res.Metrics[0].Cells
	if v := vmCellValue(t, cells[gcpsource.MCPU]); !vmNear(v, 30) {
		t.Errorf("cpu %v", v)
	}
	if c := cells[gcpsource.MMemory]; c.Value != nil || c.Gap != "source failed" || !strings.Contains(c.Hint, RoleHint) {
		t.Errorf("memory %+v, want source-failed gap with role hint", c)
	}
	if v := vmCellValue(t, cells[gcpsource.MDisk]); !vmNear(v, 25) {
		t.Errorf("disk %v, want 25", v)
	}
}

func TestFetchVM_SkipsSeriesWithoutPointsOrName(t *testing.T) {
	empty := vmCPU("vm-empty", "9", 0)
	empty.Points = nil
	f := &vmFakeLister{series: map[string][]*monitoringpb.TimeSeries{
		vmKeyOf("p", vmCPUFilter): {empty, vmCPU("", "8", 0.4), vmCPU("vm1", "1", 0.4)},
	}}
	res := FetchVM(context.Background(), f, []string{"p"}, vmTestWindow, vmTestNow)
	if len(res.Metrics) != 1 || res.Metrics[0].Resource != "vm1" {
		t.Fatalf("rows %+v", res.Metrics)
	}
}

func TestVMPickRoot_ChoosesRootPartition(t *testing.T) {
	cases := []struct {
		name string
		devs map[string]float64
		want string
		ok   bool
	}{
		{"scsi root wins over bigger data disk", map[string]float64{"/dev/sda1": 10, "/dev/sdb": 500}, "/dev/sda1", true},
		{"nvme root", map[string]float64{"/dev/nvme0n1p15": 1, "/dev/nvme0n1p1": 10}, "/dev/nvme0n1p1", true},
		{"largest non-loop otherwise", map[string]float64{"/dev/loop0": 900, "/dev/vda2": 50, "/dev/vda1": 1}, "/dev/vda2", true},
		{"only loop devices", map[string]float64{"/dev/loop0": 1}, "", false},
		{"empty", map[string]float64{}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := vmPickRoot(tc.devs)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got %q,%v want %q,%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestFetchVM_RealMonitoringWeek42 runs against the real Cloud Monitoring API.
func TestFetchVM_RealMonitoringWeek42(t *testing.T) {
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
	defer func() { _ = closeFn() }()

	res := FetchVM(ctx, l, strings.Split(os.Getenv("CLOUD0_GCP_PROJECTS"), ","), vmTestWindow, time.Now)
	t.Logf("status=%s rows=%d queries=%d message=%q", res.Status, len(res.Metrics), len(res.Provenance), res.Message)
	if res.Status != gcpsource.StatusOK {
		t.Fatalf("status %q: %s (hint %s)", res.Status, res.Message, res.Hint)
	}
	if len(res.Metrics) == 0 {
		t.Fatal("no VM rows for W42")
	}
	withAgent, noAgent := 0, 0
	for _, r := range res.Metrics {
		cpu := vmCellValue(t, r.Cells[gcpsource.MCPU])
		if cpu <= 0 || cpu >= 100 {
			t.Errorf("%s cpu %v not in (0,100)", r.Resource, cpu)
		}
		line := []any{r.Resource, cpu}
		for _, k := range []string{gcpsource.MMemory, gcpsource.MDisk} {
			c := r.Cells[k]
			switch {
			case c.Value != nil:
				if *c.Value <= 0 || *c.Value >= 100 {
					t.Errorf("%s %s %v not in (0,100)", r.Resource, k, *c.Value)
				}
				line = append(line, *c.Value)
			case c.Gap == "no agent" || c.Gap == "no data":
				line = append(line, c.Gap)
			default:
				t.Errorf("%s %s unexpected cell %+v", r.Resource, k, c)
			}
			if c.Provenance.Query == "" || c.Provenance.Aggregation == "" {
				t.Errorf("%s %s missing provenance", r.Resource, k)
			}
		}
		if r.Cells[gcpsource.MMemory].Gap == "no agent" {
			noAgent++
		} else {
			withAgent++
		}
		t.Logf("W42 %-22v cpu=%.2f%% mem=%v disk=%v", line[0], line[1], line[2], line[3])
	}
	if withAgent == 0 {
		t.Error("no VM reported Ops Agent memory/disk in W42")
	}
	t.Logf("with agent=%d, no agent=%d", withAgent, noAgent)
}
