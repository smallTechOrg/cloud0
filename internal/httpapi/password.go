package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// requirePassword puts every request behind HTTP Basic auth when
// CLOUD0_PASSWORD is set: any username, that password. The browser asks once
// and resends it on the UI's same-origin API calls, so the UI needs no login
// page. A miss answers 401 with WWW-Authenticate (the JSON envelope under
// /api/, plain text elsewhere).
func requirePassword(password string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, got, ok := r.BasicAuth()
		// Hashing first keeps the comparison constant-time whatever the lengths.
		if h := sha256.Sum256([]byte(got)); ok && subtle.ConstantTimeCompare(h[:], want[:]) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="cloud0", charset="UTF-8"`)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			e := &APIError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "This cloud0 needs a password."}
			writeError(w, r, nil, e.WithHint("Open cloud0 in a browser and enter the password, or send it with HTTP Basic auth (any username)."))
			return
		}
		http.Error(w, "cloud0 needs a password.", http.StatusUnauthorized)
	})
}
