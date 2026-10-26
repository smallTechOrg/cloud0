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
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/smallTechOrg/cloud0/internal/billing"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/monthcost"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// Mode selects what is refreshed: one week, one calendar month, or everything.
type Mode string

const (
	ModeWeek  Mode = "week"
	ModeMonth Mode = "month"
	ModeAll   Mode = "all"
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
	// ErrInvalidMonth means the month is not a real calendar month.
	ErrInvalidMonth = errors.New("refresh: invalid month")
	// ErrMonthOutOfRange means the month is after the current month.
	ErrMonthOutOfRange = errors.New("refresh: month out of range")
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

	// Phase 2 sources. Collectors are the week-window sources (run
	// concurrently, at most MaxConcurrency at once); Month is the calendar-month
	// cost source (nil = not run); Projects are the configured GCP projects
	// (used to show "source failed" gaps for a failed collector).
	Collectors []Collector
	Month      MonthSource
	Projects   []string

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
	Run            store.Run
	Snapshots      []store.SnapshotSummary
	MetricSnaps    []store.MetricSnapshot
	MonthSnapshots []store.MonthSnapshot
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

// Request is one refresh request: the mode plus its week or month argument.
type Request struct {
	Mode  Mode
	Week  isoweek.Week    // ModeWeek
	Month monthcost.Month // ModeMonth
}

// Run performs a week or all refresh (see RunRequest).
func (r *Runner) Run(ctx context.Context, mode Mode, week isoweek.Week) (Result, error) {
	return r.RunRequest(ctx, Request{Mode: mode, Week: week})
}

// plan is the validated shape of one refresh.
type plan struct {
	trigger  store.RunTrigger
	from, to isoweek.Week
	billing  *billing.Window // nil for month mode
	months   bool
	mFrom    monthcost.Month
	mTo      monthcost.Month
	metrics  bool         // run the week-window sources
	week     isoweek.Week // their window
}

func (r *Runner) plan(req Request, now time.Time) (plan, error) {
	current := isoweek.Current(now)
	switch req.Mode {
	case ModeWeek, ModeAll:
		w, trigger, err := r.window(req.Mode, req.Week, now)
		if err != nil {
			return plan{}, err
		}
		p := plan{trigger: trigger, from: w.From, to: w.To, billing: &w, metrics: true, week: req.Week}
		if req.Mode == ModeAll {
			p.months = true
			p.mFrom = monthcost.Of(w.From.Start())
			last := current.Sunday()
			if last.After(now) {
				last = now
			}
			p.mTo = monthcost.Of(last)
			p.week = current.Prev()
			if isoweek.Compare(p.week, r.FirstWeek) < 0 {
				p.week = r.FirstWeek
			}
		}
		return p, nil
	case ModeMonth:
		m := req.Month
		if m.Year < 1 || m.Month < 1 || m.Month > 12 {
			return plan{}, fmt.Errorf("%w: %v", ErrInvalidMonth, m)
		}
		if monthcost.Of(now).Before(m) {
			return plan{}, fmt.Errorf("%w: %s is after the current month %s", ErrMonthOutOfRange, m, monthcost.Of(now))
		}
		last := m.Bounds().End.AddDate(0, 0, -1)
		if last.After(now) {
			last = now
		}
		return plan{trigger: store.TriggerRefreshMonth, from: isoweek.Of(m.Start()), to: isoweek.Of(last), months: true, mFrom: m, mTo: m}, nil
	}
	return plan{}, fmt.Errorf("%w: %q", ErrInvalidMode, req.Mode)
}

// RunRequest performs one refresh. Validation errors (ErrInvalidMode,
// ErrInvalidWeek, ErrWeekOutOfRange, ErrInvalidMonth, ErrMonthOutOfRange) and
// ErrBusy are returned before any row is written. A source failure is NOT an
// error: the run is recorded (failed or partial) with the classified error on
// its source and returned normally; a failed source never aborts the others.
// A non-nil error after the run row exists means the store itself failed.
func (r *Runner) RunRequest(ctx context.Context, req Request) (Result, error) {
	startedAt := r.now()
	pl, err := r.plan(req, startedAt)
	if err != nil {
		return Result{}, err
	}
	if !r.mu.TryLock() {
		return Result{}, ErrBusy
	}
	defer r.mu.Unlock()

	// The run must complete and be recorded even if the HTTP client goes away;
	// every source enforces its own deadline.
	ctx = context.WithoutCancel(ctx)

	run, err := store.CreateRun(ctx, r.Store.DB, store.NewRun{Trigger: pl.trigger, From: pl.from, To: pl.to, StartedAt: startedAt})
	if err != nil {
		return Result{}, fmt.Errorf("refresh: create run: %w", err)
	}
	log := r.logger().With(slog.Int64("run_id", run.ID))
	log.Info("refresh.start", slog.String("mode", string(req.Mode)), slog.String("from", pl.from.String()), slog.String("to", pl.to.String()))

	out := outcome{}
	if pl.billing != nil {
		out.billing = true
		out.bres, out.berr = r.fetch(ctx, *pl.billing)
		out.bEnd = r.now()
		if out.bEnd.Before(startedAt) {
			out.bEnd = startedAt
		}
	}
	r.fetchOthers(ctx, pl, &out)
	finishedAt := r.now()
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}

	rec, recordErr := r.record(ctx, run, pl, out, startedAt, finishedAt)
	if recordErr != nil {
		// The transaction rolled back: mark the run failed with the store error
		// so it never stays "running"; only if that fails too is it a 500.
		msg := recordErr.Error()
		if ferr := store.FinishRun(ctx, r.Store.DB, run.ID, store.RunFailed, r.now(), &msg); ferr != nil {
			return Result{}, fmt.Errorf("refresh: record run %d: %w (finishing it also failed: %v)", run.ID, recordErr, ferr)
		}
		log.Error("refresh.done", slog.String("status", string(store.RunFailed)), slog.Int("snapshots", 0),
			slog.Int64("duration_ms", r.now().Sub(startedAt).Milliseconds()), slog.String("error", msg))
		return r.result(ctx, run.ID, record{})
	}

	for _, s := range rec.sources {
		logSource(log, s)
	}
	log.Info("refresh.done", slog.String("status", string(rec.status)), slog.Int("snapshots", len(rec.snaps)),
		slog.Int("metric_snapshots", len(rec.metricSnaps)), slog.Int("month_snapshots", len(rec.monthSnaps)),
		slog.Int64("duration_ms", finishedAt.Sub(startedAt).Milliseconds()))
	return r.result(ctx, run.ID, rec)
}

// fetch calls the billing source, or fails immediately when no client exists.
func (r *Runner) fetch(ctx context.Context, window billing.Window) (billing.Result, error) {
	if r.Source == nil {
		if r.SourceErr != nil {
			return billing.Result{}, r.SourceErr
		}
		return billing.Result{}, ErrNoSource
	}
	return r.Source.Fetch(ctx, window)
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
			LabelsJSON: b.LabelsJSON, SystemLabelsJSON: b.SystemLabelsJSON,
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
func (r *Runner) result(ctx context.Context, runID int64, rec record) (Result, error) {
	run, err := store.GetRun(ctx, r.Store.DB, runID)
	if err != nil {
		return Result{}, fmt.Errorf("refresh: reload run %d: %w", runID, err)
	}
	out := Result{Run: run, Snapshots: make([]store.SnapshotSummary, 0, len(rec.snaps)),
		MetricSnaps: rec.metricSnaps, MonthSnapshots: rec.monthSnaps}
	for _, s := range rec.snaps {
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
