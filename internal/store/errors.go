package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

// ErrorBatch identifies one fetch of the Errors table: the rows of a week
// written by one run. The latest batch of a week is the one with the highest run_id.
type ErrorBatch struct {
	RunID         int64          `json:"run_id"`
	Week          isoweek.Week   `json:"-"`
	WeekID        string         `json:"week"`
	FetchedAt     time.Time      `json:"fetched_at"`
	StatusAtFetch isoweek.Status `json:"status_at_fetch"`
	RowCount      int            `json:"row_count"`
}

// NewErrorBatch is the input to InsertErrorRows.
type NewErrorBatch struct {
	Week          isoweek.Week
	FetchedAt     time.Time
	StatusAtFetch isoweek.Status
	RunID         int64
	Rows          []gcpsource.ErrorRow
}

// InsertErrorRows stores a batch of error rows (gap rows included) for a week.
// Inserting twice for the same run+week appends; callers insert once per run.
func InsertErrorRows(ctx context.Context, q Querier, nb NewErrorBatch) (ErrorBatch, error) {
	switch {
	case !nb.Week.Valid():
		return ErrorBatch{}, fmt.Errorf("%w: week %v does not exist", ErrInvalidInput, nb.Week)
	case !validStatus(nb.StatusAtFetch):
		return ErrorBatch{}, fmt.Errorf("%w: status_at_fetch %q", ErrInvalidInput, nb.StatusAtFetch)
	case nb.RunID <= 0:
		return ErrorBatch{}, fmt.Errorf("%w: run_id required", ErrInvalidInput)
	case nb.FetchedAt.IsZero():
		return ErrorBatch{}, fmt.Errorf("%w: fetched_at required", ErrInvalidInput)
	}
	fetched := nb.FetchedAt.UTC().Truncate(time.Second)
	for _, r := range nb.Rows {
		pj, err := json.Marshal(ProvenanceToJSON(r.Provenance))
		if err != nil {
			return ErrorBatch{}, fmt.Errorf("store: marshal error provenance: %w", err)
		}
		var byCode any
		if len(r.ByCode) > 0 {
			bj, err := json.Marshal(r.ByCode)
			if err != nil {
				return ErrorBatch{}, fmt.Errorf("store: marshal by_code: %w", err)
			}
			byCode = string(bj)
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO error_rows (run_id, iso_year, iso_week, fetched_at, status_at_fetch, source, project_id, service, c2xx, c3xx, c4xx, c5xx, err_groups, occurrences, gap_reason, fix_hint, provenance_json, by_code_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nb.RunID, nb.Week.Year, nb.Week.Week, FormatTime(fetched), string(nb.StatusAtFetch), r.Source, r.Project, r.Service,
			nullableInt(r.C2xx), nullableInt(r.C3xx), nullableInt(r.C4xx), nullableInt(r.C5xx), nullableInt(r.Groups), nullableInt(r.Occurrences),
			r.Gap, r.Hint, string(pj), byCode); err != nil {
			return ErrorBatch{}, fmt.Errorf("store: insert error row %s: %w", nb.Week, err)
		}
	}
	return ErrorBatch{RunID: nb.RunID, Week: nb.Week, WeekID: nb.Week.String(), FetchedAt: fetched, StatusAtFetch: nb.StatusAtFetch, RowCount: len(nb.Rows)}, nil
}

// LatestErrorRows returns the newest batch of a week (highest run_id) with its
// rows in insertion order; ErrNotFound when the week has no error rows.
func LatestErrorRows(ctx context.Context, q Querier, week isoweek.Week) (ErrorBatch, []gcpsource.ErrorRow, error) {
	var maxRun sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT MAX(run_id) FROM error_rows WHERE iso_year = ? AND iso_week = ?`, week.Year, week.Week).Scan(&maxRun); err != nil {
		return ErrorBatch{}, nil, fmt.Errorf("store: latest error rows %s: %w", week, err)
	}
	if !maxRun.Valid {
		return ErrorBatch{}, nil, fmt.Errorf("%w: error rows for %s", ErrNotFound, week)
	}
	return ErrorRowsForRun(ctx, q, maxRun.Int64, week)
}

// ErrorRowsForRun returns the rows one run wrote for a week; ErrNotFound if none.
func ErrorRowsForRun(ctx context.Context, q Querier, runID int64, week isoweek.Week) (ErrorBatch, []gcpsource.ErrorRow, error) {
	rs, err := q.QueryContext(ctx, `SELECT fetched_at, status_at_fetch, source, project_id, service, c2xx, c3xx, c4xx, c5xx, err_groups, occurrences, gap_reason, fix_hint, provenance_json, by_code_json
FROM error_rows WHERE run_id = ? AND iso_year = ? AND iso_week = ? ORDER BY id`, runID, week.Year, week.Week)
	if err != nil {
		return ErrorBatch{}, nil, fmt.Errorf("store: error rows run %d: %w", runID, err)
	}
	defer rs.Close()
	batch := ErrorBatch{RunID: runID, Week: week, WeekID: week.String()}
	out := []gcpsource.ErrorRow{}
	for rs.Next() {
		var r gcpsource.ErrorRow
		var fetched, status, pj string
		var bcj sql.NullString
		var c2, c3, c4, c5, g, o sql.NullInt64
		if err := rs.Scan(&fetched, &status, &r.Source, &r.Project, &r.Service, &c2, &c3, &c4, &c5, &g, &o, &r.Gap, &r.Hint, &pj, &bcj); err != nil {
			return ErrorBatch{}, nil, fmt.Errorf("store: error rows run %d: %w", runID, err)
		}
		t, err := ParseTime(fetched)
		if err != nil {
			return ErrorBatch{}, nil, fmt.Errorf("store: error row fetched_at %q: %w", fetched, err)
		}
		batch.FetchedAt, batch.StatusAtFetch = t, isoweek.Status(status)
		r.C2xx, r.C3xx, r.C4xx, r.C5xx, r.Groups, r.Occurrences = fromNullInt(c2), fromNullInt(c3), fromNullInt(c4), fromNullInt(c5), fromNullInt(g), fromNullInt(o)
		if bcj.Valid && bcj.String != "" {
			if err := json.Unmarshal([]byte(bcj.String), &r.ByCode); err != nil {
				return ErrorBatch{}, nil, fmt.Errorf("store: error row by_code: %w", err)
			}
		}
		var p ProvenanceJSON
		if err := json.Unmarshal([]byte(pj), &p); err != nil {
			return ErrorBatch{}, nil, fmt.Errorf("store: error row provenance: %w", err)
		}
		r.Provenance = ProvenanceFromJSON(p)
		out = append(out, r)
	}
	if err := rs.Err(); err != nil {
		return ErrorBatch{}, nil, err
	}
	if len(out) == 0 {
		return ErrorBatch{}, nil, fmt.Errorf("%w: error rows for run %d week %s", ErrNotFound, runID, week)
	}
	batch.RowCount = len(out)
	return batch, out, nil
}

// ErrorRowJSON is gcpsource.ErrorRow on the wire (spec/api.md → GET /errors/table).
type ErrorRowJSON struct {
	Source      string           `json:"source"`
	Project     string           `json:"project"`
	Service     string           `json:"service"`
	C2xx        *int64           `json:"c2xx"`
	C3xx        *int64           `json:"c3xx"`
	C4xx        *int64           `json:"c4xx"`
	C5xx        *int64           `json:"c5xx"`
	Groups      *int64           `json:"groups"`
	Occurrences *int64           `json:"occurrences"`
	ByCode      map[string]int64 `json:"by_code"`
	LogsURL     *string          `json:"logs_url"`
	Gap         *string          `json:"gap"`
	Hint        *string          `json:"hint"`
	Provenance  ProvenanceJSON   `json:"provenance"`
}

// ErrorRowToJSON converts an error row to its wire form.
func ErrorRowToJSON(r gcpsource.ErrorRow) ErrorRowJSON {
	return ErrorRowJSON{Source: r.Source, Project: r.Project, Service: r.Service,
		C2xx: r.C2xx, C3xx: r.C3xx, C4xx: r.C4xx, C5xx: r.C5xx, Groups: r.Groups, Occurrences: r.Occurrences, ByCode: r.ByCode,
		Gap: strPtr(r.Gap), Hint: strPtr(r.Hint), Provenance: ProvenanceToJSON(r.Provenance)}
}
