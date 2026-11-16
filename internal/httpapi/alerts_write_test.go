package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/alerting"
)

const secretWebhook = "https://hooks.example.com/services/T000/B000/SECRETSECRET"

type fakeEngine struct {
	prevReq  alerting.PreviewRequest
	applyReq alerting.ApplyRequest
	prevErr  error
	applyErr error
	calls    int
}

func (f *fakeEngine) Preview(_ context.Context, req alerting.PreviewRequest) (alerting.Preview, error) {
	f.calls++
	f.prevReq = req
	if f.prevErr != nil {
		return alerting.Preview{}, f.prevErr
	}
	return alerting.Preview{PreviewToken: "pv_abc", ExpiresAt: time.Date(2026, 10, 27, 9, 10, 0, 0, time.UTC),
		Kind: req.Kind, Action: req.Action, Target: req.Target, Desired: json.RawMessage(`{"displayName":"x"}`)}, nil
}

func (f *fakeEngine) Apply(_ context.Context, req alerting.ApplyRequest) (alerting.ApplyResult, error) {
	f.calls++
	f.applyReq = req
	if f.applyErr != nil {
		return alerting.ApplyResult{}, f.applyErr
	}
	return alerting.ApplyResult{OK: true, Kind: "policy", Action: "delete", Target: "projects/proj-a/alertPolicies/1", ChangeHistoryID: 3}, nil
}

type fakeAdopter struct {
	req alerting.AdoptRequest
	err error
}

func (f *fakeAdopter) Preview(_ context.Context, req alerting.AdoptRequest) (alerting.AdoptPreview, error) {
	f.req = req
	if f.err != nil {
		return alerting.AdoptPreview{}, f.err
	}
	return alerting.AdoptPreview{Shape: req.Shape, Replaces: []string{req.Target}}, nil
}

func alertsWriteServer(t *testing.T, deps *AlertsDeps) (http.Handler, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	h := NewHandler(Deps{Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Alerts: deps})
	return h, logs
}

func postAlerts(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var b struct {
		Error struct {
			Code string  `json:"code"`
			Hint *string `json:"hint"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("not an error envelope: %s", rec.Body.String())
	}
	hint := ""
	if b.Error.Hint != nil {
		hint = *b.Error.Hint
	}
	return b.Error.Code, hint
}

func TestAlertsPreviewHappy(t *testing.T) {
	eng := &fakeEngine{}
	h, _ := alertsWriteServer(t, &AlertsDeps{Writable: false, Engine: eng})
	rec := postAlerts(h, "/alerts/preview", `{"kind":"policy","action":"create","project":"proj-a","template_id":"vm_cpu_high","params":{"threshold":80}}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var p map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p["preview_token"] != "pv_abc" || p["current"] != nil || p["warnings"] == nil {
		t.Fatalf("bad body %v", p)
	}
	if eng.prevReq.TemplateID != "vm_cpu_high" || !strings.Contains(string(eng.prevReq.Params), "80") {
		t.Fatalf("request not delegated: %+v", eng.prevReq)
	}
}

func TestAlertsPreviewValidation(t *testing.T) {
	eng := &fakeEngine{}
	h, _ := alertsWriteServer(t, &AlertsDeps{Writable: true, Engine: eng})
	cases := map[string]struct{ body, code string }{
		"malformed":         {`{"kind":`, "invalid_request"},
		"unknown field":     {`{"kind":"policy","action":"create","bogus":1}`, "invalid_request"},
		"trailing":          {`{"kind":"policy"} {}`, "invalid_request"},
		"empty":             {``, "invalid_request"},
		"bad kind":          {`{"kind":"nope","action":"create"}`, "invalid_params"},
		"bad action":        {`{"kind":"snooze","action":"delete","target":"x"}`, "invalid_params"},
		"enable on budget":  {`{"kind":"budget","action":"enable","target":"x"}`, "invalid_params"},
		"delete no target":  {`{"kind":"policy","action":"delete"}`, "invalid_params"},
		"update no target":  {`{"kind":"policy","action":"update","params":{}}`, "invalid_params"},
		"create no tpl":     {`{"kind":"policy","action":"create","project":"p"}`, "invalid_params"},
		"create no proj":    {`{"kind":"policy","action":"create","template_id":"vm_cpu_high"}`, "invalid_params"},
		"both":              {`{"kind":"policy","action":"update","target":"projects/p/alertPolicies/1","params":{},"desired":{}}`, "invalid_params"},
		"desired on uptime": {`{"kind":"uptime_check","action":"update","target":"t","desired":{}}`, "invalid_params"},
		"channel no params": {`{"kind":"channel","action":"create"}`, "invalid_params"},
	}
	for name, c := range cases {
		rec := postAlerts(h, "/alerts/preview", c.body)
		code, _ := errCode(t, rec)
		if rec.Code != 400 || code != c.code {
			t.Errorf("%s: got %d %s, want 400 %s", name, rec.Code, code, c.code)
		}
	}
	if eng.calls != 0 {
		t.Fatalf("engine called %d times for invalid requests", eng.calls)
	}
}

func TestAlertsWriteContentTypeAndSize(t *testing.T) {
	eng := &fakeEngine{}
	h, _ := alertsWriteServer(t, &AlertsDeps{Writable: true, Engine: eng})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/apply", strings.NewReader(`{"preview_token":"pv_a"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if code, _ := errCode(t, rec); rec.Code != 400 || code != "invalid_request" {
		t.Fatalf("content type: %d %s", rec.Code, rec.Body)
	}
	big := fmt.Sprintf(`{"kind":"policy","action":"update","target":"t","desired":{"a":"%s"}}`, strings.Repeat("x", 300<<10))
	rec = postAlerts(h, "/alerts/preview", big)
	if code, _ := errCode(t, rec); rec.Code != 400 || code != "invalid_request" {
		t.Fatalf("oversize: %d %.200s", rec.Code, rec.Body)
	}
	mid := fmt.Sprintf(`{"kind":"policy","action":"update","target":"t","desired":{"a":"%s"}}`, strings.Repeat("x", 100<<10))
	rec = postAlerts(h, "/alerts/preview", mid)
	if code, _ := errCode(t, rec); rec.Code != 400 || code != "invalid_params" {
		t.Fatalf("params size: %d %.200s", rec.Code, rec.Body)
	}
	if eng.calls != 0 {
		t.Fatal("engine must not be called")
	}
}

func TestAlertsApplyHappyAndNullResource(t *testing.T) {
	eng := &fakeEngine{}
	h, _ := alertsWriteServer(t, &AlertsDeps{Writable: true, Engine: eng})
	rec := postAlerts(h, "/alerts/apply", `{"preview_token":"pv_abc","confirm_delete":true}`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !eng.applyReq.ConfirmDelete || eng.applyReq.PreviewToken != "pv_abc" {
		t.Fatalf("not delegated: %+v", eng.applyReq)
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if v, ok := m["resource"]; !ok || v != nil || m["ok"] != true {
		t.Fatalf("resource must be null: %v", m)
	}
}

func TestAlertsApplyErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
		hint   string
	}{
		{"stale", fmt.Errorf("%w: live changed", alerting.ErrPreviewStale), 409, "preview_stale", "Run preview again and review the new diff."},
		{"confirm", alerting.ErrConfirmRequired, 400, "confirm_required", "Resend with confirm_delete: true after reviewing the delete preview."},
		{"denied", alerting.ErrPermissionDenied, 403, "permission_denied", "roles/monitoring."},
		{"upstream", fmt.Errorf("%w: status 500", alerting.ErrUpstream), 502, "upstream_error", ""},
		{"disabled", alerting.ErrAPIDisabled, 503, "api_disabled", "gcloud services enable"},
		{"notconf", alerting.ErrNotConfigured, 503, "alerts_not_configured", "CLOUD0_ALERTS_CREDENTIALS"},
		{"notfound", alerting.ErrNotFound, 404, "not_found", ""},
	}
	for _, c := range cases {
		h, _ := alertsWriteServer(t, &AlertsDeps{Writable: true, Engine: &fakeEngine{applyErr: c.err}})
		rec := postAlerts(h, "/alerts/apply", `{"preview_token":"pv_abc"}`)
		code, hint := errCode(t, rec)
		if rec.Code != c.status || code != c.code || !strings.Contains(hint, c.hint) {
			t.Errorf("%s: got %d %s %q", c.name, rec.Code, code, hint)
		}
	}
	h, _ := alertsWriteServer(t, &AlertsDeps{Writable: true, Engine: &fakeEngine{applyErr: fmt.Errorf("boom internal detail")}})
	rec := postAlerts(h, "/alerts/apply", `{"preview_token":"pv_abc"}`)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("500 must not leak cause: %d %s", rec.Code, rec.Body)
	}
	rec = postAlerts(h, "/alerts/apply", `{"preview_token":"abc"}`)
	if code, _ := errCode(t, rec); rec.Code != 400 || code != "invalid_params" {
		t.Fatalf("token validation: %d %s", rec.Code, rec.Body)
	}
}

func TestAlertsPermissionHintNamesRole(t *testing.T) {
	h, _ := alertsWriteServer(t, &AlertsDeps{Writable: true, Engine: &fakeEngine{prevErr: alerting.ErrPermissionDenied}})
	rec := postAlerts(h, "/alerts/preview", `{"kind":"uptime_check","action":"delete","target":"projects/proj-a/uptimeCheckConfigs/abc"}`)
	_, hint := errCode(t, rec)
	if rec.Code != 403 || !strings.Contains(hint, "roles/monitoring.uptimeCheckConfigEditor") || !strings.Contains(hint, "proj-a") {
		t.Fatalf("%d hint %q", rec.Code, hint)
	}
}

func TestAlertsNotConfigured(t *testing.T) {
	bodies := map[string]string{
		"/alerts/preview":       `{"kind":"policy","action":"delete","target":"t"}`,
		"/alerts/apply":         `{"preview_token":"pv_a"}`,
		"/alerts/adopt/preview": `{"shape":"zero_cpu_spike"}`,
	}
	for name, deps := range map[string]*AlertsDeps{
		"nil alerts": nil,
		"nil engine": {Writable: true},
		"empty deps": {},
	} {
		h, _ := alertsWriteServer(t, deps)
		for path, body := range bodies {
			rec := postAlerts(h, path, body)
			code, hint := errCode(t, rec)
			if rec.Code != 503 || code != "alerts_not_configured" || !strings.Contains(hint, "CLOUD0_ALERTS_CREDENTIALS") {
				t.Errorf("%s %s: %d %s", name, path, rec.Code, rec.Body)
			}
		}
	}
	// Reader-only: apply and budget previews are refused, policy previews work.
	eng := &fakeEngine{}
	h, _ := alertsWriteServer(t, &AlertsDeps{Writable: false, Engine: eng, Adopter: &fakeAdopter{}})
	if rec := postAlerts(h, "/alerts/apply", `{"preview_token":"pv_a"}`); rec.Code != 503 {
		t.Fatalf("apply read-only: %d", rec.Code)
	}
	if rec := postAlerts(h, "/alerts/preview", `{"kind":"budget","action":"delete","target":"b"}`); rec.Code != 503 {
		t.Fatalf("budget preview read-only: %d", rec.Code)
	}
	if rec := postAlerts(h, "/alerts/adopt/preview", `{"shape":"budget_account"}`); rec.Code != 503 {
		t.Fatalf("budget adopt read-only: %d", rec.Code)
	}
	if rec := postAlerts(h, "/alerts/preview", `{"kind":"policy","action":"disable","target":"t"}`); rec.Code != 200 {
		t.Fatalf("policy preview read-only: %d", rec.Code)
	}
	if eng.calls != 1 {
		t.Fatalf("engine calls %d", eng.calls)
	}
}

func TestAlertsAdoptPreview(t *testing.T) {
	ad := &fakeAdopter{}
	h, _ := alertsWriteServer(t, &AlertsDeps{Writable: true, Engine: &fakeEngine{}, Adopter: ad})
	rec := postAlerts(h, "/alerts/adopt/preview", `{"shape":"zero_cpu_spike","target":"projects/proj-a/alertPolicies/222"}`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	for _, k := range []string{"previews", "follow_up", "notes", "replaces"} {
		if _, ok := m[k].([]any); !ok {
			t.Errorf("%s must be a list: %v", k, m[k])
		}
	}
	if ad.req.Target != "projects/proj-a/alertPolicies/222" {
		t.Fatalf("not delegated: %+v", ad.req)
	}
	if rec := postAlerts(h, "/alerts/adopt/preview", `{"shape":"weird"}`); rec.Code != 400 {
		t.Fatalf("bad shape: %d", rec.Code)
	}
	if rec := postAlerts(h, "/alerts/adopt/preview", `{"shape":"zero_cpu_spike","x":1}`); rec.Code != 400 {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	h, _ = alertsWriteServer(t, &AlertsDeps{Writable: true, Engine: &fakeEngine{}, Adopter: &fakeAdopter{err: alerting.ErrNotFound}})
	if rec := postAlerts(h, "/alerts/adopt/preview", `{"shape":"high_memory_load"}`); rec.Code != 404 {
		t.Fatalf("adopter not found: %d", rec.Code)
	}
}

func TestAlertsSecretsNeverLoggedOrEchoed(t *testing.T) {
	leaky := fmt.Errorf("%w: POST %s failed: token=abc123SECRET", alerting.ErrUpstream, secretWebhook)
	eng := &fakeEngine{prevErr: leaky, applyErr: leaky}
	h, logs := alertsWriteServer(t, &AlertsDeps{Writable: true, Engine: eng, Adopter: &fakeAdopter{err: leaky}})
	body := `{"kind":"channel","action":"create","params":{"type":"webhook_tokenauth","display_name":"hook","labels":{},"sensitive_labels":{"url":"` + secretWebhook + `"}}}`
	for _, c := range []struct{ path, body string }{
		{"/alerts/preview", body},
		{"/alerts/apply", `{"preview_token":"pv_abc"}`},
		{"/alerts/adopt/preview", `{"shape":"zero_cpu_spike"}`},
		{"/alerts/preview", `{"kind":"channel","action":"create","params":{"url":"` + secretWebhook + `"},"bogus":true}`},
		{"/alerts/preview", `{"kind":"https://hooks.example.com/services/T000/B000/SECRETSECRET","action":"create"}`},
	} {
		rec := postAlerts(h, c.path, c.body)
		for _, s := range []string{"SECRETSECRET", "hooks.example.com", "abc123SECRET"} {
			if strings.Contains(rec.Body.String(), s) || strings.Contains(logs.String(), s) {
				t.Fatalf("%s leaked in %s\nresponse: %s\nlogs: %s", s, c.path, rec.Body, logs)
			}
		}
	}
	if !strings.Contains(logs.String(), "alerts.preview.failed") {
		t.Fatalf("expected a failure log line: %s", logs)
	}
}

func TestRedactSecrets(t *testing.T) {
	got := redactSecrets("call https://a.example/x?k=1 failed authorization: Bearer abc password=hunter2")
	for _, bad := range []string{"a.example", "abc", "hunter2"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q leaked in %q", bad, got)
		}
	}
}
