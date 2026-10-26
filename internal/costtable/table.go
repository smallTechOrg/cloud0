package costtable

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// Thresholds is the anomaly rule's pair as echoed in the table response.
type Thresholds struct {
	Pct    float64 `json:"pct"`
	AbsUSD float64 `json:"abs_usd"`
}

// ThresholdsFrom projects the stored settings onto the table shape.
func ThresholdsFrom(s store.Settings) Thresholds {
	return Thresholds{Pct: s.AnomalyPctThreshold, AbsUSD: s.AnomalyAbsThresholdUSD}
}

// Cell is one service or total value of a week (spec/api.md → rows[].cells).
type Cell struct {
	Net      float64  `json:"net"`
	Gross    float64  `json:"gross"`
	Credits  float64  `json:"credits"`
	DeltaAbs *float64 `json:"delta_abs"` // vs previous week's latest snapshot; nil when none
	DeltaPct *float64 `json:"delta_pct"` // nil when no previous or previous net is 0
	Flagged  bool     `json:"flagged"`
}

// SnapshotRef is the `snapshot` object of a table row.
type SnapshotRef struct {
	ID            int64          `json:"id"`
	FetchedAt     time.Time      `json:"fetched_at"`
	StatusAtFetch isoweek.Status `json:"status_at_fetch"`
	RunID         int64          `json:"run_id"`
	RowCount      int            `json:"row_count"` // all projects, regardless of scope
	Stale         bool           `json:"stale"`
}

// Row is one ISO week of the Cost table. Snapshot and Cells are nil (JSON
// null) when the week has no snapshot — never zeros.
type Row struct {
	Week       string          `json:"week"`
	WeekStart  string          `json:"week_start"`
	WeekEnd    string          `json:"week_end"`
	WeekStatus isoweek.Status  `json:"week_status"`
	Snapshot   *SnapshotRef    `json:"snapshot"`
	Cells      map[string]Cell `json:"cells"` // keyed by service / total column key
	PctChange  *float64        `json:"pct_change"`
	Flagged    bool            `json:"flagged"`
	Annotation *string         `json:"annotation"`
}

// Table is the GET /api/v1/cost/table response.
type Table struct {
	Scope       string     `json:"scope"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	GeneratedAt time.Time  `json:"generated_at"`
	Thresholds  Thresholds `json:"thresholds"`
	Columns     []Column   `json:"columns"`
	Rows        []Row      `json:"rows"`
}

// Input is everything Assemble needs; it is pure so tests inject all of it.
type Input struct {
	From, To    isoweek.Week
	Latest      map[isoweek.Week]store.Snapshot // latest snapshot per week; include From.Prev() when it exists so the first row gets deltas
	Rows        map[int64][]store.SnapshotRow   // rows by snapshot id (may already be scope-filtered; Assemble filters again)
	Annotations map[isoweek.Week]string
	Thresholds  Thresholds
	Scope       string // store.ScopeAll, "" or a project id (validated by the API layer)
	Now         time.Time
	Settle      time.Duration // CLOUD0_SETTLE_HOURS
}

// agg is the per-column sum of one week's filtered rows.
type agg struct{ gross, credits, net float64 }

// Stale reports whether a snapshot fetched at `atFetch` is behind the week's
// current status (spec/data.md → Staleness).
func Stale(atFetch, current isoweek.Status) bool { return atFetch.Rank() < current.Rank() }

// Assemble builds the Cost table for From..To from the latest snapshot per
// week (spec/capabilities/cost-table.md). Rows are ascending by week; every
// week in the range is present whether or not it has a snapshot.
func Assemble(in Input) (Table, error) {
	if in.Now.IsZero() {
		return Table{}, fmt.Errorf("costtable: now is required")
	}
	weeks, err := isoweek.Range(in.From, in.To)
	if err != nil {
		return Table{}, fmt.Errorf("costtable: %w", err)
	}

	// Aggregate each relevant week (range + the week before it) under the scope.
	sums := make(map[isoweek.Week]map[string]agg, len(weeks)+1)
	aggregate := func(w isoweek.Week) {
		snap, ok := in.Latest[w]
		if !ok {
			return
		}
		byKey := make(map[string]agg)
		for _, r := range in.Rows[snap.ID] {
			if !inScope(r.ProjectID, in.Scope) {
				continue
			}
			a := byKey[ColumnKeyFor(r.Service)]
			a.gross += r.Gross
			a.credits += r.Credits
			a.net += r.Net
			byKey[ColumnKeyFor(r.Service)] = a
		}
		sums[w] = byKey
	}
	aggregate(in.From.Prev())
	for _, w := range weeks {
		aggregate(w)
	}

	// Extra columns: unknown services with a non-zero net in some week of the range.
	extraSet := make(map[string]bool)
	for _, w := range weeks {
		for key, a := range sums[w] {
			if IsExtraKey(key) && a.net != 0 {
				extraSet[key[len(ExtraKeyPrefix):]] = true
			}
		}
	}
	extras := make([]string, 0, len(extraSet))
	for s := range extraSet {
		extras = append(extras, s)
	}
	columns := Columns(extras)
	valueKeys := make([]string, 0, len(columns))
	for _, c := range columns {
		if c.Kind == KindService {
			valueKeys = append(valueKeys, c.Key)
		}
	}

	tbl := Table{
		Scope:       scopeLabel(in.Scope),
		From:        in.From.String(),
		To:          in.To.String(),
		GeneratedAt: in.Now.UTC().Truncate(time.Second),
		Thresholds:  in.Thresholds,
		Columns:     columns,
		Rows:        make([]Row, 0, len(weeks)),
	}
	for _, w := range weeks {
		row := Row{
			Week:       w.String(),
			WeekStart:  store.FormatDate(w.Start()),
			WeekEnd:    store.FormatDate(w.Sunday()),
			WeekStatus: isoweek.StatusAt(w, in.Now, in.Settle),
		}
		if note, ok := in.Annotations[w]; ok {
			row.Annotation = &note
		}
		snap, ok := in.Latest[w]
		if !ok {
			tbl.Rows = append(tbl.Rows, row)
			continue
		}
		row.Snapshot = &SnapshotRef{ID: snap.ID, FetchedAt: snap.FetchedAt.UTC(), StatusAtFetch: snap.StatusAtFetch, RunID: snap.RunID, RowCount: snap.RowCount,
			Stale: Stale(snap.StatusAtFetch, row.WeekStatus)}
		cur := cellsFor(sums[w], valueKeys)
		prev, hasPrev := sums[w.Prev()]
		var prevCells map[string]agg
		if hasPrev {
			prevCells = cellsFor(prev, valueKeys)
		}
		row.Cells = make(map[string]Cell, len(cur))
		for key, a := range cur {
			cell := Cell{Net: a.net, Gross: a.gross, Credits: a.credits}
			if hasPrev {
				cell.DeltaAbs, cell.DeltaPct, cell.Flagged = delta(a.net, prevCells[key].net, in.Thresholds)
			}
			row.Cells[key] = cell
		}
		total := row.Cells[KeyTotal]
		row.PctChange, row.Flagged = total.DeltaPct, total.Flagged
		tbl.Rows = append(tbl.Rows, row)
	}
	return tbl, nil
}

// cellsFor fills every service column (zero when absent) and the total, which
// is the exact float sum of the service cells in column order.
func cellsFor(byKey map[string]agg, valueKeys []string) map[string]agg {
	out := make(map[string]agg, len(valueKeys)+1)
	var total agg
	for _, key := range valueKeys {
		a := byKey[key]
		out[key] = a
		total.gross += a.gross
		total.credits += a.credits
		total.net += a.net
	}
	out[KeyTotal] = total
	return out
}

// delta implements spec/capabilities/anomaly-flags.md: Δ$ = cur − prev, Δ% is
// nil when prev is 0, flagged when |Δ$| ≥ abs AND |Δ%| ≥ pct and the change is
// non-zero (so thresholds 0/0 flag any change but never a flat week).
func delta(cur, prev float64, th Thresholds) (*float64, *float64, bool) {
	abs := cur - prev
	if prev == 0 {
		return &abs, nil, false
	}
	pct := abs / math.Abs(prev) * 100
	flagged := abs != 0 && math.Abs(abs) >= th.AbsUSD && math.Abs(pct) >= th.Pct
	return &abs, &pct, flagged
}

func inScope(projectID, scope string) bool {
	return scope == "" || scope == store.ScopeAll || projectID == scope
}

func scopeLabel(scope string) string {
	if scope == "" {
		return store.ScopeAll
	}
	return scope
}

// BreakdownResponse is the GET /api/v1/snapshots/{id}/breakdown body.
type BreakdownResponse struct {
	SnapshotID int64                 `json:"snapshot_id"`
	Level      store.BreakdownLevel  `json:"level"`
	Scope      string                `json:"scope"`
	Service    *string               `json:"service"`
	SKU        *string               `json:"sku"`
	Rows       []store.BreakdownRow  `json:"rows"`
	Totals     store.BreakdownTotals `json:"totals"`
}

// Breakdown shapes a store.Breakdown result for the API, filling column_key
// on service-level rows (the Cost-table column each raw service maps to).
func Breakdown(res store.BreakdownResult) BreakdownResponse {
	out := BreakdownResponse{SnapshotID: res.Query.SnapshotID, Level: res.Query.Level, Scope: scopeLabel(res.Query.Scope),
		Rows: make([]store.BreakdownRow, len(res.Rows)), Totals: res.Totals}
	if res.Query.Level != store.LevelService && res.Query.Service != "" {
		svc := res.Query.Service
		out.Service = &svc
	}
	if res.Query.Level == store.LevelResource && res.Query.SKU != "" {
		sku := res.Query.SKU
		out.SKU = &sku
	}
	for i, r := range res.Rows {
		if r.Level == store.LevelService {
			r.ColumnKey = ColumnKeyFor(r.Key)
		}
		out.Rows[i] = r
	}
	return out
}

// Formula is the human-readable statement of how every number is computed.
const Formula = "net = SUM(cost) + SUM(credits.amount), grouped by ISO week of usage_start_time (UTC) and service.description; credits are summed per line item"

// SourceDetail is the `source` object of GET /api/v1/snapshots/{id}.
type SourceDetail struct {
	Source         string          `json:"source"`
	JobID          *string         `json:"job_id"`
	BytesProcessed *int64          `json:"bytes_processed"`
	BytesBilled    *int64          `json:"bytes_billed"`
	CacheHit       *bool           `json:"cache_hit"`
	DurationMs     int64           `json:"duration_ms"`
	SQL            string          `json:"sql"`
	Params         json.RawMessage `json:"params"`
}

// SnapshotDetail is the GET /api/v1/snapshots/{id} body (cell provenance).
type SnapshotDetail struct {
	ID            int64          `json:"id"`
	Week          string         `json:"week"`
	WeekStart     string         `json:"week_start"`
	WeekEnd       string         `json:"week_end"`
	FetchedAt     time.Time      `json:"fetched_at"`
	StatusAtFetch isoweek.Status `json:"status_at_fetch"`
	WeekStatus    isoweek.Status `json:"week_status"`
	Stale         bool           `json:"stale"`
	RunID         int64          `json:"run_id"`
	RowCount      int            `json:"row_count"`
	GrossTotal    float64        `json:"gross_total"`
	CreditsTotal  float64        `json:"credits_total"`
	NetTotal      float64        `json:"net_total"`
	Projects      []string       `json:"projects"`
	Formula       string         `json:"formula"`
	Source        SourceDetail   `json:"source"`
}

// Provenance combines a snapshot, its run source and its project list into
// the detail response, evaluating the week's status (and staleness) at now.
func Provenance(snap store.Snapshot, src store.RunSource, projects []string, now time.Time, settle time.Duration) SnapshotDetail {
	status := isoweek.StatusAt(snap.Week, now, settle)
	if projects == nil {
		projects = []string{}
	}
	params := src.Params
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	return SnapshotDetail{
		ID: snap.ID, Week: snap.WeekID, WeekStart: snap.WeekStart, WeekEnd: snap.WeekEnd,
		FetchedAt: snap.FetchedAt.UTC(), StatusAtFetch: snap.StatusAtFetch, WeekStatus: status, Stale: Stale(snap.StatusAtFetch, status),
		RunID: snap.RunID, RowCount: snap.RowCount, GrossTotal: snap.GrossTotal, CreditsTotal: snap.CreditsTotal, NetTotal: snap.NetTotal,
		Projects: projects, Formula: Formula,
		Source: SourceDetail{Source: src.Source, JobID: src.JobID, BytesProcessed: src.BytesProcessed, BytesBilled: src.BytesBilled,
			CacheHit: src.CacheHit, DurationMs: src.DurationMs, SQL: src.SQLText, Params: params},
	}
}
