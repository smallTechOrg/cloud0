package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/smallTechOrg/cloud0/internal/store"
)

const maxBodyBytes = 1 << 20

// decodeBody reads exactly one JSON object into v, rejecting unknown fields,
// wrong types, empty bodies and trailing data.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) *APIError {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return ErrInvalidRequest("The request body is not valid JSON for this endpoint.").
			WithHint("Send a single JSON object with the documented fields (see spec/api.md).")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalidRequest("The request body must contain exactly one JSON object.")
	}
	return nil
}

func (d Deps) handleGetSettings(w http.ResponseWriter, r *http.Request) *APIError {
	s, err := store.GetThresholds(r.Context(), d.Store.DB)
	if err != nil {
		return ErrInternal(err)
	}
	writeJSON(w, http.StatusOK, s)
	return nil
}

func (d Deps) handlePutSettings(w http.ResponseWriter, r *http.Request) *APIError {
	var body struct {
		Pct *float64 `json:"anomaly_pct_threshold"`
		Abs *float64 `json:"anomaly_abs_threshold_usd"`
	}
	if aerr := decodeBody(w, r, &body); aerr != nil {
		return aerr
	}
	invalid := func(msg string) *APIError {
		return (&APIError{Status: http.StatusBadRequest, Code: "invalid_settings", Message: msg}).
			WithHint("Send both anomaly_pct_threshold and anomaly_abs_threshold_usd as finite numbers >= 0.")
	}
	if body.Pct == nil || body.Abs == nil {
		return invalid("Both anomaly_pct_threshold and anomaly_abs_threshold_usd are required.")
	}
	s, err := store.PutThresholds(r.Context(), d.Store.DB, *body.Pct, *body.Abs, d.now())
	if errors.Is(err, store.ErrInvalidSettings) {
		return invalid("Thresholds must be finite numbers >= 0.")
	}
	if err != nil {
		return ErrInternal(err)
	}
	writeJSON(w, http.StatusOK, s)
	return nil
}
