package attribution

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"

	"github.com/smallTechOrg/cloud0/internal/testenv"
)

func TestInventoryConvertDisk(t *testing.T) {
	d := convertDisk("proj-a", &compute.Disk{
		Name: "example-disk", Zone: "https://x/compute/v1/projects/proj-a/zones/zone-a", Status: "READY",
		SizeGb: 50, Type: "https://x/zones/zone-a/diskTypes/pd-ssd", Labels: map[string]string{"env": "dev"},
		CreationTimestamp: "2026-01-02T03:04:05.000-07:00", LastDetachTimestamp: "2026-02-01T00:00:00.000+00:00",
	})
	if len(d.Users) != 0 || d.Zone != "zone-a" || d.Type != "pd-ssd" || d.SizeGB != 50 || d.Labels["env"] != "dev" {
		t.Fatalf("bad disk %+v", d)
	}
	if d.CreatedAt.IsZero() || d.LastDetachedAt.IsZero() || !d.LastAttachedAt.IsZero() {
		t.Fatalf("bad timestamps %+v", d)
	}
	used := convertDisk("p", &compute.Disk{Name: "d", Users: []string{"https://x/instances/example-vm"}})
	if len(used.Users) != 1 || used.Users[0] != "example-vm" {
		t.Fatalf("users %v", used.Users)
	}
}

func TestInventoryConvertAddressAndSnapshot(t *testing.T) {
	a := convertAddress("p", &compute.Address{Name: "example-ip", Status: "RESERVED", Address: "203.0.113.5", Region: "https://x/regions/region-a"}, false)
	if a.Region != "region-a" || len(a.Users) != 0 || a.Status != "RESERVED" || a.Global {
		t.Fatalf("addr %+v", a)
	}
	g := convertAddress("p", &compute.Address{Name: "g", Status: "IN_USE", Users: []string{"https://x/forwardingRules/fr"}}, true)
	if g.Region != "global" || !g.Global || g.Users[0] != "fr" {
		t.Fatalf("global addr %+v", g)
	}
	created := time.Now().Add(-45 * 24 * time.Hour).UTC().Format(time.RFC3339)
	s := convertSnapshot("p", &compute.Snapshot{Name: "snap", SourceDisk: "https://x/zones/z/disks/example-disk", StorageBytes: 1 << 30, DiskSizeGb: 20, CreationTimestamp: created})
	if s.SourceDisk != "example-disk" || s.StorageBytes != 1<<30 || s.SizeGB != 20 {
		t.Fatalf("snap %+v", s)
	}
	if age := time.Since(s.CreatedAt); age < 44*24*time.Hour || age > 46*24*time.Hour {
		t.Fatalf("age %v", age)
	}
	vm := convertVM("p", &compute.Instance{Name: "example-vm", Zone: "https://x/zones/zone-a", MachineType: "https://x/machineTypes/e2-small", Status: "RUNNING"})
	if vm.Zone != "zone-a" || vm.MachineType != "e2-small" || vm.Labels == nil {
		t.Fatalf("vm %+v", vm)
	}
}

// fakeCompute serves the Compute list endpoints; project "proj-bad" -> 403
// permission, "proj-off" -> 403 API disabled, disks paginate over 2 pages.
func fakeCompute(t *testing.T, diskCalls *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/projects/proj-bad/"):
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Required 'compute.disks.list' permission","errors":[{"reason":"forbidden"}]}}`))
			return
		case strings.Contains(path, "/projects/proj-off/"):
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Compute Engine API has not been used in project 1 before or it is disabled.","errors":[{"reason":"accessNotConfigured"}]}}`))
			return
		}
		var body any
		switch {
		case strings.HasSuffix(path, "/aggregated/disks"):
			atomic.AddInt32(diskCalls, 1)
			if r.URL.Query().Get("pageToken") == "" {
				body = map[string]any{"nextPageToken": "p2", "items": map[string]any{"zones/zone-a": map[string]any{"disks": []any{
					map[string]any{"name": "disk-1", "status": "READY", "sizeGb": "10"}}}}}
			} else {
				body = map[string]any{"items": map[string]any{"zones/zone-b": map[string]any{"disks": []any{
					map[string]any{"name": "disk-2", "status": "READY", "users": []string{"x/instances/vm-1"}}}}}}
			}
		case strings.HasSuffix(path, "/aggregated/instances"):
			body = map[string]any{"items": map[string]any{"zones/zone-a": map[string]any{"instances": []any{
				map[string]any{"name": "vm-1", "status": "RUNNING"}}}}}
		case strings.HasSuffix(path, "/aggregated/addresses"):
			body = map[string]any{"items": map[string]any{"regions/region-a": map[string]any{"addresses": []any{
				map[string]any{"name": "ip-1", "status": "RESERVED", "region": "x/regions/region-a"}}}}}
		case strings.HasSuffix(path, "/global/addresses"):
			body = map[string]any{"items": []any{map[string]any{"name": "gip-1", "status": "IN_USE", "users": []string{"x/fr"}}}}
		case strings.HasSuffix(path, "/global/snapshots"):
			body = map[string]any{"items": []any{map[string]any{"name": "snap-1", "sourceDisk": "x/disks/disk-1", "storageBytes": "2048"}}}
		default:
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fakeProvider(t *testing.T, srv *httptest.Server) InventoryProvider {
	t.Helper()
	p, closeFn, err := NewInventory(context.Background(), option.WithHTTPClient(srv.Client()), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeFn() })
	return p
}

func TestInventoryFakePagination(t *testing.T) {
	var calls int32
	p := fakeProvider(t, fakeCompute(t, &calls))
	inv, st := p.Inventory(context.Background(), []string{"proj-a"})
	if st.Status != "ok" || st.Source != SourceComputeInventory || st.ErrorCode != nil {
		t.Fatalf("status %+v", st)
	}
	if calls != 2 || len(inv.Disks) != 2 {
		t.Fatalf("calls=%d disks=%d", calls, len(inv.Disks))
	}
	if len(inv.Addresses) != 2 || len(inv.Snapshots) != 1 || inv.Snapshots[0].SourceDisk != "disk-1" {
		t.Fatalf("inv %+v", inv)
	}
	if inv.Disks[0].Name != "disk-1" || len(inv.Disks[0].Users) != 0 || inv.Disks[0].SizeGB != 10 || len(inv.Disks[1].Users) != 1 {
		t.Fatalf("disks %+v", inv.Disks)
	}
	d := p.(DetailProvider).InventoryDetail(context.Background(), []string{"proj-a"})
	if len(d.VMs) != 1 || d.VMs[0].Name != "vm-1" || len(d.Statuses) != 4 {
		t.Fatalf("detail %+v", d)
	}
}

func TestInventoryFakePermissionAndDisabled(t *testing.T) {
	var calls int32
	p := fakeProvider(t, fakeCompute(t, &calls))
	ctx := context.Background()

	inv, st := p.Inventory(ctx, []string{"proj-bad"})
	if st.Status != "failed" || st.ErrorCode == nil || *st.ErrorCode != ErrCodePermissionDenied || st.FixHint == nil || !strings.Contains(*st.FixHint, "roles/compute.viewer") {
		t.Fatalf("bad status %+v", st)
	}
	if inv.Disks == nil || len(inv.Disks) != 0 {
		t.Fatalf("expected empty non-nil disks")
	}

	inv, st = p.Inventory(ctx, []string{"proj-off"})
	if st.Status != "ok" || st.Message == nil || !strings.Contains(*st.Message, "not enabled") || len(inv.Disks) != 0 {
		t.Fatalf("disabled should be ok with note: %+v", st)
	}

	// One failing project never fails the rest.
	inv, st = p.Inventory(ctx, []string{"proj-bad", "proj-a", "proj-off"})
	if st.Status != "ok" || st.Message == nil || !strings.HasPrefix(*st.Message, "partial:") || st.FixHint == nil {
		t.Fatalf("partial status %+v", st)
	}
	if len(inv.Disks) != 2 {
		t.Fatalf("disks %d", len(inv.Disks))
	}

	_, st = p.Inventory(ctx, nil)
	if st.Status != "skipped" {
		t.Fatalf("no projects: %+v", st)
	}
}

func TestInventoryTimeoutClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	p := fakeProvider(t, srv).(*computeInventory)
	p.timeout = 50 * time.Millisecond
	_, st := p.Inventory(context.Background(), []string{"proj-a"})
	if st.Status != "failed" || st.ErrorCode == nil || *st.ErrorCode != ErrCodeUpstream {
		t.Fatalf("status %+v", st)
	}
}

// Real read-only test against CLOUD0_GCP_PROJECTS (counts only are printed).
func TestInventoryRealReadOnly(t *testing.T) {
	testenv.LoadDotEnv(t)
	key := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	projects := splitCSV(os.Getenv("CLOUD0_GCP_PROJECTS"))
	if len(projects) == 0 || key == "" {
		t.Skip("GCP credentials or CLOUD0_GCP_PROJECTS genuinely absent")
	}
	if _, err := os.Stat(key); err != nil {
		t.Skip("credential file genuinely absent")
	}
	p, closeFn, err := NewInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeFn() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d := p.(DetailProvider).InventoryDetail(ctx, projects)
	inv, st := p.Inventory(ctx, projects)
	t.Logf("projects=%d vms=%d disks=%d addresses=%d snapshots=%d status=%s", len(projects), len(d.VMs), len(d.Disks), len(d.Addresses), len(d.Snapshots), st.Status)
	for _, s := range d.Statuses {
		if s.Status != "ok" && (s.ErrorCode == nil || s.FixHint == nil) {
			t.Errorf("unclassified failure kind=%s", s.Source[strings.LastIndex(s.Source, "/")+1:])
		}
	}
	if st.Status == "failed" {
		t.Fatalf("inventory failed: code=%v", st.ErrorCode)
	}
	if len(d.VMs) < 1 || len(d.Disks) < 1 || len(inv.Disks) != len(d.Disks) {
		t.Fatalf("expected >=1 VM and >=1 disk, got vms=%d disks=%d", len(d.VMs), len(d.Disks))
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}
