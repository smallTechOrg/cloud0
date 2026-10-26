package httpapi

import (
	"fmt"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// Gap texts of the TOTAL row.
const (
	totalNotSummed     = "—"
	totalNotSummedHint = "a percentage is not summed across resources"
)

// metricGroupRows assembles the displayed resource rows of one group from the
// stored rows. Stored rows with the same (project, resource) — one per source,
// e.g. Monitoring's per-service memory total and billing's per-service
// active/idle split — are merged by service name; a cell missing from every
// source stays a "not collected" gap. A stored project-level row (Resource "")
// is a per-project aggregate: when the group has resource rows it only feeds
// the TOTAL (e.g. billing egress); when it has none (Networking) it is shown as
// the project's row, named after the project.
func metricGroupRows(stored []gcpsource.MetricRow, spec gcpsource.MetricGroupSpec, scope string, levels map[string]string) (rows, aggs []metricRowJSON) {
	type key struct{ project, resource string }
	idx := map[key]int{}
	var merged []gcpsource.MetricRow
	for _, mr := range stored {
		if mr.Group != spec.Group || (scope != store.ScopeAll && mr.Project != scope) {
			continue
		}
		k := key{mr.Project, mr.Resource}
		i, ok := idx[k]
		if !ok {
			idx[k] = len(merged)
			merged = append(merged, gcpsource.MetricRow{Group: mr.Group, Project: mr.Project, Resource: mr.Resource, Cells: map[string]gcpsource.Cell{}})
			i = idx[k]
		}
		for ck, c := range mr.Cells {
			if old, dup := merged[i].Cells[ck]; !dup || (old.Value == nil && c.Value != nil) {
				merged[i].Cells[ck] = c
			}
		}
	}
	hasResource := false
	for _, m := range merged {
		if m.Resource != "" {
			hasResource = true
		}
	}
	var out []metricRowJSON
	for _, m := range merged {
		if m.Resource == "" && hasResource {
			aggs = append(aggs, metricRow(m, spec, levels))
			continue
		}
		if m.Resource == "" {
			m.Resource = m.Project
		}
		out = append(out, metricRow(m, spec, levels))
	}
	return out, aggs
}

// withTotalRow appends the group's TOTAL row (is_total) after its resource
// rows; groups without rows get none. Per column the total is the sum of the
// resource rows' values; when no resource row has a value, the project-level
// aggregates (aggs) supply it; otherwise the first gap is carried. Percentage
// columns are never summed and show "—".
func withTotalRow(rows, aggs []metricRowJSON, spec gcpsource.MetricGroupSpec, levels map[string]string) []metricRowJSON {
	if len(rows) == 0 {
		return rows
	}
	total := metricRowJSON{Cells: make(map[string]metricCell, len(spec.Columns)), IsTotal: true}
	for _, col := range spec.Columns {
		if col.Unit == "%" {
			gap, hint := totalNotSummed, totalNotSummedHint
			total.Cells[col.Key] = metricCell{CellJSON: store.CellJSON{Unit: col.Unit, Gap: &gap, Hint: &hint,
				Provenance: store.ProvenanceToJSON(gcpsource.Provenance{})}}
			continue
		}
		cj := sumColumn(col, rows)
		if cj.Value == nil {
			if a := sumColumn(col, aggs); a.Value != nil {
				cj = a
			}
		}
		total.Cells[col.Key] = metricCell{CellJSON: cj, LimitLevel: levelFor(levels, spec.Group, col.Key, cj)}
	}
	return append(rows, total)
}

// sumColumn sums the column over rows; with no value at all it returns the
// first gap (or "no data" when there are no cells).
func sumColumn(col gcpsource.MetricColumn, rows []metricRowJSON) store.CellJSON {
	var sum float64
	n := 0
	var first *store.CellJSON
	var firstGap *store.CellJSON
	for _, r := range rows {
		c, ok := r.Cells[col.Key]
		if !ok {
			continue
		}
		if c.Value != nil {
			sum += *c.Value
			n++
			if first == nil {
				cc := c.CellJSON
				first = &cc
			}
		} else if firstGap == nil {
			cc := c.CellJSON
			firstGap = &cc
		}
	}
	if n > 0 {
		out := *first
		v := sum
		out.Value, out.Gap, out.Hint = &v, nil, nil
		h := fmt.Sprintf("sum of %d rows", n)
		out.Hint = &h
		if out.Unit == "" {
			out.Unit = col.Unit
		}
		return out
	}
	if firstGap != nil {
		return *firstGap
	}
	gap := "no data"
	return store.CellJSON{Unit: col.Unit, Gap: &gap, Provenance: store.ProvenanceToJSON(gcpsource.Provenance{})}
}
