package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// DailyRow is one per-day billing row of a snapshot (migration 0006): the
// snapshot_rows grain plus the UTC usage date, without the columns the daily
// views never read.
type DailyRow struct {
	SnapshotID         int64
	UsageDate          string // YYYY-MM-DD (UTC date of usage_start_time)
	ProjectID          string
	Service            string
	SKU                string
	ResourceName       *string
	ResourceGlobalName *string
	LabelsJSON         *string
	Gross              float64
	Credits            float64
	Net                float64 // always gross + credits once stored
}

const dailyColumns = "snapshot_id, usage_date, project_id, service, sku, resource_name, resource_global_name, labels_json, gross, credits, net"

// insertDailyRows writes a snapshot's daily rows in batches.
func insertDailyRows(ctx context.Context, q Querier, snapshotID int64, rows []DailyRow) error {
	for start := 0; start < len(rows); start += InsertBatchSize {
		end := min(start+InsertBatchSize, len(rows))
		var sb strings.Builder
		sb.WriteString("INSERT INTO snapshot_daily_rows (" + dailyColumns + ") VALUES ")
		args := make([]any, 0, (end-start)*11)
		for i, r := range rows[start:end] {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
			args = append(args, snapshotID, r.UsageDate, r.ProjectID, r.Service, r.SKU,
				nullableString(r.ResourceName), nullableString(r.ResourceGlobalName), nullableString(r.LabelsJSON), r.Gross, r.Credits, r.Gross+r.Credits)
		}
		if _, err := q.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("store: insert daily rows %d..%d of snapshot %d: %w", start, end, snapshotID, err)
		}
	}
	return nil
}

// DailyRowsForSnapshots returns the daily rows of the given snapshots with
// usage_date in [fromDate, toDate] (inclusive, YYYY-MM-DD), filtered to scope.
// Rows are ordered by date, then snapshot.
func DailyRowsForSnapshots(ctx context.Context, q Querier, ids []int64, fromDate, toDate, scope string) ([]DailyRow, error) {
	out := []DailyRow{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids)+3)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, fromDate, toDate)
	where := "snapshot_id IN (" + placeholders(len(ids)) + ") AND usage_date >= ? AND usage_date <= ?"
	if scope != "" && scope != ScopeAll {
		where += " AND project_id = ?"
		args = append(args, scope)
	}
	rows, err := q.QueryContext(ctx, "SELECT "+dailyColumns+" FROM snapshot_daily_rows WHERE "+where+" ORDER BY usage_date, snapshot_id, id", args...)
	if err != nil {
		return nil, fmt.Errorf("store: daily rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r DailyRow
		var rn, rgn, lj sql.NullString
		if err := rows.Scan(&r.SnapshotID, &r.UsageDate, &r.ProjectID, &r.Service, &r.SKU, &rn, &rgn, &lj, &r.Gross, &r.Credits, &r.Net); err != nil {
			return nil, fmt.Errorf("store: scan daily row: %w", err)
		}
		r.ResourceName, r.ResourceGlobalName, r.LabelsJSON = fromNullString(rn), fromNullString(rgn), fromNullString(lj)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SnapshotsWithDaily reports which of the given snapshots have any daily rows
// (snapshots fetched before migration 0006 have none).
func SnapshotsWithDaily(ctx context.Context, q Querier, ids []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT snapshot_id FROM snapshot_daily_rows WHERE snapshot_id IN ("+placeholders(len(ids))+")", args...)
	if err != nil {
		return nil, fmt.Errorf("store: snapshots with daily rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
