package httpapi

import (
	"net/http"
	"sort"
	"strings"
)

// router registers method-qualified patterns on the mux and, per path, one
// method-less fallback that answers 405 with the envelope and an Allow header.
// Later slices add routes by calling r.handle in registerRoutes.
type router struct {
	mux     *http.ServeMux
	deps    Deps
	methods map[string][]string
}

func newRouter(mux *http.ServeMux, deps Deps) *router {
	return &router{mux: mux, deps: deps, methods: map[string][]string{}}
}

func (r *router) handle(method, path string, h Handler) {
	r.mux.Handle(method+" "+path, wrap(r.deps.Logger, h))
	r.methods[path] = append(r.methods[path], method)
}

// finish installs the 405 fallbacks and the /api/v1 404 catch-all.
func (r *router) finish() {
	for p, methods := range r.methods {
		sort.Strings(methods)
		allow := strings.Join(methods, ", ")
		r.mux.Handle(p, wrap(r.deps.Logger, func(w http.ResponseWriter, _ *http.Request) *APIError {
			w.Header().Set("Allow", allow)
			return ErrMethodNotAllowed()
		}))
	}
	r.mux.Handle(apiPrefix+"/", wrap(r.deps.Logger, func(_ http.ResponseWriter, req *http.Request) *APIError {
		return ErrNotFound("No API route at " + req.URL.Path + ".")
	}))
}

const apiPrefix = "/api/v1"

// registerRoutes wires every /api/v1 endpoint. Scaffold: health + config.
// Phase 1 slice S3 appends the cost/refresh/snapshots/runs/settings/annotations
// routes here.
func registerRoutes(mux *http.ServeMux, deps Deps) {
	r := newRouter(mux, deps)
	r.handle(http.MethodGet, apiPrefix+"/health", deps.handleHealth)
	r.handle(http.MethodGet, apiPrefix+"/config", deps.handleConfig)
	r.finish()
}
