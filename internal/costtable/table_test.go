package costtable

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

const settle = 48 * time.Hour

// now is Monday 2026-10-26 09:00Z: W44 is partial, W43 is settling
// (ended 2026-10-26 00:00Z, < 48 h ago), W42 and earlier are final.
var now = time.Date(2026, time.October, 26, 9, 0, 0, 0, time.UTC)

var (
	w41 = isoweek.MustParse("2026-W41")
	w42 = isoweek.MustParse("2026-W42")
	w43 = isoweek.MustParse("2026-W43")
	w44 = isoweek.MustParse("2026-W44")
)

func strp(s string) *string { return &s }

func snap(id int64, w isoweek.Week, status isoweek.Status, rows []store.SnapshotRow) store.Snapshot {
	s := store.Snapshot{ID: id, Week: w, WeekID: w.String(), WeekStart: store.FormatDate(w.Start()), WeekEnd: store.FormatDate(w.Sunday()),
		FetchedAt: now.Add(-time.Hour), StatusAtFetch: status, RunID: id * 10, RowCount: len(rows)}
	for _, r := range rows {
		s.GrossTotal += r.Gross
		s.CreditsTotal += r.Credits
		s.NetTotal += r.Gross + r.Credits
	}
	return s
}

func row(project, service, sku string, gross, credits float64) store.SnapshotRow {
	return store.SnapshotRow{ProjectID: project, Service: service, SKU: sku, Gross: gross, Credits: credits, Net: gross + credits, LineItems: 1}
}

// week42Rows is the fixture week: both Gemini spellings, a fully-credited
// service, an unknown service in only one project and a zero-net unknown.
func week42Rows() []store.SnapshotRow {
	return []store.SnapshotRow{
		row("proj-a", "Compute Engine", "N1 Core", 1000, -100),
		row("proj-b", "Compute Engine", "N1 Core", 200, 0),
		row("proj-a", "Networking", "LB", 596.92, -193.51),
		row("proj-a", "Cloud SQL", "db", 206.17, 0),
		row("proj-a", "Gemini API", "tokens", 1.5, 0),
		row("proj-a", "Generative Language API", "tokens", 0.98, 0),
		row("proj-a", "VM Manager", "OS config", 96.76, -96.76),
		row("proj-b", "Cloud Monitoring", "metrics", 12, -2),
		row("proj-a", "Cloud Debugger", "zero", 4, -4),
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func assemble(t *testing.T, in Input) Table {
	t.Helper()
	tbl, err := Assemble(in)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return tbl
}

func baseInput(latest map[isoweek.Week]store.Snapshot, rows map[int64][]store.SnapshotRow) Input {
	return Input{From: w41, To: w44, Latest: latest, Rows: rows, Thresholds: Thresholds{Pct: 20, Abs: 50}, Scope: store.ScopeAll, Now: now, Settle: settle}
}

func findRow(t *testing.T, tbl Table, w isoweek.Week) Row {
	t.Helper()
	for _, r := range tbl.Rows {
		if r.Week == w.String() {
			return r
		}
	}
	t.Fatalf("row %s missing", w)
	return Row{}
}

func TestAssemble_MapsServicesExtrasAndTotalInSheetOrder(t *testing.T) {
	s42 := snap(12, w42, isoweek.StatusFinal, week42Rows())
	tbl := assemble(t, baseInput(map[isoweek.Week]store.Snapshot{w42: s42}, map[int64][]store.SnapshotRow{12: week42Rows()}))

	var keys []string
	for _, c := range tbl.Columns {
		keys = append(keys, c.Key)
	}
	want := "week,compute_engine,networking,cloud_sql,gemini_api,cloud_run,gcs,vm_manager,cloud_build,cloud_logging,secret_manager,artifact_registry,svc:Cloud Monitoring,total,pct_change,stub:groq,stub:claude"
	if got := strings.Join(keys, ","); got != want {
		t.Fatalf("columns =\n%s\nwant\n%s", got, want)
	}
	r := findRow(t, tbl, w42)
	if r.Snapshot == nil || r.Snapshot.ID != 12 || r.Snapshot.RunID != 120 || r.Snapshot.RowCount != 9 || r.Snapshot.Stale {
		t.Fatalf("snapshot ref = %+v", r.Snapshot)
	}
	c := r.Cells
	if !near(c["compute_engine"].Net, 1100) || !near(c["compute_engine"].Gross, 1200) || !near(c["compute_engine"].Credits, -100) {
		t.Errorf("compute_engine = %+v", c["compute_engine"])
	}
	if !near(c["gemini_api"].Net, 2.48) {
		t.Errorf("gemini_api (both spellings) = %+v", c["gemini_api"])
	}
	if !near(c["networking"].Net, 403.41) || !near(c["networking"].Gross, 596.92) {
		t.Errorf("networking = %+v", c["networking"])
	}
	if !near(c["vm_manager"].Net, 0) || !near(c["vm_manager"].Gross, 96.76) {
		t.Errorf("fully-credited vm_manager = %+v", c["vm_manager"])
	}
	if !near(c["svc:Cloud Monitoring"].Net, 10) {
		t.Errorf("extra column = %+v", c["svc:Cloud Monitoring"])
	}
	for _, k := range []string{"cloud_run", "gcs", "cloud_build", "cloud_logging", "secret_manager", "artifact_registry"} {
		cell, ok := c[k]
		if !ok || cell.Net != 0 || cell.Gross != 0 || cell.Credits != 0 {
			t.Errorf("fixed column %s without rows = %+v (present=%v)", k, cell, ok)
		}
	}
	if _, ok := c["svc:Cloud Debugger"]; ok {
		t.Error("zero-net unknown service produced a column/cell")
	}
	for _, k := range []string{"week", "pct_change", "stub:groq", "stub:claude"} {
		if _, ok := c[k]; ok {
			t.Errorf("non-value column %s has a cell", k)
		}
	}
	// total is the exact float sum of the service cells in column order.
	var sum float64
	for _, col := range tbl.Columns {
		if col.Kind == KindService {
			sum += c[col.Key].Net
		}
	}
	if c["total"].Net != sum {
		t.Errorf("total.net %v != Σ cells %v", c["total"].Net, sum)
	}
	if math.Abs(c["total"].Net-s42.NetTotal) > 0.005 {
		t.Errorf("total.net %v vs snapshot net_total %v", c["total"].Net, s42.NetTotal)
	}
	if len(c) != 13 { // 11 fixed + 1 extra + total
		t.Errorf("cells has %d entries, want 13: %v", len(c), c)
	}
	if tbl.Scope != "all" || tbl.From != "2026-W41" || tbl.To != "2026-W44" || tbl.Thresholds != (Thresholds{Pct: 20, Abs: 50}) || !tbl.GeneratedAt.Equal(now) {
		t.Errorf("table meta = %+v", tbl)
	}
}

func TestAssemble_ScopeFilterChangesCellsAndTotalButNotRowCount(t *testing.T) {
	s42 := snap(12, w42, isoweek.StatusFinal, week42Rows())
	in := baseInput(map[isoweek.Week]store.Snapshot{w42: s42}, map[int64][]store.SnapshotRow{12: week42Rows()})
	in.Scope = "proj-a"
	tbl := assemble(t, in)
	r := findRow(t, tbl, w42)
	if !near(r.Cells["compute_engine"].Net, 900) {
		t.Errorf("scoped compute_engine = %+v", r.Cells["compute_engine"])
	}
	if r.Snapshot.RowCount != 9 {
		t.Errorf("row_count must stay all-projects: %d", r.Snapshot.RowCount)
	}
	for _, c := range tbl.Columns {
		if c.Extra {
			t.Errorf("extra column %s present although its only rows are in proj-b", c.Key)
		}
	}
	if _, ok := r.Cells["svc:Cloud Monitoring"]; ok {
		t.Error("cell for out-of-scope extra service present")
	}
	if !near(r.Cells["total"].Net, 900+403.41+206.17+2.48) {
		t.Errorf("scoped total = %+v", r.Cells["total"])
	}
	if tbl.Scope != "proj-a" {
		t.Errorf("scope echoed as %q", tbl.Scope)
	}
	// Unknown scope: every cell is zero, never an error — the API validates scope.
	in.Scope = "proj-zzz"
	tbl = assemble(t, in)
	if cell := findRow(t, tbl, w42).Cells["total"]; cell.Net != 0 {
		t.Errorf("unknown scope total = %+v", cell)
	}
}

func TestAssemble_EveryWeekPresentAscendingAndNullWithoutSnapshot(t *testing.T) {
	s42 := snap(12, w42, isoweek.StatusFinal, week42Rows())
	tbl := assemble(t, baseInput(map[isoweek.Week]store.Snapshot{w42: s42}, map[int64][]store.SnapshotRow{12: week42Rows()}))
	if len(tbl.Rows) != 4 {
		t.Fatalf("rows = %d, want 4 (W41..W44)", len(tbl.Rows))
	}
	for i, w := range []isoweek.Week{w41, w42, w43, w44} {
		r := tbl.Rows[i]
		if r.Week != w.String() || r.WeekStart != store.FormatDate(w.Start()) || r.WeekEnd != store.FormatDate(w.Sunday()) {
			t.Errorf("row %d = %s %s %s", i, r.Week, r.WeekStart, r.WeekEnd)
		}
	}
	for _, w := range []isoweek.Week{w41, w43, w44} {
		r := findRow(t, tbl, w)
		if r.Snapshot != nil || r.Cells != nil || r.PctChange != nil || r.Flagged || r.Annotation != nil {
			t.Errorf("not-fetched row %s = %+v", w, r)
		}
	}
	if s := findRow(t, tbl, w42).WeekStatus; s != isoweek.StatusFinal {
		t.Errorf("W42 status = %s", s)
	}
	if s := findRow(t, tbl, w43).WeekStatus; s != isoweek.StatusSettling {
		t.Errorf("W43 status = %s", s)
	}
	if s := findRow(t, tbl, w44).WeekStatus; s != isoweek.StatusPartial {
		t.Errorf("W44 status = %s", s)
	}
	// JSON: null cells / snapshot, not {} or zeros.
	b, err := json.Marshal(tbl)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if string(m.Rows[0]["cells"]) != "null" || string(m.Rows[0]["snapshot"]) != "null" || string(m.Rows[0]["pct_change"]) != "null" || string(m.Rows[0]["annotation"]) != "null" {
		t.Errorf("not-fetched row JSON = %s", b)
	}
	if _, err := Assemble(Input{From: w44, To: w41, Now: now, Settle: settle}); err == nil {
		t.Error("from > to accepted")
	}
	if _, err := Assemble(Input{From: w41, To: w44, Settle: settle}); err == nil {
		t.Error("zero now accepted")
	}
}

func TestAssemble_PctChangeNullWithoutPreviousAndUsesWeekBeforeFrom(t *testing.T) {
	rows41 := []store.SnapshotRow{row("proj-a", "Compute Engine", "x", 1000, 0)}
	rows42 := []store.SnapshotRow{row("proj-a", "Compute Engine", "x", 1250, 0)}
	s41, s42 := snap(5, w41, isoweek.StatusFinal, rows41), snap(12, w42, isoweek.StatusFinal, rows42)
	latest := map[isoweek.Week]store.Snapshot{w41: s41, w42: s42}
	rows := map[int64][]store.SnapshotRow{5: rows41, 12: rows42}

	// W41 is the first fetched week → no previous → null deltas, not flagged.
	tbl := assemble(t, baseInput(latest, rows))
	r41 := findRow(t, tbl, w41)
	if r41.PctChange != nil || r41.Flagged || r41.Cells["total"].DeltaAbs != nil || r41.Cells["compute_engine"].DeltaPct != nil {
		t.Errorf("first fetched week deltas = %+v", r41)
	}
	r42 := findRow(t, tbl, w42)
	if r42.PctChange == nil || !near(*r42.PctChange, 25) || !near(*r42.Cells["total"].DeltaAbs, 250) || !r42.Flagged || !r42.Cells["total"].Flagged {
		t.Errorf("W42 vs W41 = pct %v cells %+v", r42.PctChange, r42.Cells["total"])
	}
	if !near(*r42.Cells["compute_engine"].DeltaPct, 25) || !r42.Cells["compute_engine"].Flagged {
		t.Errorf("compute_engine delta = %+v", r42.Cells["compute_engine"])
	}
	// Fixed column with no rows in either week: delta 0, not flagged.
	if gcs := r42.Cells["gcs"]; gcs.DeltaAbs == nil || *gcs.DeltaAbs != 0 || gcs.DeltaPct != nil || gcs.Flagged {
		t.Errorf("empty column delta = %+v", gcs)
	}

	// from = W42: the previous week (W41) is before from but still used.
	in := baseInput(latest, rows)
	in.From = w42
	tbl = assemble(t, in)
	if len(tbl.Rows) != 3 || tbl.Rows[0].Week != "2026-W42" {
		t.Fatalf("rows from W42 = %+v", tbl.Rows)
	}
	if pc := findRow(t, tbl, w42).PctChange; pc == nil || !near(*pc, 25) {
		t.Errorf("pct_change with prev before from = %v", pc)
	}

	// A gap: W43 fetched, W42 not → W43 has no previous snapshot → null.
	rows43 := []store.SnapshotRow{row("proj-a", "Compute Engine", "x", 2000, 0)}
	in = baseInput(map[isoweek.Week]store.Snapshot{w41: s41, w43: snap(20, w43, isoweek.StatusSettling, rows43)}, map[int64][]store.SnapshotRow{5: rows41, 20: rows43})
	if pc := findRow(t, assemble(t, in), w43).PctChange; pc != nil {
		t.Errorf("gap week pct_change = %v, want null", *pc)
	}

	// Previous total 0 → delta_abs set, delta_pct null, never flagged.
	empty := snap(7, w41, isoweek.StatusFinal, nil)
	in = baseInput(map[isoweek.Week]store.Snapshot{w41: empty, w42: s42}, map[int64][]store.SnapshotRow{12: rows42})
	r42 = findRow(t, assemble(t, in), w42)
	if r42.PctChange != nil || r42.Cells["total"].DeltaAbs == nil || !near(*r42.Cells["total"].DeltaAbs, 1250) || r42.Flagged {
		t.Errorf("prev zero total = pct %v cell %+v flagged %v", r42.PctChange, r42.Cells["total"], r42.Flagged)
	}
	// Scope applies to the previous week too.
	rows41b := []store.SnapshotRow{row("proj-a", "Compute Engine", "x", 1000, 0), row("proj-b", "Compute Engine", "x", 5000, 0)}
	in = baseInput(map[isoweek.Week]store.Snapshot{w41: snap(5, w41, isoweek.StatusFinal, rows41b), w42: s42}, map[int64][]store.SnapshotRow{5: rows41b, 12: rows42})
	in.Scope = "proj-a"
	if pc := findRow(t, assemble(t, in), w42).PctChange; pc == nil || !near(*pc, 25) {
		t.Errorf("scoped previous week pct_change = %v, want 25", pc)
	}
}

func TestAssemble_AnomalyRequiresBothThresholds(t *testing.T) {
	cases := []struct {
		name      string
		prev, cur float64
		th        Thresholds
		flagged   bool
		pctNull   bool
	}{
		{"both exceeded", 100, 160, Thresholds{20, 50}, true, false},
		{"abs below", 100, 140, Thresholds{20, 50}, false, false},
		{"pct below", 1000, 1100, Thresholds{20, 50}, false, false},
		{"prev zero", 0, 80, Thresholds{20, 50}, false, true},
		{"zero thresholds any change", 100, 100.01, Thresholds{0, 0}, true, false},
		{"zero thresholds no change", 100, 100, Thresholds{0, 0}, false, false},
		{"decrease flags too", 160, 100, Thresholds{20, 50}, true, false},
		{"exactly at thresholds", 100, 150, Thresholds{50, 50}, true, false},
		{"negative previous uses magnitude", -100, -40, Thresholds{20, 50}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows41 := []store.SnapshotRow{row("proj-a", "Cloud Run", "cpu", c.prev, 0)}
			rows42 := []store.SnapshotRow{row("proj-a", "Cloud Run", "cpu", c.cur, 0)}
			in := baseInput(map[isoweek.Week]store.Snapshot{w41: snap(1, w41, isoweek.StatusFinal, rows41), w42: snap(2, w42, isoweek.StatusFinal, rows42)}, map[int64][]store.SnapshotRow{1: rows41, 2: rows42})
			in.Thresholds = c.th
			r := findRow(t, assemble(t, in), w42)
			cell := r.Cells["cloud_run"]
			if cell.Flagged != c.flagged || r.Cells["total"].Flagged != c.flagged || r.Flagged != c.flagged {
				t.Errorf("flagged cell=%v total=%v row=%v, want %v (cell %+v)", cell.Flagged, r.Cells["total"].Flagged, r.Flagged, c.flagged, cell)
			}
			if (cell.DeltaPct == nil) != c.pctNull {
				t.Errorf("delta_pct nil=%v, want %v", cell.DeltaPct == nil, c.pctNull)
			}
			if cell.DeltaAbs == nil || !near(*cell.DeltaAbs, c.cur-c.prev) {
				t.Errorf("delta_abs = %v, want %v", cell.DeltaAbs, c.cur-c.prev)
			}
			if !c.pctNull && !near(*cell.DeltaPct, (c.cur-c.prev)/math.Abs(c.prev)*100) {
				t.Errorf("delta_pct = %v", *cell.DeltaPct)
			}
			if r.PctChange == nil && !c.pctNull || r.PctChange != nil && c.pctNull {
				t.Errorf("row pct_change nil=%v", r.PctChange == nil)
			}
		})
	}
}

func TestAssemble_StaleWhenFetchedEarlierStageThanCurrentStatus(t *testing.T) {
	rows := []store.SnapshotRow{row("proj-a", "Compute Engine", "x", 10, 0)}
	cases := []struct {
		week  isoweek.Week
		at    isoweek.Status
		stale bool
	}{
		{w42, isoweek.StatusPartial, true},   // fetched mid-week, now final
		{w42, isoweek.StatusSettling, true},  // fetched settling, now final
		{w42, isoweek.StatusFinal, false},    // equal
		{w43, isoweek.StatusPartial, true},   // now settling
		{w43, isoweek.StatusSettling, false}, // equal
		{w44, isoweek.StatusPartial, false},  // still partial
	}
	for _, c := range cases {
		in := baseInput(map[isoweek.Week]store.Snapshot{c.week: snap(3, c.week, c.at, rows)}, map[int64][]store.SnapshotRow{3: rows})
		r := findRow(t, assemble(t, in), c.week)
		if r.Snapshot.Stale != c.stale || r.Snapshot.StatusAtFetch != c.at {
			t.Errorf("%s fetched %s now %s: stale=%v, want %v", c.week, c.at, r.WeekStatus, r.Snapshot.Stale, c.stale)
		}
	}
	if !Stale(isoweek.StatusPartial, isoweek.StatusFinal) || Stale(isoweek.StatusFinal, isoweek.StatusFinal) || Stale(isoweek.StatusFinal, isoweek.StatusPartial) {
		t.Error("Stale helper wrong")
	}
}

func TestAssemble_CarriesAnnotationsEvenForUnfetchedWeeks(t *testing.T) {
	s42 := snap(12, w42, isoweek.StatusFinal, week42Rows())
	in := baseInput(map[isoweek.Week]store.Snapshot{w42: s42}, map[int64][]store.SnapshotRow{12: week42Rows()})
	in.Annotations = map[isoweek.Week]string{w42: "LB added for staging", w43: "not fetched yet but noted"}
	tbl := assemble(t, in)
	if a := findRow(t, tbl, w42).Annotation; a == nil || *a != "LB added for staging" {
		t.Errorf("W42 annotation = %v", a)
	}
	if a := findRow(t, tbl, w43).Annotation; a == nil || *a != "not fetched yet but noted" {
		t.Errorf("W43 annotation = %v", a)
	}
	if a := findRow(t, tbl, w41).Annotation; a != nil {
		t.Errorf("W41 annotation = %v, want nil", *a)
	}
}

func TestTable_JSONShapeMatchesAPI(t *testing.T) {
	rows41 := []store.SnapshotRow{row("proj-a", "Compute Engine", "x", 1000, 0)}
	s41 := snap(5, w41, isoweek.StatusFinal, rows41)
	s42 := snap(12, w42, isoweek.StatusPartial, week42Rows())
	in := baseInput(map[isoweek.Week]store.Snapshot{w41: s41, w42: s42}, map[int64][]store.SnapshotRow{5: rows41, 12: week42Rows()})
	in.Annotations = map[isoweek.Week]string{w42: "note"}
	b, err := json.Marshal(assemble(t, in))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"scope", "from", "to", "generated_at", "thresholds", "columns", "rows"} {
		if _, ok := m[k]; !ok {
			t.Errorf("table lacks %q", k)
		}
	}
	if len(m) != 7 {
		t.Errorf("table has extra keys: %v", m)
	}
	if string(m["generated_at"]) != `"2026-10-26T09:00:00Z"` || string(m["thresholds"]) != `{"pct":20,"abs_usd":50}` {
		t.Errorf("generated_at/thresholds = %s / %s", m["generated_at"], m["thresholds"])
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(m["rows"], &rows); err != nil {
		t.Fatal(err)
	}
	r := rows[1] // W42
	rowKeys := []string{"week", "week_start", "week_end", "week_status", "snapshot", "cells", "pct_change", "flagged", "annotation"}
	for _, k := range rowKeys {
		if _, ok := r[k]; !ok {
			t.Errorf("row lacks %q: %s", k, m["rows"])
		}
	}
	if len(r) != len(rowKeys) {
		t.Errorf("row has extra keys: %v", r)
	}
	var sn map[string]json.RawMessage
	if err := json.Unmarshal(r["snapshot"], &sn); err != nil {
		t.Fatal(err)
	}
	snapKeys := []string{"id", "fetched_at", "status_at_fetch", "run_id", "row_count", "stale"}
	for _, k := range snapKeys {
		if _, ok := sn[k]; !ok {
			t.Errorf("snapshot lacks %q: %s", k, r["snapshot"])
		}
	}
	if len(sn) != len(snapKeys) || string(sn["stale"]) != "true" || string(sn["status_at_fetch"]) != `"partial"` || string(sn["fetched_at"]) != `"2026-10-26T08:00:00Z"` {
		t.Errorf("snapshot = %s", r["snapshot"])
	}
	var cells map[string]map[string]json.RawMessage
	if err := json.Unmarshal(r["cells"], &cells); err != nil {
		t.Fatal(err)
	}
	cell := cells["compute_engine"]
	for _, k := range []string{"net", "gross", "credits", "delta_abs", "delta_pct", "flagged"} {
		if _, ok := cell[k]; !ok {
			t.Errorf("cell lacks %q: %v", k, cell)
		}
	}
	if len(cell) != 6 || string(cell["flagged"]) != "false" {
		t.Errorf("cell = %v", cell)
	}
	if string(r["annotation"]) != `"note"` || string(r["week_status"]) != `"final"` {
		t.Errorf("annotation/week_status = %s / %s", r["annotation"], r["week_status"])
	}
	if string(rows[0]["pct_change"]) != "null" || string(cells["total"]["delta_pct"]) == "null" {
		t.Errorf("pct_change W41 %s, total delta_pct W42 %s", rows[0]["pct_change"], cells["total"]["delta_pct"])
	}
}

func TestThresholdsFrom_ReadsStoreSettings(t *testing.T) {
	th := ThresholdsFrom(store.Settings{AnomalyPctThreshold: 12.5, AnomalyAbsThreshold: 7})
	if th != (Thresholds{Pct: 12.5, Abs: 7}) {
		t.Fatalf("th = %+v", th)
	}
}

func TestBreakdown_FillsColumnKeyAtServiceLevelAndEchoesQuery(t *testing.T) {
	res := store.BreakdownResult{
		Query: store.BreakdownQuery{SnapshotID: 13, Level: store.LevelService, Scope: store.ScopeAll},
		Rows: []store.BreakdownRow{
			{Level: store.LevelService, Key: "Compute Engine", Net: 10, Gross: 10},
			{Level: store.LevelService, Key: "Generative Language API", Net: 2, Gross: 2},
			{Level: store.LevelService, Key: "Cloud Monitoring", Net: 1, Gross: 1},
		},
		Totals: store.BreakdownTotals{Gross: 13, Net: 13},
	}
	out := Breakdown(res)
	if out.SnapshotID != 13 || out.Level != store.LevelService || out.Scope != "all" || out.Service != nil || out.SKU != nil {
		t.Fatalf("meta = %+v", out)
	}
	if out.Rows[0].ColumnKey != "compute_engine" || out.Rows[1].ColumnKey != "gemini_api" || out.Rows[2].ColumnKey != "svc:Cloud Monitoring" {
		t.Errorf("column keys = %+v", out.Rows)
	}
	if out.Totals != res.Totals {
		t.Errorf("totals = %+v", out.Totals)
	}
	b, _ := json.Marshal(out)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"snapshot_id", "level", "scope", "service", "sku", "rows", "totals"} {
		if _, ok := m[k]; !ok {
			t.Errorf("breakdown JSON lacks %q: %s", k, b)
		}
	}
	if string(m["service"]) != "null" || string(m["sku"]) != "null" || !strings.Contains(string(m["rows"]), `"column_key":"gemini_api"`) {
		t.Errorf("breakdown JSON = %s", b)
	}

	// sku level echoes service, sku stays null; empty rows marshal as [].
	skuRes := store.BreakdownResult{Query: store.BreakdownQuery{SnapshotID: 13, Level: store.LevelSKU, Scope: "proj-a", Service: "Compute Engine"}}
	skuOut := Breakdown(skuRes)
	if skuOut.Service == nil || *skuOut.Service != "Compute Engine" || skuOut.SKU != nil || skuOut.Rows == nil {
		t.Fatalf("sku meta = %+v", skuOut)
	}
	b, _ = json.Marshal(skuOut)
	if !strings.Contains(string(b), `"rows":[]`) || !strings.Contains(string(b), `"sku":null`) || !strings.Contains(string(b), `"service":"Compute Engine"`) {
		t.Errorf("sku JSON = %s", b)
	}
	resOut := Breakdown(store.BreakdownResult{Query: store.BreakdownQuery{SnapshotID: 13, Level: store.LevelResource, Scope: "all", Service: "Compute Engine", SKU: "N1 Core"}})
	if resOut.SKU == nil || *resOut.SKU != "N1 Core" {
		t.Errorf("resource meta = %+v", resOut)
	}
}

func TestProvenance_BuildsSnapshotDetailWithStatusAndStale(t *testing.T) {
	s := snap(13, w43, isoweek.StatusSettling, week42Rows())
	s.FetchedAt = time.Date(2026, time.October, 26, 0, 30, 0, 0, time.UTC)
	jobID, bp, bb, cache := "job-x", int64(41234567), int64(41943040), false
	src := store.RunSource{ID: 8, RunID: 130, Source: store.SourceBigQueryBilling, Status: store.SourceOK, DurationMs: 7900,
		SQLText: "SELECT ... WHERE DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end", Params: json.RawMessage(`{"part_end":"2026-10-27","part_start":"2026-10-18"}`),
		JobID: &jobID, BytesProcessed: &bp, BytesBilled: &bb, CacheHit: &cache}

	// now = Monday 2026-10-26 09:00Z → W43 is settling; fetched settling → not stale.
	d := Provenance(s, src, []string{"proj-a", "proj-b"}, now, settle)
	if d.ID != 13 || d.Week != "2026-W43" || d.WeekStart != "2026-10-19" || d.WeekEnd != "2026-10-25" || d.WeekStatus != isoweek.StatusSettling || d.Stale {
		t.Fatalf("detail = %+v", d)
	}
	// Two days later the week is final and the settling snapshot is stale.
	d2 := Provenance(s, src, nil, now.Add(48*time.Hour), settle)
	if d2.WeekStatus != isoweek.StatusFinal || !d2.Stale || d2.Projects == nil {
		t.Fatalf("detail later = %+v", d2)
	}
	if d.Formula != Formula || !strings.Contains(d.Formula, "SUM(cost) + SUM(credits.amount)") {
		t.Errorf("formula = %q", d.Formula)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	keys := []string{"id", "week", "week_start", "week_end", "fetched_at", "status_at_fetch", "week_status", "stale", "run_id", "row_count", "gross_total", "credits_total", "net_total", "projects", "formula", "source"}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("detail lacks %q: %s", k, b)
		}
	}
	if len(m) != len(keys) {
		t.Errorf("detail has extra keys: %s", b)
	}
	var srcM map[string]json.RawMessage
	_ = json.Unmarshal(m["source"], &srcM)
	srcKeys := []string{"source", "job_id", "bytes_processed", "bytes_billed", "cache_hit", "duration_ms", "sql", "params"}
	for _, k := range srcKeys {
		if _, ok := srcM[k]; !ok {
			t.Errorf("source lacks %q: %s", k, m["source"])
		}
	}
	if len(srcM) != len(srcKeys) || string(srcM["params"]) != `{"part_end":"2026-10-27","part_start":"2026-10-18"}` || string(m["fetched_at"]) != `"2026-10-26T00:30:00Z"` {
		t.Errorf("source/fetched_at = %s / %s", m["source"], m["fetched_at"])
	}
	// nil params / projects render as {} and [] rather than null.
	src.Params = nil
	b, _ = json.Marshal(Provenance(s, src, nil, now, settle))
	if !strings.Contains(string(b), `"params":{}`) || !strings.Contains(string(b), `"projects":[]`) {
		t.Errorf("nil params/projects = %s", b)
	}
}
