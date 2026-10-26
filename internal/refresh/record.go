package refresh

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/smallTechOrg/cloud0/internal/billing"
	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/monthcost"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// outcome is everything the sources returned, before anything is written.
type outcome struct {
	billing bool
	bres    billing.Result
	berr    error
	bEnd    time.Time

	month  *monthOutcome
	others []collected // collectors, in contract order
}

type monthOutcome struct {
	rows              []monthcost.Row
	prov              gcpsource.Provenance
	err               error
	started, finished time.Time
}

type collected struct {
	c                 Collector
	res               gcpsource.Result
	started, finished time.Time
}

// record is what the transaction produced.
type record struct {
	sources     []store.RunSource
	snaps       []store.Snapshot
	metricSnaps []store.MetricSnapshot
	monthSnaps  []store.MonthSnapshot
	status      store.RunStatus
}

// fetchOthers runs the month source and the collectors with bounded
// concurrency. Nothing here can fail the run: panics and errors become failed
// results of the one source that raised them.
func (r *Runner) fetchOthers(ctx context.Context, pl plan, out *outcome) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, MaxConcurrency)
	spawn := func(f func()) {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			f()
		}()
	}

	if pl.months && r.Month != nil {
		mo := &monthOutcome{}
		out.month = mo
		spawn(func() {
			mo.started = r.now()
			defer func() {
				if p := recover(); p != nil {
					mo.err = fmt.Errorf("month source panicked: %v", p)
				}
				mo.finished = r.now()
			}()
			c, cancel := context.WithTimeout(ctx, SourceTimeout)
			defer cancel()
			mo.rows, mo.prov, mo.err = r.Month.FetchMonths(c, pl.mFrom, pl.mTo)
		})
	}
	if pl.metrics && len(r.Collectors) > 0 {
		w := gcpsource.Window{Start: pl.week.Start(), End: pl.week.End()}
		out.others = make([]collected, len(r.Collectors))
		for i := range r.Collectors {
			i := i
			out.others[i].c = r.Collectors[i]
			spawn(func() {
				col := &out.others[i]
				col.started = r.now()
				defer func() {
					if p := recover(); p != nil {
						col.res = gcpsource.Result{Source: col.c.Name}
						col.res.Finish(r.now(), fmt.Errorf("source panicked: %v", p), "", "")
					}
					col.finished = r.now()
					if col.res.Source == "" {
						col.res.Source = col.c.Name
					}
				}()
				c, cancel := context.WithTimeout(ctx, SourceTimeout)
				defer cancel()
				col.res = col.c.Fetch(c, w)
			})
		}
	}
	wg.Wait()
}

// paramsFromJSON flattens a JSON-object aggregation into run_sources params.
func paramsFromJSON(s string) map[string]string {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil || m == nil {
		return map[string]string{"aggregation": s}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if str, ok := v.(string); ok {
			out[k] = str
			continue
		}
		b, _ := json.Marshal(v)
		out[k] = string(b)
	}
	return out
}

// provenances returns the result's provenance list, falling back to the
// distinct provenances of its cells and rows.
func provenances(res gcpsource.Result) []gcpsource.Provenance {
	if len(res.Provenance) > 0 {
		return res.Provenance
	}
	var out []gcpsource.Provenance
	seen := map[string]bool{}
	add := func(p gcpsource.Provenance) {
		k := p.Query + "\x00" + p.Aggregation
		if (p.Query == "" && p.Aggregation == "") || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, p)
	}
	for _, m := range res.Metrics {
		for _, c := range m.Cells {
			add(c.Provenance)
		}
	}
	for _, e := range res.Errors {
		add(e.Provenance)
	}
	return out
}

func distinct(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func collectorSource(c collected, runID int64) store.NewRunSource {
	res := c.res
	ns := store.NewRunSource{RunID: runID, Source: c.c.Name, StartedAt: c.started, FinishedAt: c.finished}
	provs := provenances(res)
	var queries, aggs []string
	for _, p := range provs {
		queries = append(queries, p.Query)
		aggs = append(aggs, p.Aggregation)
	}
	ns.SQLText = strings.Join(distinct(queries), "\n")
	params := map[string]string{}
	if a := distinct(aggs); len(a) > 0 {
		params["aggregation"] = strings.Join(a, "\n")
	}
	if len(provs) > 0 {
		params["window_start"] = provs[0].Window.Start.UTC().Format(time.RFC3339)
		params["window_end"] = provs[0].Window.End.UTC().Format(time.RFC3339)
	}
	ns.Params = params
	rows := int64(len(res.Metrics) + len(res.Errors))
	ns.RowsReturned = &rows
	if res.Status == "" || res.Status == gcpsource.StatusOK {
		ns.Status = store.SourceOK
		return ns
	}
	ns.Status = store.SourceFailed
	msg := res.Message
	if msg == "" {
		msg = "source " + res.Status
	}
	code := res.Status
	ns.ErrorCode, ns.ErrorMessage, ns.FixHint = &code, &msg, ptr(res.Hint)
	return ns
}

func (c collected) failed() bool { return c.res.Status != "" && c.res.Status != gcpsource.StatusOK }

// mergeMetrics merges source rows by (group, project, resource) in first-seen
// order and adds, for every failed collector, "source failed" gap cells for
// the cells it owns that no row carries, one group-total row per project.
func (r *Runner) mergeMetrics(out outcome, w gcpsource.Window, now time.Time) []gcpsource.MetricRow {
	type key struct{ group, project, resource string }
	idx := map[key]int{}
	var rows []gcpsource.MetricRow
	add := func(m gcpsource.MetricRow) {
		k := key{m.Group, m.Project, m.Resource}
		i, ok := idx[k]
		if !ok {
			cells := make(map[string]gcpsource.Cell, len(m.Cells))
			idx[k] = len(rows)
			rows = append(rows, gcpsource.MetricRow{Group: m.Group, Project: m.Project, Resource: m.Resource, Cells: cells})
			i = idx[k]
		}
		for ck, c := range m.Cells {
			if _, dup := rows[i].Cells[ck]; !dup {
				rows[i].Cells[ck] = c
			}
		}
	}
	for _, c := range out.others {
		for _, m := range c.res.Metrics {
			add(m)
		}
	}
	units := map[string]string{}
	for _, g := range gcpsource.MetricGroups() {
		for _, col := range g.Columns {
			units[g.Group+"/"+col.Key] = col.Unit
		}
	}
	for _, c := range out.others {
		if !c.failed() {
			continue
		}
		prov := gcpsource.Provenance{Source: c.c.Name, Window: w, FetchedAt: now}
		if ps := provenances(c.res); len(ps) > 0 {
			prov = ps[0]
			prov.Source = c.c.Name
		}
		for _, o := range c.c.Owns {
			for _, project := range r.Projects {
				row := gcpsource.MetricRow{Group: o.Group, Project: project, Cells: map[string]gcpsource.Cell{}}
				for _, col := range o.Columns {
					if projectHasCell(rows, o.Group, project, col) {
						continue
					}
					row.Cells[col] = gcpsource.G("source failed", c.res.Hint, units[o.Group+"/"+col], prov)
				}
				if len(row.Cells) > 0 {
					add(row)
				}
			}
		}
	}
	return rows
}

func projectHasCell(rows []gcpsource.MetricRow, group, project, col string) bool {
	for _, m := range rows {
		if m.Group == group && m.Project == project {
			if _, ok := m.Cells[col]; ok {
				return true
			}
		}
	}
	return false
}

// mergeErrors concatenates the error sources' rows; a failed error source
// that produced no rows gets one "source failed" row per project.
func (r *Runner) mergeErrors(out outcome, w gcpsource.Window, now time.Time) []gcpsource.ErrorRow {
	var rows []gcpsource.ErrorRow
	for _, c := range out.others {
		rows = append(rows, c.res.Errors...)
		if c.c.ErrorSource == "" || !c.failed() || len(c.res.Errors) > 0 {
			continue
		}
		prov := gcpsource.Provenance{Source: c.c.Name, Window: w, FetchedAt: now}
		if ps := provenances(c.res); len(ps) > 0 {
			prov = ps[0]
			prov.Source = c.c.Name
		}
		for _, p := range r.Projects {
			rows = append(rows, gcpsource.ErrorRow{Source: c.c.ErrorSource, Project: p, Gap: "source failed", Hint: c.res.Hint, Provenance: prov})
		}
	}
	return rows
}

func (r *Runner) billingSource(run store.Run, out outcome, startedAt time.Time) store.NewRunSource {
	res := out.bres
	ns := store.NewRunSource{RunID: run.ID, Source: billing.SourceName, StartedAt: startedAt, FinishedAt: out.bEnd, SQLText: res.Query.SQL}
	if res.Query.SQL != "" {
		ns.Params = res.Query.Params.Map()
	}
	if res.Stats.JobID != "" {
		ns.JobID = ptr(res.Stats.JobID)
	}
	if out.berr != nil {
		f := billing.Classify(out.berr)
		ns.Status = store.SourceFailed
		ns.ErrorCode, ns.ErrorMessage, ns.FixHint = ptr(string(f.Code)), ptr(f.Message), ptr(f.Hint)
	} else {
		ns.Status = store.SourceOK
		processed, billed, cache, rows := res.Stats.BytesProcessed, res.Stats.BytesBilled, res.Stats.CacheHit, int64(len(res.Rows))
		ns.BytesProcessed, ns.BytesBilled, ns.CacheHit, ns.RowsReturned = &processed, &billed, &cache, &rows
	}
	return ns
}

func (r *Runner) monthSource(run store.Run, mo *monthOutcome) store.NewRunSource {
	ns := store.NewRunSource{RunID: run.ID, Source: monthcost.SourceName, StartedAt: mo.started, FinishedAt: mo.finished,
		SQLText: mo.prov.Query}
	if mo.prov.Aggregation != "" {
		ns.Params = paramsFromJSON(mo.prov.Aggregation)
	}
	if mo.err != nil {
		status, msg, hint := gcpsource.Classify(mo.err, monthcost.RoleHint, monthcost.API)
		ns.Status = store.SourceFailed
		ns.ErrorCode, ns.ErrorMessage, ns.FixHint = &status, &msg, ptr(hint)
		return ns
	}
	ns.Status = store.SourceOK
	n := int64(len(mo.rows))
	ns.RowsReturned = &n
	return ns
}

// record writes, in one transaction, every run_sources row (billing, month,
// then the collectors in order), the billing snapshots, month snapshots, the
// metric snapshot and error batch of the week-window sources, and the run's
// final status. Failed sources still contribute their recorded failure and
// any partial data; nothing a source does can roll back another's data.
func (r *Runner) record(ctx context.Context, run store.Run, pl plan, out outcome, startedAt, finishedAt time.Time) (record, error) {
	var rec record
	settleHours := int(r.Settle.Hours())
	var weekWindow gcpsource.Window
	if pl.metrics {
		weekWindow = gcpsource.Window{Start: pl.week.Start(), End: pl.week.End()}
	}

	err := r.Store.WithTx(ctx, func(tx *sql.Tx) error {
		rec = record{}
		core := true // billing (week/all) or month cost (month mode) succeeded
		anyFailed := false

		if out.billing {
			src, err := store.AddSource(ctx, tx, r.billingSource(run, out, startedAt))
			if err != nil {
				return err
			}
			rec.sources = append(rec.sources, src)
			if out.berr != nil {
				core = false
			} else {
				weeks, err := pl.billing.Weeks()
				if err != nil {
					return err
				}
				byWeek := r.groupRows(*pl.billing, out.bres.Rows)
				for _, w := range weeks {
					snap, err := store.InsertSnapshot(ctx, tx, store.NewSnapshot{
						Week: w, FetchedAt: startedAt, StatusAtFetch: isoweek.StatusAt(w, startedAt, r.Settle),
						RunID: run.ID, RunSourceID: src.ID, Rows: byWeek[w],
					})
					if err != nil {
						return err
					}
					rec.snaps = append(rec.snaps, snap)
				}
			}
		}

		if out.month != nil {
			src, err := store.AddSource(ctx, tx, r.monthSource(run, out.month))
			if err != nil {
				return err
			}
			rec.sources = append(rec.sources, src)
			if out.month.err != nil {
				if !out.billing {
					core = false // month mode: month cost is the core source
				} else {
					anyFailed = true
				}
			} else {
				byMonth := map[string][]monthcost.Row{}
				for _, row := range out.month.rows {
					byMonth[row.Month] = append(byMonth[row.Month], row)
				}
				for m := pl.mFrom; !pl.mTo.Before(m); m = m.Next() {
					ms, err := store.InsertMonthSnapshot(ctx, tx, store.NewMonthSnapshot{
						Month: m, FetchedAt: startedAt, StatusAtFetch: isoweek.Status(monthcost.Status(m, startedAt, settleHours)),
						RunID: run.ID, RunSourceID: &src.ID, Rows: byMonth[m.String()],
					})
					if err != nil {
						return err
					}
					rec.monthSnaps = append(rec.monthSnaps, ms)
				}
			}
		}

		for _, c := range out.others {
			src, err := store.AddSource(ctx, tx, collectorSource(c, run.ID))
			if err != nil {
				return err
			}
			rec.sources = append(rec.sources, src)
			if c.failed() {
				anyFailed = true
			}
		}
		if len(out.others) > 0 {
			status := isoweek.StatusAt(pl.week, startedAt, r.Settle)
			ms, err := store.InsertMetricSnapshot(ctx, tx, store.NewMetricSnapshot{
				Week: pl.week, FetchedAt: startedAt, StatusAtFetch: status, RunID: run.ID,
				Rows: r.mergeMetrics(out, weekWindow, startedAt),
			})
			if err != nil {
				return err
			}
			rec.metricSnaps = append(rec.metricSnaps, ms)
			if errRows := r.mergeErrors(out, weekWindow, startedAt); len(errRows) > 0 {
				if _, err := store.InsertErrorRows(ctx, tx, store.NewErrorBatch{
					Week: pl.week, FetchedAt: startedAt, StatusAtFetch: status, RunID: run.ID, Rows: errRows,
				}); err != nil {
					return err
				}
			}
		}

		switch {
		case !core:
			rec.status = store.RunFailed
		case anyFailed:
			rec.status = store.RunPartial
		default:
			rec.status = store.RunOK
		}
		return store.FinishRun(ctx, tx, run.ID, rec.status, finishedAt, nil)
	})
	if err != nil {
		return record{}, err
	}
	return rec, nil
}
