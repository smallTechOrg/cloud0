package store

import (
	"context"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/limits"
)

func TestLimitSettings_PutGetValidateWithLimitsPackage(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()

	if ts, err := LimitsUpdatedAt(ctx, s.DB); err != nil || ts != nil {
		t.Fatalf("fresh updated_at = %v %v", ts, err)
	}
	if tgt, err := GetMonthlyTarget(ctx, s.DB); err != nil || tgt != nil {
		t.Fatalf("fresh target = %v %v", tgt, err)
	}
	kv := map[string]string{limits.SettingKey("logging_gb"): "75", "limit.warn_pct": "90"}
	if err := PutSettings(ctx, s.DB, kv, testNow); err != nil {
		t.Fatal(err)
	}
	if err := PutSettings(ctx, s.DB, map[string]string{limits.SettingKey("logging_gb"): "80"}, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	m, err := GetSettingsWithPrefix(ctx, s.DB, limits.SettingPrefix)
	if err != nil || len(m) != 2 || m["limit.logging_gb"] != "80" {
		t.Fatalf("prefix = %v %v", m, err)
	}
	ov, err := limits.ParseOverrides(m)
	if err != nil || ov["logging_gb"] != 80 || ov[limits.WarnKey] != 90 {
		t.Fatalf("overrides = %v %v", ov, err)
	}
	if ts, _ := LimitsUpdatedAt(ctx, s.DB); ts == nil || !ts.Equal(testNow.Add(time.Minute)) {
		t.Fatalf("updated_at = %v", ts)
	}
	// Anomaly settings are not limit settings.
	if m, _ := GetSettingsWithPrefix(ctx, s.DB, "limit."); len(m) != 2 {
		t.Fatalf("leak: %v", m)
	}
	if err := PutSettings(ctx, s.DB, map[string]string{KeyMonthlyTarget: "1500.5"}, testNow); err != nil {
		t.Fatal(err)
	}
	if tgt, err := GetMonthlyTarget(ctx, s.DB); err != nil || tgt == nil || *tgt != 1500.5 {
		t.Fatalf("target = %v %v", tgt, err)
	}
	if err := PutSettings(ctx, s.DB, kv, time.Time{}); err == nil {
		t.Fatal("zero now accepted")
	}
}
