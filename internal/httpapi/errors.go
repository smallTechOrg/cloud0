package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/smallTechOrg/cloud0/internal/costtable"
	"github.com/smallTechOrg/cloud0/internal/errorsrc"
	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// errorsTotals sums the non-null values of the returned rows; a field is null
// when no row carries it.
type errorsTotals struct {
	C2xx        *int64 `json:"c2xx"`
	C3xx        *int64 `json:"c3xx"`
	C4xx        *int64 `json:"c4xx"`
	C5xx        *int64 `json:"c5xx"`
	Groups      *int64 `json:"groups"`
	Occurrences *int64 `json:"occurrences"`
}

func addTo(sum **int64, v *int64) {
	if v == nil {
		return
	}
	if *sum == nil {
		z := int64(0)
		*sum = &z
	}
	**sum += *v
}

type errorsTableResponse struct {
	Week        string                 `json:"week"`
	Scope       string                 `json:"scope"`
	GeneratedAt time.Time              `json:"generated_at"`
	Snapshot    *costtable.SnapshotRef `json:"snapshot"`
	Summary     errorsSummary          `json:"summary"`
	Rows        []errorRowTriaged      `json:"rows"`
	Totals      errorsTotals           `json:"totals"`
}

// errorsSummary is errorsrc.TriageSummary on the wire.
type errorsSummary = errorsrc.TriageSummary

// errorRowTriaged is a stored error row plus its triage fields.
type errorRowTriaged struct {
	store.ErrorRowJSON
	Verdict string                 `json:"verdict"`
	Reasons []string               `json:"reasons"`
	Rank    int                    `json:"rank"`
	Rates   errorsrc.TriageRates   `json:"rates"`
	Change  *errorsrc.TriageChange `json:"change"`
}

func errRowKey(r gcpsource.ErrorRow) string {
	return r.Source + "\x00" + r.Project + "\x00" + r.Service
}

// prevWeekRows indexes the latest stored batch of the ISO week before week by
// source+project+service; nil when that week has no stored batch.
func (d Deps) prevWeekRows(r *http.Request, week isoweek.Week) (string, map[string]gcpsource.ErrorRow, *APIError) {
	prev := week.Prev()
	_, rows, err := store.LatestErrorRows(r.Context(), d.Store.DB, prev)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, ErrInternal(err)
	}
	m := make(map[string]gcpsource.ErrorRow, len(rows))
	for _, row := range rows {
		m[errRowKey(row)] = row
	}
	return prev.String(), m, nil
}

// latestErrors loads the week's newest error batch filtered to scope; ok is
// false when the week was never fetched.
func (d Deps) latestErrors(r *http.Request, week isoweek.Week, scope string) (store.ErrorBatch, []gcpsource.ErrorRow, bool, *APIError) {
	batch, rows, err := store.LatestErrorRows(r.Context(), d.Store.DB, week)
	if errors.Is(err, store.ErrNotFound) {
		return store.ErrorBatch{}, nil, false, nil
	}
	if err != nil {
		return store.ErrorBatch{}, nil, false, ErrInternal(err)
	}
	out := make([]gcpsource.ErrorRow, 0, len(rows))
	for _, row := range rows {
		if scope == store.ScopeAll || row.Project == scope {
			out = append(out, row)
		}
	}
	return batch, out, true, nil
}

func (d Deps) handleErrorsTable(w http.ResponseWriter, r *http.Request) *APIError {
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
	resp := errorsTableResponse{Week: week.String(), Scope: scope, GeneratedAt: now.Truncate(time.Second), Rows: []errorRowTriaged{}}
	resp.Summary = errorsrc.Summarize(nil)
	resp.Summary.Headline = "No error data fetched for this week yet."
	batch, rows, ok, aerr := d.latestErrors(r, week, scope)
	if aerr != nil {
		return aerr
	}
	if ok {
		cur := isoweek.StatusAt(week, now, d.settle())
		// Error batches have no snapshot header: the id is the batch's run_id.
		resp.Snapshot = snapshotRef(batch.RunID, batch.FetchedAt, batch.StatusAtFetch, batch.RunID, batch.RowCount, cur)
		prevID, prevRows, aerr := d.prevWeekRows(r, week)
		if aerr != nil {
			return aerr
		}
		results := make([]errorsrc.TriageResult, len(rows))
		for i, row := range rows {
			var prev *errorsrc.TriagePrev
			if pr, found := prevRows[errRowKey(row)]; found {
				prev = &errorsrc.TriagePrev{Week: prevID, Row: errorsrc.TriageInputOf(pr)}
			}
			results[i] = errorsrc.Triage(errorsrc.TriageInputOf(row), prev)
		}
		errorsrc.AssignRanks(results)
		resp.Summary = errorsrc.Summarize(results)
		for i, row := range rows {
			rj := store.ErrorRowToJSON(row)
			if row.Source == gcpsource.ErrSrcCloudRun && row.Service != "" && row.Gap == "" {
				u := errorsrc.LogsExplorerURL(row.Project, row.Service, gcpsource.Window{Start: week.Start(), End: week.End()})
				rj.LogsURL = &u
			}
			tr := results[i]
			resp.Rows = append(resp.Rows, errorRowTriaged{ErrorRowJSON: rj, Verdict: tr.Verdict, Reasons: tr.Reasons, Rank: tr.Rank, Rates: tr.Rates, Change: tr.Change})
			addTo(&resp.Totals.C2xx, row.C2xx)
			addTo(&resp.Totals.C3xx, row.C3xx)
			addTo(&resp.Totals.C4xx, row.C4xx)
			addTo(&resp.Totals.C5xx, row.C5xx)
			addTo(&resp.Totals.Groups, row.Groups)
			addTo(&resp.Totals.Occurrences, row.Occurrences)
		}
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}
