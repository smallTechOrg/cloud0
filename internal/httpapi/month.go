package httpapi

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/smallTechOrg/cloud0/internal/costtable"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/monthcost"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// KindMonth is the column kind of the Month view's first column.
const KindMonth costtable.ColumnKind = "month"

type monthRowJSON struct {
	Month         string                    `json:"month"`
	MonthStart    string                    `json:"month_start"`
	MonthEnd      string                    `json:"month_end"`
	MonthStatus   string                    `json:"month_status"`
	Snapshot      *costtable.SnapshotRef    `json:"snapshot"`
	Cells         map[string]costtable.Cell `json:"cells"`
	PctChange     *float64                  `json:"pct_change"`
	Flagged       bool                      `json:"flagged"`
	InvoiceMonths []string                  `json:"invoice_months"`
}

type monthSummaryJSON struct {
	Month     string   `json:"month"`
	Net       float64  `json:"net"`
	Target    *float64 `json:"target"`
	Projected float64  `json:"projected"`
	Status    string   `json:"status"`
	HasData   bool     `json:"has_data"`
}

type monthTableResponse struct {
	Scope       string               `json:"scope"`
	From        string               `json:"from"`
	To          string               `json:"to"`
	GeneratedAt time.Time            `json:"generated_at"`
	Thresholds  costtable.Thresholds `json:"thresholds"`
	Columns     []costtable.Column   `json:"columns"`
	Rows        []monthRowJSON       `json:"rows"`
	Summary     monthSummaryJSON     `json:"summary"`
}

type sums struct{ gross, credits, net float64 }

func prevMonth(m monthcost.Month) monthcost.Month { return monthcost.Of(m.Start().AddDate(0, -1, 0)) }

func inScope(project, scope string) bool { return scope == store.ScopeAll || project == scope }

// monthDelta mirrors the Cost table's anomaly rule (spec/capabilities/anomaly-flags.md).
func monthDelta(cur, prev float64, th costtable.Thresholds) (*float64, *float64, bool) {
	abs := cur - prev
	if prev == 0 {
		return &abs, nil, false
	}
	pct := abs / math.Abs(prev) * 100
	flagged := abs != 0 && math.Abs(abs) >= th.Abs && math.Abs(pct) >= th.Pct
	return &abs, &pct, flagged
}

func (d Deps) handleCostMonth(w http.ResponseWriter, r *http.Request) *APIError {
	ctx := r.Context()
	q := r.URL.Query()
	now := d.now()
	cur := monthcost.Of(now)

	scope, aerr := d.scopeParam(q.Get("scope"))
	if aerr != nil {
		return aerr
	}
	from, to := monthcost.Of(d.Config.FirstWeek.Start()), cur
	if v := q.Get("from"); v != "" {
		if from, aerr = parseMonth(v); aerr != nil {
			return aerr
		}
	}
	if v := q.Get("to"); v != "" {
		if to, aerr = parseMonth(v); aerr != nil {
			return aerr
		}
	}
	if cur.Before(from) {
		return errMonthOutOfRange("The from month " + from.String() + " is after the current month " + cur.String() + ".")
	}
	if cur.Before(to) {
		return errMonthOutOfRange("The to month " + to.String() + " is after the current month " + cur.String() + ".")
	}
	if to.Before(from) {
		return errMonthOutOfRange("The from month " + from.String() + " is after the to month " + to.String() + ".")
	}

	data, err := store.LatestMonthsInRange(ctx, d.Store.DB, prevMonth(from), to)
	if err != nil {
		return ErrInternal(err)
	}
	snaps := map[string]store.MonthData{}
	for _, md := range data {
		snaps[md.Snapshot.Month] = md
	}
	thSettings, err := store.GetThresholds(ctx, d.Store.DB)
	if err != nil {
		return ErrInternal(err)
	}
	th := costtable.ThresholdsFrom(thSettings)

	// Aggregate each relevant month (range + the month before it) under the scope.
	bySvc := map[string]map[string]sums{}
	invoices := map[string][]string{}
	aggregate := func(m monthcost.Month) {
		md, ok := snaps[m.String()]
		if !ok {
			return
		}
		agg := map[string]sums{}
		seen := map[string]bool{}
		for _, row := range md.Rows {
			if !inScope(row.ProjectID, scope) {
				continue
			}
			k := costtable.ColumnKeyFor(row.Service)
			a := agg[k]
			a.gross += row.Gross
			a.credits += row.Credits
			a.net += row.Net
			agg[k] = a
			if row.InvoiceMonth != "" && !seen[row.InvoiceMonth] {
				seen[row.InvoiceMonth] = true
				invoices[m.String()] = append(invoices[m.String()], row.InvoiceMonth)
			}
		}
		sort.Strings(invoices[m.String()])
		bySvc[m.String()] = agg
	}
	var months []monthcost.Month
	for m := from; !to.Before(m); m = m.Next() {
		months = append(months, m)
	}
	aggregate(prevMonth(from))
	for _, m := range months {
		aggregate(m)
	}

	extraSet := map[string]bool{}
	for _, m := range months {
		for k, a := range bySvc[m.String()] {
			if costtable.IsExtraKey(k) && a.net != 0 {
				extraSet[k[len(costtable.ExtraKeyPrefix):]] = true
			}
		}
	}
	extras := make([]string, 0, len(extraSet))
	for s := range extraSet {
		extras = append(extras, s)
	}
	columns := costtable.Columns(extras)
	columns[0] = costtable.Column{Key: "month", Label: "Month", Kind: KindMonth}
	var valueKeys []string
	for _, c := range columns {
		if c.Kind == costtable.KindService {
			valueKeys = append(valueKeys, c.Key)
		}
	}
	cellsFor := func(byKey map[string]sums) map[string]sums {
		out := make(map[string]sums, len(valueKeys)+1)
		var total sums
		for _, k := range valueKeys {
			a := byKey[k]
			out[k] = a
			total.gross += a.gross
			total.credits += a.credits
			total.net += a.net
		}
		out[costtable.KeyTotal] = total
		return out
	}

	resp := monthTableResponse{Scope: scope, From: from.String(), To: to.String(), GeneratedAt: now.Truncate(time.Second),
		Thresholds: th, Columns: columns, Rows: make([]monthRowJSON, 0, len(months))}
	for _, m := range months {
		status := monthcost.Status(m, now, d.settleHours())
		row := monthRowJSON{Month: m.String(), MonthStart: store.FormatDate(m.Start()),
			MonthEnd: store.FormatDate(m.Bounds().End.AddDate(0, 0, -1)), MonthStatus: status, InvoiceMonths: []string{}}
		md, ok := snaps[m.String()]
		if !ok {
			resp.Rows = append(resp.Rows, row)
			continue
		}
		row.Snapshot = snapshotRef(md.Snapshot.ID, md.Snapshot.FetchedAt, md.Snapshot.StatusAtFetch, md.Snapshot.RunID, md.Snapshot.RowCount, isoweek.Status(status))
		if inv := invoices[m.String()]; len(inv) > 0 {
			row.InvoiceMonths = inv
		}
		curCells := cellsFor(bySvc[m.String()])
		prevAgg, hasPrev := bySvc[prevMonth(m).String()]
		var prevCells map[string]sums
		if hasPrev {
			prevCells = cellsFor(prevAgg)
		}
		row.Cells = make(map[string]costtable.Cell, len(curCells))
		for k, a := range curCells {
			c := costtable.Cell{Net: a.net, Gross: a.gross, Credits: a.credits}
			if hasPrev {
				c.DeltaAbs, c.DeltaPct, c.Flagged = monthDelta(a.net, prevCells[k].net, th)
			}
			row.Cells[k] = c
		}
		total := row.Cells[costtable.KeyTotal]
		row.PctChange, row.Flagged = total.DeltaPct, total.Flagged
		resp.Rows = append(resp.Rows, row)
	}

	// Summary: the current month regardless of from/to.
	target, err := store.GetMonthlyTarget(ctx, d.Store.DB)
	if err != nil {
		return ErrInternal(err)
	}
	var curRows []monthcost.Row
	snap, rows, err := store.LatestMonthSnapshot(ctx, d.Store.DB, cur)
	hasData := true
	switch {
	case errors.Is(err, store.ErrNotFound):
		hasData = false
	case err != nil:
		return ErrInternal(err)
	default:
		_ = snap
		for _, row := range rows {
			if inScope(row.ProjectID, scope) {
				curRows = append(curRows, row)
			}
		}
	}
	line := monthcost.SummaryWithSettle(curRows, cur, target, now, d.settleHours())
	resp.Summary = monthSummaryJSON{Month: line.Month, Net: line.Net, Target: line.Target, Projected: line.Projected, Status: line.Status, HasData: hasData}
	if !hasData {
		resp.Summary.Net, resp.Summary.Projected = 0, 0
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}
