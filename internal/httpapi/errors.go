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
	Rows        []store.ErrorRowJSON   `json:"rows"`
	Totals      errorsTotals           `json:"totals"`
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
	resp := errorsTableResponse{Week: week.String(), Scope: scope, GeneratedAt: now.Truncate(time.Second), Rows: []store.ErrorRowJSON{}}
	batch, rows, ok, aerr := d.latestErrors(r, week, scope)
	if aerr != nil {
		return aerr
	}
	if ok {
		cur := isoweek.StatusAt(week, now, d.settle())
		// Error batches have no snapshot header: the id is the batch's run_id.
		resp.Snapshot = snapshotRef(batch.RunID, batch.FetchedAt, batch.StatusAtFetch, batch.RunID, batch.RowCount, cur)
		for _, row := range rows {
			resp.Rows = append(resp.Rows, store.ErrorRowToJSON(row))
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
