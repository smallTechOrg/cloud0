package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/smallTechOrg/cloud0/internal/costtable"
	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

type metricColumnJSON struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Unit  string `json:"unit"`
}

// metricCell is store.CellJSON plus the limit level of the cell's metric.
type metricCell struct {
	store.CellJSON
	LimitLevel *string `json:"limit_level"`
}

type metricRowJSON struct {
	Project  string                `json:"project"`
	Resource string                `json:"resource"`
	Cells    map[string]metricCell `json:"cells"`
}

type metricGroupJSON struct {
	Group   string             `json:"group"`
	Label   string             `json:"label"`
	Columns []metricColumnJSON `json:"columns"`
	Rows    []metricRowJSON    `json:"rows"`
}

type metricsTableResponse struct {
	Week        string                 `json:"week"`
	WeekStart   string                 `json:"week_start"`
	WeekEnd     string                 `json:"week_end"`
	WeekStatus  isoweek.Status         `json:"week_status"`
	Scope       string                 `json:"scope"`
	GeneratedAt time.Time              `json:"generated_at"`
	Snapshot    *costtable.SnapshotRef `json:"snapshot"`
	Groups      []metricGroupJSON      `json:"groups"`
}

func (d Deps) handleMetricsTable(w http.ResponseWriter, r *http.Request) *APIError {
	ctx := r.Context()
	now := d.now()
	q := r.URL.Query()
	scope, aerr := d.scopeParam(q.Get("scope"))
	if aerr != nil {
		return aerr
	}
	week, aerr := d.weekParam(q.Get("week"), now)
	if aerr != nil {
		return aerr
	}
	status := isoweek.StatusAt(week, now, d.settle())
	resp := metricsTableResponse{
		Week: week.String(), WeekStart: store.FormatDate(week.Start()), WeekEnd: store.FormatDate(week.Sunday()),
		WeekStatus: status, Scope: scope, GeneratedAt: now.Truncate(time.Second),
	}

	snap, stored, err := store.LatestMetricSnapshot(ctx, d.Store.DB, week)
	has := true
	if errors.Is(err, store.ErrNotFound) {
		has = false
	} else if err != nil {
		return ErrInternal(err)
	}

	var errRows []gcpsource.ErrorRow
	levels := map[string]string{} // group/column → limit level
	if has {
		resp.Snapshot = snapshotRef(snap.ID, snap.FetchedAt, snap.StatusAtFetch, snap.RunID, snap.RowCount, status)
		_, rows, ok, aerr := d.latestErrors(r, week, scope)
		if aerr != nil {
			return aerr
		}
		if ok {
			errRows = rows
		}
		lim, err := d.evaluateLimits(ctx, week, now)
		if err != nil {
			return ErrInternal(err)
		}
		byID := map[string]string{}
		for _, l := range lim.Limits {
			byID[l.ID] = l.Level
		}
		for _, a := range limitAllowances() {
			if lv := byID[a.ID]; !a.Stub && (lv == "ok" || lv == "near" || lv == "over") {
				levels[a.MetricGroup+"/"+a.MetricKey] = lv
			}
		}
	}

	for _, spec := range gcpsource.MetricGroups() {
		g := metricGroupJSON{Group: spec.Group, Label: spec.Label, Columns: make([]metricColumnJSON, 0, len(spec.Columns)), Rows: []metricRowJSON{}}
		for _, c := range spec.Columns {
			g.Columns = append(g.Columns, metricColumnJSON{Key: c.Key, Label: c.Label, Unit: c.Unit})
		}
		if has {
			if spec.Group == gcpsource.GroupErrors {
				for _, er := range errRows {
					if er.Source != gcpsource.ErrSrcCloudRun {
						continue
					}
					g.Rows = append(g.Rows, errorsGroupRow(er, spec, levels))
				}
			} else {
				for _, mr := range stored {
					if mr.Group != spec.Group || (scope != store.ScopeAll && mr.Project != scope) {
						continue
					}
					g.Rows = append(g.Rows, metricRow(mr, spec, levels))
				}
			}
		}
		resp.Groups = append(resp.Groups, g)
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func levelFor(levels map[string]string, group, key string, c store.CellJSON) *string {
	if c.Value == nil {
		return nil
	}
	if lv, ok := levels[group+"/"+key]; ok {
		return &lv
	}
	return nil
}

func metricRow(mr gcpsource.MetricRow, spec gcpsource.MetricGroupSpec, levels map[string]string) metricRowJSON {
	row := metricRowJSON{Project: mr.Project, Resource: mr.Resource, Cells: make(map[string]metricCell, len(spec.Columns))}
	for _, col := range spec.Columns {
		c, ok := mr.Cells[col.Key]
		if !ok {
			gap := "not collected"
			row.Cells[col.Key] = metricCell{CellJSON: store.CellJSON{Unit: col.Unit, Gap: &gap,
				Provenance: store.ProvenanceToJSON(gcpsource.Provenance{})}}
			continue
		}
		cj := store.CellToJSON(c)
		if cj.Unit == "" {
			cj.Unit = col.Unit
		}
		row.Cells[col.Key] = metricCell{CellJSON: cj, LimitLevel: levelFor(levels, spec.Group, col.Key, cj)}
	}
	return row
}

// errorsGroupRow derives one Errors-group row from a Cloud Run error row.
func errorsGroupRow(er gcpsource.ErrorRow, spec gcpsource.MetricGroupSpec, levels map[string]string) metricRowJSON {
	cell := func(col gcpsource.MetricColumn, n *int64) metricCell {
		cj := store.CellJSON{Unit: col.Unit, Provenance: store.ProvenanceToJSON(er.Provenance)}
		if n != nil {
			v := float64(*n)
			cj.Value = &v
		} else {
			gap := er.Gap
			if gap == "" {
				gap = "no data"
			}
			cj.Gap = &gap
			if er.Hint != "" {
				h := er.Hint
				cj.Hint = &h
			}
		}
		return metricCell{CellJSON: cj, LimitLevel: levelFor(levels, spec.Group, col.Key, cj)}
	}
	row := metricRowJSON{Project: er.Project, Resource: er.Service, Cells: map[string]metricCell{}}
	for _, col := range spec.Columns {
		switch col.Key {
		case gcpsource.ColErr4xx:
			row.Cells[col.Key] = cell(col, er.C4xx)
		case gcpsource.ColErr5xx:
			row.Cells[col.Key] = cell(col, er.C5xx)
		}
	}
	return row
}
