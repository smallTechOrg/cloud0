package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

// HeaderRequestID is echoed from the request when present, otherwise generated.
const HeaderRequestID = "X-Request-Id"

type ctxKey struct{}

// RequestState travels in the request context so handlers can attach an
// error summary that the access log line should carry for 5xx responses.
type RequestState struct {
	RequestID string
	Error     string
}

// StateFrom returns the per-request state, or nil outside the middleware.
func StateFrom(ctx context.Context) *RequestState {
	s, _ := ctx.Value(ctxKey{}).(*RequestState)
	return s
}

// RequestIDFrom returns the request id, or "" outside the middleware.
func RequestIDFrom(ctx context.Context) string {
	if s := StateFrom(ctx); s != nil {
		return s.RequestID
	}
	return ""
}

// SetError records a short error description for the current request; the
// access log emits it as "error" on 5xx responses. Never pass secrets.
func SetError(ctx context.Context, msg string) {
	if s := StateFrom(ctx); s != nil {
		s.Error = msg
	}
}

// Requests wraps next so every request emits exactly one JSON line:
// msg="request" with request_id, method, path, status, duration_ms, bytes_out,
// remote_addr; level WARN for 4xx and ERROR (plus "error") for 5xx.
func Requests(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			id := r.Header.Get(HeaderRequestID)
			if id == "" {
				id = newRequestID()
			}
			state := &RequestState{RequestID: id}
			w.Header().Set(HeaderRequestID, id)
			rw := &responseWriter{ResponseWriter: w}
			ctx := context.WithValue(r.Context(), ctxKey{}, state)

			next.ServeHTTP(rw, r.WithContext(ctx))

			status := rw.status
			if status == 0 {
				status = http.StatusOK
			}
			attrs := []any{
				slog.String("request_id", id),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.Int64("bytes_out", rw.bytes),
				slog.String("remote_addr", r.RemoteAddr),
			}
			switch {
			case status >= 500:
				attrs = append(attrs, slog.String("error", state.Error))
				logger.LogAttrs(ctx, slog.LevelError, "request", toAttrs(attrs)...)
			case status >= 400:
				logger.LogAttrs(ctx, slog.LevelWarn, "request", toAttrs(attrs)...)
			default:
				logger.LogAttrs(ctx, slog.LevelInfo, "request", toAttrs(attrs)...)
			}
		})
	}
}

func toAttrs(in []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(in))
	for _, a := range in {
		out = append(out, a.(slog.Attr))
	}
	return out
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b[:])
}

// responseWriter records the status code and bytes written.
type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *responseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// Flush lets streaming handlers keep working behind the wrapper.
func (w *responseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
