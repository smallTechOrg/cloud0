package alerting

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/protobuf/encoding/protojson"
)

func dbCtx(checks ...string) BuildContext {
	c := BuildContext{Project: "proj-a", Channels: []string{"projects/proj-a/notificationChannels/111"}}
	for _, id := range checks {
		c.UptimeChecks = append(c.UptimeChecks, UptimeCheck{Name: "projects/proj-a/uptimeCheckConfigs/" + id, Project: "proj-a"})
	}
	return c
}

func dbBuild(t *testing.T, id string, p TemplateParams, ctx BuildContext) (Built, error) {
	t.Helper()
	b := NewBuilder()
	tpl, ok := b.Template(id)
	if !ok {
		t.Fatalf("template %s not registered", id)
	}
	return b.Build(tpl, p, ctx)
}

func dbGolden(t *testing.T, name string, raw json.RawMessage) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	buf.WriteByte('\n')
	path := filepath.Join("testdata", "db_"+name+".golden.json")
	if os.Getenv("DB_GOLDEN_UPDATE") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (run with DB_GOLDEN_UPDATE=1)", path, err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Fatalf("golden %s mismatch:\n--- got\n%s\n--- want\n%s", path, buf.String(), want)
	}
}

func dbPtrF(v float64) *float64 { return &v }
func dbPtrI(v int) *int         { return &v }

func TestDBTemplatesRegistered(t *testing.T) {
	b := NewBuilder()
	for _, id := range []string{TplSQLDown, TplSQLCPUHigh, TplSQLMemoryHigh, TplSQLDiskHigh, TplUptimeDown} {
		tpl, ok := b.Template(id)
		if !ok {
			t.Fatalf("%s missing", id)
		}
		if tpl.Title == "" || tpl.Description == "" || len(tpl.ParamSpecs) == 0 || tpl.Defaults.Severity == "" || !tpl.DefaultEnabled {
			t.Errorf("%s: incomplete metadata %+v", id, tpl)
		}
		if _, err := b.Merge(tpl, TemplateParams{}); err != nil {
			t.Errorf("%s: defaults do not validate: %v", id, err)
		}
	}
}

func TestDBGolden(t *testing.T) {
	cases := []struct {
		name, id string
		p        TemplateParams
		ctx      BuildContext
	}{
		{"sql_down_default", TplSQLDown, TemplateParams{}, dbCtx()},
		{"sql_down_edited", TplSQLDown, TemplateParams{DurationSeconds: dbPtrI(300), Severity: SevError}, dbCtx()},
		{"sql_cpu_default", TplSQLCPUHigh, TemplateParams{}, dbCtx()},
		{"sql_cpu_edited", TplSQLCPUHigh, TemplateParams{Threshold: dbPtrF(90), DurationSeconds: dbPtrI(900), Severity: SevCritical}, dbCtx()},
		{"sql_memory_default", TplSQLMemoryHigh, TemplateParams{}, dbCtx()},
		{"sql_memory_edited", TplSQLMemoryHigh, TemplateParams{Threshold: dbPtrF(92.5), DurationSeconds: dbPtrI(1800)}, dbCtx()},
		{"sql_disk_default", TplSQLDiskHigh, TemplateParams{}, dbCtx()},
		{"sql_disk_edited", TplSQLDiskHigh, TemplateParams{Threshold: dbPtrF(70), DurationSeconds: dbPtrI(60)}, dbCtx()},
		{"uptime_warning_one", TplUptimeDown, TemplateParams{}, dbCtx("check-1")},
		{"uptime_critical_two", TplUptimeDown, TemplateParams{Instance: "critical"}, dbCtx("check-1", "check-2")},
		{"uptime_edited", TplUptimeDown, TemplateParams{Instance: "warning", DurationSeconds: dbPtrI(1200), Severity: SevError}, dbCtx("check-1")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := dbBuild(t, c.id, c.p, c.ctx)
			if err != nil {
				t.Fatal(err)
			}
			dbGolden(t, c.name, got.Policy)
		})
	}
}

func dbDecode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func dbCondition(m map[string]any) map[string]any {
	return m["conditions"].([]any)[0].(map[string]any)["conditionThreshold"].(map[string]any)
}

func TestDBDefaultsMatchSpecTable(t *testing.T) {
	cases := []struct {
		id    string
		sev   string
		thr   float64 // ratio, 0 = n/a
		dur   string
		cmp   string
		field string
	}{
		{TplSQLDown, "CRITICAL", 1, "180s", "COMPARISON_LT", "evaluationMissingData"},
		{TplSQLCPUHigh, "WARNING", 0.8, "300s", "COMPARISON_GT", ""},
		{TplSQLMemoryHigh, "ERROR", 0.85, "300s", "COMPARISON_GT", "denominatorFilter"},
		{TplSQLDiskHigh, "ERROR", 0.85, "300s", "COMPARISON_GT", ""},
	}
	for _, c := range cases {
		got, err := dbBuild(t, c.id, TemplateParams{}, dbCtx())
		if err != nil {
			t.Fatal(err)
		}
		m := dbDecode(t, got.Policy)
		cond := dbCondition(m)
		if m["severity"] != c.sev || cond["thresholdValue"] != c.thr || cond["duration"] != c.dur || cond["comparison"] != c.cmp {
			t.Errorf("%s: severity/threshold/duration/comparison = %v %v %v %v", c.id, m["severity"], cond["thresholdValue"], cond["duration"], cond["comparison"])
		}
		if c.field != "" && cond[c.field] == nil {
			t.Errorf("%s: missing %s", c.id, c.field)
		}
		if m["enabled"] != true {
			t.Errorf("%s: not enabled", c.id)
		}
		if ch := m["notificationChannels"].([]any); len(ch) != 1 || ch[0] != "projects/proj-a/notificationChannels/111" {
			t.Errorf("%s: channels %v", c.id, ch)
		}
		labels := m["userLabels"].(map[string]any)
		if labels[LabelManagedBy] != "cloud0" || labels[LabelTemplate] != c.id {
			t.Errorf("%s: labels %v", c.id, labels)
		}
		if _, has := labels[LabelInstance]; has {
			t.Errorf("%s: unexpected instance label", c.id)
		}
		doc := m["documentation"].(map[string]any)
		if doc["mimeType"] != "text/markdown" || doc["content"] == "" {
			t.Errorf("%s: documentation %v", c.id, doc)
		}
	}
}

func TestDBSQLMemoryUsesQuotaRatio(t *testing.T) {
	got, _ := dbBuild(t, TplSQLMemoryHigh, TemplateParams{}, dbCtx())
	cond := dbCondition(dbDecode(t, got.Policy))
	if !strings.Contains(cond["filter"].(string), "memory/total_usage") || !strings.Contains(cond["denominatorFilter"].(string), "memory/quota") {
		t.Fatalf("filters: %v", cond)
	}
	if strings.Contains(cond["filter"].(string), "memory/utilization") || strings.Contains(cond["denominatorFilter"].(string), "memory/utilization") {
		t.Fatal("must not build on memory/utilization")
	}
	if !strings.Contains(string(got.Policy), "shared-core") {
		t.Fatal("documentation must explain the shared-core rationale")
	}
}

func TestDBUptimeShape(t *testing.T) {
	one, err := dbBuild(t, TplUptimeDown, TemplateParams{}, dbCtx("check-1"))
	if err != nil {
		t.Fatal(err)
	}
	m := dbDecode(t, one.Policy)
	cond := dbCondition(m)
	if m["displayName"] != "cloud0: Service down (10 min)" || m["severity"] != "WARNING" || cond["duration"] != "600s" {
		t.Errorf("warning: %v %v %v", m["displayName"], m["severity"], cond["duration"])
	}
	labels := m["userLabels"].(map[string]any)
	if labels[LabelInstance] != "warning" || labels[LabelTemplate] != TplUptimeDown || labels[LabelManagedBy] != "cloud0" {
		t.Errorf("labels %v", labels)
	}
	if f := cond["filter"].(string); !strings.Contains(f, `resource.type = "uptime_url"`) || !strings.Contains(f, `one_of("check-1")`) || strings.Contains(f, "check-2") {
		t.Errorf("filter %s", f)
	}
	agg := cond["aggregations"].([]any)[0].(map[string]any)
	if agg["alignmentPeriod"] != "60s" || agg["perSeriesAligner"] != "ALIGN_NEXT_OLDER" || agg["crossSeriesReducer"] != "REDUCE_COUNT_FALSE" ||
		agg["groupByFields"].([]any)[0] != "metric.label.check_id" || cond["comparison"] != "COMPARISON_GT" || cond["thresholdValue"] != float64(1) {
		t.Errorf("aggregation %v", agg)
	}

	two, err := dbBuild(t, TplUptimeDown, TemplateParams{Instance: "critical"}, dbCtx("check-1", "check-2"))
	if err != nil {
		t.Fatal(err)
	}
	m = dbDecode(t, two.Policy)
	cond = dbCondition(m)
	if m["displayName"] != "cloud0: Service down (2 h)" || m["severity"] != "CRITICAL" || cond["duration"] != "7200s" {
		t.Errorf("critical: %v %v %v", m["displayName"], m["severity"], cond["duration"])
	}
	if m["userLabels"].(map[string]any)[LabelInstance] != "critical" {
		t.Errorf("labels %v", m["userLabels"])
	}
	if f := cond["filter"].(string); !strings.Contains(f, `one_of("check-1", "check-2")`) {
		t.Errorf("filter %s", f)
	}
}

func TestDBUptimeCheckRestriction(t *testing.T) {
	got, err := dbBuild(t, TplUptimeDown, TemplateParams{UptimeCheckIDs: []string{"projects/proj-a/uptimeCheckConfigs/check-2"}}, dbCtx("check-1", "check-2"))
	if err != nil {
		t.Fatal(err)
	}
	f := dbCondition(dbDecode(t, got.Policy))["filter"].(string)
	if !strings.Contains(f, `one_of("check-2")`) || strings.Contains(f, "check-1") {
		t.Errorf("filter %s", f)
	}
	if _, err := dbBuild(t, TplUptimeDown, TemplateParams{UptimeCheckIDs: []string{"nope"}}, dbCtx("check-1")); !errors.Is(err, ErrInvalidParams) {
		t.Errorf("unknown check id: %v", err)
	}
}

func TestDBErrors(t *testing.T) {
	_, err := dbBuild(t, TplUptimeDown, TemplateParams{}, dbCtx())
	if !errors.Is(err, ErrInvalidParams) || !strings.Contains(err.Error(), "no uptime checks in project") {
		t.Errorf("missing checks: %v", err)
	}
	if _, err := dbBuild(t, TplUptimeDown, TemplateParams{Instance: "bogus"}, dbCtx("c")); !errors.Is(err, ErrInvalidParams) {
		t.Errorf("bad instance: %v", err)
	}
	bad := []struct {
		id string
		p  TemplateParams
	}{
		{TplSQLCPUHigh, TemplateParams{Threshold: dbPtrF(0)}},
		{TplSQLCPUHigh, TemplateParams{Threshold: dbPtrF(101)}},
		{TplSQLDiskHigh, TemplateParams{DurationSeconds: dbPtrI(30)}},
		{TplSQLDiskHigh, TemplateParams{DurationSeconds: dbPtrI(90)}},
		{TplSQLMemoryHigh, TemplateParams{DurationSeconds: dbPtrI(90000)}},
		{TplSQLDown, TemplateParams{Severity: "PANIC"}},
		{TplSQLDown, TemplateParams{Threshold: dbPtrF(1)}},
		{TplUptimeDown, TemplateParams{DurationSeconds: dbPtrI(61)}},
	}
	for _, c := range bad {
		if _, err := dbBuild(t, c.id, c.p, dbCtx("c")); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("%s %+v: want ErrInvalidParams, got %v", c.id, c.p, err)
		}
	}
}

func TestDBEnabledAndNoChannels(t *testing.T) {
	off := false
	ctx := dbCtx()
	ctx.Channels = nil
	got, err := dbBuild(t, TplSQLDown, TemplateParams{Enabled: &off}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := dbDecode(t, got.Policy)
	if m["enabled"] != false {
		t.Error("enabled=false not honoured")
	}
	if _, has := m["notificationChannels"]; has {
		t.Error("channels should be omitted when none resolved")
	}
}

func TestDBProtoRoundTrip(t *testing.T) {
	for _, c := range []struct {
		id     string
		p      TemplateParams
		checks []string
	}{
		{TplSQLDown, TemplateParams{}, nil},
		{TplSQLCPUHigh, TemplateParams{}, nil},
		{TplSQLMemoryHigh, TemplateParams{}, nil},
		{TplSQLDiskHigh, TemplateParams{}, nil},
		{TplUptimeDown, TemplateParams{}, []string{"c1", "c2"}},
		{TplUptimeDown, TemplateParams{Instance: "critical"}, []string{"c1"}},
	} {
		got, err := dbBuild(t, c.id, c.p, dbCtx(c.checks...))
		if err != nil {
			t.Fatal(err)
		}
		var pol monitoringpb.AlertPolicy
		if err := protojson.Unmarshal(got.Policy, &pol); err != nil {
			t.Fatalf("%s: not a valid AlertPolicy: %v", c.id, err)
		}
		cond := pol.GetConditions()[0].GetConditionThreshold()
		if cond == nil || cond.GetFilter() == "" || pol.GetSeverity() == monitoringpb.AlertPolicy_SEVERITY_UNSPECIFIED || pol.GetCombiner() != monitoringpb.AlertPolicy_OR {
			t.Fatalf("%s: lost fields: %v", c.id, &pol)
		}
		if c.id == TplSQLDown && cond.GetEvaluationMissingData() != monitoringpb.AlertPolicy_Condition_EVALUATION_MISSING_DATA_ACTIVE {
			t.Errorf("missing-data not preserved")
		}
		back, err := protojson.Marshal(&pol)
		if err != nil {
			t.Fatal(err)
		}
		var again monitoringpb.AlertPolicy
		if err := protojson.Unmarshal(back, &again); err != nil || !jsonEqualDB(t, got.Policy, &pol) {
			t.Fatalf("%s: round trip differs (%v)", c.id, err)
		}
	}
}

// jsonEqualDB compares the builder JSON with the proto's JSON semantically.
func jsonEqualDB(t *testing.T, raw json.RawMessage, pol *monitoringpb.AlertPolicy) bool {
	t.Helper()
	b, err := protojson.Marshal(pol)
	if err != nil {
		t.Fatal(err)
	}
	var a, c any
	if json.Unmarshal(raw, &a) != nil || json.Unmarshal(b, &c) != nil {
		return false
	}
	ab, _ := json.Marshal(a)
	cb, _ := json.Marshal(c)
	return bytes.Equal(ab, cb)
}
