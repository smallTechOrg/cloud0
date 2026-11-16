package httpapi

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/attribution"
	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

type rsFakeInventory struct {
	inv    attribution.Inventory
	status attribution.SourceStatus
	got    []string
}

func (f *rsFakeInventory) Inventory(_ context.Context, projects []string) (attribution.Inventory, attribution.SourceStatus) {
	f.got = projects
	out := attribution.Inventory{FetchedAt: f.inv.FetchedAt}
	in := func(p string) bool {
		for _, x := range projects {
			if x == p {
				return true
			}
		}
		return false
	}
	for _, d := range f.inv.Disks {
		if in(d.ProjectID) {
			out.Disks = append(out.Disks, d)
		}
	}
	for _, a := range f.inv.Addresses {
		if in(a.ProjectID) {
			out.Addresses = append(out.Addresses, a)
		}
	}
	return out, f.status
}

func rsFixture(t *testing.T, att *AttributionDeps) *fixture {
	t.Helper()
	f := newFixture(t, true, phase1Now)
	f.deps.Attribution = att
	f.srv.Close()
	f.srv = httptest.NewServer(NewHandler(f.deps))
	t.Cleanup(f.srv.Close)
	return f
}

func rsRun(t *testing.T, f *fixture, week isoweek.Week) (store.Run, store.RunSource) {
	t.Helper()
	ctx := context.Background()
	run, err := store.CreateRun(ctx, f.store.DB, store.NewRun{Trigger: store.TriggerRefreshWeek, From: week, To: week, StartedAt: phase1Now})
	if err != nil {
		t.Fatal(err)
	}
	jobID := "job-rs-1"
	src, err := store.AddSource(ctx, f.store.DB, store.NewRunSource{RunID: run.ID, Source: store.SourceBigQueryBilling, Status: store.SourceOK,
		StartedAt: phase1Now, FinishedAt: phase1Now.Add(time.Second), SQLText: "SELECT 1", Params: map[string]string{}, JobID: &jobID})
	if err != nil {
		t.Fatal(err)
	}
	return run, src
}

func rsSeedBilling(t *testing.T, f *fixture, week string, rows []store.SnapshotRow) {
	t.Helper()
	w := isoweek.MustParse(week)
	run, src := rsRun(t, f, w)
	err := f.store.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := store.InsertSnapshot(context.Background(), tx, store.NewSnapshot{Week: w, FetchedAt: phase1Now, StatusAtFetch: isoweek.StatusFinal,
			RunID: run.ID, RunSourceID: src.ID, Rows: rows})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func rsSeedMetrics(t *testing.T, f *fixture, week string, project, vm string, cpu *float64, gap bool, others ...gcpsource.MetricRow) {
	t.Helper()
	w := isoweek.MustParse(week)
	run, _ := rsRun(t, f, w)
	cell := gcpsource.Cell{Value: cpu, Unit: "%", Provenance: p2prov("vm")}
	if gap {
		cell = gcpsource.Cell{Gap: "no data", Hint: "VM was off", Unit: "%", Provenance: p2prov("vm")}
	}
	_, err := store.InsertMetricSnapshot(context.Background(), f.store.DB, store.NewMetricSnapshot{Week: w, FetchedAt: phase1Now,
		StatusAtFetch: isoweek.StatusFinal, RunID: run.ID,
		Rows: append([]gcpsource.MetricRow{{Group: gcpsource.GroupVM, Project: project, Resource: vm, Cells: map[string]gcpsource.Cell{gcpsource.MCPU: cell}}}, others...)})
	if err != nil {
		t.Fatal(err)
	}
}

func rsBillRow(project, service, sku, res string, gross float64) store.SnapshotRow {
	return store.SnapshotRow{ProjectID: project, ServiceID: "svc", Service: service, SKUID: "sku-" + sku, SKU: sku, ResourceName: strp(res), Gross: gross, LineItems: 1}
}

func rsDeps(inv attribution.InventoryProvider) *AttributionDeps {
	return &AttributionDeps{Detector: attribution.NewDetector(), Inventory: inv}
}

func rsSourceByName(t *testing.T, m map[string]any, name string) map[string]any {
	t.Helper()
	for _, s := range asList(t, m["sources"]) {
		if sm := asMap(t, s); sm["source"] == name {
			return sm
		}
	}
	t.Fatalf("no source %q in %v", name, m["sources"])
	return nil
}

func TestRightsizing_NotConfigured503(t *testing.T) {
	f := rsFixture(t, nil)
	res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/rightsizing", "")
	if res.StatusCode != 503 || asMap(t, m["error"])["code"] != "attribution_not_configured" {
		t.Fatalf("%d %v", res.StatusCode, m)
	}
}

func TestRightsizing_BadParams(t *testing.T) {
	f := rsFixture(t, rsDeps(nil))
	if res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/rightsizing?week=junk", ""); res.StatusCode != 400 || asMap(t, m["error"])["code"] != "invalid_week" {
		t.Fatalf("%d %v", res.StatusCode, m)
	}
	if res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/rightsizing?scope=nope", ""); res.StatusCode != 400 || asMap(t, m["error"])["code"] != "invalid_scope" {
		t.Fatalf("%d %v", res.StatusCode, m)
	}
}

func TestRightsizing_EmptyStoreHintsWhichWeeksNeedRefresh(t *testing.T) {
	f := rsFixture(t, rsDeps(nil))
	res, m, raw := f.doJSON(t, http.MethodGet, "/api/v1/rightsizing?week=2026-W43", "")
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, raw)
	}
	if !strings.Contains(string(raw), `"candidates":[]`) {
		t.Fatalf("candidates must be []: %s", raw)
	}
	mon := rsSourceByName(t, m, "monitoring_vm")
	if mon["status"] != "skipped" || !strings.Contains(mon["fix_hint"].(string), "2026-W41, 2026-W42, 2026-W43") {
		t.Fatalf("monitoring source %v", mon)
	}
	if rsSourceByName(t, m, "billing_snapshots")["status"] != "skipped" {
		t.Fatalf("billing source %v", m["sources"])
	}
	inv := rsSourceByName(t, m, "compute_inventory")
	if inv["status"] != "skipped" || !strings.Contains(inv["fix_hint"].(string), "roles/compute.viewer") {
		t.Fatalf("inventory source %v", inv)
	}
	th := asMap(t, m["thresholds"])
	if num(th["cpu_pct"]) != 15 || num(th["weeks"]) != 3 || num(th["stale_snapshot_days"]) != 30 || num(th["idle_run_min_net"]) != 1 {
		t.Fatalf("thresholds %v", th)
	}
	if num(m["monthly_factor"]) != 4.348214 || m["currency"] != "INR" || num(asMap(t, m["totals"])["count"]) != 0 {
		t.Fatalf("body %v", m)
	}
}

func TestRightsizing_EndToEndLowCPUDiskIPAndCloudRun(t *testing.T) {
	inv := &rsFakeInventory{
		status: attribution.SourceStatus{Source: "compute_inventory", Status: "ok"},
		inv: attribution.Inventory{FetchedAt: phase1Now,
			Disks:     []attribution.Disk{{ProjectID: "project-a", Name: "spare-disk", Zone: "zone-a", Status: "READY"}},
			Addresses: []attribution.Address{{ProjectID: "project-a", Name: "spare-ip", Region: "region-a", Status: "RESERVED"}}},
	}
	f := rsFixture(t, rsDeps(inv))
	for _, wk := range []string{"2026-W41", "2026-W42", "2026-W43"} {
		busy := gcpsource.MetricRow{Group: gcpsource.GroupVM, Project: "project-a", Resource: "busy-vm",
			Cells: map[string]gcpsource.Cell{gcpsource.MCPU: fcell(60, "%", "vm")}}
		rsSeedMetrics(t, f, wk, "project-a", "example-vm", ptrF(6), false, busy)
	}
	rsSeedBilling(t, f, "2026-W42", []store.SnapshotRow{rsBillRow("project-a", "Cloud Run", "Services Min Instance CPU", "idle-svc", 8)})
	rsSeedBilling(t, f, "2026-W43", []store.SnapshotRow{
		rsBillRow("project-a", "Compute Engine", "N1 Predefined Instance Core running", "projects/1/instances/example-vm", 300),
		rsBillRow("project-a", "Compute Engine", "N1 Predefined Instance Ram running", "projects/1/instances/example-vm", 110),
		rsBillRow("project-a", "Compute Engine", "Storage PD Capacity", "projects/1/disks/spare-disk", 22),
		rsBillRow("project-a", "Compute Engine", "Static IP", "projects/1/regions/r/addresses/spare-ip", 10),
		rsBillRow("project-a", "Cloud Run", "Services Min Instance CPU", "idle-svc", 40),
	})
	res, m, raw := f.doJSON(t, http.MethodGet, "/api/v1/rightsizing?week=2026-W43", "")
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, raw)
	}
	cands := asList(t, m["candidates"])
	ids := []string{}
	for _, c := range cands {
		ids = append(ids, asMap(t, c)["id"].(string))
	}
	want := "low_cpu_vm:project-a:example-vm,cloud_run_min_instances:project-a:idle-svc,idle_disk:project-a:spare-disk,unused_ip:project-a:spare-ip"
	if strings.Join(ids, ",") != want {
		t.Fatalf("ids = %v", ids)
	}
	vm := asMap(t, cands[0])
	wantSaving := math.Round(410*0.5*4.348214*100) / 100
	if math.Abs(num(vm["estimated_monthly_saving"])-wantSaving) > 0.005 || num(vm["net_last_week"]) != 410 || len(asList(t, asMap(t, vm["evidence"])["points"])) != 3 {
		t.Fatalf("vm candidate %v", vm)
	}
	tot := asMap(t, m["totals"])
	sum := 0.0
	for _, c := range cands {
		sum += num(asMap(t, c)["estimated_monthly_saving"])
	}
	if num(tot["count"]) != 4 || math.Abs(num(tot["estimated_monthly_saving"])-sum) > 0.011 {
		t.Fatalf("totals %v vs %v", tot, sum)
	}
	if rsSourceByName(t, m, "monitoring_vm")["status"] != "ok" || rsSourceByName(t, m, "compute_inventory")["status"] != "ok" {
		t.Fatalf("sources %v", m["sources"])
	}
	if strings.Join(inv.got, ",") != "project-a,project-b" {
		t.Fatalf("inventory projects %v", inv.got)
	}
	// scope narrows projects and rows.
	_, m2, _ := f.doJSON(t, http.MethodGet, "/api/v1/rightsizing?week=2026-W43&scope=project-b", "")
	if num(asMap(t, m2["totals"])["count"]) != 0 || strings.Join(inv.got, ",") != "project-b" {
		t.Fatalf("scoped %v %v", m2["totals"], inv.got)
	}
}

func ptrF(v float64) *float64 { return &v }

func TestRightsizing_GapAndMissingWeekBreakStreak(t *testing.T) {
	f := rsFixture(t, rsDeps(nil))
	rsSeedMetrics(t, f, "2026-W41", "project-a", "gappy-vm", ptrF(5), false)
	rsSeedMetrics(t, f, "2026-W42", "project-a", "gappy-vm", nil, true)
	rsSeedMetrics(t, f, "2026-W43", "project-a", "gappy-vm", ptrF(5), false)
	_, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/rightsizing?week=2026-W43", "")
	if num(asMap(t, m["totals"])["count"]) != 0 {
		t.Fatalf("gap must break streak: %v", m["candidates"])
	}
	// Missing W41 snapshot (only W42/W43 stored): partial warning names the week.
	f2 := rsFixture(t, rsDeps(nil))
	rsSeedMetrics(t, f2, "2026-W42", "project-a", "vm", ptrF(5), false)
	rsSeedMetrics(t, f2, "2026-W43", "project-a", "vm", ptrF(5), false)
	_, m2, _ := f2.doJSON(t, http.MethodGet, "/api/v1/rightsizing?week=2026-W43", "")
	mon := rsSourceByName(t, m2, "monitoring_vm")
	if num(asMap(t, m2["totals"])["count"]) != 0 || !strings.Contains(mon["message"].(string), "2026-W41") {
		t.Fatalf("missing week: %v / %v", m2["candidates"], mon)
	}
}

func TestRightsizing_InventoryFailureStays200WithHint(t *testing.T) {
	code, msg, hint := "permission_denied", "denied", "Grant roles/compute.viewer to the service account and enable compute.googleapis.com."
	inv := &rsFakeInventory{status: attribution.SourceStatus{Source: "compute_inventory", Status: "failed", ErrorCode: &code, Message: &msg, FixHint: &hint},
		inv: attribution.Inventory{Disks: []attribution.Disk{{ProjectID: "project-a", Name: "d", Status: "READY"}}}}
	f := rsFixture(t, rsDeps(inv))
	res, m, raw := f.doJSON(t, http.MethodGet, "/api/v1/rightsizing?week=2026-W43", "")
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, raw)
	}
	s := rsSourceByName(t, m, "compute_inventory")
	if s["status"] != "failed" || s["error_code"] != "permission_denied" || s["fix_hint"] != hint {
		t.Fatalf("source %v", s)
	}
	if num(asMap(t, m["totals"])["count"]) != 0 {
		t.Fatalf("failed inventory must yield no inventory candidates: %v", m["candidates"])
	}
}

func TestRightsizing_SettingsOverrideThresholdsAndInvalidFallBack(t *testing.T) {
	f := rsFixture(t, rsDeps(nil))
	if err := store.PutSettings(context.Background(), f.store.DB, map[string]string{
		"rightsizing.cpu_pct": "40", "rightsizing.weeks": "2", "rightsizing.stale_snapshot_days": "abc", "rightsizing.idle_run_min_net": "5"}, phase1Now); err != nil {
		t.Fatal(err)
	}
	rsSeedMetrics(t, f, "2026-W42", "project-a", "mid-vm", ptrF(30), false)
	rsSeedMetrics(t, f, "2026-W43", "project-a", "mid-vm", ptrF(35), false)
	_, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/rightsizing?week=2026-W43", "")
	th := asMap(t, m["thresholds"])
	if num(th["cpu_pct"]) != 40 || num(th["weeks"]) != 2 || num(th["stale_snapshot_days"]) != 30 || num(th["idle_run_min_net"]) != 5 {
		t.Fatalf("thresholds %v", th)
	}
	c := asList(t, m["candidates"])
	if len(c) != 1 || len(asList(t, asMap(t, asMap(t, c[0])["evidence"])["points"])) != 2 {
		t.Fatalf("candidates %v", c)
	}
}
