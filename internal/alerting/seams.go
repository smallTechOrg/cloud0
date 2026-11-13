package alerting

import "context"

// Seams between the parallel Alerts workers. Frozen: implementations live in
// their own files; the HTTP layer depends only on these interfaces.

// Engine turns a PreviewRequest into a Preview (no GCP change) and applies a
// previously issued preview token (single-use, 10-minute lifetime, rejects a
// stale target with ErrPreviewStale), writing a change_history row on success.
type Engine interface {
	Preview(ctx context.Context, req PreviewRequest) (Preview, error)
	Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error)
}

// Adopter previews the "adopt & fix" replacements for the known unmanaged shapes.
type Adopter interface {
	Preview(ctx context.Context, req AdoptRequest) (AdoptPreview, error)
}

// Router holds the severity → channel routing (settings alerts.routing.<sev>)
// and resolves it to channel resource names for a project.
type Router interface {
	Get(ctx context.Context) (map[string][]string, error)
	Put(ctx context.Context, routing map[string][]string) error
	// Resolve returns channel names usable in project; override (when non-empty)
	// replaces routing; channels from another project are skipped with a warning.
	Resolve(ctx context.Context, project, severity string, override []string) (channels []string, warnings []string, err error)
}

// CoverageProvider computes GET /alerts/coverage (which resources lack which alert types).
type CoverageProvider interface {
	Coverage(ctx context.Context) ([]CoverageRow, error)
}

// HistoryProvider computes GET /alerts/history for an ISO week ("2026-W43").
type HistoryProvider interface {
	History(ctx context.Context, week string) ([]HistoryRow, error)
}

// SuggestionProvider computes GET /budgets/suggestions.
type SuggestionProvider interface {
	Suggestions(ctx context.Context) ([]Suggestion, error)
}
