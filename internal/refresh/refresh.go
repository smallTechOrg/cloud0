// Package refresh orchestrates one "Refresh" (spec/capabilities/refresh-snapshot.md):
// it records a run, asks the billing source for the window (one ISO week, or
// CLOUD0_FIRST_WEEK → the current week), then — in a single transaction —
// writes the run_sources provenance row, one immutable snapshot per week in
// the window (empty weeks included) and the run's final status. At most one
// refresh runs per process; a concurrent request gets ErrBusy (409 upstream).
//
// Every run emits the refresh.start / source.done / refresh.done log lines
// from spec/architecture.md → Logging. SQL is never logged (only the job id).
package refresh

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/smallTechOrg/cloud0/internal/billing"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// Mode selects the window: one week or every week first→current.
type Mode string

const (
	ModeWeek Mode = "week"
	ModeAll  Mode = "all"
)

var (
	// ErrBusy means another refresh holds the lock (spec/api.md → 409 refresh_in_progress).
	ErrBusy = errors.New("refresh: another refresh is in progress")
	// ErrInvalidMode means mode is neither "week" nor "all".
	ErrInvalidMode = errors.New("refresh: invalid mode")
	// ErrInvalidWeek means the week does not exist in the ISO calendar.
	ErrInvalidWeek = errors.New("refresh: invalid week")
	// ErrWeekOutOfRange means the week is before FirstWeek or after the current week.
	ErrWeekOutOfRange = errors.New("refresh: week out of range")
	// ErrNoSource is recorded on the run when the billing client could not be
	// built at startup and no more specific error was kept.
	ErrNoSource = errors.New("refresh: billing source unavailable — see the bigquery.dryrun line in the server log")
)

// Source is the slice of *billing.Source the runner uses; tests substitute a fake.
type Source interface {
	Fetch(ctx context.Context, window billing.Window) (billing.Result, error)
}

// Runner executes refreshes against one store with one billing source.
// Construct it once in main and share it; the zero value of mu is ready.
type Runner struct {
	Store *store.Store
	// Source is nil when the billing client could not be constructed; every
	// run then records a failed source classified from SourceErr.
	Source    Source
	SourceErr error
	Logger    *slog.Logger

	FirstWeek  isoweek.Week  // CLOUD0_FIRST_WEEK
	Settle     time.Duration // CLOUD0_SETTLE_HOURS
	MarginDays int           // CLOUD0_PARTITION_MARGIN_DAYS

	// Now supplies the clock; nil means time.Now. Tests inject it.
	Now func() time.Time

	mu sync.Mutex
}

// Result is what POST /api/v1/refresh returns: the finished run (sources carry
// sql/params; the API strips them) and the snapshots it created.
type Result struct {
	Run       store.Run
	Snapshots []store.SnapshotSummary
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Runner) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// Current is the ISO week containing the runner's clock.
func (r *Runner) Current() isoweek.Week { return isoweek.Current(r.now()) }

// window validates the request and derives the fetch window and run trigger.
func (r *Runner) window(mode Mode, week isoweek.Week, now time.Time) (billing.Window, store.RunTrigger, error) {
	current := isoweek.Current(now)
	switch mode {
	case ModeWeek:
		if !week.Valid() {
			return billing.Window{}, "", fmt.Errorf("%w: %v", ErrInvalidWeek, week)
		}
		if isoweek.Compare(week, r.FirstWeek) < 0 || isoweek.Compare(week, current) > 0 {
			return billing.Window{}, "", fmt.Errorf("%w: %s is outside %s..%s", ErrWeekOutOfRange, week, r.FirstWeek, current)
		}
		return billing.Window{From: week, To: week, MarginDays: r.MarginDays}, store.TriggerRefreshWeek, nil
	case ModeAll:
		if !r.FirstWeek.Valid() || isoweek.Compare(r.FirstWeek, current) > 0 {
			return billing.Window{}, "", fmt.Errorf("%w: CLOUD0_FIRST_WEEK %s is after the current week %s", ErrWeekOutOfRange, r.FirstWeek, current)
		}
		return billing.Window{From: r.FirstWeek, To: current, MarginDays: r.MarginDays}, store.TriggerRefreshAll, nil
	}
	return billing.Window{}, "", fmt.Errorf("%w: %q", ErrInvalidMode, mode)
}

// Run performs one refresh. Validation errors (ErrInvalidMode, ErrInvalidWeek,
// ErrWeekOutOfRange) and ErrBusy are returned before any row is written. A
// source failure is NOT an error: the run is recorded as failed with the
// classified error on its source and returned normally. A non-nil error after
// the run row exists means the store itself failed.
func (r *Runner) Run(ctx context.Context, mode Mode, week isoweek.Week) (Result, error) {
	startedAt := r.now()
	window, trigger, err := r.window(mode, week, startedAt)
	if err != nil {
		return Result{}, err
	}
	if !r.mu.TryLock() {
		return Result{}, ErrBusy
	}
	defer r.mu.Unlock()

	// The run must complete and be recorded even if the HTTP client goes away;
	// the billing source enforces its own 120 s deadline.
	ctx = context.WithoutCancel(ctx)

	run, err := store.CreateRun(ctx, r.Store.DB, store.NewRun{Trigger: trigger, From: window.From, To: window.To, StartedAt: startedAt})
	if err != nil {
		return Result{}, fmt.Errorf("refresh: create run: %w", err)
	}
	log := r.logger().With(slog.Int64("run_id", run.ID))
	log.Info("refresh.start", slog.String("mode", string(mode)), slog.String("from", window.From.String()), slog.String("to", window.To.String()))

	res, fetchErr := r.fetch(ctx, window)
	finishedAt := r.now()
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}

	src, snaps, recordErr := r.record(ctx, run, window, res, fetchErr, startedAt, finishedAt)
	if recordErr != nil {
		// The transaction rolled back: mark the run failed with the store error
		// so it never stays "running"; only if that fails too is it a 500.
		msg := recordErr.Error()
		if ferr := store.FinishRun(ctx, r.Store.DB, run.ID, store.RunFailed, r.now(), &msg); ferr != nil {
			return Result{}, fmt.Errorf("refresh: record run %d: %w (finishing it also failed: %v)", run.ID, recordErr, ferr)
		}
		log.Error("refresh.done", slog.String("status", string(store.RunFailed)), slog.Int("snapshots", 0),
			slog.Int64("duration_ms", r.now().Sub(startedAt).Milliseconds()), slog.String("error", msg))
		return r.result(ctx, run.ID, nil)
	}

	logSource(log, src)
	status := store.RunOK
	if fetchErr != nil {
		status = store.RunFailed
	}
	log.Info("refresh.done", slog.String("status", string(status)), slog.Int("snapshots", len(snaps)),
		slog.Int64("duration_ms", finishedAt.Sub(startedAt).Milliseconds()))
	return r.result(ctx, run.ID, snaps)
}

// fetch calls the source, or fails immediately when no client exists.
func (r *Runner) fetch(ctx context.Context, window billing.Window) (billing.Result, error) {
	if r.Source == nil {
		if r.SourceErr != nil {
			return billing.Result{}, r.SourceErr
		}
		return billing.Result{}, ErrNoSource
	}
	return r.Source.Fetch(ctx, window)
}

// record writes, in one transaction, the run_sources row, one snapshot per
// week of the window (only when the fetch succeeded) and the run's final status.
func (r *Runner) record(ctx context.Context, run store.Run, window billing.Window, res billing.Result, fetchErr error, startedAt, finishedAt time.Time) (store.RunSource, []store.Snapshot, error) {
	ns := store.NewRunSource{RunID: run.ID, Source: billing.SourceName, StartedAt: startedAt, FinishedAt: finishedAt, SQLText: res.Query.SQL}
	if res.Query.SQL != "" {
		ns.Params = res.Query.Params.Map()
	}
	if res.Stats.JobID != "" {
		ns.JobID = ptr(res.Stats.JobID)
	}
	if fetchErr != nil {
		f := billing.Classify(fetchErr)
		ns.Status = store.SourceFailed
		ns.ErrorCode, ns.ErrorMessage, ns.FixHint = ptr(string(f.Code)), ptr(f.Message), ptr(f.Hint)
	} else {
		ns.Status = store.SourceOK
		processed, billed, cache, rows := res.Stats.BytesProcessed, res.Stats.BytesBilled, res.Stats.CacheHit, int64(len(res.Rows))
		ns.BytesProcessed, ns.BytesBilled, ns.CacheHit, ns.RowsReturned = &processed, &billed, &cache, &rows
	}

	var src store.RunSource
	var snaps []store.Snapshot
	err := r.Store.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if src, err = store.AddSource(ctx, tx, ns); err != nil {
			return err
		}
		if fetchErr == nil {
			weeks, err := window.Weeks()
			if err != nil {
				return err
			}
			byWeek := r.groupRows(window, res.Rows)
			for _, w := range weeks {
				snap, err := store.InsertSnapshot(ctx, tx, store.NewSnapshot{
					Week: w, FetchedAt: startedAt, StatusAtFetch: isoweek.StatusAt(w, startedAt, r.Settle),
					RunID: run.ID, RunSourceID: src.ID, Rows: byWeek[w],
				})
				if err != nil {
					return err
				}
				snaps = append(snaps, snap)
			}
		}
		status := store.RunOK
		if fetchErr != nil {
			status = store.RunFailed
		}
		return store.FinishRun(ctx, tx, run.ID, status, finishedAt, nil)
	})
	if err != nil {
		return store.RunSource{}, nil, err
	}
	return src, snaps, nil
}

// groupRows buckets the fetched rows by ISO week. Rows outside the window
// cannot come back from the usage_start_time filter; if any do they are
// dropped with a warning rather than attributed to the wrong snapshot.
func (r *Runner) groupRows(window billing.Window, rows []billing.Row) map[isoweek.Week][]store.SnapshotRow {
	out := make(map[isoweek.Week][]store.SnapshotRow)
	dropped := 0
	for _, b := range rows {
		w := b.Week()
		if isoweek.Compare(w, window.From) < 0 || isoweek.Compare(w, window.To) > 0 {
			dropped++
			continue
		}
		out[w] = append(out[w], store.SnapshotRow{
			ProjectID: b.ProjectID, ServiceID: b.ServiceID, Service: b.Service, SKUID: b.SKUID, SKU: b.SKU,
			ResourceName: b.ResourceName, ResourceGlobalName: b.ResourceGlobalName,
			Gross: b.Gross, Credits: b.Credits, Net: b.Net, LineItems: b.LineItems,
		})
	}
	if dropped > 0 {
		r.logger().Warn("refresh.rows_outside_window", slog.Int("dropped", dropped), slog.String("from", window.From.String()), slog.String("to", window.To.String()))
	}
	return out
}

// result reloads the finished run (with its sources and snapshot ids) and
// pairs it with the snapshot summaries.
func (r *Runner) result(ctx context.Context, runID int64, snaps []store.Snapshot) (Result, error) {
	run, err := store.GetRun(ctx, r.Store.DB, runID)
	if err != nil {
		return Result{}, fmt.Errorf("refresh: reload run %d: %w", runID, err)
	}
	out := Result{Run: run, Snapshots: make([]store.SnapshotSummary, 0, len(snaps))}
	for _, s := range snaps {
		out.Snapshots = append(out.Snapshots, s.Summary())
	}
	return out, nil
}

// logSource emits source.done with the fields from spec/architecture.md → Logging.
func logSource(log *slog.Logger, s store.RunSource) {
	attrs := []any{
		slog.String("source", s.Source),
		slog.String("status", string(s.Status)),
		slog.Any("job_id", deref(s.JobID)),
		slog.Any("bytes_processed", derefInt(s.BytesProcessed)),
		slog.Any("bytes_billed", derefInt(s.BytesBilled)),
		slog.Any("cache_hit", derefBool(s.CacheHit)),
		slog.Any("rows", derefInt(s.RowsReturned)),
		slog.Int64("duration_ms", s.DurationMs),
		slog.Any("error_code", deref(s.ErrorCode)),
		slog.Any("fix_hint", deref(s.FixHint)),
	}
	if s.Status == store.SourceFailed {
		log.Error("source.done", attrs...)
		return
	}
	log.Info("source.done", attrs...)
}

func ptr(s string) *string { return &s }

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func derefInt(n *int64) any {
	if n == nil {
		return nil
	}
	return *n
}

func derefBool(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}
