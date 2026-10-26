package httpapi

import (
	"net/http"
	"strconv"

	"github.com/smallTechOrg/cloud0/internal/costtable"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// pathID parses a positive integer path segment; anything else is "not found".
func pathID(r *http.Request, what string) (int64, *APIError) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrNotFound("No " + what + " with id " + quote(r.PathValue("id")) + ".")
	}
	return id, nil
}

func errInvalidLevel(msg string) *APIError {
	return (&APIError{Status: http.StatusBadRequest, Code: "invalid_level", Message: msg}).
		WithHint("Use level=service, level=sku&service=<name> or level=resource&service=<name>&sku=<name>.")
}

func (d Deps) handleSnapshot(w http.ResponseWriter, r *http.Request) *APIError {
	ctx := r.Context()
	id, aerr := pathID(r, "snapshot")
	if aerr != nil {
		return aerr
	}
	snap, err := store.GetSnapshot(ctx, d.Store.DB, id)
	if err != nil {
		return storeErr(err, "No snapshot with id "+strconv.FormatInt(id, 10)+".")
	}
	src, err := store.GetRunSource(ctx, d.Store.DB, snap.RunSourceID)
	if err != nil {
		return ErrInternal(err)
	}
	projects, err := store.SnapshotProjects(ctx, d.Store.DB, id)
	if err != nil {
		return ErrInternal(err)
	}
	writeJSON(w, http.StatusOK, costtable.Provenance(snap, src, projects, d.now(), d.settle()))
	return nil
}

func (d Deps) handleBreakdown(w http.ResponseWriter, r *http.Request) *APIError {
	ctx := r.Context()
	id, aerr := pathID(r, "snapshot")
	if aerr != nil {
		return aerr
	}
	q := r.URL.Query()
	level := store.BreakdownLevel(q.Get("level"))
	switch level {
	case store.LevelService:
	case store.LevelSKU:
		if q.Get("service") == "" {
			return errInvalidLevel("level=sku requires the service parameter.")
		}
	case store.LevelResource:
		if q.Get("service") == "" || q.Get("sku") == "" {
			return errInvalidLevel("level=resource requires the service and sku parameters.")
		}
	default:
		return errInvalidLevel("The level parameter must be service, sku or resource.")
	}
	scope, aerr := d.scopeParam(q.Get("scope"))
	if aerr != nil {
		return aerr
	}
	if _, err := store.GetSnapshot(ctx, d.Store.DB, id); err != nil {
		return storeErr(err, "No snapshot with id "+strconv.FormatInt(id, 10)+".")
	}
	bq := store.BreakdownQuery{SnapshotID: id, Level: level, Scope: scope}
	if level != store.LevelService {
		bq.Service = q.Get("service")
	}
	if level == store.LevelResource {
		bq.SKU = q.Get("sku")
	}
	if handled, aerr := d.breakdownFilterHook(w, r, bq); handled {
		return aerr
	}
	res, err := store.Breakdown(ctx, d.Store.DB, bq)
	if err != nil {
		return ErrInternal(err)
	}
	writeJSON(w, http.StatusOK, costtable.Breakdown(res))
	return nil
}
