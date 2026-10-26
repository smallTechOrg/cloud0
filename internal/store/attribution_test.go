package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

func rule(prio int, field, op, key, val, product, env string, shared bool, src string) AttributionRule {
	return AttributionRule{Priority: prio, MatchField: field, MatchOp: op, MatchKey: key, MatchValue: val, Product: product, Environment: env, Shared: shared, Enabled: true, Source: src}
}

func replaceTx(t *testing.T, s *Store, rules []AttributionRule, now time.Time) ([]AttributionRule, error) {
	t.Helper()
	var out []AttributionRule
	err := s.WithTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = ReplaceAttributionRules(context.Background(), tx, rules, now)
		return err
	})
	return out, err
}

func TestSnapshotRows_LabelsRoundTripAndOldRowsAreNull(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	week := isoweek.MustParse("2026-W43")
	rows := []SnapshotRow{
		{ProjectID: "proj-a", Service: "Compute Engine", SKU: "N1 Core", ResourceName: strp("example-vm"), LabelsJSON: strp(`[{"key":"env","value":"prod"}]`), SystemLabelsJSON: strp(`[]`), Gross: 10},
		{ProjectID: "proj-a", Service: "Compute Engine", SKU: "PD", ResourceName: strp("example-disk"), Gross: 5}, // pre-0005 style: no labels
	}
	snap := insertFixtureSnapshot(t, s, week, rows, isoweek.StatusFinal)
	got, err := SnapshotRows(ctx, s.DB, snap.ID, ScopeAll)
	if err != nil || len(got) != 2 {
		t.Fatalf("SnapshotRows: %v %v", got, err)
	}
	if got[0].LabelsJSON == nil || *got[0].LabelsJSON != `[{"key":"env","value":"prod"}]` || got[0].SystemLabelsJSON == nil || *got[0].SystemLabelsJSON != `[]` {
		t.Errorf("labels not round-tripped: %+v", got[0])
	}
	if got[1].LabelsJSON != nil || got[1].SystemLabelsJSON != nil {
		t.Errorf("row without labels must read back nil: %+v", got[1])
	}
	byID, err := RowsForSnapshots(ctx, s.DB, []int64{snap.ID}, ScopeAll)
	if err != nil || byID[snap.ID][0].LabelsJSON == nil {
		t.Errorf("RowsForSnapshots lost labels: %v %v", byID, err)
	}
}

func TestMigration0005_UpgradeKeepsOldRows(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, testenv.TempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.provider()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 4); err != nil {
		t.Fatalf("UpTo(4): %v", err)
	}
	week := isoweek.MustParse("2026-W43")
	run, src := seedRunAndSource(t, s.DB, week, week)
	snapRes, err := s.DB.ExecContext(ctx, `INSERT INTO snapshots (iso_year, iso_week, week_start, week_end, fetched_at, status_at_fetch, run_id, run_source_id, row_count, gross_total, credits_total, net_total, created_at)
VALUES (2026, 43, '2026-10-19', '2026-10-25', ?, 'final', ?, ?, 1, 1, 0, 1, ?)`, FormatTime(testNow), run.ID, src.ID, FormatTime(testNow))
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := snapRes.LastInsertId()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO snapshot_rows (snapshot_id, project_id, service, sku, gross, credits, net, line_items) VALUES (?, 'proj-a', 'Cloud Run', 'cpu', 1, 0, 1, 1)`, sid); err != nil {
		t.Fatalf("0004-shaped insert: %v", err)
	}
	applied, err := s.Migrate(ctx)
	if err != nil || len(applied) != 1 || applied[0].Source != "0005_attribution.sql" {
		t.Fatalf("Migrate: %+v %v", applied, err)
	}
	rows, err := SnapshotRows(ctx, s.DB, sid, ScopeAll)
	if err != nil || len(rows) != 1 || rows[0].LabelsJSON != nil || rows[0].Net != 1 {
		t.Fatalf("old row after upgrade: %+v %v", rows, err)
	}
}

func TestAttributionRules_ReplaceInsertUpdateDelete(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	if got, err := ListAttributionRules(ctx, s.DB); err != nil || len(got) != 0 {
		t.Fatalf("fresh list: %v %v", got, err)
	}
	if ts, err := AttributionRulesUpdatedAt(ctx, s.DB); err != nil || ts != nil {
		t.Fatalf("fresh updated_at: %v %v", ts, err)
	}
	note := "OS patching"
	first := []AttributionRule{
		rule(310, "resource_name", "contains", "", "example-product", "example-product", "", false, "seed"),
		rule(100, "label", "equals", "app", "example-alias", "example-product", "", false, "seed"),
		func() AttributionRule {
			r := rule(900, "service", "equals", "", "VM Manager", "", "", true, "user")
			r.Note = &note
			return r
		}(),
	}
	stored, err := replaceTx(t, s, first, testNow)
	if err != nil || len(stored) != 3 {
		t.Fatalf("replace: %v %v", stored, err)
	}
	if stored[0].Priority != 100 || stored[0].MatchKey != "app" || stored[1].Priority != 310 || !stored[2].Shared || stored[2].Note == nil || *stored[2].Note != note {
		t.Fatalf("order/fields wrong: %+v", stored)
	}
	if stored[0].ID == 0 || !stored[0].CreatedAt.Equal(testNow) {
		t.Fatalf("id/created: %+v", stored[0])
	}

	// Second PUT: keep rule 310 (edit it, disable), drop the label rule, add a new env rule.
	later := testNow.Add(time.Hour)
	keep := stored[1]
	keep.Enabled, keep.MatchValue = false, "example-product-v2"
	second := []AttributionRule{keep, stored[2], rule(320, "resource_name", "regex", "", "(^|-)prod($|-)", "", "prod", false, "proposed")}
	got, err := replaceTx(t, s, second, later)
	if err != nil || len(got) != 3 {
		t.Fatalf("second replace: %v %v", got, err)
	}
	var kept AttributionRule
	for _, r := range got {
		if r.ID == keep.ID {
			kept = r
		}
		if r.ID == stored[0].ID {
			t.Errorf("deleted rule %d still present", r.ID)
		}
	}
	if kept.Enabled || kept.MatchValue != "example-product-v2" || !kept.CreatedAt.Equal(testNow) || !kept.UpdatedAt.Equal(later) {
		t.Errorf("update in place wrong: %+v", kept)
	}
	if ts, _ := AttributionRulesUpdatedAt(ctx, s.DB); ts == nil || !ts.Equal(later) {
		t.Errorf("updated_at = %v, want %v", ts, later)
	}

	// Empty PUT clears everything.
	if got, err := replaceTx(t, s, nil, later); err != nil || len(got) != 0 {
		t.Fatalf("clear: %v %v", got, err)
	}
}

func TestAttributionRules_ValidationRejectsAndSavesNothing(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	if _, err := replaceTx(t, s, []AttributionRule{rule(1, "project", "equals", "", "proj-a", "example-product", "", false, "user")}, testNow); err != nil {
		t.Fatal(err)
	}
	bad := map[string]AttributionRule{
		"unknown field":      rule(1, "bogus", "equals", "", "x", "p", "", false, "user"),
		"unknown op":         rule(1, "service", "like", "", "x", "p", "", false, "user"),
		"unknown source":     rule(1, "service", "equals", "", "x", "p", "", false, "llm"),
		"empty value":        rule(1, "service", "equals", "", "  ", "p", "", false, "user"),
		"label without key":  rule(1, "label", "equals", "", "x", "p", "", false, "user"),
		"key on non-label":   rule(1, "service", "equals", "k", "x", "p", "", false, "user"),
		"no action":          rule(1, "service", "equals", "", "x", "", "", false, "user"),
		"shared and product": rule(1, "service", "equals", "", "x", "p", "", true, "user"),
		"bad product name":   rule(1, "service", "equals", "", "x", "Not Valid!", "", false, "user"),
		"negative priority":  rule(-1, "service", "equals", "", "x", "p", "", false, "user"),
		"unknown id":         {ID: 999, Priority: 1, MatchField: "service", MatchOp: "equals", MatchValue: "x", Product: "p", Enabled: true, Source: "user"},
	}
	for name, r := range bad {
		if _, err := replaceTx(t, s, []AttributionRule{r}, testNow); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
	tooMany := make([]AttributionRule, MaxAttributionRules+1)
	for i := range tooMany {
		tooMany[i] = rule(i, "service", "equals", "", "x", "p", "", false, "user")
	}
	if _, err := replaceTx(t, s, tooMany, testNow); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("too many rules: %v", err)
	}
	got, err := ListAttributionRules(ctx, s.DB)
	if err != nil || len(got) != 1 || got[0].MatchValue != "proj-a" {
		t.Fatalf("a rejected PUT changed the stored set: %+v %v", got, err)
	}
}

func TestAttributionRules_RollbackOnMidWriteFailureAndReopen(t *testing.T) {
	path := testenv.TempDB(t)
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := replaceTx(t, s, []AttributionRule{rule(5, "sku", "contains", "", "Forwarding", "", "", true, "seed")}, testNow); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := ListAttributionRules(ctx, s2.DB)
	if err != nil || len(got) != 1 || !got[0].Shared || got[0].MatchValue != "Forwarding" {
		t.Fatalf("rules did not survive reopen: %+v %v", got, err)
	}
}

func TestTargets_EffectiveHistoryAndClear(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	f := func(v float64) *float64 { return &v }
	put := func(ts []Target, now time.Time) error {
		return s.WithTx(ctx, func(tx *sql.Tx) error { return PutTargets(ctx, tx, ts, now) })
	}
	if err := put([]Target{
		{Product: "example-product", MonthFrom: "2026-09", Amount: f(5000)},
		{Product: AccountTargetProduct, MonthFrom: "2026-09", Amount: f(25000)},
	}, testNow); err != nil {
		t.Fatal(err)
	}
	if err := put([]Target{{Product: "example-product", MonthFrom: "2026-11", Amount: f(6000)}}, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cases := map[string]float64{"2026-09": 5000, "2026-10": 5000, "2026-11": 6000, "2027-01": 6000}
	for month, want := range cases {
		eff, err := EffectiveTargets(ctx, s.DB, month)
		if err != nil || eff["example-product"].Amount == nil || *eff["example-product"].Amount != want {
			t.Errorf("month %s: %+v %v, want %v", month, eff["example-product"], err, want)
		}
	}
	if eff, _ := EffectiveTargets(ctx, s.DB, "2026-08"); len(eff) != 0 {
		t.Errorf("before first month_from nothing applies: %+v", eff)
	}
	// Same (product, month_from) replaces; nil clears from that month on; history retained.
	if err := put([]Target{{Product: "example-product", MonthFrom: "2026-11", Amount: nil}}, testNow.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if eff, _ := EffectiveTargets(ctx, s.DB, "2026-12"); eff["example-product"].Amount != nil || len(eff) != 1 || eff[AccountTargetProduct].Amount == nil {
		t.Errorf("cleared product must be omitted, account kept: %+v", eff)
	}
	if eff, _ := EffectiveTargets(ctx, s.DB, "2026-10"); *eff["example-product"].Amount != 5000 {
		t.Errorf("earlier month must keep the old target: %+v", eff)
	}
	all, err := ListTargets(ctx, s.DB)
	if err != nil || len(all) != 3 {
		t.Fatalf("history = %+v %v, want 3 rows", all, err)
	}
}

func TestTargets_Validation(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	neg := -1.0
	for name, ts := range map[string][]Target{
		"empty":         nil,
		"bad product":   {{Product: "Bad Name", MonthFrom: "2026-10"}},
		"bad month":     {{Product: "p", MonthFrom: "2026-13"}},
		"negative":      {{Product: "p", MonthFrom: "2026-10", Amount: &neg}},
		"good then bad": {{Product: "p", MonthFrom: "2026-10"}, {Product: "q", MonthFrom: "nope"}},
	} {
		err := s.WithTx(ctx, func(tx *sql.Tx) error { return PutTargets(ctx, tx, ts, testNow) })
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if all, _ := ListTargets(ctx, s.DB); len(all) != 0 {
		t.Errorf("invalid PUT saved rows: %+v", all)
	}
	if _, err := EffectiveTargets(ctx, s.DB, "2026-1"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad month: %v", err)
	}
}

func TestAttributionSettingsKeys_RoundTrip(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	if err := PutSettings(ctx, s.DB, map[string]string{KeyAttributionAllocate: "proportional", KeyAttributionAliases: `{"example-alias":"example-product"}`}, testNow); err != nil {
		t.Fatal(err)
	}
	m, err := GetSettingsWithPrefix(ctx, s.DB, "attribution.")
	if err != nil || m[KeyAttributionAllocate] != "proportional" || m[KeyAttributionAliases] == "" {
		t.Fatalf("settings: %v %v", m, err)
	}
	if ts, _ := AttributionRulesUpdatedAt(ctx, s.DB); ts == nil || !ts.Equal(testNow) {
		t.Errorf("updated_at should reflect attribution settings: %v", ts)
	}
}
