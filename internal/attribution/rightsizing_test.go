package attribution

import (
	"math"
	"strings"
	"testing"
	"time"
)

var rsNow = time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)

func rsVal(v float64) *float64 { return &v }

func rsSeries(project, vm string, vals ...*float64) VMCPUSeries {
	weeks := []string{"2026-W41", "2026-W42", "2026-W43"}
	s := VMCPUSeries{ProjectID: project, Instance: vm, Zone: "zone-a"}
	off := len(weeks) - len(vals)
	for i, v := range vals {
		s.Points = append(s.Points, EvidencePoint{Week: weeks[off+i], Value: v, Unit: "%"})
	}
	return s
}

func rsInput(vm ...VMCPUSeries) RightsizingInput {
	return RightsizingInput{Week: "2026-W43", Thresholds: DefaultRightsizingThresholds(), Now: rsNow, VMCPU: vm, BillingRows: map[string][]CostRow{}}
}

func rsRow(project, service, sku, res string, net float64) CostRow {
	return CostRow{Row: Row{ProjectID: project, Service: service, SKU: sku, ResourceName: res}, Net: net}
}

func TestDetectorLowCPUBoundaryAndStreak(t *testing.T) {
	cases := []struct {
		name string
		vals []*float64
		want bool
	}{
		{"all below", []*float64{rsVal(6), rsVal(7), rsVal(5)}, true},
		{"exactly at threshold is not low", []*float64{rsVal(6), rsVal(15), rsVal(5)}, false},
		{"just below threshold", []*float64{rsVal(14.99), rsVal(14.99), rsVal(14.99)}, true},
		{"one week above", []*float64{rsVal(7), rsVal(17), rsVal(6)}, false},
		{"only two weeks stored", []*float64{rsVal(5), rsVal(5)}, false},
		{"gap in the middle breaks streak", []*float64{rsVal(5), nil, rsVal(5)}, false},
		{"gap in the last week", []*float64{rsVal(5), rsVal(5), nil}, false},
		{"zero is a real value", []*float64{rsVal(0), rsVal(0), rsVal(0)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewDetector().Detect(rsInput(rsSeries("proj-a", "example-vm", tc.vals...)))
			if (len(got) == 1) != tc.want {
				t.Fatalf("got %d candidates, want hit=%v: %+v", len(got), tc.want, got)
			}
			if tc.want {
				c := got[0]
				if c.Kind != KindLowCPUVM || c.Confidence != ConfidenceMedium || len(c.Evidence.Points) != 3 ||
					c.Evidence.WindowStart != "2026-10-05" || c.Evidence.WindowEnd != "2026-10-25" ||
					c.ID != "low_cpu_vm:proj-a:example-vm" || c.Location == nil || *c.Location != "zone-a" {
					t.Fatalf("unexpected candidate %+v", c)
				}
			}
		})
	}
}

func TestDetectorLowCPUWeeksThreshold(t *testing.T) {
	in := rsInput(rsSeries("p", "vm", rsVal(5), rsVal(5)))
	in.Thresholds.Weeks = 2
	got := NewDetector().Detect(in)
	if len(got) != 1 || len(got[0].Evidence.Points) != 2 || got[0].Evidence.WindowStart != "2026-10-12" {
		t.Fatalf("weeks=2 window wrong: %+v", got)
	}
	in = rsInput(rsSeries("p", "vm", rsVal(5), rsVal(5), rsVal(5)))
	in.Thresholds.CPUPct = 5 // 5 is not < 5
	if got := NewDetector().Detect(in); len(got) != 0 {
		t.Fatalf("custom threshold boundary: %+v", got)
	}
}

func TestDetectorLowCPUSavingMath(t *testing.T) {
	in := rsInput(rsSeries("proj-a", "example-vm", rsVal(6), rsVal(7), rsVal(5)))
	in.BillingRows["2026-W43"] = []CostRow{
		rsRow("proj-a", "Compute Engine", "N1 Predefined Instance Core running in Americas", "projects/1/instances/example-vm", 150),
		rsRow("proj-a", "Compute Engine", "N1 Predefined Instance Ram running in Americas", "projects/1/instances/example-vm", 60),
		rsRow("proj-a", "Compute Engine", "Storage PD Capacity", "projects/1/disks/example-vm", 40),                      // boot disk: not core/ram
		rsRow("proj-a", "Compute Engine", "N1 Predefined Instance Core running", "projects/1/instances/other-vm", 999),   // other VM
		rsRow("proj-b", "Compute Engine", "N1 Predefined Instance Core running", "projects/1/instances/example-vm", 999), // other project
	}
	in.BillingRows["2026-W42"] = []CostRow{rsRow("proj-a", "Compute Engine", "Core", "projects/1/instances/example-vm", 5000)}
	got := NewDetector().Detect(in)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	c := got[0]
	if c.NetLastWeek == nil || *c.NetLastWeek != 210 {
		t.Fatalf("net_last_week = %v, want 210", c.NetLastWeek)
	}
	want := math.Round(210*0.5*4.348214*100) / 100
	if c.EstimatedMonthlySaving == nil || math.Abs(*c.EstimatedMonthlySaving-want) > 0.005 {
		t.Fatalf("saving = %v, want %v", c.EstimatedMonthlySaving, want)
	}
	if !strings.Contains(c.SavingBasis, "50 %") || !strings.Contains(c.SavingBasis, "4.348") {
		t.Fatalf("basis = %q", c.SavingBasis)
	}
}

func TestDetectorVMWithoutBillingRows(t *testing.T) {
	got := NewDetector().Detect(rsInput(rsSeries("p", "vm", rsVal(1), rsVal(1), rsVal(1))))
	if len(got) != 1 || got[0].NetLastWeek != nil || got[0].EstimatedMonthlySaving != nil || !strings.Contains(got[0].SavingBasis, "cannot be estimated") {
		t.Fatalf("got %+v", got)
	}
}

func TestDetectorCloudRunIdle(t *testing.T) {
	in := rsInput()
	in.BillingRows["2026-W42"] = []CostRow{rsRow("p", "Cloud Run", "Services Min Instance CPU Allocation Time", "projects/1/locations/r/services/idle-svc", 8)}
	in.BillingRows["2026-W43"] = []CostRow{
		rsRow("p", "Cloud Run", "Services Min Instance CPU Allocation Time", "projects/1/locations/r/services/idle-svc", 30),
		rsRow("p", "Cloud Run", "Services Min Instance Memory Allocation Time", "idle-svc", 20),
		rsRow("p", "Cloud Run", "CPU Allocation Time", "idle-svc", 10),
		// active service with a tiny idle amount below the minimum net
		rsRow("p", "Cloud Run", "Idle CPU", "tiny-svc", 0.5),
		rsRow("p", "Cloud Run", "CPU Allocation Time", "tiny-svc", 90),
		// non-idle only
		rsRow("p", "Cloud Run", "CPU Allocation Time", "busy-svc", 100),
		// idle SKU in another service is ignored
		rsRow("p", "Compute Engine", "Idle thing", "idle-svc", 500),
	}
	got := NewDetector().Detect(in)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	c := got[0]
	if c.Kind != KindCloudRunMinInst || c.ResourceName != "idle-svc" || c.Confidence != ConfidenceMedium || c.ResourceType != TypeCloudRunService {
		t.Fatalf("candidate %+v", c)
	}
	if *c.NetLastWeek != 50 || math.Abs(*c.EstimatedMonthlySaving-math.Round(50*4.348214*100)/100) > 0.005 {
		t.Fatalf("net/saving %v %v", *c.NetLastWeek, *c.EstimatedMonthlySaving)
	}
	// points: stored weeks only (W42, W43); W41 absent.
	if len(c.Evidence.Points) != 2 || c.Evidence.Points[0].Week != "2026-W42" || *c.Evidence.Points[0].Value != 8 || *c.Evidence.Points[1].Value != 50 {
		t.Fatalf("points %+v", c.Evidence.Points)
	}
	if !strings.Contains(c.Evidence.Notes[0], "dominates") {
		t.Fatalf("notes %v", c.Evidence.Notes)
	}
	// Lowering the bar picks up the small one.
	in.Thresholds.IdleRunMinNet = 0.1
	if got := NewDetector().Detect(in); len(got) != 2 {
		t.Fatalf("with low min net: %+v", got)
	}
	// No billing for the requested week: nothing.
	in.BillingRows = map[string][]CostRow{"2026-W42": in.BillingRows["2026-W42"]}
	if got := NewDetector().Detect(in); len(got) != 0 {
		t.Fatalf("missing last week: %+v", got)
	}
}

func rsInventory() *Inventory {
	old := rsNow.AddDate(0, 0, -45)
	return &Inventory{
		FetchedAt: rsNow,
		Disks: []Disk{
			{ProjectID: "p", Name: "orphan-disk", Zone: "zone-a", Status: "READY"},
			{ProjectID: "p", Name: "attached-disk", Zone: "zone-a", Status: "READY", Users: []string{"vm"}},
			{ProjectID: "p", Name: "live-disk", Zone: "zone-a", Status: "READY", Users: []string{"vm"}},
			{ProjectID: "p", Name: "creating", Zone: "zone-a", Status: "CREATING"},
		},
		Addresses: []Address{
			{ProjectID: "p", Name: "spare-ip", Region: "region-a", Status: "RESERVED"},
			{ProjectID: "p", Name: "used-ip", Region: "region-a", Status: "RESERVED", Users: []string{"fr"}},
			{ProjectID: "p", Name: "in-use-ip", Region: "region-a", Status: "IN_USE"},
		},
		Snapshots: []Snapshot{
			{ProjectID: "p", Name: "snap-gone", CreatedAt: old, SourceDisk: "zones/z/disks/deleted-disk"},
			{ProjectID: "p", Name: "snap-live", CreatedAt: old, SourceDisk: "live-disk"},
			{ProjectID: "p", Name: "snap-ancient", CreatedAt: rsNow.AddDate(0, 0, -130), SourceDisk: "live-disk"},
			{ProjectID: "p", Name: "snap-new", CreatedAt: rsNow.AddDate(0, 0, -5), SourceDisk: "deleted-disk"},
		},
	}
}

func TestDetectorInventoryKinds(t *testing.T) {
	in := rsInput()
	in.Inventory = rsInventory()
	in.BillingRows["2026-W43"] = []CostRow{
		rsRow("p", "Compute Engine", "Storage PD Capacity", "projects/1/disks/orphan-disk", 22),
		rsRow("p", "Compute Engine", "Static IP Charge", "projects/1/regions/r/addresses/spare-ip", 10),
		rsRow("p", "Compute Engine", "Storage PD Snapshot", "projects/1/global/snapshots/snap-gone", 4),
		rsRow("p", "Compute Engine", "Core", "projects/1/instances/orphan-disk", 1000), // VM sharing a name
	}
	got := NewDetector().Detect(in)
	byID := map[string]Candidate{}
	for _, c := range got {
		byID[c.ID] = c
	}
	for _, id := range []string{"idle_disk:p:orphan-disk", "unused_ip:p:spare-ip", "stale_snapshot:p:snap-gone", "stale_snapshot:p:snap-ancient"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("missing %s in %v", id, got)
		}
	}
	if len(got) != 4 {
		t.Fatalf("want 4 candidates, got %d: %+v", len(got), got)
	}
	d := byID["idle_disk:p:orphan-disk"]
	if d.Confidence != ConfidenceHigh || *d.NetLastWeek != 22 || math.Abs(*d.EstimatedMonthlySaving-95.66) > 0.005 {
		t.Fatalf("disk %+v", d)
	}
	if len(d.Evidence.Points) != 0 || !strings.Contains(d.Evidence.Notes[0], "READY, no attached users") || d.Evidence.WindowStart != "2026-10-26" {
		t.Fatalf("disk evidence %+v", d.Evidence)
	}
	if byID["stale_snapshot:p:snap-gone"].Confidence != ConfidenceMedium || byID["stale_snapshot:p:snap-ancient"].EstimatedMonthlySaving != nil {
		t.Fatalf("snapshots %+v", got)
	}
	// Order: saving desc, nil last (then id).
	if got[0].ID != "idle_disk:p:orphan-disk" || got[len(got)-1].EstimatedMonthlySaving != nil {
		t.Fatalf("order %+v", got)
	}
	// Inventory nil -> kinds skipped.
	in.Inventory = nil
	if got := NewDetector().Detect(in); len(got) != 0 {
		t.Fatalf("nil inventory: %+v", got)
	}
}

func TestDetectorSortingAndDeterminism(t *testing.T) {
	in := rsInput(
		rsSeries("p", "vm-small", rsVal(1), rsVal(1), rsVal(1)),
		rsSeries("p", "vm-big", rsVal(1), rsVal(1), rsVal(1)),
		rsSeries("p", "vm-none", rsVal(1), rsVal(1), rsVal(1)),
		rsSeries("p", "vm-a-none", rsVal(1), rsVal(1), rsVal(1)),
	)
	in.BillingRows["2026-W43"] = []CostRow{
		rsRow("p", "Compute Engine", "Core", "projects/1/instances/vm-small", 10),
		rsRow("p", "Compute Engine", "Core", "projects/1/instances/vm-big", 100),
	}
	a := NewDetector().Detect(in)
	ids := []string{}
	for _, c := range a {
		ids = append(ids, c.ID)
	}
	want := []string{"low_cpu_vm:p:vm-big", "low_cpu_vm:p:vm-small", "low_cpu_vm:p:vm-a-none", "low_cpu_vm:p:vm-none"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v want %v", ids, want)
	}
}

func TestDetectorBadWeekAndEmpty(t *testing.T) {
	in := rsInput()
	in.Week = "nonsense"
	if got := NewDetector().Detect(in); got == nil || len(got) != 0 {
		t.Fatalf("bad week must yield empty non-nil slice, got %#v", got)
	}
	if got := NewDetector().Detect(rsInput()); got == nil || len(got) != 0 {
		t.Fatalf("empty must be non-nil: %#v", got)
	}
}
