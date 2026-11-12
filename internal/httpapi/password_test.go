package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/config"
)

const testPassword = "placeholder-password"

var gateNow = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

// passThrough marks requests the gate let through.
var passThrough = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })

func gateAt(password string, now time.Time) http.Handler {
	return newPasswordGate(password, func() time.Time { return now }).wrap(passThrough)
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) APIError {
	t.Helper()
	var body struct {
		Error APIError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the JSON envelope: %q: %v", rec.Body.String(), err)
	}
	return body.Error
}

func login(t *testing.T, h http.Handler, password string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"password":`+jsonString(password)+`}`))
	req.Header.Set("Content-Type", "application/json")
	return serve(h, req)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func sessionFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", sessionCookie, rec.Header()["Set-Cookie"])
	return nil
}

func TestPasswordGate_PageWithoutSessionRedirectsToLogin(t *testing.T) {
	rec := serve(gateAt(testPassword, gateNow), httptest.NewRequest(http.MethodGet, "/alerts?week=2026-W45", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got, want := rec.Header().Get("Location"), "/login?next=%2Falerts%3Fweek%3D2026-W45"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if v := rec.Header().Get("WWW-Authenticate"); v != "" {
		t.Errorf("WWW-Authenticate = %q: the browser would show its own dialog", v)
	}
}

func TestPasswordGate_APIWithoutSessionAnswersTheUnauthorizedEnvelope(t *testing.T) {
	rec := serve(gateAt(testPassword, gateNow), httptest.NewRequest(http.MethodGet, "/api/v1/cost/table", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if e := errorCode(t, rec); e.Code != "unauthorized" || e.Hint == nil {
		t.Errorf("error = %+v, want code unauthorized with a hint", e)
	}
	if v := rec.Header().Get("WWW-Authenticate"); v != "" {
		t.Errorf("WWW-Authenticate = %q: the browser would show its own dialog", v)
	}
}

func TestPasswordGate_TheLoginPageAndItsAssetsArePublic(t *testing.T) {
	h := gateAt(testPassword, gateNow)
	for _, p := range []string{"/login", "/login.html", "/login.txt", "/login/__next.login", "/_next/static/chunks/app.js"} {
		if rec := serve(h, httptest.NewRequest(http.MethodGet, p, nil)); rec.Code != http.StatusTeapot {
			t.Errorf("GET %s = %d, want it served without a session", p, rec.Code)
		}
	}
}

func TestPasswordGate_LoginSetsASessionThatLetsRequestsThrough(t *testing.T) {
	h := gateAt(testPassword, gateNow)
	rec := login(t, h, testPassword)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("login = %d %q, want 200 {\"ok\":true}", rec.Code, rec.Body.String())
	}
	c := sessionFrom(t, rec)
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure {
		t.Errorf("cookie flags: HttpOnly=%v SameSite=%v Path=%q Secure=%v (plain HTTP)", c.HttpOnly, c.SameSite, c.Path, c.Secure)
	}
	if !c.Expires.Equal(gateNow.Add(sessionTTL).Truncate(time.Second)) {
		t.Errorf("cookie expires %v, want %v", c.Expires, gateNow.Add(sessionTTL))
	}
	for _, p := range []string{"/", "/api/v1/health"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.AddCookie(c)
		if rec := serve(h, req); rec.Code != http.StatusTeapot {
			t.Errorf("GET %s with the session = %d, want it let through", p, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"password":"`+testPassword+`"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	if c := sessionFrom(t, serve(h, req)); !c.Secure {
		t.Errorf("cookie over HTTPS (X-Forwarded-Proto) is not Secure")
	}
}

func TestPasswordGate_LoginRejectsAWrongPasswordAndBadBodies(t *testing.T) {
	h := gateAt(testPassword, gateNow)
	rec := login(t, h, "wrong")
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec).Code != "invalid_password" {
		t.Errorf("wrong password = %d %q, want 401 invalid_password", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Errorf("a wrong password set cookies: %v", rec.Header()["Set-Cookie"])
	}
	for _, body := range []string{`{}`, `not json`, `{"password":"x","user":"y"}`} {
		rec := serve(h, httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, rec.Code)
		}
	}
	if rec := serve(h, httptest.NewRequest(http.MethodGet, "/api/v1/login", nil)); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/v1/login = %d, want 405", rec.Code)
	}
}

func TestPasswordGate_SessionsExpireAndCannotBeForged(t *testing.T) {
	c := sessionFrom(t, login(t, gateAt(testPassword, gateNow), testPassword))
	tampered := *c
	tampered.Value = c.Value[:len(c.Value)-2] + "xx"
	cases := []struct {
		name   string
		gate   http.Handler
		cookie *http.Cookie
	}{
		{"expired", gateAt(testPassword, gateNow.Add(sessionTTL+time.Second)), c},
		{"password changed", gateAt("another-password", gateNow), c},
		{"tampered signature", gateAt(testPassword, gateNow), &tampered},
		{"garbage", gateAt(testPassword, gateNow), &http.Cookie{Name: sessionCookie, Value: "9999999999"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
			req.AddCookie(tc.cookie)
			if rec := serve(tc.gate, req); rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestPasswordGate_BasicAuthIgnoresTheUsername(t *testing.T) {
	h := gateAt(testPassword, gateNow)
	for _, c := range []struct {
		user, pass string
		want       int
	}{
		{"", testPassword, http.StatusTeapot},
		{"anyone", testPassword, http.StatusTeapot},
		{"", "wrong", http.StatusUnauthorized},
		{"", "placeholder", http.StatusUnauthorized},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.SetBasicAuth(c.user, c.pass)
		if rec := serve(h, req); rec.Code != c.want {
			t.Errorf("basic %q:%q = %d, want %d", c.user, c.pass, rec.Code, c.want)
		}
	}
}

func TestNewHandler_AsksForAPasswordOnlyWhenOneIsConfigured(t *testing.T) {
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := func(password string) http.Handler {
		return NewHandler(Deps{
			Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
			UI:     ui,
			Config: config.Config{Password: password},
		})
	}
	if rec := serve(handler(""), httptest.NewRequest(http.MethodGet, "/", nil)); rec.Code != http.StatusOK {
		t.Errorf("no password: GET / = %d, want 200", rec.Code)
	}
	if rec := serve(handler(""), httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{}`))); rec.Code != http.StatusNotFound {
		t.Errorf("no password: POST /api/v1/login = %d, want 404 (no login without a password)", rec.Code)
	}
	if rec := serve(handler(testPassword), httptest.NewRequest(http.MethodGet, "/", nil)); rec.Code != http.StatusSeeOther {
		t.Errorf("password set: GET / = %d, want 303 to /login", rec.Code)
	}
}
