package alerting

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/protobuf/encoding/protojson"
)

var errIDs = []string{TplRun5xxBurst, TplRun5xxRate, TplRunErrorLogged, TplLB5xxBurst, TplRun401403Spike}

const errChan = "projects/proj-a/notificationChannels/1"

func errTpl(t *testing.T, id string) (Builder, Template) {
	t.Helper()
	b := NewBuilder()
	tpl, ok := b.Template(id)
	if !ok {
		t.Fatalf("template %s not registered", id)
	}
	return b, tpl
}

func errBuild(t *testing.T, id string, p TemplateParams, chans []string) Built {
	t.Helper()
	b, tpl := errTpl(t, id)
	got, err := b.Build(tpl, p, BuildContext{Project: "proj-a", Channels: chans})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func errPretty(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return string(out) + "\n"
}

func errDecode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func errEdited(id string) TemplateParams {
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }
	switch id {
	case TplRun5xxRate:
		return TemplateParams{Threshold: f(5), DurationSeconds: i(900), Severity: SevCritical}
	case TplRunErrorLogged:
		return TemplateParams{RateLimitSeconds: i(900), Severity: SevError}
	default:
		return TemplateParams{Threshold: f(20), WindowSeconds: i(300), Severity: SevCritical}
	}
}

func TestErrorGolden(t *testing.T) {
	for _, id := range errIDs {
		for name, p := range map[string]TemplateParams{"default": {}, "edited": errEdited(id)} {
			got := errPretty(t, errBuild(t, id, p, []string{errChan}).Policy)
			path := "testdata/" + id + "_" + name + ".golden.json"
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: %v (run with UPDATE_GOLDEN=1)", path, err)
			}
			if string(want) != got {
				t.Errorf("%s differs:\n%s", path, got)
			}
		}
	}
}

func TestErrorDefaultsMatchFrozenTable(t *testing.T) {
	type row struct {
		kind, sev string
		thr       float64
		window    int
		dur       int
		rate      int
		enabled   bool
	}
	want := map[string]row{
		TplRun5xxBurst:    {KindThreshold, SevError, 5, 600, 0, 0, true},
		TplRun5xxRate:     {KindRatio, SevError, 2, 0, 600, 0, true},
		TplRunErrorLogged: {KindLogMatch, SevWarning, 0, 0, 0, 3600, true},
		TplLB5xxBurst:     {KindThreshold, SevError, 5, 600, 0, 0, true},
		TplRun401403Spike: {KindThreshold, SevWarning, 50, 600, 0, 0, false},
	}
	for id, w := range want {
		_, tpl := errTpl(t, id)
		d := tpl.Defaults
		if tpl.Kind != w.kind || tpl.Severity != w.sev || d.Severity != w.sev || tpl.DefaultEnabled != w.enabled ||
			d.Enabled == nil || *d.Enabled != w.enabled {
			t.Errorf("%s: kind/severity/enabled mismatch: %+v", id, tpl)
		}
		if (w.thr != 0) && (d.Threshold == nil || *d.Threshold != w.thr) {
			t.Errorf("%s: threshold %v want %v", id, d.Threshold, w.thr)
		}
		if (w.window != 0) && (d.WindowSeconds == nil || *d.WindowSeconds != w.window) {
			t.Errorf("%s: window %v", id, d.WindowSeconds)
		}
		if (w.dur != 0) && (d.DurationSeconds == nil || *d.DurationSeconds != w.dur) {
			t.Errorf("%s: duration %v", id, d.DurationSeconds)
		}
		if (w.rate != 0) && (d.RateLimitSeconds == nil || *d.RateLimitSeconds != w.rate) {
			t.Errorf("%s: rate limit %v", id, d.RateLimitSeconds)
		}
		if tpl.Title == "" || tpl.Description == "" || len(tpl.ParamSpecs) == 0 {
			t.Errorf("%s: incomplete metadata", id)
		}
		for _, s := range tpl.ParamSpecs {
			if s.Type != "enum" && (s.Min == nil || s.Max == nil) {
				t.Errorf("%s.%s: min/max missing", id, s.Key)
			}
		}
	}
	_, logged := errTpl(t, TplRunErrorLogged)
	if logged.Title != "Application error logged (Cloud Run)" {
		t.Errorf("title %q", logged.Title)
	}
}

func TestErrorMergeValidation(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }
	cases := []struct {
		id string
		p  TemplateParams
	}{
		{TplRun5xxBurst, TemplateParams{Threshold: f(0)}},
		{TplRun5xxBurst, TemplateParams{WindowSeconds: i(30)}},
		{TplLB5xxBurst, TemplateParams{WindowSeconds: i(7200)}},
		{TplRun5xxRate, TemplateParams{Threshold: f(0.01)}},
		{TplRun5xxRate, TemplateParams{Threshold: f(101)}},
		{TplRun5xxRate, TemplateParams{DurationSeconds: i(30)}},
		{TplRunErrorLogged, TemplateParams{RateLimitSeconds: i(60)}},
		{TplRunErrorLogged, TemplateParams{RateLimitSeconds: i(100000)}},
		{TplRun401403Spike, TemplateParams{Severity: "PANIC"}},
	}
	for _, c := range cases {
		b, tpl := errTpl(t, c.id)
		if _, err := b.Merge(tpl, c.p); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("%s %+v: want ErrInvalidParams, got %v", c.id, c.p, err)
		}
		if _, err := b.Build(tpl, c.p, BuildContext{}); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("%s Build %+v: want ErrInvalidParams, got %v", c.id, c.p, err)
		}
	}
}

func TestErrorNo4xxExceptOptInSpike(t *testing.T) {
	for _, id := range errIDs {
		m := errDecode(t, errBuild(t, id, TemplateParams{}, nil).Policy)
		cb, _ := json.Marshal(m["conditions"]) // documentation may say "4xx are not counted"
		s := string(cb)
		has4 := strings.Contains(s, "4xx") || strings.Contains(s, `response_code=`) || strings.Contains(s, "response_code_class=400")
		if id == TplRun401403Spike {
			if !strings.Contains(s, `monitoring.regex.full_match(\"401|403\")`) {
				t.Errorf("spike missing 401|403: %s", s)
			}
			continue
		}
		if has4 {
			t.Errorf("%s mentions 4xx", id)
		}
	}
}

func TestErrorSpikeDisabledByDefault(t *testing.T) {
	m := errDecode(t, errBuild(t, TplRun401403Spike, TemplateParams{}, nil).Policy)
	if m["enabled"] != false {
		t.Errorf("enabled = %v", m["enabled"])
	}
	on := true
	m = errDecode(t, errBuild(t, TplRun401403Spike, TemplateParams{Enabled: &on}, nil).Policy)
	if m["enabled"] != true {
		t.Errorf("explicit enable ignored")
	}
	for _, id := range []string{TplRun5xxBurst, TplRun5xxRate, TplRunErrorLogged, TplLB5xxBurst} {
		if errDecode(t, errBuild(t, id, TemplateParams{}, nil).Policy)["enabled"] != true {
			t.Errorf("%s not enabled by default", id)
		}
	}
}

func errCond(t *testing.T, m map[string]any, i int) map[string]any {
	t.Helper()
	return m["conditions"].([]any)[i].(map[string]any)["conditionThreshold"].(map[string]any)
}

func TestErrorGroupByAndNoPerServiceRestriction(t *testing.T) {
	groups := map[string]string{
		TplRun5xxBurst: "resource.labels.service_name", TplRun5xxRate: "resource.labels.service_name",
		TplLB5xxBurst: "resource.labels.url_map_name", TplRun401403Spike: "resource.labels.service_name",
	}
	for id, g := range groups {
		m := errDecode(t, errBuild(t, id, TemplateParams{}, nil).Policy)
		for i := range m["conditions"].([]any) {
			c := errCond(t, m, i)
			agg := c["aggregations"].([]any)[0].(map[string]any)
			if gb := agg["groupByFields"].([]any); len(gb) != 1 || gb[0] != g {
				t.Errorf("%s cond %d groupBy %v", id, i, gb)
			}
			if agg["crossSeriesReducer"] != "REDUCE_SUM" {
				t.Errorf("%s reducer %v", id, agg["crossSeriesReducer"])
			}
			f := c["filter"].(string)
			if strings.Contains(f, "service_name=") || strings.Contains(f, "url_map_name=") {
				t.Errorf("%s restricts to one resource: %s", id, f)
			}
		}
	}
	lb := errCond(t, errDecode(t, errBuild(t, TplLB5xxBurst, TemplateParams{}, nil).Policy), 0)["filter"].(string)
	for _, s := range []string{`loadbalancing.googleapis.com/https/request_count`, `resource.type="https_lb_rule"`, `response_code_class=500`} {
		if !strings.Contains(lb, s) {
			t.Errorf("lb filter lacks %s: %s", s, lb)
		}
	}
	run := errCond(t, errDecode(t, errBuild(t, TplRun5xxBurst, TemplateParams{}, nil).Policy), 0)
	for _, s := range []string{`run.googleapis.com/request_count`, `resource.type="cloud_run_revision"`, `response_code_class="5xx"`} {
		if !strings.Contains(run["filter"].(string), s) {
			t.Errorf("run filter lacks %s", s)
		}
	}
	if run["thresholdValue"].(float64) != 5 || run["comparison"] != "COMPARISON_GT" || run["duration"] != "0s" {
		t.Errorf("burst condition: %v", run)
	}
	agg := run["aggregations"].([]any)[0].(map[string]any)
	if agg["perSeriesAligner"] != "ALIGN_DELTA" || agg["alignmentPeriod"] != "600s" {
		t.Errorf("burst agg: %v", agg)
	}
}

func TestErrorBurstWindowAndThresholdEdited(t *testing.T) {
	m := errDecode(t, errBuild(t, TplRun5xxBurst, errEdited(TplRun5xxBurst), nil).Policy)
	c := errCond(t, m, 0)
	agg := c["aggregations"].([]any)[0].(map[string]any)
	if c["thresholdValue"].(float64) != 20 || agg["alignmentPeriod"] != "300s" || m["severity"] != SevCritical {
		t.Errorf("edited burst: %v", m)
	}
}

func TestErrorRateIsRatioWithANDTrafficGuard(t *testing.T) {
	m := errDecode(t, errBuild(t, TplRun5xxRate, TemplateParams{}, nil).Policy)
	if m["combiner"] != "AND" {
		t.Fatalf("combiner %v", m["combiner"])
	}
	if n := len(m["conditions"].([]any)); n != 2 {
		t.Fatalf("conditions = %d", n)
	}
	r := errCond(t, m, 0)
	if r["thresholdValue"].(float64) != 0.02 || r["duration"] != "600s" {
		t.Errorf("ratio: %v", r)
	}
	if !strings.Contains(r["filter"].(string), `response_code_class="5xx"`) {
		t.Errorf("numerator lacks 5xx")
	}
	den := r["denominatorFilter"].(string)
	if strings.Contains(den, "response_code") || !strings.Contains(den, "run.googleapis.com/request_count") {
		t.Errorf("denominator must be all requests: %s", den)
	}
	if len(r["denominatorAggregations"].([]any)) != 1 {
		t.Errorf("denominatorAggregations missing")
	}
	g := errCond(t, m, 1)
	if g["thresholdValue"].(float64) != 19 || g["comparison"] != "COMPARISON_GT" {
		t.Errorf("guard: %v", g) // > 19 == at least 20 requests
	}
	if strings.Contains(g["filter"].(string), "response_code") {
		t.Errorf("guard must count all requests")
	}
	e := errDecode(t, errBuild(t, TplRun5xxRate, errEdited(TplRun5xxRate), nil).Policy)
	if v := errCond(t, e, 0)["thresholdValue"].(float64); v < 0.0499 || v > 0.0501 {
		t.Errorf("edited ratio %v", v)
	}
	if errCond(t, e, 0)["duration"] != "900s" {
		t.Errorf("edited duration")
	}
}

func TestErrorLoggedPolicy(t *testing.T) {
	m := errDecode(t, errBuild(t, TplRunErrorLogged, TemplateParams{}, nil).Policy)
	c := m["conditions"].([]any)[0].(map[string]any)["conditionMatchedLog"].(map[string]any)
	if c["filter"] != `resource.type="cloud_run_revision" AND severity>=ERROR` {
		t.Errorf("filter %v", c["filter"])
	}
	as := m["alertStrategy"].(map[string]any)
	if as["notificationRateLimit"].(map[string]any)["period"] != "3600s" || as["autoClose"] != "604800s" {
		t.Errorf("alertStrategy %v", as)
	}
	if m["displayName"] != "cloud0: Application error logged (Cloud Run)" {
		t.Errorf("name %v", m["displayName"])
	}
	doc := m["documentation"].(map[string]any)["content"].(string)
	if !strings.Contains(doc, "Error Reporting") || !strings.Contains(doc, "Monitoring API") {
		t.Errorf("documentation not honest about Error Reporting: %s", doc)
	}
	e := errDecode(t, errBuild(t, TplRunErrorLogged, errEdited(TplRunErrorLogged), nil).Policy)
	if e["alertStrategy"].(map[string]any)["notificationRateLimit"].(map[string]any)["period"] != "900s" {
		t.Errorf("edited rate limit")
	}
}

func TestErrorLabelsChannelsDocsPropagate(t *testing.T) {
	for _, id := range errIDs {
		got := errBuild(t, id, TemplateParams{Instance: "x"}, []string{errChan, "projects/proj-a/notificationChannels/2"})
		m := errDecode(t, got.Policy)
		l := m["userLabels"].(map[string]any)
		if l["managed-by"] != "cloud0" || l["cloud0-template"] != id || l["cloud0-instance"] != "x" {
			t.Errorf("%s labels %v", id, l)
		}
		if ch := m["notificationChannels"].([]any); len(ch) != 2 || ch[0] != errChan {
			t.Errorf("%s channels %v", id, ch)
		}
		if len(got.Warnings) != 0 {
			t.Errorf("%s warnings %v", id, got.Warnings)
		}
		if !strings.HasPrefix(m["displayName"].(string), "cloud0: ") || !strings.HasSuffix(m["displayName"].(string), " — x") {
			t.Errorf("%s name %v", id, m["displayName"])
		}
		doc := m["documentation"].(map[string]any)
		if doc["mimeType"] != "text/markdown" || !strings.Contains(doc["content"].(string), "Logs Explorer") {
			t.Errorf("%s documentation %v", id, doc)
		}
		if id != TplLB5xxBurst && !strings.Contains(doc["content"].(string), "Errors") {
			t.Errorf("%s doc lacks Errors tab pointer", id)
		}
		// override wins over routing
		o := errDecode(t, errBuild(t, id, TemplateParams{ChannelOverride: []string{"projects/proj-a/notificationChannels/9"}}, []string{errChan}).Policy)
		if ch := o["notificationChannels"].([]any); len(ch) != 1 || ch[0] != "projects/proj-a/notificationChannels/9" {
			t.Errorf("%s override %v", id, ch)
		}
		// no channels -> empty list + warning
		n := errBuild(t, id, TemplateParams{}, nil)
		if !strings.Contains(string(n.Policy), `"notificationChannels":[]`) || len(n.Warnings) == 0 {
			t.Errorf("%s no-channel handling: %v", id, n.Warnings)
		}
	}
}

func TestErrorSeverityPropagates(t *testing.T) {
	for _, id := range errIDs {
		m := errDecode(t, errBuild(t, id, TemplateParams{Severity: SevCritical}, nil).Policy)
		if m["severity"] != SevCritical {
			t.Errorf("%s severity %v", id, m["severity"])
		}
	}
	if errDecode(t, errBuild(t, TplRun5xxBurst, TemplateParams{}, nil).Policy)["severity"] != SevError {
		t.Error("default severity")
	}
}

func TestErrorPolicyIsValidRESTAlertPolicy(t *testing.T) {
	for _, id := range errIDs {
		for _, p := range []TemplateParams{{}, errEdited(id)} {
			raw := errBuild(t, id, p, []string{errChan}).Policy
			var pol monitoringpb.AlertPolicy
			if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, &pol); err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			if pol.Severity == monitoringpb.AlertPolicy_SEVERITY_UNSPECIFIED || len(pol.Conditions) == 0 ||
				pol.UserLabels["managed-by"] != "cloud0" || len(pol.NotificationChannels) != 1 {
				t.Errorf("%s: parsed policy incomplete: %v", id, &pol)
			}
			switch id {
			case TplRunErrorLogged:
				if pol.Conditions[0].GetConditionMatchedLog() == nil || pol.AlertStrategy.GetNotificationRateLimit().GetPeriod() == nil ||
					pol.AlertStrategy.GetAutoClose().GetSeconds() != 604800 {
					t.Errorf("log condition not parsed: %v", &pol)
				}
			case TplRun5xxRate:
				if pol.Combiner != monitoringpb.AlertPolicy_AND || len(pol.Conditions) != 2 ||
					len(pol.Conditions[0].GetConditionThreshold().GetDenominatorAggregations()) != 1 ||
					pol.Conditions[0].GetConditionThreshold().GetDenominatorFilter() == "" {
					t.Errorf("rate policy not parsed: %v", &pol)
				}
			default:
				c := pol.Conditions[0].GetConditionThreshold()
				if c == nil || len(c.Aggregations[0].GroupByFields) != 1 || c.Aggregations[0].PerSeriesAligner != monitoringpb.Aggregation_ALIGN_DELTA {
					t.Errorf("%s threshold not parsed: %v", id, &pol)
				}
			}
		}
	}
}
