package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/smallTechOrg/cloud0/internal/logging"
)

// APIError is the failure a handler returns; envelope.go is the only place
// that turns it into a response body. Hint is the concrete next action or nil.
type APIError struct {
	Status  int     `json:"-"`
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Hint    *string `json:"hint"`
	// Cause is logged (never sent) for 5xx responses.
	Cause error `json:"-"`
}

func (e *APIError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// WithHint attaches a hint to the error and returns it for chaining.
func (e *APIError) WithHint(hint string) *APIError {
	e.Hint = &hint
	return e
}

// Error constructors for the codes in spec/api.md → Envelope.

func ErrInvalidRequest(msg string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_request", Message: msg}
}

func ErrNotFound(msg string) *APIError {
	return &APIError{Status: http.StatusNotFound, Code: "not_found", Message: msg}
}

func ErrMethodNotAllowed() *APIError {
	return &APIError{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: "This method is not allowed on this path."}
}

func ErrInternal(cause error) *APIError {
	return &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Something went wrong on the server; the request id is in the server log.", Cause: cause}
}

func ErrUnhealthy(msg string, cause error) *APIError {
	return &APIError{Status: http.StatusServiceUnavailable, Code: "unhealthy", Message: msg, Cause: cause}
}

// Handler is an API endpoint: it writes the success body itself and returns
// a non-nil *APIError to have the envelope write the failure body.
type Handler func(w http.ResponseWriter, r *http.Request) *APIError

type errorBody struct {
	Error *APIError `json:"error"`
}

// writeJSON encodes v as the top-level response object.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeError emits the error envelope, records the cause for the access log
// on 5xx, and never leaks stack traces or internal messages for 500s.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, e *APIError) {
	if e.Status >= 500 {
		summary := e.Message
		if e.Cause != nil {
			summary = e.Cause.Error()
			if logger != nil {
				logger.ErrorContext(r.Context(), "handler.error",
					slog.String("request_id", logging.RequestIDFrom(r.Context())),
					slog.String("code", e.Code),
					slog.String("error", e.Cause.Error()))
			}
		}
		logging.SetError(r.Context(), summary)
	}
	writeJSON(w, e.Status, errorBody{Error: e})
}

// wrap adapts a Handler to http.Handler, converting panics to 500s.
func wrap(logger *slog.Logger, h Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				writeError(w, r, logger, ErrInternal(fmt.Errorf("panic: %v", p)))
			}
		}()
		if err := h(w, r); err != nil {
			writeError(w, r, logger, err)
		}
	})
}
