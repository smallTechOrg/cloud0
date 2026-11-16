package alerting

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	monitoringpb "cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
)

func vmTpl(t *testing.T, id string) (Builder, Template) {
	t.Helper()
	b := NewBuilder()
	tpl, ok := b.Template(id)
	if !ok {
		t.Fatalf("template %s missing", id)
	}
	return b, tpl
}

var vmIDs = []string{TplVMCPUHigh, TplVMMemoryHigh, TplVMDiskHigh}

func buildVMT(t *testing.T, id string, p TemplateParams, chans []string) string {
	t.Helper()
	b, tpl := vmTpl(t, id)
	got, err := b.Build(tpl, p, BuildContext{Project: "proj-a", Channels: chans})
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(got.Policy, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return string(out)
}

func TestVMGolden(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }
	cases := map[string]TemplateParams{
		"default": {},
		"edited":  {Threshold: f(90), DurationSeconds: i(120), Severity: SevCritical},
	}
	for _, id := range vmIDs {
		for name, p := range cases {
			path := "testdata/" + id + "_" + name + ".golden.json"
			got := buildVMT(t, id, p, []string{"projects/proj-a/notificationChannels/1"}) + "\n"
			if os.Getenv("UPDATE_GOLDEN") != "" {
				_ = os.MkdirAll("testdata", 0o755)
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

func TestVMDefaultsMatchFrozenTable(t *testing.T) {
	want := map[string]struct {
		thr float64
		dur int
		sev string
	}{TplVMCPUHigh: {80, 300, SevWarning}, TplVMMemoryHigh: {85, 900, SevError}, TplVMDiskHigh: {85, 300, SevError}}
	for id, w := range want {
		_, tpl := vmTpl(t, id)
		d := tpl.Defaults
		if *d.Threshold != w.thr || *d.DurationSeconds != w.dur || tpl.Severity != w.sev || d.Severity != w.sev || !tpl.DefaultEnabled || !*d.Enabled || tpl.Kind != KindThreshold {
			t.Errorf("%s defaults wrong: %+v", id, tpl)
		}
	}
}

func TestVMCPURatioConversion(t *testing.T) {
	var m map[string]any
	_ = json.Unmarshal([]byte(buildVMT(t, TplVMCPUHigh, TemplateParams{}, nil)), &m)
	ct := m["conditions"].([]any)[0].(map[string]any)["conditionThreshold"].(map[string]any)
	if ct["thresholdValue"].(float64) != 0.8 || ct["duration"] != "300s" {
		t.Errorf("bad cpu condition: %v", ct)
	}
	_ = json.Unmarshal([]byte(buildVMT(t, TplVMMemoryHigh, TemplateParams{}, nil)), &m)
	ct = m["conditions"].([]any)[0].(map[string]any)["conditionThreshold"].(map[string]any)
	if ct["thresholdValue"].(float64) != 85 || !strings.Contains(ct["filter"].(string), `metric.labels.state="used"`) {
		t.Errorf("bad memory condition: %v", ct)
	}
}

func TestVMDiskHasSecondRatioCondition(t *testing.T) {
	var m map[string]any
	_ = json.Unmarshal([]byte(buildVMT(t, TplVMDiskHigh, TemplateParams{}, nil)), &m)
	cs := m["conditions"].([]any)
	if len(cs) != 2 || m["combiner"] != "OR" {
		t.Fatalf("want 2 OR conditions, got %d", len(cs))
	}
	ct := cs[1].(map[string]any)["conditionThreshold"].(map[string]any)
	if !strings.Contains(ct["denominatorFilter"].(string), `state!=""`) || ct["thresholdValue"].(float64) != 0.85 {
		t.Errorf("bad ratio condition: %v", ct)
	}
	if !strings.Contains(m["documentation"].(map[string]any)["content"].(string), "root device") {
		t.Error("limitation not documented")
	}
}

func TestVMCommonShape(t *testing.T) {
	chans := []string{"projects/proj-a/notificationChannels/1", "projects/proj-a/notificationChannels/2"}
	for _, id := range vmIDs {
		s := buildVMT(t, id, TemplateParams{}, chans)
		if strings.Contains(s, "instance_name") || strings.Contains(s, `resource.labels.instance_id="`) || strings.Contains(s, `resource.label.instance_id="`) {
			t.Errorf("%s restricts to an instance", id)
		}
		if strings.Contains(s, `"name"`) {
			t.Errorf("%s body carries name", id)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(s), &m)
		labels := m["userLabels"].(map[string]any)
		if labels["managed-by"] != "cloud0" || labels["cloud0-template"] != id {
			t.Errorf("%s labels: %v", id, labels)
		}
		if len(m["notificationChannels"].([]any)) != 2 {
			t.Errorf("%s channels not propagated", id)
		}
		if m["enabled"] != true || m["combiner"] != "OR" || !strings.HasPrefix(m["displayName"].(string), "cloud0: VM ") {
			t.Errorf("%s header wrong: %v", id, m)
		}
		doc := m["documentation"].(map[string]any)
		if doc["mimeType"] != "text/markdown" || !strings.Contains(doc["subject"].(string), "${resource.label.instance_id}") {
			t.Errorf("%s documentation: %v", id, doc)
		}
	}
}

func TestVMExistingDoesNotLeakName(t *testing.T) {
	b, tpl := vmTpl(t, TplVMCPUHigh)
	got, err := b.Build(tpl, TemplateParams{}, BuildContext{Existing: &PolicyView{Name: "projects/proj-a/alertPolicies/9"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got.Policy), "alertPolicies/9") || strings.Contains(string(got.Policy), `"name"`) {
		t.Error("name leaked")
	}
}

func TestVMEnabledAndNoChannelsWarning(t *testing.T) {
	b, tpl := vmTpl(t, TplVMCPUHigh)
	off := false
	got, _ := b.Build(tpl, TemplateParams{Enabled: &off}, BuildContext{})
	if !strings.Contains(string(got.Policy), `"enabled":false`) || len(got.Warnings) == 0 || !strings.Contains(string(got.Policy), `"notificationChannels":[]`) {
		t.Errorf("unexpected: %s %v", got.Policy, got.Warnings)
	}
}

func TestVMValidation(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }
	for _, id := range vmIDs {
		b, tpl := vmTpl(t, id)
		for _, p := range []TemplateParams{
			{Threshold: f(0)}, {Threshold: f(101)}, {DurationSeconds: i(30)}, {DurationSeconds: i(90000)}, {Severity: "BOGUS"},
		} {
			if _, err := b.Merge(tpl, p); !errors.Is(err, ErrInvalidParams) {
				t.Errorf("%s %+v: want ErrInvalidParams, got %v", id, p, err)
			}
			if _, err := b.Build(tpl, p, BuildContext{}); !errors.Is(err, ErrInvalidParams) {
				t.Errorf("%s build %+v: want ErrInvalidParams, got %v", id, p, err)
			}
		}
	}
}

func TestVMPolicyIsValidRESTAlertPolicy(t *testing.T) {
	for _, id := range vmIDs {
		s := buildVMT(t, id, TemplateParams{}, []string{"projects/proj-a/notificationChannels/1"})
		var pol monitoringpb.AlertPolicy
		if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(s), &pol); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if pol.Severity.String() == "" || len(pol.Conditions) == 0 || pol.UserLabels["managed-by"] != "cloud0" {
			t.Errorf("%s: parsed policy incomplete", id)
		}
	}
}
