package httpapi

import (
	"net/http"
	"strconv"

	"github.com/smallTechOrg/cloud0/internal/store"
)

const (
	defaultRunsLimit = 20
	maxRunsLimit     = 200
)

func (d Deps) handleRuns(w http.ResponseWriter, r *http.Request) *APIError {
	limit := defaultRunsLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return ErrInvalidRequest("The limit parameter must be a positive integer.").
				WithHint("Use limit between 1 and 200 (default 20).")
		}
		limit = min(n, maxRunsLimit)
	}
	runs, err := store.ListRuns(r.Context(), d.Store.DB, limit)
	if err != nil {
		return ErrInternal(err)
	}
	for i := range runs {
		runs[i] = runs[i].WithoutSQL()
	}
	if runs == nil {
		runs = []store.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
	return nil
}

func (d Deps) handleRun(w http.ResponseWriter, r *http.Request) *APIError {
	id, aerr := pathID(r, "run")
	if aerr != nil {
		return aerr
	}
	run, err := store.GetRun(r.Context(), d.Store.DB, id)
	if err != nil {
		return storeErr(err, "No run with id "+strconv.FormatInt(id, 10)+".")
	}
	writeJSON(w, http.StatusOK, run)
	return nil
}
