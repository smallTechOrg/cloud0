package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Password access (CLOUD0_PASSWORD, spec/api.md → Password access). Browsers
// sign in once on /login (one password field) and get a signed session
// cookie; scripts may send the password with HTTP Basic auth instead, the
// username ignored. No WWW-Authenticate challenge is ever sent, so the browser
// never shows its own username/password dialog.

const (
	sessionCookie = "cloud0_session"
	sessionTTL    = 30 * 24 * time.Hour
)

type passwordGate struct {
	want [32]byte // sha256 of the password: compared in constant time whatever the lengths
	key  []byte   // signs session cookies; derived from the password, so changing it signs everyone out
	now  func() time.Time
}

func newPasswordGate(password string, now func() time.Time) *passwordGate {
	key := sha256.Sum256([]byte("cloud0 session key\x00" + password))
	return &passwordGate{want: sha256.Sum256([]byte(password)), key: key[:], now: now}
}

func (g *passwordGate) matches(got string) bool {
	h := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(h[:], g.want[:]) == 1
}

// sessionValue is "<expiry unix seconds>.<base64url HMAC of it>".
func (g *passwordGate) sessionValue(exp time.Time) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	return e + "." + g.sign(e)
}

func (g *passwordGate) sign(exp string) string {
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte("cloud0-session|" + exp))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (g *passwordGate) validSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	exp, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	unix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || !g.now().Before(time.Unix(unix, 0)) {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(g.sign(exp)))
}

func (g *passwordGate) basicOK(r *http.Request) bool {
	_, pass, ok := r.BasicAuth()
	return ok && g.matches(pass)
}

// public is what the sign-in page itself needs: the page, its RSC payload
// (login.txt, login/*) and the static assets.
func public(r *http.Request) bool {
	p := r.URL.Path
	switch p {
	case "/login", "/login.html", "/login.txt":
		return true
	}
	return strings.HasPrefix(p, "/login/") || strings.HasPrefix(p, "/_next/")
}

func (g *passwordGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPrefix+"/login" {
			if r.Method != http.MethodPost {
				writeError(w, r, nil, ErrMethodNotAllowed())
				return
			}
			g.handleLogin(w, r)
			return
		}
		if public(r) || g.validSession(r) || g.basicOK(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			e := &APIError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "Sign in to cloud0 first."}
			writeError(w, r, nil, e.WithHint("Open cloud0 in a browser and enter the password, or send it with HTTP Basic auth and no username (curl -u :PASSWORD)."))
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	})
}

func (g *passwordGate) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password *string `json:"password"`
	}
	if aerr := decodeBody(w, r, &body); aerr != nil {
		writeError(w, r, nil, aerr)
		return
	}
	if body.Password == nil {
		writeError(w, r, nil, ErrInvalidRequest(`Send {"password": "..."}.`))
		return
	}
	if !g.matches(*body.Password) {
		e := &APIError{Status: http.StatusUnauthorized, Code: "invalid_password", Message: "That password is not right."}
		writeError(w, r, nil, e.WithHint("Check the CLOUD0_PASSWORD value and try again."))
		return
	}
	exp := g.now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    g.sessionValue(exp),
		Path:     "/",
		Expires:  exp,
		MaxAge:   int(sessionTTL / time.Second),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
