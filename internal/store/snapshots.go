package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

// Querier is the subset of *sql.DB and *sql.Tx the repositories use, so every
// function runs either standalone or inside Store.WithTx (spec/data.md →
// Portability: repositories accept DB/Tx interfaces).
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var (
	_ Querier = (*sql.DB)(nil)
	_ Querier = (*sql.Tx)(nil)
)

// ScopeAll is the scope value meaning "every billed project" (spec/api.md → Scope).
const ScopeAll = "all"

// InsertBatchSize caps the rows per multi-row INSERT (spec/architecture.md → SQL).
const InsertBatchSize = 500

// UnattributedKey is the breakdown key for rows whose resource_name is NULL.
const UnattributedKey = "(unattributed)"

var (
	// ErrNotFound is returned when an id or week has no row.
	ErrNotFound = errors.New("store: not found")
	// ErrInvalidInput is returned for arguments that violate the schema's rules
	// before any SQL runs.
	ErrInvalidInput = errors.New("store: invalid input")
	// ErrInvalidLevel is returned for a breakdown level that is unknown or
	// lacks its required service / sku filter (spec/api.md → invalid_level).
	ErrInvalidLevel = errors.New("store: invalid breakdown level")
)

// Snapshot is one row of `snapshots`: one immutable fetch of one ISO week
// covering all billed projects (spec/data.md → snapshots).
type Snapshot struct {
	ID            int64          `json:"id"`
	Week          isoweek.Week   `json:"-"`
	WeekID        string         `json:"week"`
	WeekStart     string         `json:"week_start"` // Monday, YYYY-MM-DD
	WeekEnd       string         `json:"week_end"`   // Sunday, YYYY-MM-DD
	FetchedAt     time.Time      `json:"fetched_at"`
	StatusAtFetch isoweek.Status `json:"status_at_fetch"`
	RunID         int64          `json:"run_id"`
	RunSourceID   int64          `json:"-"`
	RowCount      int            `json:"row_count"`
	GrossTotal    float64        `json:"gross_total"`
	CreditsTotal  float64        `json:"credits_total"`
	NetTotal      float64        `json:"net_total"`
	CreatedAt     time.Time      `json:"-"`
}

// SnapshotSummary is the `snapshots[]` element of POST /api/v1/refresh.
type SnapshotSummary struct {
	ID            int64          `json:"id"`
	Week          string         `json:"week"`
	StatusAtFetch isoweek.Status `json:"status_at_fetch"`
	RowCount      int            `json:"row_count"`
	NetTotal      float64        `json:"net_total"`
}

// Summary projects the snapshot onto the refresh-response shape.
func (s Snapshot) Summary() SnapshotSummary {
	return SnapshotSummary{ID: s.ID, Week: s.WeekID, StatusAtFetch: s.StatusAtFetch, RowCount: s.RowCount, NetTotal: s.NetTotal}
}

// SnapshotRow is the finest grain kept: one (project, service, SKU, resource)
// aggregate for the snapshot's week (spec/data.md → snapshot_rows).
type SnapshotRow struct {
	ID                 int64
	SnapshotID         int64
	ProjectID          string
	ServiceID          string
	Service            string // service.description exactly as exported
	SKUID              string
	SKU                string
	ResourceName       *string // nil = unattributed
	ResourceGlobalName *string
	// LabelsJSON / SystemLabelsJSON hold the export's labels as a JSON array of
	// {"key","value"} objects (migration 0005); nil for snapshots fetched
	// before labels were captured.
	LabelsJSON       *string
	SystemLabelsJSON *string
	Gross            float64
	Credits          float64 // ≤ 0 normally
	Net              float64 // always gross + credits once stored
	LineItems        int64
}

// NewSnapshot is the input to InsertSnapshot. Row Net values are recomputed
// as Gross + Credits so the drill-down reconciliation invariant always holds.
type NewSnapshot struct {
	Week          isoweek.Week
	FetchedAt     time.Time
	StatusAtFetch isoweek.Status
	RunID         int64
	RunSourceID   int64
	CreatedAt     time.Time // zero → FetchedAt
	Rows          []SnapshotRow
}

func validStatus(s isoweek.Status) bool {
	return s == isoweek.StatusPartial || s == isoweek.StatusSettling || s == isoweek.StatusFinal
}

// InsertSnapshot writes the snapshot header and its rows in batches of
// InsertBatchSize. It issues several statements, so call it inside
// Store.WithTx together with the run / source writes (spec/data.md → Lifecycle).
func InsertSnapshot(ctx context.Context, q Querier, ns NewSnapshot) (Snapshot, error) {
	switch {
	case !ns.Week.Valid():
		return Snapshot{}, fmt.Errorf("%w: week %v does not exist", ErrInvalidInput, ns.Week)
	case !validStatus(ns.StatusAtFetch):
		return Snapshot{}, fmt.Errorf("%w: status_at_fetch %q", ErrInvalidInput, ns.StatusAtFetch)
	case ns.RunID <= 0:
		return Snapshot{}, fmt.Errorf("%w: run_id required", ErrInvalidInput)
	case ns.RunSourceID <= 0:
		return Snapshot{}, fmt.Errorf("%w: run_source_id required", ErrInvalidInput)
	case ns.FetchedAt.IsZero():
		return Snapshot{}, fmt.Errorf("%w: fetched_at required", ErrInvalidInput)
	}
	created := ns.CreatedAt
	if created.IsZero() {
		created = ns.FetchedAt
	}
	snap := Snapshot{
		Week:          ns.Week,
		WeekID:        ns.Week.String(),
		WeekStart:     FormatDate(ns.Week.Start()),
		WeekEnd:       FormatDate(ns.Week.Sunday()),
		FetchedAt:     ns.FetchedAt.UTC().Truncate(time.Second),
		StatusAtFetch: ns.StatusAtFetch,
		RunID:         ns.RunID,
		RunSourceID:   ns.RunSourceID,
		RowCount:      len(ns.Rows),
		CreatedAt:     created.UTC().Truncate(time.Second),
	}
	rows := make([]SnapshotRow, len(ns.Rows))
	for i, r := range ns.Rows {
		r.Net = r.Gross + r.Credits
		rows[i] = r
		snap.GrossTotal += r.Gross
		snap.CreditsTotal += r.Credits
		snap.NetTotal += r.Net
	}

	res, err := q.ExecContext(ctx, `INSERT INTO snapshots (iso_year, iso_week, week_start, week_end, fetched_at, status_at_fetch, run_id, run_source_id, row_count, gross_total, credits_total, net_total, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snap.Week.Year, snap.Week.Week, snap.WeekStart, snap.WeekEnd, FormatTime(snap.FetchedAt), string(snap.StatusAtFetch),
		snap.RunID, snap.RunSourceID, snap.RowCount, snap.GrossTotal, snap.CreditsTotal, snap.NetTotal, FormatTime(snap.CreatedAt))
	if err != nil {
		return Snapshot{}, fmt.Errorf("store: insert snapshot %s: %w", snap.WeekID, err)
	}
	snap.ID, err = LastInsertID(res)
	if err != nil {
		return Snapshot{}, err
	}
	for start := 0; start < len(rows); start += InsertBatchSize {
		end := min(start+InsertBatchSize, len(rows))
		if err := insertRowBatch(ctx, q, snap.ID, rows[start:end]); err != nil {
			return Snapshot{}, fmt.Errorf("store: insert snapshot %d rows %d..%d: %w", snap.ID, start, end, err)
		}
	}
	return snap, nil
}

const rowColumns = "snapshot_id, project_id, service_id, service, sku_id, sku, resource_name, resource_global_name, labels_json, system_labels_json, gross, credits, net, line_items"

func insertRowBatch(ctx context.Context, q Querier, snapshotID int64, rows []SnapshotRow) error {
	var sb strings.Builder
	sb.WriteString("INSERT INTO snapshot_rows (" + rowColumns + ") VALUES ")
	args := make([]any, 0, len(rows)*14)
	for i, r := range rows {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(args, snapshotID, r.ProjectID, r.ServiceID, r.Service, r.SKUID, r.SKU,
			nullableString(r.ResourceName), nullableString(r.ResourceGlobalName), nullableString(r.LabelsJSON), nullableString(r.SystemLabelsJSON), r.Gross, r.Credits, r.Net, r.LineItems)
	}
	_, err := q.ExecContext(ctx, sb.String(), args...)
	return err
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func fromNullString(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

const snapshotColumns = "id, iso_year, iso_week, week_start, week_end, fetched_at, status_at_fetch, run_id, run_source_id, row_count, gross_total, credits_total, net_total, created_at"

type scanner interface{ Scan(dest ...any) error }

func scanSnapshot(sc scanner) (Snapshot, error) {
	var s Snapshot
	var fetched, created, status string
	if err := sc.Scan(&s.ID, &s.Week.Year, &s.Week.Week, &s.WeekStart, &s.WeekEnd, &fetched, &status, &s.RunID, &s.RunSourceID,
		&s.RowCount, &s.GrossTotal, &s.CreditsTotal, &s.NetTotal, &created); err != nil {
		return Snapshot{}, err
	}
	var err error
	if s.FetchedAt, err = ParseTime(fetched); err != nil {
		return Snapshot{}, fmt.Errorf("store: snapshot %d fetched_at %q: %w", s.ID, fetched, err)
	}
	if s.CreatedAt, err = ParseTime(created); err != nil {
		return Snapshot{}, fmt.Errorf("store: snapshot %d created_at %q: %w", s.ID, created, err)
	}
	s.StatusAtFetch = isoweek.Status(status)
	s.WeekID = s.Week.String()
	return s, nil
}

// weekOrdinal makes (year, week) comparable in a portable BETWEEN clause.
func weekOrdinal(w isoweek.Week) int { return w.Year*100 + w.Week }

// LatestPerWeek returns, for every week from first to last inclusive that has
// at least one snapshot, the snapshot with the highest id (spec/data.md:
// "the latest snapshot per week (highest id) is what the table displays").
func LatestPerWeek(ctx context.Context, q Querier, first, last isoweek.Week) (map[isoweek.Week]Snapshot, error) {
	if !first.Valid() || !last.Valid() || isoweek.Compare(first, last) > 0 {
		return nil, fmt.Errorf("%w: week range %v..%v", ErrInvalidInput, first, last)
	}
	rows, err := q.QueryContext(ctx, `SELECT s.`+strings.ReplaceAll(snapshotColumns, ", ", ", s.")+`
FROM snapshots s
JOIN (SELECT iso_year, iso_week, MAX(id) AS max_id FROM snapshots GROUP BY iso_year, iso_week) m ON m.max_id = s.id
WHERE (s.iso_year * 100 + s.iso_week) BETWEEN ? AND ?
ORDER BY s.iso_year, s.iso_week`, weekOrdinal(first), weekOrdinal(last))
	if err != nil {
		return nil, fmt.Errorf("store: latest per week: %w", err)
	}
	defer rows.Close()
	out := make(map[isoweek.Week]Snapshot)
	for rows.Next() {
		s, err := scanSnapshot(rows)
		if err != nil {
			return nil, fmt.Errorf("store: latest per week: %w", err)
		}
		out[s.Week] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: latest per week: %w", err)
	}
	return out, nil
}

// GetSnapshot loads one snapshot header; ErrNotFound for an unknown id.
func GetSnapshot(ctx context.Context, q Querier, id int64) (Snapshot, error) {
	s, err := scanSnapshot(q.QueryRowContext(ctx, `SELECT `+snapshotColumns+` FROM snapshots WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, fmt.Errorf("%w: snapshot %d", ErrNotFound, id)
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("store: get snapshot %d: %w", id, err)
	}
	return s, nil
}

func scopeClause(scope string, args []any) (string, []any) {
	if scope == "" || scope == ScopeAll {
		return "", args
	}
	return " AND project_id = ?", append(args, scope)
}

const selectRowColumns = "id, " + rowColumns

func scanRows(rows *sql.Rows) ([]SnapshotRow, error) {
	out := make([]SnapshotRow, 0, 64)
	for rows.Next() {
		var r SnapshotRow
		var name, global, labels, sysLabels sql.NullString
		if err := rows.Scan(&r.ID, &r.SnapshotID, &r.ProjectID, &r.ServiceID, &r.Service, &r.SKUID, &r.SKU, &name, &global, &labels, &sysLabels, &r.Gross, &r.Credits, &r.Net, &r.LineItems); err != nil {
			return nil, err
		}
		r.ResourceName, r.ResourceGlobalName = fromNullString(name), fromNullString(global)
		r.LabelsJSON, r.SystemLabelsJSON = fromNullString(labels), fromNullString(sysLabels)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SnapshotRows returns every row of one snapshot, filtered to the scope
// (ScopeAll or a project id), in insertion order.
func SnapshotRows(ctx context.Context, q Querier, snapshotID int64, scope string) ([]SnapshotRow, error) {
	where, args := scopeClause(scope, []any{snapshotID})
	rows, err := q.QueryContext(ctx, `SELECT `+selectRowColumns+` FROM snapshot_rows WHERE snapshot_id = ?`+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: snapshot %d rows: %w", snapshotID, err)
	}
	defer rows.Close()
	out, err := scanRows(rows)
	if err != nil {
		return nil, fmt.Errorf("store: snapshot %d rows: %w", snapshotID, err)
	}
	return out, nil
}

// RowsForSnapshots loads the rows of several snapshots in one query (the cost
// table needs every week's latest snapshot), keyed by snapshot id. Ids with no
// rows are absent from the map.
func RowsForSnapshots(ctx context.Context, q Querier, ids []int64, scope string) (map[int64][]SnapshotRow, error) {
	out := make(map[int64][]SnapshotRow, len(ids))
	for start := 0; start < len(ids); start += InsertBatchSize {
		chunk := ids[start:min(start+InsertBatchSize, len(ids))]
		args := make([]any, 0, len(chunk)+1)
		for _, id := range chunk {
			args = append(args, id)
		}
		where, args := scopeClause(scope, args)
		rows, err := q.QueryContext(ctx, `SELECT `+selectRowColumns+` FROM snapshot_rows WHERE snapshot_id IN (`+placeholders(len(chunk))+`)`+where+` ORDER BY snapshot_id, id`, args...)
		if err != nil {
			return nil, fmt.Errorf("store: rows for snapshots: %w", err)
		}
		got, err := scanRows(rows)
		_ = rows.Close()
		if err != nil {
			return nil, fmt.Errorf("store: rows for snapshots: %w", err)
		}
		for _, r := range got {
			out[r.SnapshotID] = append(out[r.SnapshotID], r)
		}
	}
	return out, nil
}

func placeholders(n int) string {
	if n == 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// SnapshotProjects lists the distinct project ids present in a snapshot, sorted.
func SnapshotProjects(ctx context.Context, q Querier, snapshotID int64) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT project_id FROM snapshot_rows WHERE snapshot_id = ? ORDER BY project_id`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("store: snapshot %d projects: %w", snapshotID, err)
	}
	defer rows.Close()
	out := make([]string, 0, 4)
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("store: snapshot %d projects: %w", snapshotID, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BreakdownLevel selects the drill-down grouping (spec/capabilities/drilldown.md).
type BreakdownLevel string

const (
	LevelService  BreakdownLevel = "service"
	LevelSKU      BreakdownLevel = "sku"
	LevelResource BreakdownLevel = "resource"
)

// BreakdownQuery is the validated input of GET /snapshots/{id}/breakdown.
type BreakdownQuery struct {
	SnapshotID int64
	Level      BreakdownLevel
	Scope      string // ScopeAll or a project id
	Service    string // raw service.description; required for sku and resource
	SKU        string // raw sku.description; required for resource
}

// BreakdownRow is one grouped row. Which fields carry meaning depends on Level
// and MarshalJSON emits exactly the per-level shape of spec/api.md.
type BreakdownRow struct {
	Level              BreakdownLevel `json:"-"`
	Key                string         // service description | sku description | resource name or UnattributedKey
	ColumnKey          string         // service level only, filled by costtable.Breakdown
	SKUID              string         // sku level only
	ProjectID          string         // resource level only
	ResourceName       *string        // resource level only
	ResourceGlobalName *string        // resource level only
	Gross              float64
	Credits            float64
	Net                float64
	LineItems          int64
}

// MarshalJSON renders the level-specific field set.
func (r BreakdownRow) MarshalJSON() ([]byte, error) {
	type money struct {
		Gross     float64 `json:"gross"`
		Credits   float64 `json:"credits"`
		Net       float64 `json:"net"`
		LineItems int64   `json:"line_items"`
	}
	m := money{r.Gross, r.Credits, r.Net, r.LineItems}
	switch r.Level {
	case LevelSKU:
		return json.Marshal(struct {
			Key   string `json:"key"`
			SKUID string `json:"sku_id"`
			money
		}{r.Key, r.SKUID, m})
	case LevelResource:
		return json.Marshal(struct {
			Key                string  `json:"key"`
			ProjectID          string  `json:"project_id"`
			ResourceName       *string `json:"resource_name"`
			ResourceGlobalName *string `json:"resource_global_name"`
			money
		}{r.Key, r.ProjectID, r.ResourceName, r.ResourceGlobalName, m})
	default:
		return json.Marshal(struct {
			Key       string `json:"key"`
			ColumnKey string `json:"column_key"`
			money
		}{r.Key, r.ColumnKey, m})
	}
}

// BreakdownTotals sums the rows of a breakdown.
type BreakdownTotals struct {
	Gross   float64 `json:"gross"`
	Credits float64 `json:"credits"`
	Net     float64 `json:"net"`
}

// BreakdownResult is what Breakdown returns; costtable.Breakdown turns it into
// the API response (adding column_key at service level).
type BreakdownResult struct {
	Query  BreakdownQuery
	Rows   []BreakdownRow
	Totals BreakdownTotals
}

// Breakdown groups one snapshot's rows at the requested level under the scope
// filter. Rows are sorted by net descending then key; totals sum the rows, so
// sku totals equal the service cell and resource totals equal the SKU row for
// the same snapshot and scope. Superfluous filters (service at service level)
// are ignored; missing required ones return ErrInvalidLevel.
func Breakdown(ctx context.Context, q Querier, bq BreakdownQuery) (BreakdownResult, error) {
	var keyCols, where string
	args := []any{bq.SnapshotID}
	switch bq.Level {
	case LevelService:
		keyCols = "service"
	case LevelSKU:
		if bq.Service == "" {
			return BreakdownResult{}, fmt.Errorf("%w: level sku requires service", ErrInvalidLevel)
		}
		keyCols, where = "sku, MIN(sku_id)", " AND service = ?"
		args = append(args, bq.Service)
	case LevelResource:
		if bq.Service == "" || bq.SKU == "" {
			return BreakdownResult{}, fmt.Errorf("%w: level resource requires service and sku", ErrInvalidLevel)
		}
		keyCols, where = "project_id, resource_name, resource_global_name", " AND service = ? AND sku = ?"
		args = append(args, bq.Service, bq.SKU)
	default:
		return BreakdownResult{}, fmt.Errorf("%w: %q", ErrInvalidLevel, bq.Level)
	}
	if _, err := GetSnapshot(ctx, q, bq.SnapshotID); err != nil {
		return BreakdownResult{}, err
	}
	groupBy := keyCols
	if bq.Level == LevelSKU {
		groupBy = "sku"
	}
	scopeWhere, args := scopeClause(bq.Scope, args)
	rows, err := q.QueryContext(ctx, `SELECT `+keyCols+`, SUM(gross), SUM(credits), SUM(net), SUM(line_items)
FROM snapshot_rows WHERE snapshot_id = ?`+where+scopeWhere+` GROUP BY `+groupBy, args...)
	if err != nil {
		return BreakdownResult{}, fmt.Errorf("store: breakdown %s of snapshot %d: %w", bq.Level, bq.SnapshotID, err)
	}
	defer rows.Close()

	res := BreakdownResult{Query: bq, Rows: make([]BreakdownRow, 0, 32)}
	for rows.Next() {
		r := BreakdownRow{Level: bq.Level}
		var err error
		switch bq.Level {
		case LevelService:
			err = rows.Scan(&r.Key, &r.Gross, &r.Credits, &r.Net, &r.LineItems)
		case LevelSKU:
			err = rows.Scan(&r.Key, &r.SKUID, &r.Gross, &r.Credits, &r.Net, &r.LineItems)
		case LevelResource:
			var name, global sql.NullString
			err = rows.Scan(&r.ProjectID, &name, &global, &r.Gross, &r.Credits, &r.Net, &r.LineItems)
			r.ResourceName, r.ResourceGlobalName = fromNullString(name), fromNullString(global)
			r.Key = UnattributedKey
			if r.ResourceName != nil {
				r.Key = *r.ResourceName
			}
		}
		if err != nil {
			return BreakdownResult{}, fmt.Errorf("store: breakdown %s of snapshot %d: %w", bq.Level, bq.SnapshotID, err)
		}
		res.Rows = append(res.Rows, r)
		res.Totals.Gross += r.Gross
		res.Totals.Credits += r.Credits
		res.Totals.Net += r.Net
	}
	if err := rows.Err(); err != nil {
		return BreakdownResult{}, fmt.Errorf("store: breakdown %s of snapshot %d: %w", bq.Level, bq.SnapshotID, err)
	}
	// Sorted in Go so NULL ordering never depends on the SQL dialect.
	sort.SliceStable(res.Rows, func(i, j int) bool {
		if res.Rows[i].Net != res.Rows[j].Net {
			return res.Rows[i].Net > res.Rows[j].Net
		}
		if res.Rows[i].Key != res.Rows[j].Key {
			return res.Rows[i].Key < res.Rows[j].Key
		}
		return res.Rows[i].ProjectID < res.Rows[j].ProjectID
	})
	return res, nil
}
