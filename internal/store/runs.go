package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

// RunTrigger is what started a run (spec/data.md → runs.trigger).
type RunTrigger string

const (
	TriggerRefreshWeek RunTrigger = "refresh_week"
	TriggerRefreshAll  RunTrigger = "refresh_all"
	// TriggerRefreshMonth is a Phase 2 month refresh (migration 0002 relaxes the CHECK).
	TriggerRefreshMonth RunTrigger = "refresh_month"
)

// RunStatus is the run lifecycle state.
type RunStatus string

const (
	RunRunning RunStatus = "running"
	RunOK      RunStatus = "ok"
	RunPartial RunStatus = "partial" // some sources failed (possible from Phase 2)
	RunFailed  RunStatus = "failed"
)

// SourceStatus is the outcome of one data source within a run.
type SourceStatus string

const (
	SourceOK     SourceStatus = "ok"
	SourceFailed SourceStatus = "failed"
)

// SourceBigQueryBilling is the only source in Phase 1.
const SourceBigQueryBilling = "bigquery_billing"

// MaxErrorMessageLen caps run_sources.error_message in characters.
const MaxErrorMessageLen = 2000

// DefaultRunsLimit and MaxRunsLimit bound ListRuns (spec/api.md → GET /runs).
const (
	DefaultRunsLimit = 20
	MaxRunsLimit     = 200
)

// Run is one refresh with its sources and resulting snapshot ids; its JSON is
// the `run` object of spec/api.md (GET /runs, GET /runs/{id}, POST /refresh).
type Run struct {
	ID          int64        `json:"id"`
	Trigger     RunTrigger   `json:"trigger"`
	From        string       `json:"from"`
	To          string       `json:"to"`
	FromWeek    isoweek.Week `json:"-"`
	ToWeek      isoweek.Week `json:"-"`
	StartedAt   time.Time    `json:"started_at"`
	FinishedAt  *time.Time   `json:"finished_at"`
	DurationMs  *int64       `json:"duration_ms"`
	Status      RunStatus    `json:"status"`
	Error       *string      `json:"error"`
	Sources     []RunSource  `json:"sources"`
	SnapshotIDs []int64      `json:"snapshot_ids"`
}

// RunSource is one data source invocation — the provenance record. SQLText and
// Params are populated by GetRun / GetRunSource and left empty by ListRuns so
// the list omits `sql` / `params` as spec/api.md requires.
type RunSource struct {
	ID             int64           `json:"id"`
	RunID          int64           `json:"-"`
	Source         string          `json:"source"`
	Status         SourceStatus    `json:"status"`
	StartedAt      time.Time       `json:"-"`
	FinishedAt     time.Time       `json:"-"`
	DurationMs     int64           `json:"duration_ms"`
	JobID          *string         `json:"job_id"`
	BytesProcessed *int64          `json:"bytes_processed"`
	BytesBilled    *int64          `json:"bytes_billed"`
	CacheHit       *bool           `json:"cache_hit"`
	RowsReturned   *int64          `json:"rows_returned"`
	ErrorCode      *string         `json:"error_code"`
	ErrorMessage   *string         `json:"error_message"`
	FixHint        *string         `json:"fix_hint"`
	SQLText        string          `json:"sql,omitempty"`
	Params         json.RawMessage `json:"params,omitempty"`
}

// WithoutSQL returns a copy whose sources carry no sql / params (list shape).
func (r Run) WithoutSQL() Run {
	out := r
	out.Sources = make([]RunSource, len(r.Sources))
	for i, s := range r.Sources {
		s.SQLText, s.Params = "", nil
		out.Sources[i] = s
	}
	return out
}

// NewRun is the input of CreateRun.
type NewRun struct {
	Trigger   RunTrigger
	From, To  isoweek.Week
	StartedAt time.Time
}

// CreateRun inserts a `running` run covering the inclusive week range.
func CreateRun(ctx context.Context, q Querier, nr NewRun) (Run, error) {
	switch {
	case nr.Trigger != TriggerRefreshWeek && nr.Trigger != TriggerRefreshAll && nr.Trigger != TriggerRefreshMonth:
		return Run{}, fmt.Errorf("%w: trigger %q", ErrInvalidInput, nr.Trigger)
	case !nr.From.Valid() || !nr.To.Valid() || isoweek.Compare(nr.From, nr.To) > 0:
		return Run{}, fmt.Errorf("%w: week range %v..%v", ErrInvalidInput, nr.From, nr.To)
	case nr.StartedAt.IsZero():
		return Run{}, fmt.Errorf("%w: started_at required", ErrInvalidInput)
	}
	started := nr.StartedAt.UTC().Truncate(time.Second)
	res, err := q.ExecContext(ctx, `INSERT INTO runs ("trigger", from_iso_year, from_iso_week, to_iso_year, to_iso_week, started_at, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, string(nr.Trigger), nr.From.Year, nr.From.Week, nr.To.Year, nr.To.Week, FormatTime(started), string(RunRunning), FormatTime(started))
	if err != nil {
		return Run{}, fmt.Errorf("store: create run: %w", err)
	}
	id, err := LastInsertID(res)
	if err != nil {
		return Run{}, err
	}
	return Run{ID: id, Trigger: nr.Trigger, From: nr.From.String(), To: nr.To.String(), FromWeek: nr.From, ToWeek: nr.To,
		StartedAt: started, Status: RunRunning, Sources: []RunSource{}, SnapshotIDs: []int64{}}, nil
}

// FinishRun records the final status, finish time, duration and optional
// run-level error. ErrNotFound for an unknown id.
func FinishRun(ctx context.Context, q Querier, id int64, status RunStatus, finishedAt time.Time, runErr *string) error {
	if status != RunOK && status != RunPartial && status != RunFailed {
		return fmt.Errorf("%w: finish status %q", ErrInvalidInput, status)
	}
	if finishedAt.IsZero() {
		return fmt.Errorf("%w: finished_at required", ErrInvalidInput)
	}
	var startedText string
	if err := q.QueryRowContext(ctx, `SELECT started_at FROM runs WHERE id = ?`, id).Scan(&startedText); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: run %d", ErrNotFound, id)
		}
		return fmt.Errorf("store: finish run %d: %w", id, err)
	}
	started, err := ParseTime(startedText)
	if err != nil {
		return fmt.Errorf("store: run %d started_at %q: %w", id, startedText, err)
	}
	finished := finishedAt.UTC().Truncate(time.Second)
	duration := max(finishedAt.UTC().Sub(started).Milliseconds(), 0)
	res, err := q.ExecContext(ctx, `UPDATE runs SET status = ?, finished_at = ?, duration_ms = ?, error = ? WHERE id = ?`,
		string(status), FormatTime(finished), duration, nullableString(runErr), id)
	if err != nil {
		return fmt.Errorf("store: finish run %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: run %d", ErrNotFound, id)
	}
	return nil
}

// NewRunSource is the input of AddSource: the exact SQL and named parameters
// that ran plus either job statistics or the classified error.
type NewRunSource struct {
	RunID          int64
	Source         string
	Status         SourceStatus
	StartedAt      time.Time
	FinishedAt     time.Time
	SQLText        string
	Params         map[string]string // nil → {}
	JobID          *string
	BytesProcessed *int64
	BytesBilled    *int64
	CacheHit       *bool
	RowsReturned   *int64
	ErrorCode      *string
	ErrorMessage   *string // trimmed to MaxErrorMessageLen characters
	FixHint        *string
}

// AddSource inserts one run_sources row and returns it fully populated.
func AddSource(ctx context.Context, q Querier, ns NewRunSource) (RunSource, error) {
	switch {
	case ns.RunID <= 0:
		return RunSource{}, fmt.Errorf("%w: run_id required", ErrInvalidInput)
	case ns.Source == "":
		return RunSource{}, fmt.Errorf("%w: source required", ErrInvalidInput)
	case ns.Status != SourceOK && ns.Status != SourceFailed:
		return RunSource{}, fmt.Errorf("%w: source status %q", ErrInvalidInput, ns.Status)
	case ns.StartedAt.IsZero() || ns.FinishedAt.IsZero() || ns.FinishedAt.Before(ns.StartedAt):
		return RunSource{}, fmt.Errorf("%w: source started_at/finished_at", ErrInvalidInput)
	}
	params := ns.Params
	if params == nil {
		params = map[string]string{}
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return RunSource{}, fmt.Errorf("store: marshal params: %w", err)
	}
	msg := ns.ErrorMessage
	if msg != nil {
		if r := []rune(*msg); len(r) > MaxErrorMessageLen {
			trimmed := string(r[:MaxErrorMessageLen])
			msg = &trimmed
		}
	}
	src := RunSource{
		RunID: ns.RunID, Source: ns.Source, Status: ns.Status,
		StartedAt:  ns.StartedAt.UTC().Truncate(time.Second),
		FinishedAt: ns.FinishedAt.UTC().Truncate(time.Second),
		DurationMs: ns.FinishedAt.Sub(ns.StartedAt).Milliseconds(),
		JobID:      ns.JobID, BytesProcessed: ns.BytesProcessed, BytesBilled: ns.BytesBilled, CacheHit: ns.CacheHit, RowsReturned: ns.RowsReturned,
		ErrorCode: ns.ErrorCode, ErrorMessage: msg, FixHint: ns.FixHint,
		SQLText: ns.SQLText, Params: paramsJSON,
	}
	res, err := q.ExecContext(ctx, `INSERT INTO run_sources (run_id, source, status, started_at, finished_at, duration_ms, sql_text, params_json, job_id, bytes_processed, bytes_billed, cache_hit, rows_returned, error_code, error_message, fix_hint)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		src.RunID, src.Source, string(src.Status), FormatTime(src.StartedAt), FormatTime(src.FinishedAt), src.DurationMs, src.SQLText, string(paramsJSON),
		nullableString(src.JobID), nullableInt(src.BytesProcessed), nullableInt(src.BytesBilled), nullableBool(src.CacheHit), nullableInt(src.RowsReturned),
		nullableString(src.ErrorCode), nullableString(src.ErrorMessage), nullableString(src.FixHint))
	if err != nil {
		return RunSource{}, fmt.Errorf("store: add source to run %d: %w", ns.RunID, err)
	}
	if src.ID, err = LastInsertID(res); err != nil {
		return RunSource{}, err
	}
	return src, nil
}

func nullableInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableBool(v *bool) any {
	if v == nil {
		return nil
	}
	if *v {
		return 1
	}
	return 0
}

const runColumns = `id, "trigger", from_iso_year, from_iso_week, to_iso_year, to_iso_week, started_at, finished_at, duration_ms, status, error`

func scanRun(sc scanner) (Run, error) {
	var r Run
	var started string
	var finished, errText sql.NullString
	var duration sql.NullInt64
	if err := sc.Scan(&r.ID, &r.Trigger, &r.FromWeek.Year, &r.FromWeek.Week, &r.ToWeek.Year, &r.ToWeek.Week, &started, &finished, &duration, &r.Status, &errText); err != nil {
		return Run{}, err
	}
	var err error
	if r.StartedAt, err = ParseTime(started); err != nil {
		return Run{}, fmt.Errorf("store: run %d started_at %q: %w", r.ID, started, err)
	}
	if finished.Valid {
		t, err := ParseTime(finished.String)
		if err != nil {
			return Run{}, fmt.Errorf("store: run %d finished_at %q: %w", r.ID, finished.String, err)
		}
		r.FinishedAt = &t
	}
	if duration.Valid {
		d := duration.Int64
		r.DurationMs = &d
	}
	r.Error = fromNullString(errText)
	r.From, r.To = r.FromWeek.String(), r.ToWeek.String()
	r.Sources, r.SnapshotIDs = []RunSource{}, []int64{}
	return r, nil
}

const sourceColumns = "id, run_id, source, status, started_at, finished_at, duration_ms, sql_text, params_json, job_id, bytes_processed, bytes_billed, cache_hit, rows_returned, error_code, error_message, fix_hint"

func scanSource(sc scanner, withSQL bool) (RunSource, error) {
	var s RunSource
	var started, finished, sqlText, paramsJSON string
	var jobID, errCode, errMsg, hint sql.NullString
	var bp, bb, rr, cache sql.NullInt64
	if err := sc.Scan(&s.ID, &s.RunID, &s.Source, &s.Status, &started, &finished, &s.DurationMs, &sqlText, &paramsJSON, &jobID, &bp, &bb, &cache, &rr, &errCode, &errMsg, &hint); err != nil {
		return RunSource{}, err
	}
	var err error
	if s.StartedAt, err = ParseTime(started); err != nil {
		return RunSource{}, fmt.Errorf("store: source %d started_at %q: %w", s.ID, started, err)
	}
	if s.FinishedAt, err = ParseTime(finished); err != nil {
		return RunSource{}, fmt.Errorf("store: source %d finished_at %q: %w", s.ID, finished, err)
	}
	s.JobID, s.ErrorCode, s.ErrorMessage, s.FixHint = fromNullString(jobID), fromNullString(errCode), fromNullString(errMsg), fromNullString(hint)
	s.BytesProcessed, s.BytesBilled, s.RowsReturned = fromNullInt(bp), fromNullInt(bb), fromNullInt(rr)
	if cache.Valid {
		b := cache.Int64 != 0
		s.CacheHit = &b
	}
	if withSQL {
		s.SQLText = sqlText
		s.Params = json.RawMessage(paramsJSON)
	}
	return s, nil
}

func fromNullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// GetRunSource loads one provenance record including sql and params.
func GetRunSource(ctx context.Context, q Querier, id int64) (RunSource, error) {
	s, err := scanSource(q.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM run_sources WHERE id = ?`, id), true)
	if errors.Is(err, sql.ErrNoRows) {
		return RunSource{}, fmt.Errorf("%w: run source %d", ErrNotFound, id)
	}
	if err != nil {
		return RunSource{}, fmt.Errorf("store: get run source %d: %w", id, err)
	}
	return s, nil
}

// GetRun loads one run with its sources (including sql / params) and snapshot ids.
func GetRun(ctx context.Context, q Querier, id int64) (Run, error) {
	r, err := scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("%w: run %d", ErrNotFound, id)
	}
	if err != nil {
		return Run{}, fmt.Errorf("store: get run %d: %w", id, err)
	}
	runs := []Run{r}
	if err := attachSourcesAndSnapshots(ctx, q, runs, true); err != nil {
		return Run{}, err
	}
	return runs[0], nil
}

// ListRuns returns the newest runs first (started_at, then id), each with its
// sources (without sql / params) and snapshot ids. limit ≤ 0 → DefaultRunsLimit;
// limit > MaxRunsLimit → MaxRunsLimit.
func ListRuns(ctx context.Context, q Querier, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = DefaultRunsLimit
	}
	limit = min(limit, MaxRunsLimit)
	rows, err := q.QueryContext(ctx, `SELECT `+runColumns+` FROM runs ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	defer rows.Close()
	runs := make([]Run, 0, limit)
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list runs: %w", err)
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	if err := attachSourcesAndSnapshots(ctx, q, runs, false); err != nil {
		return nil, err
	}
	return runs, nil
}

func attachSourcesAndSnapshots(ctx context.Context, q Querier, runs []Run, withSQL bool) error {
	if len(runs) == 0 {
		return nil
	}
	index := make(map[int64]int, len(runs))
	args := make([]any, 0, len(runs))
	for i, r := range runs {
		index[r.ID] = i
		args = append(args, r.ID)
	}
	in := placeholders(len(runs))

	srcRows, err := q.QueryContext(ctx, `SELECT `+sourceColumns+` FROM run_sources WHERE run_id IN (`+in+`) ORDER BY run_id, id`, args...)
	if err != nil {
		return fmt.Errorf("store: run sources: %w", err)
	}
	for srcRows.Next() {
		s, err := scanSource(srcRows, withSQL)
		if err != nil {
			_ = srcRows.Close()
			return fmt.Errorf("store: run sources: %w", err)
		}
		if i, ok := index[s.RunID]; ok {
			runs[i].Sources = append(runs[i].Sources, s)
		}
	}
	if err := srcRows.Err(); err != nil {
		_ = srcRows.Close()
		return fmt.Errorf("store: run sources: %w", err)
	}
	_ = srcRows.Close()

	snapRows, err := q.QueryContext(ctx, `SELECT id, run_id FROM snapshots WHERE run_id IN (`+in+`) ORDER BY id`, args...)
	if err != nil {
		return fmt.Errorf("store: run snapshots: %w", err)
	}
	defer snapRows.Close()
	for snapRows.Next() {
		var id, runID int64
		if err := snapRows.Scan(&id, &runID); err != nil {
			return fmt.Errorf("store: run snapshots: %w", err)
		}
		if i, ok := index[runID]; ok {
			runs[i].SnapshotIDs = append(runs[i].SnapshotIDs, id)
		}
	}
	return snapRows.Err()
}
