package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/smallTechOrg/cloud0/internal/alerting"
	"github.com/smallTechOrg/cloud0/internal/store"
)

const (
	alertsSetupHint        = "Set CLOUD0_ALERTS_CREDENTIALS to the cloud0-alerts service-account key path and restart — see README → Alerts setup"
	unverifiedEmailWarning = "Email channel is not verified: notifications may not be delivered. Open the channel in the console and verify the address."
	noBudgetChannelWarning = "No monitoring notification channels: alerts go to billing admins by email only."
	routingSettingPrefix   = "alerts.routing."
)

// registerAlertsRead wires the read-only Alerts endpoints (and PUT routing,
// which is a local setting) of spec/api.md → "Alerts additions".
func (d Deps) registerAlertsRead(r *router) {
	r.handle(http.MethodGet, apiPrefix+"/alerts/status", d.handleAlertsStatus)
	r.handle(http.MethodGet, apiPrefix+"/alerts/templates", d.handleAlertsTemplates)
	r.handle(http.MethodGet, apiPrefix+"/alerts/policies", d.handleAlertsPolicies)
	r.handle(http.MethodGet, apiPrefix+"/alerts/policies/{project}/{id}", d.handleAlertsPolicy)
	r.handle(http.MethodGet, apiPrefix+"/alerts/channels", d.handleAlertsChannels)
	r.handle(http.MethodGet, apiPrefix+"/alerts/uptime-checks", d.handleAlertsUptime)
	r.handle(http.MethodGet, apiPrefix+"/alerts/incidents", d.handleAlertsIncidents)
	r.handle(http.MethodGet, apiPrefix+"/alerts/history", d.handleAlertsHistory)
	r.handle(http.MethodGet, apiPrefix+"/alerts/coverage", d.handleAlertsCoverage)
	r.handle(http.MethodGet, apiPrefix+"/alerts/snoozes", d.handleAlertsSnoozes)
	r.handle(http.MethodGet, apiPrefix+"/alerts/routing", d.handleAlertsRoutingGet)
	r.handle(http.MethodPut, apiPrefix+"/alerts/routing", d.handleAlertsRoutingPut)
	r.handle(http.MethodGet, apiPrefix+"/budgets", d.handleBudgets)
	r.handle(http.MethodGet, apiPrefix+"/budgets/suggestions", d.handleBudgetSuggestions)
	r.handle(http.MethodGet, apiPrefix+"/history", d.handleChangeHistory)
}

// ---- shared helpers ----------------------------------------------------------

func errAlertsNotConfigured() *APIError {
	return (&APIError{Status: http.StatusServiceUnavailable, Code: "alerts_not_configured",
		Message: "Alerts credentials are not configured."}).WithHint(alertsSetupHint)
}

// alertsReader is the 503 gate of reader-credential endpoints: Alerts wiring
// present and the named dependency non-nil.
func (d Deps) alertsReader(dep bool) *APIError {
	if d.Alerts == nil || !dep {
		return errAlertsNotConfigured()
	}
	return nil
}

// alertsWriter is the gate of endpoints that need the alerts credentials.
func (d Deps) alertsWriter(dep bool) *APIError {
	if d.Alerts == nil || !d.Alerts.Writable || !dep {
		return errAlertsNotConfigured()
	}
	return nil
}

func (d Deps) firstProject() string {
	if len(d.Config.Projects) > 0 {
		return d.Config.Projects[0]
	}
	return "PROJECT_ID"
}

func (d Deps) alertHint(code, msg string) *string {
	var h string
	switch code {
	case "alerts_not_configured":
		h = alertsSetupHint
	case "permission_denied":
		h = "Grant the role named in the message to the cloud0 service account (reads need roles/monitoring.viewer), then retry."
		if strings.Contains(msg, "billing") {
			h = "Grant roles/billing.costsManager to the cloud0-alerts service account on the billing account, then retry."
		}
	case "preview_stale":
		h = "Run preview again and review the new diff."
	case "confirm_required":
		h = "Resend with confirm_delete: true after reviewing the delete preview."
	case "api_disabled":
		h = "Enable it with: gcloud services enable monitoring.googleapis.com billingbudgets.googleapis.com --project=" + d.firstProject()
	default:
		return nil
	}
	return &h
}

// alertErr maps an alerting error into the envelope (alerting.CodeFor + hint).
func (d Deps) alertErr(err error) *APIError {
	status, code := alerting.CodeFor(err)
	if code == "internal_error" {
		return ErrInternal(err)
	}
	e := &APIError{Status: status, Code: code, Message: err.Error()}
	if status >= 500 {
		e.Cause = err
	}
	e.Hint = d.alertHint(code, e.Message)
	return e
}

// ProjectFailure is one project whose upstream call failed; it never fails the request.
type alertProjectFailure struct {
	Project string  `json:"project"`
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Hint    *string `json:"hint"`
}

func (d Deps) projectFailure(project string, err error) alertProjectFailure {
	_, code := alerting.CodeFor(err)
	return alertProjectFailure{Project: project, Code: code, Message: err.Error(), Hint: d.alertHint(code, err.Error())}
}

type projResult[T any] struct {
	project string
	val     T
	err     error
}

// perProject runs fn for every configured project (optionally restricted to
// only) concurrently and returns the results in project order.
func perProject[T any](ctx context.Context, projects []string, fn func(context.Context, string) (T, error)) []projResult[T] {
	out := make([]projResult[T], len(projects))
	var wg sync.WaitGroup
	for i, p := range projects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := fn(ctx, p)
			out[i] = projResult[T]{project: p, val: v, err: err}
		}()
	}
	wg.Wait()
	return out
}

// projectsFor returns the configured projects, or just the ?project= one.
func (d Deps) projectsFor(q string) ([]string, *APIError) {
	if q == "" {
		return d.Config.Projects, nil
	}
	if _, aerr := d.scopeParam(q); aerr != nil {
		return nil, aerr
	}
	return []string{q}, nil
}

// collectAll merges per-project results; if every project failed the first
// error is returned (used by endpoints without a projects_failed field).
func (d Deps) collectAll(r *http.Request, what string, errs []error, total int) *APIError {
	if total == 0 || len(errs) < total {
		for _, err := range errs {
			if d.Logger != nil {
				d.Logger.WarnContext(r.Context(), "alerts.partial_failure", "what", what, "error", err.Error())
			}
		}
		return nil
	}
	return d.alertErr(errs[0])
}

func nz[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func nullRaw(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}

func normPolicy(p alerting.PolicyView) alerting.PolicyView {
	p.Conditions = nz(p.Conditions)
	p.Channels = nz(p.Channels)
	p.Warnings = nz(p.Warnings)
	p.RawJSON = nullRaw(p.RawJSON)
	return p
}

func maskEmail(a string) string {
	i := strings.Index(a, "@")
	if i <= 0 || strings.Contains(a[:i], "***") {
		return a
	}
	return a[:1] + "***" + a[i:]
}

func normChannel(c alerting.Channel) alerting.Channel {
	if c.Type == "email" {
		c.Address = maskEmail(c.Address)
		if c.VerificationStatus != "VERIFIED" && !slices.Contains(c.Warnings, unverifiedEmailWarning) {
			c.Warnings = append(slices.Clone(c.Warnings), unverifiedEmailWarning)
		}
	} else if strings.Contains(c.Address, "@") {
		c.Address = maskEmail(c.Address)
	}
	c.Warnings = nz(c.Warnings)
	return c
}

func (d Deps) listPolicies(ctx context.Context, projects []string) ([]alerting.PolicyView, []alertProjectFailure) {
	res := perProject(ctx, projects, d.Alerts.Policies.List)
	all := []alerting.PolicyView{}
	failed := []alertProjectFailure{}
	for _, r := range res {
		if r.err != nil {
			failed = append(failed, d.projectFailure(r.project, r.err))
			continue
		}
		for _, p := range r.val {
			all = append(all, normPolicy(p))
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Project != all[j].Project {
			return all[i].Project < all[j].Project
		}
		if strings.ToLower(all[i].DisplayName) != strings.ToLower(all[j].DisplayName) {
			return strings.ToLower(all[i].DisplayName) < strings.ToLower(all[j].DisplayName)
		}
		return all[i].Name < all[j].Name
	})
	return all, failed
}

func (d Deps) listChannels(ctx context.Context) ([]alerting.Channel, []error) {
	res := perProject(ctx, d.Config.Projects, d.Alerts.Channels.List)
	all := []alerting.Channel{}
	var errs []error
	for _, r := range res {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		for _, c := range r.val {
			all = append(all, normChannel(c))
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Project != all[j].Project {
			return all[i].Project < all[j].Project
		}
		if strings.ToLower(all[i].DisplayName) != strings.ToLower(all[j].DisplayName) {
			return strings.ToLower(all[i].DisplayName) < strings.ToLower(all[j].DisplayName)
		}
		return all[i].Name < all[j].Name
	})
	return all, errs
}

// ---- status ------------------------------------------------------------------

func (d Deps) handleAlertsStatus(w http.ResponseWriter, r *http.Request) *APIError {
	writeJSON(w, http.StatusOK, map[string]any{
		"alerts_configured":          d.Alerts != nil && d.Alerts.Writable,
		"billing_account_configured": d.Config.BillingAccountID != "",
		"projects":                   nz(slices.Clone(d.Config.Projects)),
		"generated_at":               d.now().Truncate(time.Second),
	})
	return nil
}

// ---- templates ---------------------------------------------------------------

// templateSignal maps a template to the coverage (resource_type, signal) it provides.
var templateSignal = map[string][2]string{
	alerting.TplVMCPUHigh:      {"vm", "cpu"},
	alerting.TplVMMemoryHigh:   {"vm", "memory"},
	alerting.TplVMDiskHigh:     {"vm", "disk"},
	alerting.TplRun5xxBurst:    {"cloud_run", "errors"},
	alerting.TplRun5xxRate:     {"cloud_run", "errors"},
	alerting.TplRunErrorLogged: {"cloud_run", "errors"},
	alerting.TplRun401403Spike: {"cloud_run", "errors"},
	alerting.TplLB5xxBurst:     {"lb", "errors"},
	alerting.TplSQLDown:        {"cloudsql", "down"},
	alerting.TplSQLCPUHigh:     {"cloudsql", "cpu"},
	alerting.TplSQLMemoryHigh:  {"cloudsql", "memory"},
	alerting.TplSQLDiskHigh:    {"cloudsql", "disk"},
	alerting.TplUptimeDown:     {"uptime_check", "down"},
}

func rawPath(raw json.RawMessage, path ...any) any {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func secondsOf(v any) *int {
	s, ok := v.(string)
	if !ok || !strings.HasSuffix(s, "s") {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSuffix(s, "s"))
	if err != nil {
		return nil
	}
	return &n
}

// effectiveParams reads the installed managed policy back into TemplateParams.
func effectiveParams(t alerting.Template, pv alerting.PolicyView) *alerting.TemplateParams {
	enabled := pv.Enabled
	p := alerting.TemplateParams{Severity: pv.Severity, Enabled: &enabled, Instance: pv.Instance}
	spec := map[string]alerting.ParamSpec{}
	for _, s := range t.ParamSpecs {
		spec[s.Key] = s
	}
	if len(pv.Conditions) > 0 {
		c := pv.Conditions[0]
		if s, ok := spec["threshold"]; ok && c.Threshold != nil {
			v := *c.Threshold
			if s.Unit == "%" {
				v = math.Round(v*100*1e4) / 1e4
			}
			p.Threshold = &v
		}
		if _, ok := spec["duration_seconds"]; ok && c.DurationSeconds != nil {
			v := *c.DurationSeconds
			p.DurationSeconds = &v
		}
	}
	if _, ok := spec["window_seconds"]; ok {
		p.WindowSeconds = secondsOf(rawPath(pv.RawJSON, "conditions", 0, "conditionThreshold", "aggregations", 0, "alignmentPeriod"))
	}
	if _, ok := spec["rate_limit_seconds"]; ok {
		p.RateLimitSeconds = secondsOf(rawPath(pv.RawJSON, "alertStrategy", "notificationRateLimit", "period"))
	}
	return &p
}

func (d Deps) handleAlertsTemplates(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsReader(d.Alerts != nil && d.Alerts.Builder != nil); aerr != nil {
		return aerr
	}
	ctx := r.Context()
	tpls := d.Alerts.Builder.Templates()

	var policies []alerting.PolicyView
	if d.Alerts.Policies != nil {
		var failed []alertProjectFailure
		policies, failed = d.listPolicies(ctx, d.Config.Projects)
		for _, f := range failed {
			if d.Logger != nil {
				d.Logger.WarnContext(ctx, "alerts.partial_failure", "what", "templates.policies", "project", f.Project, "error", f.Message)
			}
		}
	}
	var cover []alerting.CoverageRow
	if d.Alerts.Coverage != nil {
		rows, err := d.Alerts.Coverage.Coverage(ctx)
		if err != nil {
			if d.Logger != nil {
				d.Logger.WarnContext(ctx, "alerts.partial_failure", "what", "templates.coverage", "error", err.Error())
			}
		} else {
			cover = rows
		}
	}

	out := make([]alerting.Template, 0, len(tpls))
	for _, t := range tpls {
		var mine []alerting.PolicyView
		for _, p := range policies {
			if p.Managed && p.TemplateID == t.ID {
				mine = append(mine, p)
			}
		}
		t.Installed = []string{}
		t.Params = nil
		for i, p := range mine {
			t.Installed = append(t.Installed, p.DisplayName)
			if i == 0 {
				t.Params = effectiveParams(t, p)
			}
		}
		t.ParamSpecs = nz(t.ParamSpecs)
		for i := range t.ParamSpecs {
			t.ParamSpecs[i].Options = nz(t.ParamSpecs[i].Options)
		}
		t.CoveredResources = []alerting.CoveredResource{}
		if sig, ok := templateSignal[t.ID]; ok {
			for _, row := range cover {
				if row.ResourceType != sig[0] {
					continue
				}
				for _, s := range row.Signals {
					if s.Signal == sig[1] {
						t.CoveredResources = append(t.CoveredResources, alerting.CoveredResource{
							Project: row.Project, ResourceType: row.ResourceType, Name: row.Resource, Covered: s.Covered, Note: s.Reason})
					}
				}
			}
		}
		out = append(out, t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out, "generated_at": d.now().Truncate(time.Second)})
	return nil
}

// ---- policies ----------------------------------------------------------------

func (d Deps) handleAlertsPolicies(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsReader(d.Alerts != nil && d.Alerts.Policies != nil); aerr != nil {
		return aerr
	}
	q := r.URL.Query()
	projects, aerr := d.projectsFor(q.Get("project"))
	if aerr != nil {
		return aerr
	}
	var managedFilter *bool
	if v := q.Get("managed"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil || (v != "true" && v != "false") {
			return ErrInvalidRequest("The managed filter " + quote(v) + " is not true or false.")
		}
		managedFilter = &b
	}
	all, failed := d.listPolicies(r.Context(), projects)
	out := make([]alerting.PolicyView, 0, len(all))
	for _, p := range all {
		if managedFilter != nil && p.Managed != *managedFilter {
			continue
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": out, "projects_failed": failed, "generated_at": d.now().Truncate(time.Second)})
	return nil
}

func (d Deps) handleAlertsPolicy(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsReader(d.Alerts != nil && d.Alerts.Policies != nil); aerr != nil {
		return aerr
	}
	project, id := r.PathValue("project"), r.PathValue("id")
	if !slices.Contains(d.Config.Projects, project) {
		return ErrNotFound("No alert policy " + quote(project+"/"+id) + ": the project is not configured.")
	}
	p, err := d.Alerts.Policies.Get(r.Context(), "projects/"+project+"/alertPolicies/"+id)
	if err != nil {
		return d.alertErr(err)
	}
	writeJSON(w, http.StatusOK, normPolicy(p))
	return nil
}

// ---- channels ----------------------------------------------------------------

func (d Deps) handleAlertsChannels(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsReader(d.Alerts != nil && d.Alerts.Channels != nil); aerr != nil {
		return aerr
	}
	ctx := r.Context()
	channels, errs := d.listChannels(ctx)
	if aerr := d.collectAll(r, "channels", errs, len(d.Config.Projects)); aerr != nil {
		return aerr
	}
	dres := perProject(ctx, d.Config.Projects, d.Alerts.Channels.Descriptors)
	byType := map[string]alerting.ChannelDescriptor{}
	var derrs []error
	for _, res := range dres {
		if res.err != nil {
			derrs = append(derrs, res.err)
			continue
		}
		for _, ds := range res.val {
			ds.Labels = nz(ds.Labels)
			if cur, ok := byType[ds.Type]; ok {
				cur.Creatable = cur.Creatable && ds.Creatable
				if cur.Hint == "" {
					cur.Hint = ds.Hint
				}
				byType[ds.Type] = cur
			} else {
				byType[ds.Type] = ds
			}
		}
	}
	if aerr := d.collectAll(r, "descriptors", derrs, len(d.Config.Projects)); aerr != nil {
		return aerr
	}
	descs := make([]alerting.ChannelDescriptor, 0, len(byType))
	for _, ds := range byType {
		descs = append(descs, ds)
	}
	sort.Slice(descs, func(i, j int) bool { return descs[i].Type < descs[j].Type })
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels, "descriptors": descs, "generated_at": d.now().Truncate(time.Second)})
	return nil
}

// ---- uptime checks -----------------------------------------------------------

func (d Deps) handleAlertsUptime(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsReader(d.Alerts != nil && d.Alerts.Uptime != nil); aerr != nil {
		return aerr
	}
	res := perProject(r.Context(), d.Config.Projects, d.Alerts.Uptime.List)
	checks := []alerting.UptimeCheck{}
	var errs []error
	for _, p := range res {
		if p.err != nil {
			errs = append(errs, p.err)
			continue
		}
		for _, c := range p.val {
			c.RawJSON = nullRaw(c.RawJSON)
			checks = append(checks, c)
		}
	}
	if aerr := d.collectAll(r, "uptime", errs, len(d.Config.Projects)); aerr != nil {
		return aerr
	}
	sort.SliceStable(checks, func(i, j int) bool {
		if checks[i].Project != checks[j].Project {
			return checks[i].Project < checks[j].Project
		}
		return strings.ToLower(checks[i].DisplayName) < strings.ToLower(checks[j].DisplayName)
	})
	writeJSON(w, http.StatusOK, map[string]any{"checks": checks, "generated_at": d.now().Truncate(time.Second)})
	return nil
}

// ---- incidents ---------------------------------------------------------------

func (d Deps) handleAlertsIncidents(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsReader(d.Alerts != nil && d.Alerts.Incidents != nil); aerr != nil {
		return aerr
	}
	q := r.URL.Query()
	state := q.Get("state")
	switch state {
	case "":
		state = "open"
	case "open", "closed", "all":
	default:
		return ErrInvalidRequest("The state " + quote(state) + " is not open, closed or all.")
	}
	projects, aerr := d.projectsFor(q.Get("project"))
	if aerr != nil {
		return aerr
	}
	f := alerting.IncidentFilter{}
	if state != "all" {
		f.State = state
	}
	res := perProject(r.Context(), projects, func(ctx context.Context, p string) ([]alerting.Incident, error) {
		return d.Alerts.Incidents.List(ctx, p, f)
	})
	incs := []alerting.Incident{}
	failed := []alertProjectFailure{}
	for _, p := range res {
		if p.err != nil {
			failed = append(failed, d.projectFailure(p.project, p.err))
			continue
		}
		for _, in := range p.val {
			if state != "all" && in.State != state {
				continue
			}
			incs = append(incs, in)
		}
	}
	sort.SliceStable(incs, func(i, j int) bool { return incs[i].OpenedAt.After(incs[j].OpenedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"incidents": incs, "total": len(incs), "projects_failed": failed, "generated_at": d.now().Truncate(time.Second)})
	return nil
}

// ---- alert history (per ISO week) ---------------------------------------------

func (d Deps) handleAlertsHistory(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsReader(d.Alerts != nil && d.Alerts.History != nil); aerr != nil {
		return aerr
	}
	now := d.now()
	week, aerr := d.weekParam(r.URL.Query().Get("week"), now)
	if aerr != nil {
		return aerr
	}
	rows, err := d.Alerts.History.History(r.Context(), week.String())
	if err != nil {
		return d.alertErr(err)
	}
	rows = slices.Clone(rows)
	opened, still := 0, 0
	for i := range rows {
		rows[i].Resources = nz(rows[i].Resources)
		opened += rows[i].Opened
		still += rows[i].StillOpen
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Opened != rows[j].Opened {
			return rows[i].Opened > rows[j].Opened
		}
		return rows[i].PolicyDisplayName < rows[j].PolicyDisplayName
	})
	writeJSON(w, http.StatusOK, map[string]any{"week": week.String(), "total_opened": opened, "still_open": still,
		"rows": nz(rows), "projects_failed": []alertProjectFailure{}, "generated_at": now.Truncate(time.Second)})
	return nil
}

// ---- coverage ----------------------------------------------------------------

func (d Deps) handleAlertsCoverage(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsReader(d.Alerts != nil && d.Alerts.Coverage != nil); aerr != nil {
		return aerr
	}
	rows, err := d.Alerts.Coverage.Coverage(r.Context())
	if err != nil {
		return d.alertErr(err)
	}
	rows = slices.Clone(rows)
	for i := range rows {
		rows[i].Signals = slices.Clone(nz(rows[i].Signals))
		for j := range rows[i].Signals {
			rows[i].Signals[j].Policies = nz(rows[i].Signals[j].Policies)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": nz(rows), "generated_at": d.now().Truncate(time.Second)})
	return nil
}

// ---- snoozes -----------------------------------------------------------------

func (d Deps) handleAlertsSnoozes(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsReader(d.Alerts != nil && d.Alerts.Snoozes != nil); aerr != nil {
		return aerr
	}
	res := perProject(r.Context(), d.Config.Projects, d.Alerts.Snoozes.List)
	all := []alerting.Snooze{}
	var errs []error
	for _, p := range res {
		if p.err != nil {
			errs = append(errs, p.err)
			continue
		}
		for _, s := range p.val {
			s.PolicyNames = nz(s.PolicyNames)
			all = append(all, s)
		}
	}
	if aerr := d.collectAll(r, "snoozes", errs, len(d.Config.Projects)); aerr != nil {
		return aerr
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].StartTime.After(all[j].StartTime) })
	writeJSON(w, http.StatusOK, map[string]any{"snoozes": all, "generated_at": d.now().Truncate(time.Second)})
	return nil
}

// ---- routing -----------------------------------------------------------------

func (d Deps) routingUpdatedAt(ctx context.Context) (*time.Time, bool, error) {
	if d.Store == nil {
		return nil, true, nil
	}
	var s sql.NullString
	if err := d.Store.DB.QueryRowContext(ctx, `SELECT MAX(updated_at) FROM settings WHERE "key" LIKE 'alerts.routing.%'`).Scan(&s); err != nil {
		return nil, false, err
	}
	if !s.Valid {
		return nil, true, nil
	}
	t, err := store.ParseTime(s.String)
	if err != nil {
		return nil, false, err
	}
	return &t, false, nil
}

func normRouting(m map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, sev := range alerting.Severities {
		out[sev] = nz(slices.Clone(m[sev]))
	}
	return out
}

func (d Deps) routingResponse(r *http.Request) (map[string]any, *APIError) {
	ctx := r.Context()
	routing, err := d.Alerts.Router.Get(ctx)
	if err != nil {
		return nil, d.alertErr(err)
	}
	routing = normRouting(routing)
	channels := []alerting.Channel{}
	if d.Alerts.Channels != nil {
		var errs []error
		channels, errs = d.listChannels(ctx)
		if aerr := d.collectAll(r, "routing.channels", errs, len(d.Config.Projects)); aerr != nil {
			return nil, aerr
		}
	}
	byName := map[string]alerting.Channel{}
	for _, c := range channels {
		byName[c.Name] = c
	}
	warnings := []string{}
	for _, sev := range alerting.Severities {
		if len(routing[sev]) == 0 {
			warnings = append(warnings, "No notification channel is routed for severity "+sev+": its alerts would notify nobody.")
		}
		for _, n := range routing[sev] {
			if c, ok := byName[n]; ok && c.Type == "email" && c.VerificationStatus != "VERIFIED" {
				warnings = append(warnings, "Channel "+c.DisplayName+" ("+c.Project+") routed for "+sev+" is an UNVERIFIED email channel: notifications may not be delivered.")
			}
		}
	}
	updated, defaulted := (*time.Time)(nil), false
	if d.Store != nil {
		u, def, err := d.routingUpdatedAt(ctx)
		if err != nil {
			return nil, ErrInternal(err)
		}
		updated, defaulted = u, def
	}
	return map[string]any{"routing": routing, "defaulted": defaulted, "updated_at": updated, "warnings": warnings, "channels": channels}, nil
}

func (d Deps) handleAlertsRoutingGet(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsReader(d.Alerts != nil && d.Alerts.Router != nil); aerr != nil {
		return aerr
	}
	resp, aerr := d.routingResponse(r)
	if aerr != nil {
		return aerr
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func rdInvalidParams(msg string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_params", Message: msg}
}

// PUT needs no GCP write credentials (the routing is a local setting); it
// reads channels with the reader client to validate the names.
func (d Deps) handleAlertsRoutingPut(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsReader(d.Alerts != nil && d.Alerts.Router != nil && d.Alerts.Channels != nil); aerr != nil {
		return aerr
	}
	var body struct {
		Routing map[string][]string `json:"routing"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&body); err != nil {
		return ErrInvalidRequest("The request body is not valid JSON of the form {\"routing\": {...}}.")
	}
	if body.Routing == nil {
		return ErrInvalidRequest("The routing field is required.")
	}
	for k := range body.Routing {
		if !slices.Contains(alerting.Severities, k) {
			return rdInvalidParams("routing key " + quote(k) + " is not one of CRITICAL, ERROR, WARNING.")
		}
	}
	channels, errs := d.listChannels(r.Context())
	if aerr := d.collectAll(r, "routing.put", errs, len(d.Config.Projects)); aerr != nil {
		return aerr
	}
	known := map[string]bool{}
	for _, c := range channels {
		known[c.Name] = true
	}
	for _, sev := range alerting.Severities {
		names, ok := body.Routing[sev]
		if !ok {
			return rdInvalidParams("routing key " + sev + " is required.")
		}
		if len(names) == 0 {
			return rdInvalidParams("routing " + sev + " must list at least one channel.")
		}
		seen := map[string]bool{}
		for _, n := range names {
			if seen[n] {
				return rdInvalidParams("routing " + sev + " lists " + quote(n) + " twice.")
			}
			seen[n] = true
			if !known[n] {
				return rdInvalidParams("routing " + sev + " names an unknown channel " + quote(n) + ".")
			}
		}
	}
	ctx := r.Context()
	if err := d.Alerts.Router.Put(ctx, normRouting(body.Routing)); err != nil {
		return d.alertErr(err)
	}
	// Router.Put writes the settings and the change_history row itself.
	resp, aerr := d.routingResponse(r)
	if aerr != nil {
		return aerr
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

// ---- budgets -----------------------------------------------------------------

func (d Deps) handleBudgets(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsWriter(d.Alerts != nil && d.Alerts.Budgets != nil); aerr != nil {
		return aerr
	}
	list, err := d.Alerts.Budgets.List(r.Context())
	if err != nil {
		return d.alertErr(err)
	}
	out := make([]alerting.BudgetView, 0, len(list))
	for _, b := range list {
		b.Managed = strings.HasPrefix(b.DisplayName, alerting.BudgetNamePrefix)
		switch {
		case b.Managed:
			b.AdoptShape = ""
		case b.AdoptShape == "":
			b.AdoptShape = alerting.ShapeBudgetBoilerplate
		}
		b.Projects = nz(b.Projects)
		b.ServiceIDs = nz(b.ServiceIDs)
		b.Thresholds = nz(b.Thresholds)
		b.ChannelNames = nz(b.ChannelNames)
		b.Warnings = nz(b.Warnings)
		if len(b.ChannelNames) == 0 && !slices.Contains(b.Warnings, noBudgetChannelWarning) {
			b.Warnings = append(slices.Clone(b.Warnings), noBudgetChannelWarning)
		}
		b.RawJSON = nullRaw(b.RawJSON)
		out = append(out, b)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].DisplayName) < strings.ToLower(out[j].DisplayName) })
	writeJSON(w, http.StatusOK, map[string]any{"billing_account": d.Config.BillingAccountID, "currency": d.Config.Currency,
		"budgets": out, "generated_at": d.now().Truncate(time.Second)})
	return nil
}

func (d Deps) handleBudgetSuggestions(w http.ResponseWriter, r *http.Request) *APIError {
	if aerr := d.alertsWriter(d.Alerts != nil && d.Alerts.Suggestions != nil); aerr != nil {
		return aerr
	}
	list, err := d.Alerts.Suggestions.Suggestions(r.Context())
	if err != nil {
		return d.alertErr(err)
	}
	list = slices.Clone(list)
	for i := range list {
		dr := &list[i].Draft
		dr.Projects = nz(dr.Projects)
		dr.ServiceIDs = nz(dr.ServiceIDs)
		dr.Thresholds = nz(dr.Thresholds)
	}
	writeJSON(w, http.StatusOK, map[string]any{"currency": d.Config.Currency, "suggestions": nz(list)})
	return nil
}

// ---- change history ------------------------------------------------------------

var historyKinds = []string{alerting.TargetPolicy, alerting.TargetUptimeCheck, alerting.TargetBudget,
	alerting.TargetSnooze, alerting.TargetChannel, alerting.TargetRouting}

func (d Deps) handleChangeHistory(w http.ResponseWriter, r *http.Request) *APIError {
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind != "" && !slices.Contains(historyKinds, kind) {
		return ErrInvalidRequest("The kind " + quote(kind) + " is not one of " + strings.Join(historyKinds, ", ") + ".")
	}
	limit := store.DefaultChangeLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > store.MaxChangeLimit {
			return ErrInvalidRequest("The limit " + quote(v) + " is not an integer between 1 and " + strconv.Itoa(store.MaxChangeLimit) + ".")
		}
		limit = n
	}
	entries, err := store.ListChanges(r.Context(), d.Store.DB, store.ChangeFilter{Kind: kind}, limit)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			entries = nil
		} else {
			return ErrInternal(err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": nz(entries), "limit": limit})
	return nil
}
