package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "cloud0.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpen_CreatesParentDirectoryAndAppliesPragmas(t *testing.T) {
	s := openTemp(t)
	var journal string
	if err := s.DB.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
	var fk int
	if err := s.DB.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
	var busy int
	if err := s.DB.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busy)
	}
}

func TestOpen_RejectsEmptyPath(t *testing.T) {
	if _, err := Open(context.Background(), ""); err == nil {
		t.Fatal("empty path accepted")
	}
}

func TestMigrate_AppliesInitAndStatusReportsIt(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	pending, err := s.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 5 {
		t.Fatalf("pending before migrate = %d, want 5", pending)
	}
	before, err := s.MigrationStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 5 || before[0].Applied || before[0].Source != "0001_init.sql" || before[1].Source != "0002_phase2.sql" || before[2].Source != "0003_error_codes.sql" || before[3].Source != "0004_alerts.sql" || before[4].Source != "0005_attribution.sql" {
		t.Fatalf("status before migrate: %+v", before)
	}

	applied, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) != 5 || applied[0].Source != "0001_init.sql" || applied[1].Source != "0002_phase2.sql" || applied[2].Source != "0003_error_codes.sql" || applied[3].Source != "0004_alerts.sql" || applied[4].Source != "0005_attribution.sql" {
		t.Fatalf("applied = %+v", applied)
	}

	after, err := s.MigrationStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 5 || !after[4].Applied || !after[3].Applied || !after[2].Applied || !after[1].Applied || after[0].AppliedAt.IsZero() || after[0].Version != 1 || after[1].Version != 2 {
		t.Fatalf("status after migrate: %+v", after)
	}
	if time.Since(after[0].AppliedAt) > time.Minute {
		t.Errorf("AppliedAt %s is not recent", after[0].AppliedAt)
	}
	pending, _ = s.Pending(ctx)
	if pending != 0 {
		t.Fatalf("pending after migrate = %d", pending)
	}

	// Idempotent: a second up applies nothing and does not fail.
	again, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second Migrate applied %+v", again)
	}
}

func TestMigrate_CreatesFullPhase1Schema(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tables := map[string][]string{
		"runs":          {"id", "trigger", "from_iso_year", "from_iso_week", "to_iso_year", "to_iso_week", "started_at", "finished_at", "duration_ms", "status", "error", "created_at"},
		"run_sources":   {"id", "run_id", "source", "status", "started_at", "finished_at", "duration_ms", "sql_text", "params_json", "job_id", "bytes_processed", "bytes_billed", "cache_hit", "rows_returned", "error_code", "error_message", "fix_hint"},
		"snapshots":     {"id", "iso_year", "iso_week", "week_start", "week_end", "fetched_at", "status_at_fetch", "run_id", "run_source_id", "row_count", "gross_total", "credits_total", "net_total", "created_at"},
		"snapshot_rows": {"id", "snapshot_id", "project_id", "service_id", "service", "sku_id", "sku", "resource_name", "resource_global_name", "labels_json", "system_labels_json", "gross", "credits", "net", "line_items"},
		"annotations":   {"id", "iso_year", "iso_week", "body", "created_at", "updated_at"},
		"settings":      {"key", "value", "updated_at"},
	}
	for table, cols := range tables {
		rows, err := s.DB.Query("SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		got := map[string]bool{}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			got[n] = true
		}
		_ = rows.Close()
		if len(got) == 0 {
			t.Errorf("table %s missing", table)
			continue
		}
		for _, c := range cols {
			if !got[c] {
				t.Errorf("%s lacks column %s", table, c)
			}
		}
		if len(got) != len(cols) {
			t.Errorf("%s has %d columns, want %d: %v", table, len(got), len(cols), got)
		}
	}

	indexes := []string{"idx_runs_started_at", "idx_run_sources_run_id", "idx_snapshots_week_latest", "idx_snapshots_run_id", "idx_snapshot_rows_service", "idx_snapshot_rows_service_sku", "idx_snapshot_rows_project"}
	for _, idx := range indexes {
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("index %s missing", idx)
		}
	}
}

func TestMigrate_SeedsAnomalyThresholds(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"anomaly_pct_threshold": "20", "anomaly_abs_threshold_usd": "200"}
	for k, v := range want {
		var value, updated string
		if err := s.DB.QueryRow(`SELECT value, updated_at FROM settings WHERE "key" = ?`, k).Scan(&value, &updated); err != nil {
			t.Fatalf("settings %s: %v", k, err)
		}
		if value != v {
			t.Errorf("%s = %q, want %q", k, value, v)
		}
		if _, err := ParseTime(updated); err != nil {
			t.Errorf("%s updated_at %q is not RFC3339: %v", k, updated, err)
		}
	}
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM settings").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("settings rows = %d, want 2", n)
	}
}

func TestSchema_EnforcesChecksForeignKeysAndCascade(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := FormatTime(time.Now())
	// CHECK constraint on runs.trigger.
	_, err := s.DB.Exec(`INSERT INTO runs ("trigger", from_iso_year, from_iso_week, to_iso_year, to_iso_week, started_at, status, created_at) VALUES ('bogus', 2026, 42, 2026, 42, ?, 'running', ?)`, now, now)
	if err == nil {
		t.Fatal("invalid trigger accepted")
	}
	// FK on run_sources.run_id.
	_, err = s.DB.Exec(`INSERT INTO run_sources (run_id, source, status, started_at, finished_at, duration_ms, sql_text, params_json) VALUES (999, 'bigquery_billing', 'ok', ?, ?, 1, 'SELECT 1', '{}')`, now, now)
	if err == nil {
		t.Fatal("dangling run_id accepted; foreign keys not enforced")
	}
	// Happy path through WithTx, then cascade delete.
	var snapID int64
	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO runs ("trigger", from_iso_year, from_iso_week, to_iso_year, to_iso_week, started_at, status, created_at) VALUES ('refresh_week', 2026, 42, 2026, 42, ?, 'running', ?)`, now, now)
		if err != nil {
			return err
		}
		runID, err := LastInsertID(res)
		if err != nil {
			return err
		}
		res, err = tx.Exec(`INSERT INTO run_sources (run_id, source, status, started_at, finished_at, duration_ms, sql_text, params_json) VALUES (?, 'bigquery_billing', 'ok', ?, ?, 1, 'SELECT 1', '{}')`, runID, now, now)
		if err != nil {
			return err
		}
		srcID, err := LastInsertID(res)
		if err != nil {
			return err
		}
		res, err = tx.Exec(`INSERT INTO snapshots (iso_year, iso_week, week_start, week_end, fetched_at, status_at_fetch, run_id, run_source_id, row_count, gross_total, credits_total, net_total, created_at) VALUES (2026, 42, '2026-10-12', '2026-10-18', ?, 'final', ?, ?, 1, 1.5, -0.5, 1.0, ?)`, now, runID, srcID, now)
		if err != nil {
			return err
		}
		snapID, err = LastInsertID(res)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO snapshot_rows (snapshot_id, project_id, service, sku, gross, credits, net, line_items) VALUES (?, 'p', 'Compute Engine', 'sku', 1.5, -0.5, 1.0, 3)`, snapID)
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if _, err := s.DB.Exec("DELETE FROM snapshots WHERE id = ?", snapID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM snapshot_rows WHERE snapshot_id = ?", snapID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("snapshot_rows not cascaded: %d left", n)
	}
	// UNIQUE (iso_year, iso_week) on annotations.
	if _, err := s.DB.Exec("INSERT INTO annotations (iso_year, iso_week, body, created_at, updated_at) VALUES (2026, 42, 'a', ?, ?)", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO annotations (iso_year, iso_week, body, created_at, updated_at) VALUES (2026, 42, 'b', ?, ?)", now, now); err == nil {
		t.Fatal("duplicate annotation week accepted")
	}
}

func TestWithTx_RollsBackOnError(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO settings ("key", value, updated_at) VALUES ('tmp', '1', ?)`, FormatTime(time.Now())); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx returned %v, want boom", err)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE "key" = 'tmp'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("rolled-back row is present")
	}
}

func TestTimeHelpers_RoundTripRFC3339UTC(t *testing.T) {
	loc := time.FixedZone("plus5", 5*3600)
	in := time.Date(2026, time.October, 26, 14, 0, 0, 0, loc)
	s := FormatTime(in)
	if s != "2026-10-26T09:00:00Z" {
		t.Fatalf("FormatTime = %q", s)
	}
	back, err := ParseTime(s)
	if err != nil || !back.Equal(in) {
		t.Fatalf("ParseTime(%q) = %v, %v", s, back, err)
	}
	if FormatDate(in) != "2026-10-26" {
		t.Fatalf("FormatDate = %q", FormatDate(in))
	}
}
