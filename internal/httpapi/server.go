// Package httpapi is the HTTP surface of cloud0: the /api/v1 JSON API
// (spec/api.md) and the embedded web UI mounted on the same origin.
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/errorsrc"
	"github.com/smallTechOrg/cloud0/internal/logging"
	"github.com/smallTechOrg/cloud0/internal/refresh"
	"github.com/smallTechOrg/cloud0/internal/store"
	"github.com/smallTechOrg/cloud0/internal/webui"
)

// Deps is everything the handlers need; main wires it once.
type Deps struct {
	Config  config.Config
	Store   *store.Store
	Logger  *slog.Logger
	Version string
	// Now is injected so tests control the clock; nil means time.Now.
	Now func() time.Time
	// UI serves the embedded frontend; nil means webui.Handler().
	UI http.Handler
	// UIMode is reported by /health; empty means webui.CurrentMode().
	UIMode webui.Mode
	// BigQuery reports the startup dry-run result for /health; nil means
	// "bigquery": null.
	BigQuery func() *BigQueryHealth
	// Refresh runs POST /refresh; main builds it with the real BigQuery source.
	Refresh *refresh.Runner
	// Samples reads Cloud Logging for GET /errors/samples; nil answers
	// 503 samples_unavailable.
	Samples errorsrc.LogEntrySource
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

// NewHandler builds the full routing tree: request logging around the API
// and the UI. Exposed for httptest-based tests.
func NewHandler(deps Deps) http.Handler {
	if deps.UI == nil {
		deps.UI = webui.Handler()
	}
	if deps.UIMode == "" {
		deps.UIMode = webui.CurrentMode()
	}
	mux := http.NewServeMux()
	registerRoutes(mux, deps)
	mux.Handle("/", deps.UI)
	return logging.Requests(deps.Logger)(mux)
}

// New returns the configured *http.Server listening on Config.Addr().
func New(deps Deps) *http.Server {
	return &http.Server{
		Addr:              deps.Config.Addr(),
		Handler:           NewHandler(deps),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       130 * time.Second,
		WriteTimeout:      130 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
