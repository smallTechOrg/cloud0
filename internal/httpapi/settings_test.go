package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/store"
)

func TestSettings_DefaultsThenPutRoundTripSurvivesReopen(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{}, &now)

	res, m, raw := f.doJSON(t, http.MethodGet, "/api/v1/settings", "")
	if res.StatusCode != 200 || num(m["anomaly_pct_threshold"]) != 20 || num(m["anomaly_abs_threshold_usd"]) != 50 || m["updated_at"] == nil {
		t.Fatalf("defaults = %d %s", res.StatusCode, raw)
	}
	if len(m) != 3 {
		t.Fatalf("settings body must have exactly 3 fields: %s", raw)
	}

	now = now.Add(time.Minute)
	res, m, _ = f.doJSON(t, http.MethodPut, "/api/v1/settings", `{"anomaly_pct_threshold":10,"anomaly_abs_threshold_usd":25}`)
	if res.StatusCode != 200 || num(m["anomaly_pct_threshold"]) != 10 || num(m["anomaly_abs_threshold_usd"]) != 25 || m["updated_at"] != "2026-10-26T09:01:00Z" {
		t.Fatalf("put = %d %v", res.StatusCode, m)
	}
	_, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/settings", "")
	if num(m["anomaly_pct_threshold"]) != 10 || num(m["anomaly_abs_threshold_usd"]) != 25 {
		t.Fatalf("get after put = %v", m)
	}
	_, tbl, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table", "")
	if th := tbl["thresholds"].(map[string]any); num(th["pct"]) != 10 || num(th["abs_usd"]) != 25 {
		t.Fatalf("table thresholds = %v", th)
	}
	// Zero is allowed (flags any non-zero change).
	if res, _, _ = f.doJSON(t, http.MethodPut, "/api/v1/settings", `{"anomaly_pct_threshold":0,"anomaly_abs_threshold_usd":0}`); res.StatusCode != 200 {
		t.Fatalf("zero thresholds must be accepted: %d", res.StatusCode)
	}
	if res, _, _ = f.doJSON(t, http.MethodPut, "/api/v1/settings", `{"anomaly_pct_threshold":10,"anomaly_abs_threshold_usd":25}`); res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}

	// State survival: a new process over the same DB file sees 10 / 25.
	path := f.store.Path()
	f.srv.Close()
	_ = f.store.Close()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f2 := newFixtureOn(t, st, testConfig(t), &fakeSource{}, &now)
	_, m, _ = f2.doJSON(t, http.MethodGet, "/api/v1/settings", "")
	if num(m["anomaly_pct_threshold"]) != 10 || num(m["anomaly_abs_threshold_usd"]) != 25 {
		t.Fatalf("settings after reopen = %v", m)
	}
}

func TestSettings_PutRejectsInvalidValues(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{}, &now)
	cases := []struct{ name, body, code string }{
		{"negative pct", `{"anomaly_pct_threshold":-1,"anomaly_abs_threshold_usd":25}`, "invalid_settings"},
		{"negative abs", `{"anomaly_pct_threshold":10,"anomaly_abs_threshold_usd":-0.5}`, "invalid_settings"},
		{"missing abs", `{"anomaly_pct_threshold":10}`, "invalid_settings"},
		{"missing both", `{}`, "invalid_settings"},
		{"null value", `{"anomaly_pct_threshold":null,"anomaly_abs_threshold_usd":25}`, "invalid_settings"},
		{"string value", `{"anomaly_pct_threshold":"10","anomaly_abs_threshold_usd":25}`, "invalid_request"},
		{"unknown field", `{"anomaly_pct_threshold":10,"anomaly_abs_threshold_usd":25,"extra":1}`, "invalid_request"},
		{"bad json", `{"anomaly_pct_threshold":`, "invalid_request"},
		{"empty body", ``, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, m, _ := f.doJSON(t, http.MethodPut, "/api/v1/settings", tc.body)
			expectError(t, res, m, 400, tc.code)
		})
	}
	_, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/settings", "")
	if num(m["anomaly_pct_threshold"]) != 20 || num(m["anomaly_abs_threshold_usd"]) != 50 {
		t.Fatalf("rejected PUTs must not change settings: %v", m)
	}
	res, m, _ := f.doJSON(t, http.MethodDelete, "/api/v1/settings", "")
	expectError(t, res, m, 405, "method_not_allowed")
}
