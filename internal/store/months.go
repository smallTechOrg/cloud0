package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/monthcost"
)

// MonthSnapshot is one row of `month_snapshots`: one fetch of one UTC calendar
// month covering all projects.
type MonthSnapshot struct {
	ID            int64          `json:"id"`
	Month         string         `json:"month"` // YYYY-MM
	MonthStart    string         `json:"month_start"`
	MonthEnd      string         `json:"month_end"` // last day of the month, inclusive
	FetchedAt     time.Time      `json:"fetched_at"`
	StatusAtFetch isoweek.Status `json:"status_at_fetch"`
	RunID         int64          `json:"run_id"`
	RunSourceID   *int64         `json:"-"`
	RowCount      int            `json:"row_count"`
	GrossTotal    float64        `json:"gross_total"`
	CreditsTotal  float64        `json:"credits_total"`
	NetTotal      float64        `json:"net_total"`
}

// NewMonthSnapshot is the input to InsertMonthSnapshot. Rows whose Month is
// not Month are rejected (a snapshot holds exactly one month).
type NewMonthSnapshot struct {
	Month         monthcost.Month
	FetchedAt     time.Time
	StatusAtFetch isoweek.Status
	RunID         int64
	RunSourceID   *int64
	Rows          []monthcost.Row
}

// InsertMonthSnapshot writes the header and rows; call inside Store.WithTx.
// Row Net is recomputed as Gross + Credits.
func InsertMonthSnapshot(ctx context.Context, q Querier, ns NewMonthSnapshot) (MonthSnapshot, error) {
	switch {
	case ns.Month.Year < 1 || ns.Month.Month < 1 || ns.Month.Month > 12:
		return MonthSnapshot{}, fmt.Errorf("%w: month %v", ErrInvalidInput, ns.Month)
	case !validStatus(ns.StatusAtFetch):
		return MonthSnapshot{}, fmt.Errorf("%w: status_at_fetch %q", ErrInvalidInput, ns.StatusAtFetch)
	case ns.RunID <= 0:
		return MonthSnapshot{}, fmt.Errorf("%w: run_id required", ErrInvalidInput)
	case ns.FetchedAt.IsZero():
		return MonthSnapshot{}, fmt.Errorf("%w: fetched_at required", ErrInvalidInput)
	}
	key := ns.Month.String()
	snap := MonthSnapshot{
		Month: key, MonthStart: FormatDate(ns.Month.Start()), MonthEnd: FormatDate(ns.Month.Bounds().End.AddDate(0, 0, -1)),
		FetchedAt: ns.FetchedAt.UTC().Truncate(time.Second), StatusAtFetch: ns.StatusAtFetch,
		RunID: ns.RunID, RunSourceID: ns.RunSourceID, RowCount: len(ns.Rows),
	}
	rows := make([]monthcost.Row, len(ns.Rows))
	for i, r := range ns.Rows {
		if r.Month != key {
			return MonthSnapshot{}, fmt.Errorf("%w: row month %q in snapshot for %s", ErrInvalidInput, r.Month, key)
		}
		r.Net = r.Gross + r.Credits
		rows[i] = r
		snap.GrossTotal += r.Gross
		snap.CreditsTotal += r.Credits
		snap.NetTotal += r.Net
	}
	res, err := q.ExecContext(ctx, `INSERT INTO month_snapshots (month, month_start, month_end, fetched_at, status_at_fetch, run_id, run_source_id, row_count, gross_total, credits_total, net_total, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, snap.Month, snap.MonthStart, snap.MonthEnd, FormatTime(snap.FetchedAt), string(snap.StatusAtFetch),
		snap.RunID, nullableInt(snap.RunSourceID), snap.RowCount, snap.GrossTotal, snap.CreditsTotal, snap.NetTotal, FormatTime(snap.FetchedAt))
	if err != nil {
		return MonthSnapshot{}, fmt.Errorf("store: insert month snapshot %s: %w", key, err)
	}
	if snap.ID, err = LastInsertID(res); err != nil {
		return MonthSnapshot{}, err
	}
	for _, r := range rows {
		if _, err := q.ExecContext(ctx, `INSERT INTO month_rows (snapshot_id, month, invoice_month, project_id, service_id, service, gross, credits, net) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			snap.ID, r.Month, r.InvoiceMonth, r.ProjectID, r.ServiceID, r.Service, r.Gross, r.Credits, r.Net); err != nil {
			return MonthSnapshot{}, fmt.Errorf("store: insert month row snapshot %d: %w", snap.ID, err)
		}
	}
	return snap, nil
}

const monthSnapshotColumns = `id, month, month_start, month_end, fetched_at, status_at_fetch, run_id, run_source_id, row_count, gross_total, credits_total, net_total`

func scanMonthSnapshot(sc scanner) (MonthSnapshot, error) {
	var s MonthSnapshot
	var fetched, status string
	var rs sql.NullInt64
	if err := sc.Scan(&s.ID, &s.Month, &s.MonthStart, &s.MonthEnd, &fetched, &status, &s.RunID, &rs, &s.RowCount, &s.GrossTotal, &s.CreditsTotal, &s.NetTotal); err != nil {
		return MonthSnapshot{}, err
	}
	t, err := ParseTime(fetched)
	if err != nil {
		return MonthSnapshot{}, fmt.Errorf("store: month snapshot %d fetched_at %q: %w", s.ID, fetched, err)
	}
	s.FetchedAt, s.StatusAtFetch, s.RunSourceID = t, isoweek.Status(status), fromNullInt(rs)
	return s, nil
}

// LatestMonthSnapshot returns the newest snapshot of a month with its rows;
// ErrNotFound when the month was never fetched.
func LatestMonthSnapshot(ctx context.Context, q Querier, m monthcost.Month) (MonthSnapshot, []monthcost.Row, error) {
	snap, err := scanMonthSnapshot(q.QueryRowContext(ctx, `SELECT `+monthSnapshotColumns+` FROM month_snapshots WHERE month = ? ORDER BY id DESC LIMIT 1`, m.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return MonthSnapshot{}, nil, fmt.Errorf("%w: month snapshot for %s", ErrNotFound, m)
	}
	if err != nil {
		return MonthSnapshot{}, nil, fmt.Errorf("store: latest month snapshot %s: %w", m, err)
	}
	rows, err := monthRows(ctx, q, snap.ID)
	return snap, rows, err
}

// GetMonthSnapshot returns one snapshot by id with its rows; ErrNotFound for an unknown id.
func GetMonthSnapshot(ctx context.Context, q Querier, id int64) (MonthSnapshot, []monthcost.Row, error) {
	snap, err := scanMonthSnapshot(q.QueryRowContext(ctx, `SELECT `+monthSnapshotColumns+` FROM month_snapshots WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MonthSnapshot{}, nil, fmt.Errorf("%w: month snapshot %d", ErrNotFound, id)
	}
	if err != nil {
		return MonthSnapshot{}, nil, fmt.Errorf("store: get month snapshot %d: %w", id, err)
	}
	rows, err := monthRows(ctx, q, snap.ID)
	return snap, rows, err
}

// MonthData is a month's latest snapshot with its rows.
type MonthData struct {
	Snapshot MonthSnapshot
	Rows     []monthcost.Row
}

// LatestMonthsInRange returns the latest snapshot (with rows) of every month in
// [from, to] that has one, ascending by month. Months never fetched are absent.
func LatestMonthsInRange(ctx context.Context, q Querier, from, to monthcost.Month) ([]MonthData, error) {
	if to.Before(from) {
		return nil, fmt.Errorf("%w: month range %s..%s", ErrInvalidInput, from, to)
	}
	rs, err := q.QueryContext(ctx, `SELECT `+monthSnapshotColumns+` FROM month_snapshots s
WHERE month >= ? AND month <= ? AND id = (SELECT MAX(id) FROM month_snapshots m WHERE m.month = s.month) ORDER BY month`, from.String(), to.String())
	if err != nil {
		return nil, fmt.Errorf("store: month range %s..%s: %w", from, to, err)
	}
	var snaps []MonthSnapshot
	for rs.Next() {
		s, err := scanMonthSnapshot(rs)
		if err != nil {
			rs.Close()
			return nil, err
		}
		snaps = append(snaps, s)
	}
	if err := rs.Err(); err != nil {
		rs.Close()
		return nil, err
	}
	rs.Close()
	out := make([]MonthData, 0, len(snaps))
	for _, s := range snaps {
		rows, err := monthRows(ctx, q, s.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, MonthData{Snapshot: s, Rows: rows})
	}
	return out, nil
}

func monthRows(ctx context.Context, q Querier, snapshotID int64) ([]monthcost.Row, error) {
	rs, err := q.QueryContext(ctx, `SELECT month, invoice_month, project_id, service_id, service, gross, credits, net FROM month_rows WHERE snapshot_id = ? ORDER BY id`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("store: month rows %d: %w", snapshotID, err)
	}
	defer rs.Close()
	out := []monthcost.Row{}
	for rs.Next() {
		var r monthcost.Row
		if err := rs.Scan(&r.Month, &r.InvoiceMonth, &r.ProjectID, &r.ServiceID, &r.Service, &r.Gross, &r.Credits, &r.Net); err != nil {
			return nil, fmt.Errorf("store: month rows %d: %w", snapshotID, err)
		}
		out = append(out, r)
	}
	return out, rs.Err()
}
