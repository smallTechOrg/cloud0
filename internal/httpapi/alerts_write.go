package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strings"

	"github.com/smallTechOrg/cloud0/internal/alerting"
)

const (
	// maxAlertsBodyBytes caps every alerts write request body.
	maxAlertsBodyBytes = 256 << 10
	// maxAlertsParamsBytes caps the params/desired object inside a request.
	maxAlertsParamsBytes = 64 << 10

	alertsNotConfiguredHint = "Set CLOUD0_ALERTS_CREDENTIALS to the cloud0-alerts service-account key path and restart — see README → Alerts setup"
)

// registerAlertsWrite wires the preview / apply / adopt endpoints of spec/api.md.
// Channel, uptime, budget and snooze writes all go through preview + apply
// with a kind; there is no separate channel preview route.
func (d Deps) registerAlertsWrite(r *router) {
	r.handle(http.MethodPost, apiPrefix+"/alerts/preview", d.handleAlertsPreview)
	r.handle(http.MethodPost, apiPrefix+"/alerts/apply", d.handleAlertsApply)
	r.handle(http.MethodPost, apiPrefix+"/alerts/adopt/preview", d.handleAlertsAdoptPreview)
}

func errInvalidParams(msg string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_params", Message: msg}
}

// decodeAlertsBody reads exactly one JSON object (max 256 KiB, JSON content
// type, no unknown fields, no trailing data). Error messages never echo the body.
func decodeAlertsBody(w http.ResponseWriter, r *http.Request, v any) *APIError {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return ErrInvalidRequest("Content-Type must be application/json.").
			WithHint("Send the body as JSON with the header Content-Type: application/json.")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAlertsBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return ErrInvalidRequest("The request body is larger than 256 KiB.").
				WithHint("Send a smaller body; template params and drafts are a few hundred bytes.")
		}
		return ErrInvalidRequest("The request body is not valid JSON for this endpoint.").
			WithHint("Send a single JSON object with the documented fields (see spec/api.md).")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return ErrInvalidRequest("The request body is larger than 256 KiB.")
		}
		return ErrInvalidRequest("The request body must contain exactly one JSON object.")
	}
	return nil
}

func (d Deps) alertsEngine() (alerting.Engine, *APIError) {
	if d.Alerts == nil || d.Alerts.Engine == nil {
		return nil, errAlertsNotConfigured()
	}
	return d.Alerts.Engine, nil
}

func isJSONAbsent(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func validPreviewActions(kind string) []string {
	switch kind {
	case alerting.TargetPolicy:
		return []string{alerting.ActionCreate, alerting.ActionUpdate, alerting.ActionDelete, alerting.ActionEnable, alerting.ActionDisable}
	case alerting.TargetUptimeCheck, alerting.TargetBudget:
		return []string{alerting.ActionCreate, alerting.ActionUpdate, alerting.ActionDelete}
	case alerting.TargetSnooze:
		return []string{alerting.ActionCreate, alerting.ActionEnd}
	case alerting.TargetChannel:
		return []string{alerting.ActionCreate, alerting.ActionDelete}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// validatePreviewRequest applies the structural rules of spec/api.md; value
// ranges of template params are the engine's job.
func validatePreviewRequest(req *alerting.PreviewRequest) *APIError {
	actions := validPreviewActions(req.Kind)
	if actions == nil {
		return errInvalidParams("kind must be one of policy, uptime_check, budget, snooze, channel.")
	}
	if !contains(actions, req.Action) {
		return errInvalidParams("action " + quoteShort(req.Action) + " is not valid for kind " + req.Kind + "; use one of " + strings.Join(actions, ", ") + ".")
	}
	if isJSONAbsent(req.Params) {
		req.Params = nil
	}
	if isJSONAbsent(req.Desired) {
		req.Desired = nil
	}
	if req.Params != nil && req.Desired != nil {
		return errInvalidParams("Send exactly one of params or desired, not both.")
	}
	if len(req.Params) > maxAlertsParamsBytes || len(req.Desired) > maxAlertsParamsBytes {
		return errInvalidParams("params/desired is larger than 64 KiB.")
	}
	if req.Action != alerting.ActionCreate && strings.TrimSpace(req.Target) == "" {
		return errInvalidParams("target is required for action " + req.Action + ".")
	}
	if req.Action == alerting.ActionCreate {
		switch req.Kind {
		case alerting.TargetPolicy:
			if strings.TrimSpace(req.Project) == "" {
				return errInvalidParams("project is required to create a policy.")
			}
			if req.Desired == nil && strings.TrimSpace(req.TemplateID) == "" {
				return errInvalidParams("template_id is required to create a policy.")
			}
		case alerting.TargetUptimeCheck, alerting.TargetSnooze:
			if strings.TrimSpace(req.Project) == "" && req.Params == nil {
				return errInvalidParams("project is required to create a " + req.Kind + ".")
			}
		}
		if req.Kind != alerting.TargetPolicy && req.Params == nil {
			return errInvalidParams("params is required to create a " + req.Kind + ".")
		}
	}
	if req.Action == alerting.ActionUpdate && req.Params == nil && req.Desired == nil {
		return errInvalidParams("params (managed resource) or desired (unmanaged policy) is required for update.")
	}
	if req.Desired != nil && req.Kind != alerting.TargetPolicy {
		return errInvalidParams("desired is only accepted for kind policy.")
	}
	return nil
}

// quoteShort echoes a short, sanitised user value in a message.
func quoteShort(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	return `"` + redactSecrets(s) + `"`
}

func (d Deps) handleAlertsPreview(w http.ResponseWriter, r *http.Request) *APIError {
	var req alerting.PreviewRequest
	if aerr := decodeAlertsBody(w, r, &req); aerr != nil {
		return aerr
	}
	eng, aerr := d.alertsEngine()
	if aerr != nil {
		return aerr
	}
	if aerr := validatePreviewRequest(&req); aerr != nil {
		return aerr
	}
	// Budget previews read the billing account and need the alerts credentials.
	if req.Kind == alerting.TargetBudget && !d.Alerts.Writable {
		return errAlertsNotConfigured()
	}
	p, err := eng.Preview(r.Context(), req)
	if err != nil {
		d.logAlertsWrite(r, "alerts.preview.failed", req.Kind, req.Action, req.Target, err)
		return mapAlertsError(err, req.Kind, req.Project, req.Target)
	}
	d.logAlertsWrite(r, "alerts.preview", req.Kind, req.Action, req.Target, nil)
	writeJSON(w, http.StatusOK, normalizePreview(p))
	return nil
}

func (d Deps) handleAlertsApply(w http.ResponseWriter, r *http.Request) *APIError {
	var req alerting.ApplyRequest
	if aerr := decodeAlertsBody(w, r, &req); aerr != nil {
		return aerr
	}
	eng, aerr := d.alertsEngine()
	if aerr != nil {
		return aerr
	}
	if !d.Alerts.Writable {
		return errAlertsNotConfigured()
	}
	req.PreviewToken = strings.TrimSpace(req.PreviewToken)
	if !strings.HasPrefix(req.PreviewToken, "pv_") || len(req.PreviewToken) > 128 {
		return errInvalidParams("preview_token is required and must come from a preview response.")
	}
	res, err := eng.Apply(r.Context(), req)
	if err != nil {
		d.logAlertsWrite(r, "alerts.apply.failed", "", "", "", err)
		return mapAlertsError(err, "", "", "")
	}
	d.logAlertsWrite(r, "alerts.apply", res.Kind, res.Action, res.Target, nil)
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	if len(res.Resource) == 0 {
		res.Resource = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (d Deps) handleAlertsAdoptPreview(w http.ResponseWriter, r *http.Request) *APIError {
	var req alerting.AdoptRequest
	if aerr := decodeAlertsBody(w, r, &req); aerr != nil {
		return aerr
	}
	if d.Alerts == nil || d.Alerts.Adopter == nil {
		return errAlertsNotConfigured()
	}
	switch req.Shape {
	case alerting.ShapeZeroCPUSpike, alerting.ShapeHighMemory, alerting.ShapeUptimeBackend, alerting.ShapeUptimeTwoHours, alerting.ShapeBudgetBoilerplate:
	default:
		return errInvalidParams("shape must be one of zero_cpu_spike, high_memory_load, uptime_backend_down, uptime_down_2h, budget_account.")
	}
	if req.Shape == alerting.ShapeBudgetBoilerplate && !d.Alerts.Writable {
		return errAlertsNotConfigured()
	}
	ap, err := d.Alerts.Adopter.Preview(r.Context(), req)
	if err != nil {
		d.logAlertsWrite(r, "alerts.adopt.failed", "adopt:"+req.Shape, "", req.Target, err)
		return mapAlertsError(err, adoptKind(req.Shape), "", req.Target)
	}
	d.logAlertsWrite(r, "alerts.adopt", "adopt:"+req.Shape, "", req.Target, nil)
	if ap.Replaces == nil {
		ap.Replaces = []string{}
	}
	if ap.Previews == nil {
		ap.Previews = []alerting.Preview{}
	}
	for i := range ap.Previews {
		ap.Previews[i] = normalizePreview(ap.Previews[i])
	}
	if ap.FollowUp == nil {
		ap.FollowUp = []alerting.AdoptFollowUp{}
	}
	if ap.Notes == nil {
		ap.Notes = []string{}
	}
	writeJSON(w, http.StatusOK, ap)
	return nil
}

func adoptKind(shape string) string {
	switch shape {
	case alerting.ShapeBudgetBoilerplate:
		return alerting.TargetBudget
	}
	return alerting.TargetPolicy
}

// normalizePreview keeps the wire shape stable: lists are [] and absent
// current/desired are JSON null.
func normalizePreview(p alerting.Preview) alerting.Preview {
	if p.Warnings == nil {
		p.Warnings = []string{}
	}
	if p.Diff.Changes == nil {
		p.Diff.Changes = []alerting.DiffChange{}
	}
	if len(p.Current) == 0 {
		p.Current = json.RawMessage("null")
	}
	if len(p.Desired) == 0 {
		p.Desired = json.RawMessage("null")
	}
	return p
}

// logAlertsWrite logs only identifiers (kind, action, target, outcome) —
// never request bodies, params, desired JSON or raw upstream messages.
func (d Deps) logAlertsWrite(r *http.Request, msg, kind, action, target string, err error) {
	if d.Logger == nil {
		return
	}
	attrs := []any{slog.String("kind", kind), slog.String("action", action), slog.String("target", redactSecrets(target))}
	if err != nil {
		_, code := alerting.CodeFor(err)
		attrs = append(attrs, slog.String("code", code), slog.String("error", redactSecrets(err.Error())))
		d.Logger.WarnContext(r.Context(), msg, attrs...)
		return
	}
	d.Logger.InfoContext(r.Context(), msg, attrs...)
}

var (
	urlRe    = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	secretRe = regexp.MustCompile(`(?i)((?:auth_?token|token|secret|password|passwd|api_?key|key|authorization|bearer)["']?\s*[:=]\s*(?:bearer\s+)?|bearer\s+)["']?[^\s"',;}]+`)
)

// redactSecrets removes URLs (webhook endpoints) and key/token assignments.
func redactSecrets(s string) string {
	s = urlRe.ReplaceAllString(s, "[redacted-url]")
	return secretRe.ReplaceAllString(s, "${1}[redacted]")
}

// roleHint names the IAM role whose absence causes a 403 for the kind.
func roleHint(kind, project, target string) string {
	if project == "" {
		project = projectOf(target)
	}
	on := "on " + project
	if project == "" {
		on = "on the affected project"
	}
	switch kind {
	case alerting.TargetPolicy:
		return "Grant roles/monitoring.alertPolicyEditor to the cloud0-alerts service account " + on
	case alerting.TargetChannel:
		return "Grant roles/monitoring.notificationChannelEditor to the cloud0-alerts service account " + on
	case alerting.TargetUptimeCheck:
		return "Grant roles/monitoring.uptimeCheckConfigEditor to the cloud0-alerts service account " + on
	case alerting.TargetSnooze:
		return "Grant roles/monitoring.snoozeEditor to the cloud0-alerts service account " + on
	case alerting.TargetBudget:
		return "Grant roles/billing.costsManager to the cloud0-alerts service account on the billing account"
	}
	return "Grant the cloud0-alerts service account the editor role for this resource kind (roles/monitoring.alertPolicyEditor, notificationChannelEditor, uptimeCheckConfigEditor, snoozeEditor, or roles/billing.costsManager for budgets) " + on
}

func projectOf(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) >= 2 && parts[0] == "projects" {
		return parts[1]
	}
	return ""
}

// mapAlertsError converts an alerting error into the envelope, with a
// sanitised message and the hint of spec/api.md. Unknown errors are 500s whose
// cause is logged by the envelope, not sent.
func mapAlertsError(err error, kind, project, target string) *APIError {
	status, code := alerting.CodeFor(err)
	if status == http.StatusInternalServerError {
		return ErrInternal(err)
	}
	msg := strings.TrimSpace(strings.TrimPrefix(redactSecrets(err.Error()), "alerting: "))
	ae := &APIError{Status: status, Code: code, Message: msg}
	switch code {
	case "alerts_not_configured":
		ae.Message = "Alert management is not configured on this server."
		ae.WithHint(alertsNotConfiguredHint)
	case "preview_stale":
		ae.WithHint("Run preview again and review the new diff.")
	case "confirm_required":
		ae.WithHint("Resend with confirm_delete: true after reviewing the delete preview.")
	case "permission_denied":
		ae.WithHint(roleHint(kind, project, target))
	case "api_disabled":
		ae.WithHint("Enable the API with: gcloud services enable monitoring.googleapis.com billingbudgets.googleapis.com --project=<project>")
	case "not_found":
		ae.WithHint("Reload the list; the resource may have been deleted.")
	case "upstream_error":
		ae.WithHint("Retry in a minute; if it persists check the Google Cloud status page.")
	}
	return ae
}
