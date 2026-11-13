package limits

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

const tol = 0.005

func near(a, b float64) bool { return math.Abs(a-b) < tol }

var oct = gcpsource.Window{
	Start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
}

func mustDefault(t *testing.T, id string) Allowance {
	t.Helper()
	for _, a := range Defaults() {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no default allowance %q", id)
	return Allowance{}
}

func TestDefaults_SeedsSheetValuesWithStableIDs(t *testing.T) {
	want := map[string]float64{
		"gcs_storage_gb_month":  5,
		"gcs_class_a_ops":       5000,
		"gcs_class_b_ops":       50000,
		"gcs_egress_na_gb":      100,
		"cloudrun_requests":     2000000,
		"cloudrun_cpu_vcpu_s":   180000,
		"cloudrun_memory_gib_s": 360000,
		"cloudrun_egress_gb":    1,
		"logging_gb":            50,
		"disk_throughput_gib":   50,
		"groq_llama4_scout_rpm": 30,
		"groq_llama4_scout_rpd": 1000,
		"groq_llama4_scout_tpm": 30000,
		"groq_llama4_scout_tpd": 500000,
	}
	got := Defaults()
	if len(got) != len(want) {
		t.Fatalf("Defaults() has %d rows, want %d", len(got), len(want))
	}
	seen := map[string]bool{}
	for _, a := range got {
		if seen[a.ID] {
			t.Errorf("duplicate id %q", a.ID)
		}
		seen[a.ID] = true
		w, ok := want[a.ID]
		if !ok {
			t.Errorf("unexpected id %q", a.ID)
			continue
		}
		if a.Monthly != w {
			t.Errorf("%s: Monthly = %v, want %v", a.ID, a.Monthly, w)
		}
		if a.Label == "" || a.Unit == "" || a.Service == "" {
			t.Errorf("%s: label/unit/service must be set: %+v", a.ID, a)
		}
		if a.Stub {
			if a.Reason == "" {
				t.Errorf("%s: stub row needs a Reason", a.ID)
			}
		} else if a.MetricGroup == "" || a.MetricKey == "" {
			t.Errorf("%s: non-stub row needs group/key", a.ID)
		}
	}
	for _, id := range []string{"disk_throughput_gib", "groq_llama4_scout_rpm", "groq_llama4_scout_rpd", "groq_llama4_scout_tpm", "groq_llama4_scout_tpd"} {
		if !mustDefault(t, id).Stub {
			t.Errorf("%s should be a stub row", id)
		}
	}
	gcs := mustDefault(t, "gcs_storage_gb_month")
	if gcs.MetricGroup != gcpsource.GroupGCS || gcs.MetricKey != gcpsource.MStorageGBMonth {
		t.Errorf("gcs storage maps to %s/%s", gcs.MetricGroup, gcs.MetricKey)
	}
}

func TestDefaults_ReturnsFreshCopy(t *testing.T) {
	a := Defaults()
	a[0].Monthly = -1
	if Defaults()[0].Monthly == -1 {
		t.Fatal("Defaults() shares backing storage between calls")
	}
}

func TestEvaluate_LevelsFromPercentAfterMonthEnd(t *testing.T) {
	a := mustDefault(t, "gcs_storage_gb_month")
	after := oct.End.Add(48 * time.Hour)
	cases := []struct {
		used  float64
		level string
		pct   float64
	}{
		{4.2, LevelNear, 84},
		{5.1, LevelOver, 102},
		{5.0, LevelOver, 100},
		{4.0, LevelNear, 80},
		{3.9, LevelOK, 78},
		{0, LevelOK, 0},
	}
	for _, c := range cases {
		s := Evaluate(a, Usage{MonthToDate: c.used, Week: 1.5}, oct, after, 80)
		if s.Level != c.level || !near(s.Pct, c.pct) {
			t.Errorf("used %v: level %q pct %v, want %q %v", c.used, s.Level, s.Pct, c.level, c.pct)
		}
		if !near(s.Projected, c.used) {
			t.Errorf("used %v: after month end projected = %v, want used", c.used, s.Projected)
		}
		if s.ID != a.ID || s.Allowance != 5 || s.Used != c.used || s.Week != 1.5 {
			t.Errorf("status fields wrong: %+v", s)
		}
	}
}

func TestEvaluate_ProjectsDay10Of31Linearly(t *testing.T) {
	a := mustDefault(t, "gcs_storage_gb_month")
	now := oct.Start.Add(10 * 24 * time.Hour) // 10 full days elapsed of 31
	s := Evaluate(a, Usage{MonthToDate: 1.0}, oct, now, 80)
	if !near(s.Projected, 3.1) {
		t.Fatalf("projected = %v, want 3.1", s.Projected)
	}
	if !near(s.Pct, 62) || s.Level != LevelOK {
		t.Fatalf("pct %v level %q, want 62 ok", s.Pct, s.Level)
	}
	// 1.4 GB-month by day 10 projects to 4.34 → 86.8 % → near, though only 28 % used.
	s = Evaluate(a, Usage{MonthToDate: 1.4}, oct, now, 80)
	if !near(s.Projected, 4.34) || s.Level != LevelNear {
		t.Fatalf("projected %v level %q, want 4.34 near", s.Projected, s.Level)
	}
	// 1.7 → 5.27 → over.
	s = Evaluate(a, Usage{MonthToDate: 1.7}, oct, now, 80)
	if s.Level != LevelOver {
		t.Fatalf("level %q, want over", s.Level)
	}
}

func TestEvaluate_ClampsAtAndBeforeMonthStart(t *testing.T) {
	a := mustDefault(t, "logging_gb")
	s := Evaluate(a, Usage{MonthToDate: 2}, oct, oct.Start, 80)
	if !near(s.Projected, 2) || math.IsInf(s.Pct, 0) || math.IsNaN(s.Pct) {
		t.Fatalf("at start: %+v", s)
	}
	s = Evaluate(a, Usage{MonthToDate: 2}, oct, oct.Start.Add(-time.Hour), 80)
	if !near(s.Projected, 2) {
		t.Fatalf("before start: %+v", s)
	}
	s = Evaluate(a, Usage{MonthToDate: 2}, oct, oct.End, 80)
	if !near(s.Projected, 2) {
		t.Fatalf("at end: %+v", s)
	}
}

func TestEvaluate_DefaultsInvalidWarnTo80(t *testing.T) {
	a := mustDefault(t, "gcs_storage_gb_month")
	after := oct.End
	for _, w := range []float64{0, -5, math.NaN()} {
		if s := Evaluate(a, Usage{MonthToDate: 4.2}, oct, after, w); s.Level != LevelNear {
			t.Errorf("warn %v: level %q, want near", w, s.Level)
		}
	}
	if s := Evaluate(a, Usage{MonthToDate: 4.2}, oct, after, 90); s.Level != LevelOK {
		t.Errorf("warn 90: level %q, want ok", s.Level)
	}
}

func TestEvaluate_ZeroAllowanceDoesNotDivideByZero(t *testing.T) {
	a := Allowance{ID: "x", Monthly: 0}
	s := Evaluate(a, Usage{}, oct, oct.End, 80)
	if s.Level != LevelOK || s.Pct != 0 {
		t.Fatalf("zero usage of zero allowance: %+v", s)
	}
	s = Evaluate(a, Usage{MonthToDate: 1}, oct, oct.End, 80)
	if s.Level != LevelOver || math.IsInf(s.Pct, 0) {
		t.Fatalf("usage of zero allowance: %+v", s)
	}
}

func TestEvaluate_StubRowsAreStubLevel(t *testing.T) {
	a := mustDefault(t, "groq_llama4_scout_rpm")
	s := Evaluate(a, Usage{MonthToDate: 999}, oct, oct.Start.Add(240*time.Hour), 80)
	if s.Level != LevelStub || s.Allowance != 30 || s.Used != 0 || s.Projected != 0 || s.Pct != 0 {
		t.Fatalf("stub status: %+v", s)
	}
}

func TestNoData_ReportsNoDataLevel(t *testing.T) {
	s := NoData(mustDefault(t, "logging_gb"))
	if s.Level != LevelNoData || s.Allowance != 50 || s.ID != "logging_gb" {
		t.Fatalf("no-data status: %+v", s)
	}
	if NoData(mustDefault(t, "disk_throughput_gib")).Level != LevelStub {
		t.Fatal("NoData on a stub row must stay stub")
	}
}

func cell(v float64) gcpsource.Cell { return gcpsource.V(v, "count", gcpsource.Provenance{}) }

func gap() gcpsource.Cell { return gcpsource.G("no data", "", "count", gcpsource.Provenance{}) }

func TestUsageFromRows_PrefersGroupTotals(t *testing.T) {
	a := mustDefault(t, "cloudrun_requests")
	rows := []gcpsource.MetricRow{
		{Group: gcpsource.GroupCloudRun, Project: "p1", Resource: "svc-a", Cells: map[string]gcpsource.Cell{gcpsource.MRequests: cell(100)}},
		{Group: gcpsource.GroupCloudRun, Project: "p1", Resource: "", Cells: map[string]gcpsource.Cell{gcpsource.MRequests: cell(150)}},
		{Group: gcpsource.GroupCloudRun, Project: "p2", Resource: "", Cells: map[string]gcpsource.Cell{gcpsource.MRequests: cell(50)}},
		{Group: gcpsource.GroupCloudRun, Project: "p2", Resource: "svc-b", Cells: map[string]gcpsource.Cell{gcpsource.MRequests: cell(7)}},
		{Group: gcpsource.GroupGCS, Project: "p1", Resource: "", Cells: map[string]gcpsource.Cell{gcpsource.MRequests: cell(1e6)}},
	}
	v, ok := UsageFromRows(rows, a)
	if !ok || !near(v, 200) {
		t.Fatalf("got %v %v, want 200 true (sum of group totals only)", v, ok)
	}
}

func TestUsageFromRows_FallsBackToResourceRows(t *testing.T) {
	a := mustDefault(t, "cloudrun_requests")
	rows := []gcpsource.MetricRow{
		{Group: gcpsource.GroupCloudRun, Resource: "svc-a", Cells: map[string]gcpsource.Cell{gcpsource.MRequests: cell(100)}},
		{Group: gcpsource.GroupCloudRun, Resource: "svc-b", Cells: map[string]gcpsource.Cell{gcpsource.MRequests: cell(25)}},
		{Group: gcpsource.GroupCloudRun, Resource: "svc-c", Cells: map[string]gcpsource.Cell{gcpsource.MRequests: gap()}},
		// A group total whose cell is a gap does not count as "present".
		{Group: gcpsource.GroupCloudRun, Resource: "", Cells: map[string]gcpsource.Cell{gcpsource.MRequests: gap()}},
	}
	v, ok := UsageFromRows(rows, a)
	if !ok || !near(v, 125) {
		t.Fatalf("got %v %v, want 125 true", v, ok)
	}
}

func TestUsageFromRows_CloudRunMemoryAndCPUUseTotalColumnNotSplit(t *testing.T) {
	// Monitoring group-total row (active + idle), billing split rows per service.
	rows := []gcpsource.MetricRow{
		{Group: gcpsource.GroupCloudRun, Project: "p1", Resource: "svc-a", Cells: map[string]gcpsource.Cell{
			gcpsource.MServiceMemGiBs: cell(310), gcpsource.MServiceMemActiveGiBs: cell(10), gcpsource.MServiceMemIdleGiBs: cell(300),
			gcpsource.MServiceCPUSec: cell(620), gcpsource.MServiceCPUActiveSec: cell(20), gcpsource.MServiceCPUIdleSec: cell(600)}},
		{Group: gcpsource.GroupCloudRun, Project: "p1", Resource: "svc-b", Cells: map[string]gcpsource.Cell{
			gcpsource.MServiceMemGiBs: cell(15), gcpsource.MServiceMemActiveGiBs: cell(15),
			gcpsource.MServiceCPUSec: cell(30), gcpsource.MServiceCPUActiveSec: cell(30)}},
		{Group: gcpsource.GroupCloudRun, Project: "p1", Resource: "", Cells: map[string]gcpsource.Cell{
			gcpsource.MServiceMemGiBs: cell(325), gcpsource.MServiceCPUSec: cell(650), gcpsource.MEgressInterGB: cell(0.5)}},
	}
	for id, want := range map[string]float64{"cloudrun_memory_gib_s": 325, "cloudrun_cpu_vcpu_s": 650} {
		a := mustDefault(t, id)
		if a.MetricKey == gcpsource.MServiceMemActiveGiBs || a.MetricKey == gcpsource.MServiceMemIdleGiBs ||
			a.MetricKey == gcpsource.MServiceCPUActiveSec || a.MetricKey == gcpsource.MServiceCPUIdleSec {
			t.Fatalf("%s evaluates on a split column %s", id, a.MetricKey)
		}
		v, ok := UsageFromRows(rows, a)
		if !ok || !near(v, want) {
			t.Errorf("%s: got %v %v, want %v true (the total row, not active or idle alone)", id, v, ok, want)
		}
		if !strings.Contains(a.Reason, "active + idle min-instance") {
			t.Errorf("%s reason = %q", id, a.Reason)
		}
	}
	// Without a total row the per-service TOTAL cells sum (never the split).
	v, ok := UsageFromRows(rows[:2], mustDefault(t, "cloudrun_memory_gib_s"))
	if !ok || !near(v, 325) {
		t.Errorf("resource fallback: got %v %v, want 325", v, ok)
	}
}

func TestUsageFromRows_FalseWhenNoValue(t *testing.T) {
	a := mustDefault(t, "logging_gb")
	cases := map[string][]gcpsource.MetricRow{
		"nil rows":  nil,
		"only gaps": {{Group: gcpsource.GroupLogging, Cells: map[string]gcpsource.Cell{gcpsource.MLoggingGB: gap()}}},
		"other key": {{Group: gcpsource.GroupLogging, Cells: map[string]gcpsource.Cell{gcpsource.MSentGB: cell(3)}}},
		"other grp": {{Group: gcpsource.GroupVM, Cells: map[string]gcpsource.Cell{gcpsource.MLoggingGB: cell(3)}}},
		"nil cells": {{Group: gcpsource.GroupLogging}},
	}
	for name, rows := range cases {
		if v, ok := UsageFromRows(rows, a); ok || v != 0 {
			t.Errorf("%s: got %v %v, want 0 false", name, v, ok)
		}
	}
	// A real zero is data, not a gap.
	rows := []gcpsource.MetricRow{{Group: gcpsource.GroupLogging, Cells: map[string]gcpsource.Cell{gcpsource.MLoggingGB: cell(0)}}}
	if v, ok := UsageFromRows(rows, a); !ok || v != 0 {
		t.Errorf("zero value: got %v %v, want 0 true", v, ok)
	}
}

func TestUsageFromRows_StubAlwaysFalse(t *testing.T) {
	a := mustDefault(t, "disk_throughput_gib")
	rows := []gcpsource.MetricRow{{Group: a.MetricGroup, Cells: map[string]gcpsource.Cell{a.MetricKey: cell(3)}}}
	if _, ok := UsageFromRows(rows, a); ok {
		t.Fatal("stub allowance must never report usage")
	}
}

func TestParseOverrides_ReadsLimitsAndWarn(t *testing.T) {
	got, err := ParseOverrides(map[string]string{
		"limit.gcs_storage_gb_month": "10",
		"limit.logging_gb":           " 75.5 ",
		"limit.warn_pct":             "90",
		"anomaly.pct":                "abc", // not ours
		"limit.unknown_thing":        "abc", // unknown id ignored
	})
	if err != nil {
		t.Fatalf("ParseOverrides: %v", err)
	}
	if got["gcs_storage_gb_month"] != 10 || got["logging_gb"] != 75.5 || got[WarnKey] != 90 {
		t.Fatalf("got %v", got)
	}
	if _, ok := got["unknown_thing"]; ok {
		t.Fatal("unknown id should be ignored")
	}
}

func TestParseOverrides_WarnDefaultsTo80(t *testing.T) {
	got, err := ParseOverrides(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[WarnKey] != DefaultWarnPct || len(got) != 1 {
		t.Fatalf("got %v, want only warn_pct=80", got)
	}
}

func TestParseOverrides_RejectsBadValues(t *testing.T) {
	bad := []map[string]string{
		{"limit.gcs_class_a_ops": "five thousand"},
		{"limit.gcs_class_a_ops": ""},
		{"limit.gcs_class_a_ops": "-1"},
		{"limit.gcs_class_a_ops": "NaN"},
		{"limit.gcs_class_a_ops": "Inf"},
		{"limit.warn_pct": "high"},
		{"limit.warn_pct": "0"},
		{"limit.warn_pct": "101"},
	}
	for _, m := range bad {
		if _, err := ParseOverrides(m); !errors.Is(err, ErrInvalidOverride) {
			t.Errorf("%v: err = %v, want ErrInvalidOverride", m, err)
		}
	}
}

func TestApply_OverridesMonthlyByID(t *testing.T) {
	out := Apply(Defaults(), map[string]float64{"logging_gb": 75, WarnKey: 90})
	for _, a := range out {
		switch a.ID {
		case "logging_gb":
			if a.Monthly != 75 {
				t.Errorf("logging_gb = %v, want 75", a.Monthly)
			}
		case "gcs_storage_gb_month":
			if a.Monthly != 5 {
				t.Errorf("gcs untouched = %v", a.Monthly)
			}
		}
	}
	if mustDefault(t, "logging_gb").Monthly != 50 {
		t.Fatal("Apply mutated defaults")
	}
}

func TestSettingKey_UsesLimitPrefix(t *testing.T) {
	if SettingKey("logging_gb") != "limit.logging_gb" {
		t.Fatal(SettingKey("logging_gb"))
	}
}
