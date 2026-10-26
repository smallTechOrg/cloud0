package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

// Settings keys seeded by migration 0001 (spec/data.md → settings).
const (
	KeyAnomalyPctThreshold = "anomaly_pct_threshold"
	KeyAnomalyAbsThreshold = "anomaly_abs_threshold_usd"
)

// ErrInvalidSettings is returned for a threshold that is not a finite number ≥ 0.
var ErrInvalidSettings = errors.New("store: invalid settings")

// Settings is the GET/PUT /api/v1/settings body.
type Settings struct {
	AnomalyPctThreshold float64   `json:"anomaly_pct_threshold"`
	AnomalyAbsThreshold float64   `json:"anomaly_abs_threshold_usd"`
	UpdatedAt           time.Time `json:"updated_at"` // latest of the two rows
}

// GetThresholds reads both anomaly thresholds.
func GetThresholds(ctx context.Context, q Querier) (Settings, error) {
	rows, err := q.QueryContext(ctx, `SELECT "key", value, updated_at FROM settings WHERE "key" IN (?, ?)`, KeyAnomalyPctThreshold, KeyAnomalyAbsThreshold)
	if err != nil {
		return Settings{}, fmt.Errorf("store: get thresholds: %w", err)
	}
	defer rows.Close()
	var out Settings
	seen := 0
	for rows.Next() {
		var key, value, updated string
		if err := rows.Scan(&key, &value, &updated); err != nil {
			return Settings{}, fmt.Errorf("store: get thresholds: %w", err)
		}
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return Settings{}, fmt.Errorf("store: setting %s has non-numeric value %q: %w", key, value, err)
		}
		t, err := ParseTime(updated)
		if err != nil {
			return Settings{}, fmt.Errorf("store: setting %s updated_at %q: %w", key, updated, err)
		}
		switch key {
		case KeyAnomalyPctThreshold:
			out.AnomalyPctThreshold = f
		case KeyAnomalyAbsThreshold:
			out.AnomalyAbsThreshold = f
		}
		if t.After(out.UpdatedAt) {
			out.UpdatedAt = t
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return Settings{}, fmt.Errorf("store: get thresholds: %w", err)
	}
	if seen != 2 {
		return Settings{}, fmt.Errorf("store: settings rows missing (%d of 2) — run `cloud0 migrate up`", seen)
	}
	return out, nil
}

func validThreshold(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }

// PutThresholds replaces both thresholds in one statement (atomic on any
// engine) and returns the stored settings. Values must be finite and ≥ 0.
func PutThresholds(ctx context.Context, q Querier, pct, absThreshold float64, now time.Time) (Settings, error) {
	if !validThreshold(pct) || !validThreshold(absThreshold) {
		return Settings{}, fmt.Errorf("%w: thresholds must be finite numbers >= 0 (got pct=%v abs=%v)", ErrInvalidSettings, pct, absThreshold)
	}
	if now.IsZero() {
		return Settings{}, fmt.Errorf("%w: now required", ErrInvalidInput)
	}
	updated := now.UTC().Truncate(time.Second)
	res, err := q.ExecContext(ctx, `UPDATE settings SET value = CASE "key" WHEN ? THEN ? WHEN ? THEN ? END, updated_at = ? WHERE "key" IN (?, ?)`,
		KeyAnomalyPctThreshold, strconv.FormatFloat(pct, 'f', -1, 64),
		KeyAnomalyAbsThreshold, strconv.FormatFloat(absThreshold, 'f', -1, 64),
		FormatTime(updated), KeyAnomalyPctThreshold, KeyAnomalyAbsThreshold)
	if err != nil {
		return Settings{}, fmt.Errorf("store: put thresholds: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n != 2 {
		return Settings{}, fmt.Errorf("store: put thresholds updated %d rows, want 2 — run `cloud0 migrate up`", n)
	}
	return Settings{AnomalyPctThreshold: pct, AnomalyAbsThreshold: absThreshold, UpdatedAt: updated}, nil
}
