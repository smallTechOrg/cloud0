package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

// WindowJSON is gcpsource.Window on the wire (RFC3339 UTC).
type WindowJSON struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// ProvenanceJSON is gcpsource.Provenance on the wire and in the JSON columns
// (spec/api.md → Phase 2 → Cell provenance).
type ProvenanceJSON struct {
	Source      string     `json:"source"`
	Query       string     `json:"query"`
	Aggregation string     `json:"aggregation"`
	Window      WindowJSON `json:"window"`
	FetchedAt   time.Time  `json:"fetched_at"`
}

// CellJSON is gcpsource.Cell on the wire: value XOR gap (the other is null),
// hint only meaningful with a gap, provenance always present.
type CellJSON struct {
	Value      *float64       `json:"value"`
	Unit       string         `json:"unit"`
	Gap        *string        `json:"gap"`
	Hint       *string        `json:"hint"`
	Provenance ProvenanceJSON `json:"provenance"`
}

// ProvenanceToJSON converts a source provenance to its wire form (UTC).
func ProvenanceToJSON(p gcpsource.Provenance) ProvenanceJSON {
	return ProvenanceJSON{
		Source: p.Source, Query: p.Query, Aggregation: p.Aggregation,
		Window:    WindowJSON{Start: p.Window.Start.UTC(), End: p.Window.End.UTC()},
		FetchedAt: p.FetchedAt.UTC(),
	}
}

// ProvenanceFromJSON is the inverse of ProvenanceToJSON.
func ProvenanceFromJSON(p ProvenanceJSON) gcpsource.Provenance {
	return gcpsource.Provenance{
		Source: p.Source, Query: p.Query, Aggregation: p.Aggregation,
		Window:    gcpsource.Window{Start: p.Window.Start.UTC(), End: p.Window.End.UTC()},
		FetchedAt: p.FetchedAt.UTC(),
	}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// CellToJSON converts a cell to its wire form.
func CellToJSON(c gcpsource.Cell) CellJSON {
	return CellJSON{Value: c.Value, Unit: c.Unit, Gap: strPtr(c.Gap), Hint: strPtr(c.Hint), Provenance: ProvenanceToJSON(c.Provenance)}
}

// CellFromJSON is the inverse of CellToJSON.
func CellFromJSON(c CellJSON) gcpsource.Cell {
	out := gcpsource.Cell{Value: c.Value, Unit: c.Unit, Provenance: ProvenanceFromJSON(c.Provenance)}
	if c.Gap != nil {
		out.Gap = *c.Gap
	}
	if c.Hint != nil {
		out.Hint = *c.Hint
	}
	return out
}

// MetricSnapshot is one row of `metric_snapshots`: one fetch of the Metrics
// block for one ISO week covering all projects.
type MetricSnapshot struct {
	ID            int64          `json:"id"`
	Week          isoweek.Week   `json:"-"`
	WeekID        string         `json:"week"`
	WeekStart     string         `json:"week_start"`
	WeekEnd       string         `json:"week_end"`
	FetchedAt     time.Time      `json:"fetched_at"`
	StatusAtFetch isoweek.Status `json:"status_at_fetch"`
	RunID         int64          `json:"run_id"`
	RowCount      int            `json:"row_count"`
}

// NewMetricSnapshot is the input to InsertMetricSnapshot.
type NewMetricSnapshot struct {
	Week          isoweek.Week
	FetchedAt     time.Time
	StatusAtFetch isoweek.Status
	RunID         int64
	Rows          []gcpsource.MetricRow
}

// InsertMetricSnapshot writes the header and rows (several statements: call it
// inside Store.WithTx with the run writes).
func InsertMetricSnapshot(ctx context.Context, q Querier, ns NewMetricSnapshot) (MetricSnapshot, error) {
	switch {
	case !ns.Week.Valid():
		return MetricSnapshot{}, fmt.Errorf("%w: week %v does not exist", ErrInvalidInput, ns.Week)
	case !validStatus(ns.StatusAtFetch):
		return MetricSnapshot{}, fmt.Errorf("%w: status_at_fetch %q", ErrInvalidInput, ns.StatusAtFetch)
	case ns.RunID <= 0:
		return MetricSnapshot{}, fmt.Errorf("%w: run_id required", ErrInvalidInput)
	case ns.FetchedAt.IsZero():
		return MetricSnapshot{}, fmt.Errorf("%w: fetched_at required", ErrInvalidInput)
	}
	snap := MetricSnapshot{
		Week: ns.Week, WeekID: ns.Week.String(),
		WeekStart: FormatDate(ns.Week.Start()), WeekEnd: FormatDate(ns.Week.Sunday()),
		FetchedAt: ns.FetchedAt.UTC().Truncate(time.Second), StatusAtFetch: ns.StatusAtFetch,
		RunID: ns.RunID, RowCount: len(ns.Rows),
	}
	res, err := q.ExecContext(ctx, `INSERT INTO metric_snapshots (iso_year, iso_week, week_start, week_end, fetched_at, status_at_fetch, run_id, row_count, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, snap.Week.Year, snap.Week.Week, snap.WeekStart, snap.WeekEnd, FormatTime(snap.FetchedAt), string(snap.StatusAtFetch), snap.RunID, snap.RowCount, FormatTime(snap.FetchedAt))
	if err != nil {
		return MetricSnapshot{}, fmt.Errorf("store: insert metric snapshot %s: %w", snap.WeekID, err)
	}
	if snap.ID, err = LastInsertID(res); err != nil {
		return MetricSnapshot{}, err
	}
	for _, r := range ns.Rows {
		cells := make(map[string]CellJSON, len(r.Cells))
		for k, c := range r.Cells {
			cells[k] = CellToJSON(c)
		}
		b, err := json.Marshal(cells)
		if err != nil {
			return MetricSnapshot{}, fmt.Errorf("store: marshal metric cells: %w", err)
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO metric_rows (snapshot_id, grp, project_id, resource, cells_json) VALUES (?, ?, ?, ?, ?)`,
			snap.ID, r.Group, r.Project, r.Resource, string(b)); err != nil {
			return MetricSnapshot{}, fmt.Errorf("store: insert metric row snapshot %d: %w", snap.ID, err)
		}
	}
	return snap, nil
}

const metricSnapshotColumns = `id, iso_year, iso_week, week_start, week_end, fetched_at, status_at_fetch, run_id, row_count`

func scanMetricSnapshot(sc scanner) (MetricSnapshot, error) {
	var s MetricSnapshot
	var fetched, status string
	if err := sc.Scan(&s.ID, &s.Week.Year, &s.Week.Week, &s.WeekStart, &s.WeekEnd, &fetched, &status, &s.RunID, &s.RowCount); err != nil {
		return MetricSnapshot{}, err
	}
	t, err := ParseTime(fetched)
	if err != nil {
		return MetricSnapshot{}, fmt.Errorf("store: metric snapshot %d fetched_at %q: %w", s.ID, fetched, err)
	}
	s.WeekID, s.FetchedAt, s.StatusAtFetch = s.Week.String(), t, isoweek.Status(status)
	return s, nil
}

// LatestMetricSnapshot returns the newest snapshot of a week with its rows in
// insertion order; ErrNotFound when the week was never fetched.
func LatestMetricSnapshot(ctx context.Context, q Querier, week isoweek.Week) (MetricSnapshot, []gcpsource.MetricRow, error) {
	snap, err := scanMetricSnapshot(q.QueryRowContext(ctx, `SELECT `+metricSnapshotColumns+` FROM metric_snapshots WHERE iso_year = ? AND iso_week = ? ORDER BY id DESC LIMIT 1`, week.Year, week.Week))
	if errors.Is(err, sql.ErrNoRows) {
		return MetricSnapshot{}, nil, fmt.Errorf("%w: metric snapshot for %s", ErrNotFound, week)
	}
	if err != nil {
		return MetricSnapshot{}, nil, fmt.Errorf("store: latest metric snapshot %s: %w", week, err)
	}
	rows, err := metricRows(ctx, q, snap.ID)
	return snap, rows, err
}

// GetMetricSnapshot returns one snapshot by id with its rows; ErrNotFound for an unknown id.
func GetMetricSnapshot(ctx context.Context, q Querier, id int64) (MetricSnapshot, []gcpsource.MetricRow, error) {
	snap, err := scanMetricSnapshot(q.QueryRowContext(ctx, `SELECT `+metricSnapshotColumns+` FROM metric_snapshots WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MetricSnapshot{}, nil, fmt.Errorf("%w: metric snapshot %d", ErrNotFound, id)
	}
	if err != nil {
		return MetricSnapshot{}, nil, fmt.Errorf("store: get metric snapshot %d: %w", id, err)
	}
	rows, err := metricRows(ctx, q, snap.ID)
	return snap, rows, err
}

func metricRows(ctx context.Context, q Querier, snapshotID int64) ([]gcpsource.MetricRow, error) {
	rs, err := q.QueryContext(ctx, `SELECT grp, project_id, resource, cells_json FROM metric_rows WHERE snapshot_id = ? ORDER BY id`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("store: metric rows %d: %w", snapshotID, err)
	}
	defer rs.Close()
	out := []gcpsource.MetricRow{}
	for rs.Next() {
		var r gcpsource.MetricRow
		var raw string
		if err := rs.Scan(&r.Group, &r.Project, &r.Resource, &raw); err != nil {
			return nil, fmt.Errorf("store: metric rows %d: %w", snapshotID, err)
		}
		var cells map[string]CellJSON
		if err := json.Unmarshal([]byte(raw), &cells); err != nil {
			return nil, fmt.Errorf("store: metric row cells snapshot %d: %w", snapshotID, err)
		}
		r.Cells = make(map[string]gcpsource.Cell, len(cells))
		for k, c := range cells {
			r.Cells[k] = CellFromJSON(c)
		}
		out = append(out, r)
	}
	return out, rs.Err()
}
