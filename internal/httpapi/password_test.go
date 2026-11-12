package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/smallTechOrg/cloud0/internal/config"
)

func TestRequirePassword_AsksForThePasswordAndLetsTheRightOneThrough(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := requirePassword("placeholder-password", next)
	cases := []struct {
		name, path, user, pass string
		sendAuth               bool
		want                   int
	}{
		{"no credentials, UI", "/", "", "", false, http.StatusUnauthorized},
		{"no credentials, API", "/api/v1/health", "", "", false, http.StatusUnauthorized},
		{"wrong password", "/api/v1/health", "cloud0", "wrong", true, http.StatusUnauthorized},
		{"password prefix only", "/", "cloud0", "placeholder", true, http.StatusUnauthorized},
		{"right password, any username", "/", "anyone", "placeholder-password", true, http.StatusTeapot},
		{"right password, empty username", "/api/v1/health", "", "placeholder-password", true, http.StatusTeapot},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			if c.sendAuth {
				req.SetBasicAuth(c.user, c.pass)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d", rec.Code, c.want)
			}
			if c.want == http.StatusUnauthorized && !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Basic ") {
				t.Errorf("WWW-Authenticate = %q, want a Basic challenge so the browser asks", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

func TestRequirePassword_APIMissAnswersTheJSONEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	requirePassword("placeholder-password", http.NotFoundHandler()).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/cost/table", nil))
	var body struct {
		Error APIError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the JSON envelope: %q: %v", rec.Body.String(), err)
	}
	if body.Error.Code != "unauthorized" || body.Error.Hint == nil {
		t.Errorf("error = %+v, want code unauthorized with a hint", body.Error)
	}
}

func TestNewHandler_AsksForAPasswordOnlyWhenOneIsConfigured(t *testing.T) {
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, c := range []struct {
		password string
		want     int
	}{{"", http.StatusOK}, {"placeholder-password", http.StatusUnauthorized}} {
		h := NewHandler(Deps{
			Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
			UI:     ui,
			Config: config.Config{Password: c.password},
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != c.want {
			t.Errorf("password set=%v: GET / = %d, want %d", c.password != "", rec.Code, c.want)
		}
	}
}
