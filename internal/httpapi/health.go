package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/smallTechOrg/cloud0/internal/billing"
	"github.com/smallTechOrg/cloud0/internal/webui"
)

// BigQueryHealth mirrors the "bigquery" object of GET /api/v1/health.
type BigQueryHealth struct {
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
	ErrorCode *string   `json:"error_code"`
	Message   *string   `json:"message"`
	FixHint   *string   `json:"fix_hint"`
}

// NewBigQueryHealth turns the startup dry-run outcome (nil error = ok) into
// the health object, classifying failures into a code, message and fix hint.
func NewBigQueryHealth(checkedAt time.Time, err error) BigQueryHealth {
	h := BigQueryHealth{Status: "ok", CheckedAt: checkedAt.UTC().Truncate(time.Second)}
	if err == nil {
		return h
	}
	f := billing.Classify(err)
	code, msg, hint := string(f.Code), f.Message, f.Hint
	h.Status, h.ErrorCode, h.Message = "error", &code, &msg
	if hint != "" {
		h.FixHint = &hint
	}
	return h
}

// HealthResponse is the body of GET /api/v1/health.
type HealthResponse struct {
	OK         bool            `json:"ok"`
	Version    string          `json:"version"`
	DB         string          `json:"db"`
	Migrations string          `json:"migrations"`
	UI         webui.Mode      `json:"ui"`
	BigQuery   *BigQueryHealth `json:"bigquery"`
}

func (d Deps) handleHealth(w http.ResponseWriter, r *http.Request) *APIError {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	resp := HealthResponse{OK: true, Version: d.Version, DB: "ok", Migrations: "current", UI: d.UIMode}
	if d.BigQuery != nil {
		resp.BigQuery = d.BigQuery()
	}

	if err := d.Store.Ping(ctx); err != nil {
		resp.OK, resp.DB, resp.Migrations = false, "error", "unknown"
		return ErrUnhealthy("Database is unreachable.", err).
			WithHint("Check CLOUD0_DB_PATH and that the directory is writable, then restart cloud0.")
	}
	pending, err := d.Store.Pending(ctx)
	if err != nil {
		return ErrUnhealthy("Migration status could not be read.", err).
			WithHint("Run ./bin/cloud0 migrate status from the repo root.")
	}
	if pending > 0 {
		return ErrUnhealthy("Database migrations are pending.", nil).
			WithHint("Run ./bin/cloud0 migrate up from the repo root, then restart cloud0.")
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}
