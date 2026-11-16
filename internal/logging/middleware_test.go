package logging

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestRequests_EmitsOneJSONLinePerRequestWithRequiredFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWriter(&buf, "info")
	h := Requests(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestIDFrom(r.Context()) == "" {
			t.Error("request id missing from context")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if rec.Header().Get(HeaderRequestID) == "" {
		t.Fatal("response lacks X-Request-Id")
	}
	lines := decodeLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1: %s", len(lines), buf.String())
	}
	l := lines[0]
	for _, k := range []string{"time", "level", "msg", "request_id", "method", "path", "status", "duration_ms", "bytes_out", "remote_addr"} {
		if _, ok := l[k]; !ok {
			t.Errorf("missing field %q in %v", k, l)
		}
	}
	if l["msg"] != "request" || l["level"] != "INFO" || l["method"] != "GET" || l["path"] != "/api/v1/health" {
		t.Errorf("unexpected values: %v", l)
	}
	if l["status"].(float64) != 200 || l["bytes_out"].(float64) != 11 {
		t.Errorf("status/bytes wrong: %v", l)
	}
	if l["request_id"] != rec.Header().Get(HeaderRequestID) {
		t.Errorf("logged request_id differs from header")
	}
}

func TestRequests_EchoesIncomingRequestIDAndWarnsOn4xx(t *testing.T) {
	var buf bytes.Buffer
	h := Requests(NewWriter(&buf, "info"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	req.Header.Set(HeaderRequestID, "abc-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get(HeaderRequestID) != "abc-123" {
		t.Fatalf("request id not echoed: %q", rec.Header().Get(HeaderRequestID))
	}
	l := decodeLines(t, &buf)[0]
	if l["level"] != "WARN" || l["request_id"] != "abc-123" || l["status"].(float64) != 404 {
		t.Errorf("4xx line wrong: %v", l)
	}
	if _, has := l["error"]; has {
		t.Errorf("4xx line should not carry error field: %v", l)
	}
}

func TestRequests_ErrorsOn5xxWithErrorField(t *testing.T) {
	var buf bytes.Buffer
	h := Requests(NewWriter(&buf, "info"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetError(r.Context(), "db exploded")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/refresh", nil))
	l := decodeLines(t, &buf)[0]
	if l["level"] != "ERROR" || l["error"] != "db exploded" || l["status"].(float64) != 500 {
		t.Errorf("5xx line wrong: %v", l)
	}
}

func TestRequests_DefaultsStatusTo200WhenHandlerWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	h := Requests(NewWriter(&buf, "info"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	l := decodeLines(t, &buf)[0]
	if l["status"].(float64) != 200 || l["bytes_out"].(float64) != 0 {
		t.Errorf("empty handler line wrong: %v", l)
	}
}

func TestParseLevel_MapsNamesAndDefaultsToInfo(t *testing.T) {
	if ParseLevel("debug").String() != "DEBUG" || ParseLevel("WARN").String() != "WARN" || ParseLevel("error").String() != "ERROR" {
		t.Fatal("level names not mapped")
	}
	if ParseLevel("").String() != "INFO" || ParseLevel("bogus").String() != "INFO" {
		t.Fatal("default level should be INFO")
	}
	var buf bytes.Buffer
	NewWriter(&buf, "warn").Info("hidden")
	if buf.Len() != 0 {
		t.Fatal("info line emitted at warn level")
	}
}
