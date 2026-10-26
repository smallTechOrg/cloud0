package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

// openMigrated opens a fresh temp SQLite file and applies the migrations.
func openMigrated(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), testenv.TempDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

var testNow = time.Date(2026, time.October, 26, 9, 0, 0, 0, time.UTC)

// seedRunAndSource creates one ok run with one ok bigquery source so snapshots
// have something to point at.
func seedRunAndSource(t *testing.T, q Querier, from, to isoweek.Week) (Run, RunSource) {
	t.Helper()
	ctx := context.Background()
	run, err := CreateRun(ctx, q, NewRun{Trigger: TriggerRefreshWeek, From: from, To: to, StartedAt: testNow})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	jobID := "job-test-1"
	bp, bb, rr := int64(1000), int64(10485760), int64(3)
	cache := false
	src, err := AddSource(ctx, q, NewRunSource{
		RunID: run.ID, Source: SourceBigQueryBilling, Status: SourceOK,
		StartedAt: testNow, FinishedAt: testNow.Add(2 * time.Second),
		SQLText: "SELECT 1 -- DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end",
		Params:  map[string]string{"part_start": "2026-10-11", "part_end": "2026-10-20", "usage_start": "2026-10-12T00:00:00Z", "usage_end": "2026-10-19T00:00:00Z"},
		JobID:   &jobID, BytesProcessed: &bp, BytesBilled: &bb, CacheHit: &cache, RowsReturned: &rr,
	})
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	return run, src
}

func strp(s string) *string { return &s }

// fixtureRows is a small realistic week: two projects, an unattributed SKU,
// a fully-credited service, both Gemini spellings and an unknown service.
func fixtureRows() []SnapshotRow {
	return []SnapshotRow{
		{ProjectID: "proj-a", ServiceID: "svc-ce", Service: "Compute Engine", SKUID: "sku-core", SKU: "N1 Core", ResourceName: strp("vm-1"), ResourceGlobalName: strp("//compute/vm-1"), Gross: 100, Credits: -10},
		{ProjectID: "proj-a", ServiceID: "svc-ce", Service: "Compute Engine", SKUID: "sku-core", SKU: "N1 Core", ResourceName: strp("vm-2"), Gross: 50, Credits: 0},
		{ProjectID: "proj-b", ServiceID: "svc-ce", Service: "Compute Engine", SKUID: "sku-core", SKU: "N1 Core", ResourceName: strp("vm-3"), Gross: 20, Credits: -5},
		{ProjectID: "proj-a", ServiceID: "svc-ce", Service: "Compute Engine", SKUID: "sku-pd", SKU: "PD Capacity", Gross: 30, Credits: 0},
		{ProjectID: "proj-a", ServiceID: "svc-net", Service: "Networking", SKUID: "sku-fr", SKU: "Forwarding Rule Minimum", Gross: 40, Credits: -40},
		{ProjectID: "proj-a", ServiceID: "svc-gl", Service: "Generative Language API", SKUID: "sku-tok", SKU: "Gemini tokens", Gross: 5, Credits: 0},
		{ProjectID: "proj-b", ServiceID: "svc-mon", Service: "Cloud Monitoring", SKUID: "sku-met", SKU: "Metric volume", Gross: 3, Credits: 0},
		{ProjectID: "proj-a", ServiceID: "svc-log", Service: "Cloud Logging", SKUID: "sku-ing", SKU: "Log ingestion", Gross: 3, Credits: 0},
	}
}

func insertFixtureSnapshot(t *testing.T, s *Store, week isoweek.Week, rows []SnapshotRow, status isoweek.Status) Snapshot {
	t.Helper()
	ctx := context.Background()
	var snap Snapshot
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		run, src := seedRunAndSource(t, tx, week, week)
		var err error
		snap, err = InsertSnapshot(ctx, tx, NewSnapshot{Week: week, FetchedAt: testNow, StatusAtFetch: status, RunID: run.ID, RunSourceID: src.ID, Rows: rows})
		if err != nil {
			return err
		}
		return FinishRun(ctx, tx, run.ID, RunOK, testNow.Add(3*time.Second), nil)
	})
	if err != nil {
		t.Fatalf("insert fixture snapshot: %v", err)
	}
	return snap
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestInsertSnapshot_WritesRowsInBatchesAndComputesTotals(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	week := isoweek.MustParse("2026-W42")

	// More than two batches of 500 so the chunking is exercised.
	const n = 1203
	rows := make([]SnapshotRow, 0, n)
	var wantGross, wantCredits float64
	for i := 0; i < n; i++ {
		gross := float64(i%97) + 0.25
		credits := -float64(i%13) * 0.5
		rows = append(rows, SnapshotRow{ProjectID: fmt.Sprintf("proj-%c", 'a'+i%2), Service: "Compute Engine", SKU: fmt.Sprintf("sku-%d", i%40), ResourceName: strp(fmt.Sprintf("vm-%d", i%200)), Gross: gross, Credits: credits, LineItems: 1})
		wantGross += gross
		wantCredits += credits
	}

	snap := insertFixtureSnapshot(t, s, week, rows, isoweek.StatusFinal)

	if snap.ID <= 0 {
		t.Fatalf("snapshot id = %d", snap.ID)
	}
	if snap.RowCount != n {
		t.Errorf("RowCount = %d, want %d", snap.RowCount, n)
	}
	if !near(snap.GrossTotal, wantGross) || !near(snap.CreditsTotal, wantCredits) || !near(snap.NetTotal, wantGross+wantCredits) {
		t.Errorf("totals = %v/%v/%v, want %v/%v/%v", snap.GrossTotal, snap.CreditsTotal, snap.NetTotal, wantGross, wantCredits, wantGross+wantCredits)
	}
	if snap.WeekID != "2026-W42" || snap.WeekStart != "2026-10-12" || snap.WeekEnd != "2026-10-18" {
		t.Errorf("week fields = %s %s %s", snap.WeekID, snap.WeekStart, snap.WeekEnd)
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM snapshot_rows WHERE snapshot_id = ?", snap.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("snapshot_rows in DB = %d, want %d", count, n)
	}
	// net is derived as gross + credits on every stored row.
	var bad int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM snapshot_rows WHERE snapshot_id = ? AND ABS(net - (gross + credits)) > 0.000001", snap.ID).Scan(&bad); err != nil {
		t.Fatal(err)
	}
	if bad != 0 {
		t.Fatalf("%d rows have net != gross + credits", bad)
	}
	got, err := GetSnapshot(ctx, s.DB, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != snap.ID || got.RowCount != n || got.StatusAtFetch != isoweek.StatusFinal || !got.FetchedAt.Equal(testNow) || got.Week != week {
		t.Errorf("GetSnapshot = %+v", got)
	}
}

func TestInsertSnapshot_ZeroRowsStillCreatesAnEmptySnapshot(t *testing.T) {
	s := openMigrated(t)
	snap := insertFixtureSnapshot(t, s, isoweek.MustParse("2026-W19"), nil, isoweek.StatusFinal)
	if snap.RowCount != 0 || snap.NetTotal != 0 || snap.GrossTotal != 0 || snap.CreditsTotal != 0 {
		t.Fatalf("empty snapshot = %+v", snap)
	}
	rows, err := SnapshotRows(context.Background(), s.DB, snap.ID, ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %d", len(rows))
	}
}

func TestInsertSnapshot_RejectsInvalidInput(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	run, src := seedRunAndSource(t, s.DB, isoweek.MustParse("2026-W42"), isoweek.MustParse("2026-W42"))
	cases := map[string]NewSnapshot{
		"invalid week":   {Week: isoweek.Week{Year: 2027, Week: 53}, FetchedAt: testNow, StatusAtFetch: isoweek.StatusFinal, RunID: run.ID, RunSourceID: src.ID},
		"bad status":     {Week: isoweek.MustParse("2026-W42"), FetchedAt: testNow, StatusAtFetch: "done", RunID: run.ID, RunSourceID: src.ID},
		"missing run":    {Week: isoweek.MustParse("2026-W42"), FetchedAt: testNow, StatusAtFetch: isoweek.StatusFinal, RunSourceID: src.ID},
		"missing source": {Week: isoweek.MustParse("2026-W42"), FetchedAt: testNow, StatusAtFetch: isoweek.StatusFinal, RunID: run.ID},
		"zero fetched":   {Week: isoweek.MustParse("2026-W42"), StatusAtFetch: isoweek.StatusFinal, RunID: run.ID, RunSourceID: src.ID},
	}
	for name, ns := range cases {
		if _, err := InsertSnapshot(ctx, s.DB, ns); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
	// A dangling run_source_id is a foreign-key violation, not a silent insert.
	if _, err := InsertSnapshot(ctx, s.DB, NewSnapshot{Week: isoweek.MustParse("2026-W42"), FetchedAt: testNow, StatusAtFetch: isoweek.StatusFinal, RunID: run.ID, RunSourceID: 9999}); err == nil {
		t.Error("dangling run_source_id accepted")
	}
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM snapshots").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d snapshots written by rejected inputs", n)
	}
}

func TestLatestPerWeek_PicksHighestIDPerWeekWithinRange(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	w40, w41, w42, w43 := isoweek.MustParse("2026-W40"), isoweek.MustParse("2026-W41"), isoweek.MustParse("2026-W42"), isoweek.MustParse("2026-W43")

	insertFixtureSnapshot(t, s, w40, fixtureRows(), isoweek.StatusFinal)
	first41 := insertFixtureSnapshot(t, s, w41, fixtureRows(), isoweek.StatusFinal)
	old42 := insertFixtureSnapshot(t, s, w42, fixtureRows(), isoweek.StatusSettling)
	new42 := insertFixtureSnapshot(t, s, w42, fixtureRows()[:2], isoweek.StatusFinal)
	if new42.ID <= old42.ID {
		t.Fatalf("ids not increasing: %d then %d", old42.ID, new42.ID)
	}

	latest, err := LatestPerWeek(ctx, s.DB, w41, w43)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 {
		t.Fatalf("latest has %d weeks, want 2: %+v", len(latest), latest)
	}
	if got := latest[w42]; got.ID != new42.ID || got.RowCount != 2 || got.StatusAtFetch != isoweek.StatusFinal {
		t.Errorf("W42 latest = %+v, want id %d", got, new42.ID)
	}
	if got := latest[w41]; got.ID != first41.ID {
		t.Errorf("W41 latest = %+v, want id %d", got, first41.ID)
	}
	if _, ok := latest[w40]; ok {
		t.Error("W40 is outside the range but was returned")
	}
	if _, ok := latest[w43]; ok {
		t.Error("W43 has no snapshot but was returned")
	}
}

func TestLatestPerWeek_HandlesYearRollover(t *testing.T) {
	s := openMigrated(t)
	w52, w53, w01 := isoweek.MustParse("2026-W52"), isoweek.MustParse("2026-W53"), isoweek.MustParse("2027-W01")
	insertFixtureSnapshot(t, s, w53, fixtureRows(), isoweek.StatusFinal)
	insertFixtureSnapshot(t, s, w01, fixtureRows(), isoweek.StatusFinal)
	insertFixtureSnapshot(t, s, isoweek.MustParse("2027-W02"), fixtureRows(), isoweek.StatusFinal)

	latest, err := LatestPerWeek(context.Background(), s.DB, w52, w01)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 {
		t.Fatalf("got %d weeks, want W53 and W01: %v", len(latest), latest)
	}
	if _, ok := latest[w53]; !ok {
		t.Error("2026-W53 missing")
	}
	if _, ok := latest[w01]; !ok {
		t.Error("2027-W01 missing")
	}
	if _, err := LatestPerWeek(context.Background(), s.DB, w01, w52); err == nil {
		t.Error("reversed range accepted")
	}
}

func TestGetSnapshot_ReturnsErrNotFoundForUnknownID(t *testing.T) {
	s := openMigrated(t)
	if _, err := GetSnapshot(context.Background(), s.DB, 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSnapshotRows_FiltersByScopeAndKeepsNulls(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	snap := insertFixtureSnapshot(t, s, isoweek.MustParse("2026-W42"), fixtureRows(), isoweek.StatusFinal)

	all, err := SnapshotRows(ctx, s.DB, snap.ID, ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 8 {
		t.Fatalf("all rows = %d, want 8", len(all))
	}
	nulls := 0
	for _, r := range all {
		if r.SnapshotID != snap.ID {
			t.Errorf("row snapshot_id = %d", r.SnapshotID)
		}
		if r.ResourceName == nil {
			nulls++
		}
		if r.Service == "Compute Engine" && r.SKU == "N1 Core" && r.ResourceName != nil && *r.ResourceName == "vm-1" {
			if r.ResourceGlobalName == nil || *r.ResourceGlobalName != "//compute/vm-1" || r.ServiceID != "svc-ce" || r.SKUID != "sku-core" {
				t.Errorf("vm-1 row lost fields: %+v", r)
			}
			if !near(r.Net, 90) {
				t.Errorf("vm-1 net = %v, want 90", r.Net)
			}
		}
	}
	if nulls != 5 {
		t.Errorf("rows with NULL resource_name = %d, want 5", nulls)
	}

	b, err := SnapshotRows(ctx, s.DB, snap.ID, "proj-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 2 {
		t.Fatalf("proj-b rows = %d, want 2", len(b))
	}
	for _, r := range b {
		if r.ProjectID != "proj-b" {
			t.Errorf("scope leak: %+v", r)
		}
	}
	none, err := SnapshotRows(ctx, s.DB, snap.ID, "proj-zzz")
	if err != nil || len(none) != 0 {
		t.Fatalf("unknown project rows = %v, %v", none, err)
	}

	projects, err := SnapshotProjects(ctx, s.DB, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || projects[0] != "proj-a" || projects[1] != "proj-b" {
		t.Fatalf("projects = %v", projects)
	}

	byID, err := RowsForSnapshots(ctx, s.DB, []int64{snap.ID, 9999}, "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(byID[snap.ID]) != 6 || len(byID[9999]) != 0 {
		t.Fatalf("RowsForSnapshots = %d / %d", len(byID[snap.ID]), len(byID[9999]))
	}
	empty, err := RowsForSnapshots(ctx, s.DB, nil, ScopeAll)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty ids: %v %v", empty, err)
	}
}

func TestBreakdown_ReconcilesSkuToServiceAndResourceToSku(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	snap := insertFixtureSnapshot(t, s, isoweek.MustParse("2026-W42"), fixtureRows(), isoweek.StatusFinal)

	// service level: sorted by net desc then key; ties broken by key.
	svc, err := Breakdown(ctx, s.DB, BreakdownQuery{SnapshotID: snap.ID, Level: LevelService, Scope: ScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"Compute Engine", "Generative Language API", "Cloud Logging", "Cloud Monitoring", "Networking"}
	if len(svc.Rows) != len(wantOrder) {
		t.Fatalf("service rows = %d: %+v", len(svc.Rows), svc.Rows)
	}
	for i, w := range wantOrder {
		if svc.Rows[i].Key != w {
			t.Errorf("service row %d = %q, want %q", i, svc.Rows[i].Key, w)
		}
		if svc.Rows[i].Level != LevelService {
			t.Errorf("row level = %q", svc.Rows[i].Level)
		}
	}
	if !near(svc.Rows[0].Net, 185) || !near(svc.Rows[0].Gross, 200) || !near(svc.Rows[0].Credits, -15) || svc.Rows[0].LineItems != 0 {
		t.Errorf("Compute Engine service row = %+v", svc.Rows[0])
	}
	if !near(svc.Totals.Net, 196) || !near(svc.Totals.Net, snap.NetTotal) {
		t.Errorf("service totals = %+v, snapshot net %v", svc.Totals, snap.NetTotal)
	}
	netRow := svc.Rows[4]
	if !near(netRow.Gross, 40) || !near(netRow.Credits, -40) || !near(netRow.Net, 0) {
		t.Errorf("fully-credited Networking row = %+v", netRow)
	}

	// sku level reconciles to the service row.
	sku, err := Breakdown(ctx, s.DB, BreakdownQuery{SnapshotID: snap.ID, Level: LevelSKU, Scope: ScopeAll, Service: "Compute Engine"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sku.Rows) != 2 || sku.Rows[0].Key != "N1 Core" || sku.Rows[0].SKUID != "sku-core" || sku.Rows[1].Key != "PD Capacity" {
		t.Fatalf("sku rows = %+v", sku.Rows)
	}
	if !near(sku.Rows[0].Net, 155) || !near(sku.Rows[1].Net, 30) || !near(sku.Totals.Net, svc.Rows[0].Net) {
		t.Errorf("sku totals %v != service net %v", sku.Totals.Net, svc.Rows[0].Net)
	}

	// resource level reconciles to the SKU row; NULL resource is "(unattributed)".
	res, err := Breakdown(ctx, s.DB, BreakdownQuery{SnapshotID: snap.ID, Level: LevelResource, Scope: ScopeAll, Service: "Compute Engine", SKU: "N1 Core"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 || res.Rows[0].Key != "vm-1" || res.Rows[1].Key != "vm-2" || res.Rows[2].Key != "vm-3" {
		t.Fatalf("resource rows = %+v", res.Rows)
	}
	if res.Rows[2].ProjectID != "proj-b" || res.Rows[0].ResourceGlobalName == nil || *res.Rows[0].ResourceGlobalName != "//compute/vm-1" {
		t.Errorf("resource row fields = %+v / %+v", res.Rows[0], res.Rows[2])
	}
	if !near(res.Totals.Net, sku.Rows[0].Net) {
		t.Errorf("resource totals %v != sku net %v", res.Totals.Net, sku.Rows[0].Net)
	}
	pd, err := Breakdown(ctx, s.DB, BreakdownQuery{SnapshotID: snap.ID, Level: LevelResource, Scope: ScopeAll, Service: "Compute Engine", SKU: "PD Capacity"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pd.Rows) != 1 || pd.Rows[0].Key != UnattributedKey || pd.Rows[0].ResourceName != nil || pd.Rows[0].ProjectID != "proj-a" || !near(pd.Rows[0].Net, 30) {
		t.Fatalf("unattributed row = %+v", pd.Rows)
	}

	// scope filter applies identically at every level.
	skuA, err := Breakdown(ctx, s.DB, BreakdownQuery{SnapshotID: snap.ID, Level: LevelSKU, Scope: "proj-a", Service: "Compute Engine"})
	if err != nil {
		t.Fatal(err)
	}
	if !near(skuA.Rows[0].Net, 140) || !near(skuA.Totals.Net, 170) {
		t.Errorf("scoped sku rows = %+v", skuA)
	}
	resA, err := Breakdown(ctx, s.DB, BreakdownQuery{SnapshotID: snap.ID, Level: LevelResource, Scope: "proj-a", Service: "Compute Engine", SKU: "N1 Core"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resA.Rows) != 2 || !near(resA.Totals.Net, skuA.Rows[0].Net) {
		t.Errorf("scoped resource rows = %+v", resA)
	}
	// Unknown service yields an empty (not nil) row list and zero totals.
	none, err := Breakdown(ctx, s.DB, BreakdownQuery{SnapshotID: snap.ID, Level: LevelSKU, Scope: ScopeAll, Service: "Nope"})
	if err != nil {
		t.Fatal(err)
	}
	if none.Rows == nil || len(none.Rows) != 0 || none.Totals.Net != 0 {
		t.Errorf("unknown service breakdown = %+v", none)
	}
}

func TestBreakdown_RejectsBadLevelsAndUnknownSnapshot(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	snap := insertFixtureSnapshot(t, s, isoweek.MustParse("2026-W42"), fixtureRows(), isoweek.StatusFinal)

	bad := []BreakdownQuery{
		{SnapshotID: snap.ID, Level: "project"},
		{SnapshotID: snap.ID, Level: LevelSKU},                                          // service required
		{SnapshotID: snap.ID, Level: LevelResource, Service: "Compute Engine"},          // sku required
		{SnapshotID: snap.ID, Level: LevelResource, SKU: "N1 Core"},                     // service required
		{SnapshotID: snap.ID, Level: LevelService, Service: "Compute Engine", SKU: "x"}, // tolerated? no: level=service takes no filters
	}
	for i, q := range bad[:4] {
		if _, err := Breakdown(ctx, s.DB, q); !errors.Is(err, ErrInvalidLevel) {
			t.Errorf("case %d: err = %v, want ErrInvalidLevel", i, err)
		}
	}
	// Extra filters at service level are ignored rather than rejected (the API
	// validates query params; the store is lenient about superfluous ones).
	if _, err := Breakdown(ctx, s.DB, bad[4]); err != nil {
		t.Errorf("service level with extra filters: %v", err)
	}
	if _, err := Breakdown(ctx, s.DB, BreakdownQuery{SnapshotID: 9999, Level: LevelService}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown snapshot err = %v, want ErrNotFound", err)
	}
}

func TestBreakdownRow_MarshalsOnlyTheFieldsOfItsLevel(t *testing.T) {
	name := "vm-1"
	cases := []struct {
		row  BreakdownRow
		want map[string]bool // keys that must be present
		no   []string        // keys that must be absent
	}{
		{BreakdownRow{Level: LevelService, Key: "Compute Engine", ColumnKey: "compute_engine", Net: 1}, map[string]bool{"key": true, "column_key": true, "gross": true, "credits": true, "net": true, "line_items": true}, []string{"sku_id", "project_id", "resource_name"}},
		{BreakdownRow{Level: LevelSKU, Key: "N1", SKUID: "s"}, map[string]bool{"key": true, "sku_id": true, "net": true}, []string{"column_key", "project_id", "resource_name"}},
		{BreakdownRow{Level: LevelResource, Key: UnattributedKey, ProjectID: "proj-a"}, map[string]bool{"key": true, "project_id": true, "resource_name": true, "resource_global_name": true, "net": true}, []string{"column_key", "sku_id"}},
		{BreakdownRow{Level: LevelResource, Key: name, ProjectID: "proj-a", ResourceName: &name}, map[string]bool{"resource_name": true}, nil},
	}
	for i, c := range cases {
		b, err := json.Marshal(c.row)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		for k := range c.want {
			if _, ok := m[k]; !ok {
				t.Errorf("case %d: key %q missing in %s", i, k, b)
			}
		}
		for _, k := range c.no {
			if _, ok := m[k]; ok {
				t.Errorf("case %d: key %q present in %s", i, k, b)
			}
		}
		if _, ok := m["Level"]; ok {
			t.Errorf("case %d: internal Level leaked: %s", i, b)
		}
	}
	// NULL resource_name serialises as JSON null, not as a missing key.
	b, _ := json.Marshal(BreakdownRow{Level: LevelResource, Key: UnattributedKey, ProjectID: "proj-a"})
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	if string(m["resource_name"]) != "null" || string(m["resource_global_name"]) != "null" {
		t.Errorf("null resource fields = %s", b)
	}
}

func TestSnapshot_SummaryAndJSONShape(t *testing.T) {
	s := openMigrated(t)
	snap := insertFixtureSnapshot(t, s, isoweek.MustParse("2026-W42"), fixtureRows(), isoweek.StatusSettling)
	sum := snap.Summary()
	if sum.ID != snap.ID || sum.Week != "2026-W42" || sum.StatusAtFetch != isoweek.StatusSettling || sum.RowCount != 8 || !near(sum.NetTotal, 196) {
		t.Fatalf("summary = %+v", sum)
	}
	b, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "week", "status_at_fetch", "row_count", "net_total"} {
		if _, ok := m[k]; !ok {
			t.Errorf("summary JSON lacks %q: %s", k, b)
		}
	}
	if len(m) != 5 {
		t.Errorf("summary JSON has extra keys: %s", b)
	}
	if string(m["week"]) != `"2026-W42"` {
		t.Errorf("week = %s", m["week"])
	}
}
