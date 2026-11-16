package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

func TestCreateRun_StartsRunningAndFinishRunSetsDuration(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	from, to := isoweek.MustParse("2026-W18"), isoweek.MustParse("2026-W44")

	run, err := CreateRun(ctx, s.DB, NewRun{Trigger: TriggerRefreshAll, From: from, To: to, StartedAt: testNow})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.ID <= 0 || run.Status != RunRunning || run.Trigger != TriggerRefreshAll || run.From != "2026-W18" || run.To != "2026-W44" {
		t.Fatalf("run = %+v", run)
	}
	if run.FinishedAt != nil || run.DurationMs != nil || run.Error != nil {
		t.Fatalf("new run has finish fields: %+v", run)
	}
	if run.Sources == nil || run.SnapshotIDs == nil {
		t.Fatalf("slices must be non-nil so JSON renders [] not null: %+v", run)
	}

	if err := FinishRun(ctx, s.DB, run.ID, RunOK, testNow.Add(8123*time.Millisecond), nil); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	got, err := GetRun(ctx, s.DB, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != RunOK || got.FinishedAt == nil || got.DurationMs == nil || *got.DurationMs != 8123 || got.Error != nil {
		t.Fatalf("finished run = %+v", got)
	}
	if !got.StartedAt.Equal(testNow) || !got.FinishedAt.Equal(testNow.Add(8123*time.Millisecond).Truncate(time.Second)) {
		t.Errorf("timestamps = %v / %v", got.StartedAt, got.FinishedAt)
	}
	if got.FromWeek != from || got.ToWeek != to {
		t.Errorf("typed weeks = %v / %v", got.FromWeek, got.ToWeek)
	}
}

func TestFinishRun_RecordsFailureAndRejectsBadInput(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	run, _ := CreateRun(ctx, s.DB, NewRun{Trigger: TriggerRefreshWeek, From: isoweek.MustParse("2026-W42"), To: isoweek.MustParse("2026-W42"), StartedAt: testNow})

	msg := "db write failed"
	if err := FinishRun(ctx, s.DB, run.ID, RunFailed, testNow.Add(time.Second), &msg); err != nil {
		t.Fatal(err)
	}
	got, _ := GetRun(ctx, s.DB, run.ID)
	if got.Status != RunFailed || got.Error == nil || *got.Error != msg {
		t.Fatalf("failed run = %+v", got)
	}
	if err := FinishRun(ctx, s.DB, run.ID, RunRunning, testNow, nil); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("finishing with 'running' err = %v", err)
	}
	if err := FinishRun(ctx, s.DB, 9999, RunOK, testNow, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown run err = %v", err)
	}
	if _, err := CreateRun(ctx, s.DB, NewRun{Trigger: "bogus", From: isoweek.MustParse("2026-W42"), To: isoweek.MustParse("2026-W42"), StartedAt: testNow}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bogus trigger err = %v", err)
	}
	if _, err := CreateRun(ctx, s.DB, NewRun{Trigger: TriggerRefreshWeek, From: isoweek.MustParse("2026-W43"), To: isoweek.MustParse("2026-W42"), StartedAt: testNow}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("reversed range err = %v", err)
	}
}

func TestAddSource_StoresStatsOrErrorAndTrimsMessage(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	run, _ := CreateRun(ctx, s.DB, NewRun{Trigger: TriggerRefreshWeek, From: isoweek.MustParse("2026-W42"), To: isoweek.MustParse("2026-W42"), StartedAt: testNow})

	long := strings.Repeat("é", 2500) // multi-byte: trimming must count characters
	code, hint := "permission_denied", "Grant roles/bigquery.jobUser on CLOUD0_BIGQUERY_PROJECT."
	failed, err := AddSource(ctx, s.DB, NewRunSource{
		RunID: run.ID, Source: SourceBigQueryBilling, Status: SourceFailed,
		StartedAt: testNow, FinishedAt: testNow.Add(1500 * time.Millisecond),
		SQLText: "SELECT ...", Params: nil,
		ErrorCode: &code, ErrorMessage: &long, FixHint: &hint,
	})
	if err != nil {
		t.Fatalf("AddSource failed source: %v", err)
	}
	if failed.ID <= 0 || failed.Status != SourceFailed || failed.DurationMs != 1500 {
		t.Fatalf("failed source = %+v", failed)
	}
	if failed.ErrorMessage == nil || len([]rune(*failed.ErrorMessage)) != MaxErrorMessageLen {
		t.Fatalf("error_message not trimmed to %d chars", MaxErrorMessageLen)
	}
	if failed.ErrorCode == nil || *failed.ErrorCode != code || failed.FixHint == nil || *failed.FixHint != hint {
		t.Errorf("error fields = %+v", failed)
	}
	if string(failed.Params) != "{}" {
		t.Errorf("nil params stored as %s, want {}", failed.Params)
	}
	if failed.JobID != nil || failed.BytesBilled != nil || failed.CacheHit != nil || failed.RowsReturned != nil {
		t.Errorf("failed source has job stats: %+v", failed)
	}

	_, ok := seedRunAndSource(t, s.DB, isoweek.MustParse("2026-W42"), isoweek.MustParse("2026-W42"))
	if ok.JobID == nil || *ok.JobID != "job-test-1" || ok.BytesBilled == nil || *ok.BytesBilled != 10485760 || ok.CacheHit == nil || *ok.CacheHit || ok.RowsReturned == nil || *ok.RowsReturned != 3 {
		t.Fatalf("ok source stats = %+v", ok)
	}
	var params map[string]string
	if err := json.Unmarshal(ok.Params, &params); err != nil || len(params) != 4 || params["usage_start"] != "2026-10-12T00:00:00Z" {
		t.Fatalf("params = %s (%v)", ok.Params, err)
	}
	if !strings.Contains(ok.SQLText, "_PARTITIONTIME") {
		t.Errorf("sql_text lost: %q", ok.SQLText)
	}

	bad := []NewRunSource{
		{RunID: 0, Source: SourceBigQueryBilling, Status: SourceOK, StartedAt: testNow, FinishedAt: testNow},
		{RunID: run.ID, Source: "", Status: SourceOK, StartedAt: testNow, FinishedAt: testNow},
		{RunID: run.ID, Source: SourceBigQueryBilling, Status: "meh", StartedAt: testNow, FinishedAt: testNow},
		{RunID: run.ID, Source: SourceBigQueryBilling, Status: SourceOK, StartedAt: testNow, FinishedAt: testNow.Add(-time.Second)},
	}
	for i, ns := range bad {
		if _, err := AddSource(ctx, s.DB, ns); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("bad source %d: err = %v", i, err)
		}
	}
	if _, err := AddSource(ctx, s.DB, NewRunSource{RunID: 9999, Source: SourceBigQueryBilling, Status: SourceOK, StartedAt: testNow, FinishedAt: testNow}); err == nil {
		t.Error("dangling run_id accepted")
	}
}

func TestListRuns_NewestFirstWithSourcesAndSnapshotIDsWithoutSQL(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	w42 := isoweek.MustParse("2026-W42")

	older := insertFixtureSnapshot(t, s, w42, fixtureRows(), isoweek.StatusFinal)
	// A later run that starts earlier on the clock must still sort by started_at.
	early, _ := CreateRun(ctx, s.DB, NewRun{Trigger: TriggerRefreshWeek, From: w42, To: w42, StartedAt: testNow.Add(-time.Hour)})
	newer := insertFixtureSnapshot(t, s, w42, fixtureRows(), isoweek.StatusFinal)

	runs, err := ListRuns(ctx, s.DB, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("runs = %d, want 3", len(runs))
	}
	// Two runs share started_at = testNow; the higher id (newer) comes first.
	if runs[0].ID != newer.RunID || runs[1].ID != older.RunID || runs[2].ID != early.ID {
		t.Fatalf("order = %d %d %d, want %d %d %d", runs[0].ID, runs[1].ID, runs[2].ID, newer.RunID, older.RunID, early.ID)
	}
	if len(runs[0].Sources) != 1 || runs[0].Sources[0].SQLText != "" || runs[0].Sources[0].Params != nil {
		t.Errorf("list must omit sql/params: %+v", runs[0].Sources)
	}
	if len(runs[0].SnapshotIDs) != 1 || runs[0].SnapshotIDs[0] != newer.ID {
		t.Errorf("snapshot_ids = %v, want [%d]", runs[0].SnapshotIDs, newer.ID)
	}
	if runs[2].Sources == nil || len(runs[2].Sources) != 0 || runs[2].SnapshotIDs == nil || len(runs[2].SnapshotIDs) != 0 {
		t.Errorf("run without sources must have empty non-nil slices: %+v", runs[2])
	}

	limited, err := ListRuns(ctx, s.DB, 1)
	if err != nil || len(limited) != 1 || limited[0].ID != newer.RunID {
		t.Fatalf("limit 1 = %+v (%v)", limited, err)
	}
	def, _ := ListRuns(ctx, s.DB, 0)
	if len(def) != 3 {
		t.Errorf("limit 0 should default, got %d", len(def))
	}

	full, err := GetRun(ctx, s.DB, newer.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Sources) != 1 || !strings.Contains(full.Sources[0].SQLText, "SELECT") || full.Sources[0].Params == nil {
		t.Fatalf("GetRun must include sql/params: %+v", full.Sources)
	}
	stripped := full.WithoutSQL()
	if stripped.Sources[0].SQLText != "" || stripped.Sources[0].Params != nil || full.Sources[0].SQLText == "" {
		t.Errorf("WithoutSQL must copy, not mutate: %+v / %+v", stripped.Sources[0].SQLText, full.Sources[0].SQLText)
	}
	src, err := GetRunSource(ctx, s.DB, newer.RunSourceID)
	if err != nil || src.ID != newer.RunSourceID || src.RunID != newer.RunID || src.SQLText == "" {
		t.Fatalf("GetRunSource = %+v (%v)", src, err)
	}
	if _, err := GetRun(ctx, s.DB, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown run err = %v", err)
	}
	if _, err := GetRunSource(ctx, s.DB, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown source err = %v", err)
	}
}

func TestRun_JSONShapeMatchesAPI(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	snap := insertFixtureSnapshot(t, s, isoweek.MustParse("2026-W42"), fixtureRows(), isoweek.StatusFinal)
	run, err := GetRun(ctx, s.DB, snap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"id", "trigger", "from", "to", "started_at", "finished_at", "duration_ms", "status", "error", "sources", "snapshot_ids"}
	for _, k := range wantKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("run JSON lacks %q: %s", k, b)
		}
	}
	if len(m) != len(wantKeys) {
		t.Errorf("run JSON has unexpected keys: %s", b)
	}
	if string(m["from"]) != `"2026-W42"` || string(m["started_at"]) != `"2026-10-26T09:00:00Z"` || string(m["error"]) != "null" {
		t.Errorf("from/started_at/error = %s / %s / %s", m["from"], m["started_at"], m["error"])
	}
	var sources []map[string]json.RawMessage
	if err := json.Unmarshal(m["sources"], &sources); err != nil || len(sources) != 1 {
		t.Fatalf("sources = %s", m["sources"])
	}
	srcKeys := []string{"id", "source", "status", "duration_ms", "job_id", "bytes_processed", "bytes_billed", "cache_hit", "rows_returned", "error_code", "error_message", "fix_hint", "sql", "params"}
	for _, k := range srcKeys {
		if _, ok := sources[0][k]; !ok {
			t.Errorf("source JSON lacks %q: %s", k, m["sources"])
		}
	}
	if len(sources[0]) != len(srcKeys) {
		t.Errorf("source JSON has unexpected keys: %s", m["sources"])
	}
	if string(sources[0]["cache_hit"]) != "false" || string(sources[0]["error_code"]) != "null" {
		t.Errorf("cache_hit/error_code = %s / %s", sources[0]["cache_hit"], sources[0]["error_code"])
	}
	var params map[string]string
	if err := json.Unmarshal(sources[0]["params"], &params); err != nil || params["part_start"] != "2026-10-11" {
		t.Errorf("params = %s", sources[0]["params"])
	}
	// The list form drops sql/params entirely.
	lb, _ := json.Marshal(run.WithoutSQL())
	if strings.Contains(string(lb), `"sql"`) || strings.Contains(string(lb), `"params"`) {
		t.Errorf("WithoutSQL JSON still carries sql/params: %s", lb)
	}
}
