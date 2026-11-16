package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

func TestGetThresholds_ReturnsSeededDefaults(t *testing.T) {
	s := openMigrated(t)
	got, err := GetThresholds(context.Background(), s.DB)
	if err != nil {
		t.Fatal(err)
	}
	if got.AnomalyPctThreshold != 20 || got.AnomalyAbsThreshold != 200 {
		t.Fatalf("defaults = %+v, want 20 / 50", got)
	}
	if got.UpdatedAt.IsZero() || got.UpdatedAt.Location() != time.UTC {
		t.Fatalf("updated_at = %v", got.UpdatedAt)
	}
}

func TestPutThresholds_RoundTripsAtomicallyAndSurvivesReopen(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	later := testNow.Add(time.Minute)

	put, err := PutThresholds(ctx, s.DB, 10, 25.5, later)
	if err != nil {
		t.Fatalf("PutThresholds: %v", err)
	}
	if put.AnomalyPctThreshold != 10 || put.AnomalyAbsThreshold != 25.5 || !put.UpdatedAt.Equal(later) {
		t.Fatalf("put = %+v", put)
	}
	got, err := GetThresholds(ctx, s.DB)
	if err != nil {
		t.Fatal(err)
	}
	if got != put {
		t.Fatalf("get = %+v, want %+v", got, put)
	}
	// Zero is a legal threshold ("flag any change").
	if _, err := PutThresholds(ctx, s.DB, 0, 0, later.Add(time.Second)); err != nil {
		t.Fatalf("zero thresholds rejected: %v", err)
	}
	zero, _ := GetThresholds(ctx, s.DB)
	if zero.AnomalyPctThreshold != 0 || zero.AnomalyAbsThreshold != 0 || !zero.UpdatedAt.After(got.UpdatedAt) {
		t.Fatalf("zero = %+v", zero)
	}

	// State survival: reopen the same file.
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	re, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	again, err := GetThresholds(ctx, re.DB)
	if err != nil {
		t.Fatal(err)
	}
	if again != zero {
		t.Fatalf("after reopen = %+v, want %+v", again, zero)
	}
}

func TestPutThresholds_RejectsNegativeNaNAndInf(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	cases := [][2]float64{{-1, 50}, {20, -0.01}, {math.NaN(), 50}, {20, math.Inf(1)}, {math.Inf(-1), 50}}
	for _, c := range cases {
		if _, err := PutThresholds(ctx, s.DB, c[0], c[1], testNow); !errors.Is(err, ErrInvalidSettings) {
			t.Errorf("PutThresholds(%v, %v) err = %v, want ErrInvalidSettings", c[0], c[1], err)
		}
	}
	got, _ := GetThresholds(ctx, s.DB)
	if got.AnomalyPctThreshold != 20 || got.AnomalyAbsThreshold != 200 {
		t.Fatalf("rejected writes changed settings: %+v", got)
	}
}

func TestSettings_JSONShapeMatchesAPI(t *testing.T) {
	s := openMigrated(t)
	got, _ := PutThresholds(context.Background(), s.DB, 12.5, 30, testNow)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"anomaly_pct_threshold":12.5,"anomaly_abs_threshold_usd":30,"updated_at":"2026-10-26T09:00:00Z"}`
	if string(b) != want {
		t.Fatalf("json = %s\nwant   %s", b, want)
	}
}
