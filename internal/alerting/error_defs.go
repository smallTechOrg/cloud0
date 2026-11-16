package alerting

import (
	"encoding/json"
	"fmt"
)

// Error-family templates: Cloud Run 5xx burst / rate, application error log
// match, load-balancer 5xx burst and the opt-in 401/403 spike. All filters are
// written per resource TYPE (never per service / url map), and every metric
// condition groups by the service / url-map label so each service is evaluated
// separately and future services are covered automatically. 4xx never alerts
// except in run_401_403_spike, which is created disabled.

const (
	errRunReqCount = `metric.type="run.googleapis.com/request_count" AND resource.type="cloud_run_revision"`
	errLBReqCount  = `metric.type="loadbalancing.googleapis.com/https/request_count" AND resource.type="https_lb_rule"`

	errRunGroup = "resource.labels.service_name"
	errLBGroup  = "resource.labels.url_map_name"

	// errMinTraffic is the minimum number of requests in the window before the
	// rate template may fire (see errRateBuild).
	errMinTraffic = 20
)

func errF(v float64) *float64 { return &v }
func errI(v int) *int         { return &v }
func errB(v bool) *bool       { return &v }

func errSeveritySpec() ParamSpec {
	return ParamSpec{Key: "severity", Label: "Severity", Type: "enum", Options: append([]string{}, Severities...),
		Help: "Routes the notification through the channels mapped to this severity."}
}

func errWindowSpec() ParamSpec {
	return ParamSpec{Key: "window_seconds", Label: "Window", Type: "int", Unit: "seconds", Min: errF(60), Max: errF(3600),
		Help: "Requests are counted over this rolling window."}
}

func errCountSpec(label string) ParamSpec {
	return ParamSpec{Key: "threshold", Label: label, Type: "number", Unit: "count", Min: errF(1), Max: errF(100000),
		Help: "The policy fires when the count in the window is greater than this."}
}

// errCommon assembles the fields every template shares.
func errCommon(id, title string, p TemplateParams, defaultEnabled bool, ctx BuildContext) map[string]any {
	name := "cloud0: " + title
	labels := map[string]string{LabelManagedBy: LabelManagedByVal, LabelTemplate: id}
	if p.Instance != "" {
		name += " — " + p.Instance
		labels[LabelInstance] = p.Instance
	}
	enabled := defaultEnabled
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	channels := ctx.Channels
	if len(p.ChannelOverride) > 0 {
		channels = p.ChannelOverride
	}
	pol := map[string]any{
		"displayName": name,
		"severity":    p.Severity,
		"enabled":     enabled,
		"userLabels":  labels,
	}
	pol["notificationChannels"] = append([]string{}, channels...)
	return pol
}

// errWarnings reports a policy that would notify nobody.
func errWarnings(pol map[string]any) []string {
	if ch, _ := pol["notificationChannels"].([]string); len(ch) == 0 {
		return []string{fmt.Sprintf("No notification channel resolved for severity %v: the policy would notify nobody.", pol["severity"])}
	}
	return nil
}

func errDoc(md string) map[string]any {
	return map[string]any{"mimeType": "text/markdown", "content": md}
}

func errSecs(n int) string { return fmt.Sprintf("%ds", n) }

func errInt(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}

func errMarshal(pol map[string]any, warnings []string) (Built, error) {
	warnings = append(warnings, errWarnings(pol)...)
	b, err := json.Marshal(pol)
	if err != nil {
		return Built{}, err
	}
	return Built{Policy: b, Warnings: warnings}, nil
}

const errFirstLook = "\n\n**First look:** open the cloud0 **Errors** tab and use the triage view to see which service, status code and message dominate, " +
	"then open the sample in **Logs Explorer** (resource type `Cloud Run Revision`, filter on the service named in the incident)."

// errBurst builds a "count of matching requests in a window" policy, grouped per resource.
func errBurst(id, title, filter, group string, doc string, defaultEnabled bool) func(TemplateParams, BuildContext) (Built, error) {
	return func(p TemplateParams, ctx BuildContext) (Built, error) {
		window := errInt(p.WindowSeconds, 600)
		thr := 5.0
		if p.Threshold != nil {
			thr = *p.Threshold
		}
		pol := errCommon(id, title, p, defaultEnabled, ctx)
		pol["combiner"] = "OR"
		pol["documentation"] = errDoc(doc)
		pol["conditions"] = []any{map[string]any{
			"displayName": fmt.Sprintf("%s: more than %g in %d min", title, thr, window/60),
			"conditionThreshold": map[string]any{
				"filter":         filter,
				"comparison":     "COMPARISON_GT",
				"thresholdValue": thr,
				"duration":       errSecs(0),
				"trigger":        map[string]any{"count": 1},
				"aggregations": []any{
					map[string]any{
						"alignmentPeriod":    errSecs(window),
						"perSeriesAligner":   "ALIGN_DELTA",
						"crossSeriesReducer": "REDUCE_SUM",
						"groupByFields":      []string{group},
					},
				},
			},
		}}
		return errMarshal(pol, nil)
	}
}

func errBurstTemplate(id, title, desc, sev string, thr float64, extra ...ParamSpec) Template {
	return Template{
		ID: id, Kind: KindThreshold, Title: title, Description: desc, Severity: sev, DefaultEnabled: true,
		ParamSpecs: append([]ParamSpec{errCountSpec("Requests in window"), errWindowSpec(), errSeveritySpec()}, extra...),
		Defaults:   TemplateParams{Threshold: errF(thr), WindowSeconds: errI(600), Severity: sev, Enabled: errB(true)},
		Installed:  []string{}, CoveredResources: []CoveredResource{},
	}
}

func errRateBuild(p TemplateParams, ctx BuildContext) (Built, error) {
	pct := 2.0
	if p.Threshold != nil {
		pct = *p.Threshold
	}
	dur := errInt(p.DurationSeconds, 600)
	const title = "Cloud Run 5xx rate"
	pol := errCommon(TplRun5xxRate, title, p, true, ctx)
	// Combiner AND: the rate condition only counts when the minimum-traffic
	// guard condition is also true for the same service. A ratio alone would
	// page on 1 failure out of 3 requests (33 %) at 3 a.m.; the guard needs
	// at least errMinTraffic requests in the window. The `trigger` field of a
	// single condition cannot express a traffic floor, hence two conditions.
	pol["combiner"] = "AND"
	pol["documentation"] = errDoc(fmt.Sprintf("The share of HTTP 5xx responses on a Cloud Run service exceeded %g %% for %d minutes, "+
		"with at least %d requests in the last 10 minutes (a minimum-traffic guard so a single failure out of a handful of requests does not page you)."+
		"\n\nEach service is evaluated separately; services created later are covered automatically. 4xx responses are not counted.%s",
		pct, dur/60, errMinTraffic, errFirstLook))
	pol["conditions"] = []any{
		map[string]any{
			"displayName": fmt.Sprintf("%s: 5xx share above %g %% for %d min", title, pct, dur/60),
			"conditionThreshold": map[string]any{
				"filter":            errRunReqCount + ` AND metric.labels.response_code_class="5xx"`,
				"denominatorFilter": errRunReqCount,
				"comparison":        "COMPARISON_GT",
				"thresholdValue":    pct / 100,
				"duration":          errSecs(dur),
				"trigger":           map[string]any{"count": 1},
				"aggregations": []any{map[string]any{
					"alignmentPeriod": errSecs(60), "perSeriesAligner": "ALIGN_RATE",
					"crossSeriesReducer": "REDUCE_SUM", "groupByFields": []string{errRunGroup},
				}},
				"denominatorAggregations": []any{map[string]any{
					"alignmentPeriod": errSecs(60), "perSeriesAligner": "ALIGN_RATE",
					"crossSeriesReducer": "REDUCE_SUM", "groupByFields": []string{errRunGroup},
				}},
			},
		},
		map[string]any{
			"displayName": fmt.Sprintf("%s: at least %d requests in 10 min (minimum traffic)", title, errMinTraffic),
			"conditionThreshold": map[string]any{
				"filter":         errRunReqCount,
				"comparison":     "COMPARISON_GT",
				"thresholdValue": errMinTraffic - 1,
				"duration":       errSecs(0),
				"trigger":        map[string]any{"count": 1},
				"aggregations": []any{map[string]any{
					"alignmentPeriod": errSecs(600), "perSeriesAligner": "ALIGN_DELTA",
					"crossSeriesReducer": "REDUCE_SUM", "groupByFields": []string{errRunGroup},
				}},
			},
		},
	}
	return errMarshal(pol, nil)
}

func errLoggedBuild(p TemplateParams, ctx BuildContext) (Built, error) {
	rl := errInt(p.RateLimitSeconds, 3600)
	const title = "Application error logged (Cloud Run)"
	pol := errCommon(TplRunErrorLogged, title, p, true, ctx)
	pol["combiner"] = "OR"
	pol["documentation"] = errDoc("A Cloud Run service wrote a log entry with severity ERROR or higher." +
		"\n\nThis is a log-match alert, not an Error Reporting \"new error group\" alert: the Monitoring API offers no condition for new Error Reporting groups, " +
		fmt.Sprintf("so every error log is matched, limited to one notification per %d minutes.", rl/60) + errFirstLook)
	pol["conditions"] = []any{map[string]any{
		"displayName": "Cloud Run log entry with severity ERROR or higher",
		"conditionMatchedLog": map[string]any{
			"filter": `resource.type="cloud_run_revision" AND severity>=ERROR`,
		},
	}}
	pol["alertStrategy"] = map[string]any{
		"notificationRateLimit": map[string]any{"period": errSecs(rl)},
		"autoClose":             errSecs(7 * 24 * 3600),
	}
	return errMarshal(pol, nil)
}

// errorDefs returns the error-family templates.
func errorDefs() []templateDef {
	burst := errBurstTemplate(TplRun5xxBurst, "Cloud Run 5xx burst",
		"More than N HTTP 5xx responses within the window on any Cloud Run service (each service evaluated separately, future services included).",
		SevError, 5)
	rate := Template{
		ID: TplRun5xxRate, Kind: KindRatio, Title: "Cloud Run 5xx rate", Severity: SevError, DefaultEnabled: true,
		Description: "Share of 5xx responses above the threshold for the duration on any Cloud Run service, with a minimum of 20 requests so tiny samples do not page.",
		ParamSpecs: []ParamSpec{
			{Key: "threshold", Label: "5xx share", Type: "number", Unit: "%", Min: errF(0.1), Max: errF(100), Help: "Percent of requests answered with 5xx."},
			{Key: "duration_seconds", Label: "For", Type: "int", Unit: "seconds", Min: errF(60), Max: errF(86400), Help: "How long the share must stay above the threshold."},
			errSeveritySpec(),
		},
		Defaults:  TemplateParams{Threshold: errF(2), DurationSeconds: errI(600), Severity: SevError, Enabled: errB(true)},
		Installed: []string{}, CoveredResources: []CoveredResource{},
	}
	logged := Template{
		ID: TplRunErrorLogged, Kind: KindLogMatch, Title: "Application error logged (Cloud Run)", Severity: SevWarning, DefaultEnabled: true,
		Description: "A Cloud Run service logged an entry with severity ERROR or higher. Not an Error Reporting \"new group\" alert (the Monitoring API cannot express that); rate limited.",
		ParamSpecs: []ParamSpec{
			{Key: "rate_limit_seconds", Label: "At most one notification per", Type: "int", Unit: "seconds", Min: errF(300), Max: errF(86400), Help: "Notification rate limit."},
			errSeveritySpec(),
		},
		Defaults:  TemplateParams{RateLimitSeconds: errI(3600), Severity: SevWarning, Enabled: errB(true)},
		Installed: []string{}, CoveredResources: []CoveredResource{},
	}
	lb := errBurstTemplate(TplLB5xxBurst, "Load balancer 5xx burst",
		"More than N HTTP 5xx responses within the window on any external HTTP(S) load balancer (each URL map evaluated separately).",
		SevError, 5)
	spike := errBurstTemplate(TplRun401403Spike, "Cloud Run 401/403 spike",
		"Opt-in: more than N 401 or 403 responses within the window on any Cloud Run service. Created disabled; other 4xx codes never alert.",
		SevWarning, 50)
	spike.DefaultEnabled = false
	spike.Defaults.Enabled = errB(false)

	return []templateDef{
		{Template: burst, Build: errBurst(TplRun5xxBurst, burst.Title,
			errRunReqCount+` AND metric.labels.response_code_class="5xx"`, errRunGroup,
			"More than the configured number of HTTP 5xx responses were served by a Cloud Run service within the window. Each service is evaluated separately; services created later are covered automatically. 4xx responses are not counted."+errFirstLook, true)},
		{Template: rate, Build: errRateBuild},
		{Template: logged, Build: errLoggedBuild},
		{Template: lb, Build: errBurst(TplLB5xxBurst, lb.Title,
			errLBReqCount+` AND metric.labels.response_code_class=500`, errLBGroup,
			"More than the configured number of HTTP 5xx responses were returned by an external HTTP(S) load balancer within the window. Each URL map is evaluated separately."+
				"\n\n**First look:** check backend health in the load balancer page, then the Cloud Run **Errors** tab in cloud0 and Logs Explorer (`resource.type=\"http_load_balancer\"`).", true)},
		{Template: spike, Build: errBurst(TplRun401403Spike, spike.Title,
			errRunReqCount+` AND metric.labels.response_code=monitoring.regex.full_match("401|403")`, errRunGroup,
			"Many 401/403 responses on a Cloud Run service: usually a scanner, a leaked or expired credential, or a broken auth integration. This opt-in policy is created disabled."+errFirstLook, false)},
	}
}
