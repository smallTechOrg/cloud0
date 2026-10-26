package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// KeyMonthlyTarget is the Month view's monthly total target (USD); absent = no target.
const KeyMonthlyTarget = "target.monthly_total"

// GetSettingsWithPrefix returns every settings row whose key starts with
// prefix, as key → raw value (e.g. "limit." for limits.ParseOverrides).
func GetSettingsWithPrefix(ctx context.Context, q Querier, prefix string) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT "key", value FROM settings WHERE "key" LIKE ? ESCAPE '\' ORDER BY "key"`, escapeLike(prefix)+"%")
	if err != nil {
		return nil, fmt.Errorf("store: settings prefix %q: %w", prefix, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: settings prefix %q: %w", prefix, err)
		}
		out[k] = v
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// PutSettings upserts raw key → value pairs with updated_at = now. Callers
// validate first (limits.ParseOverrides for "limit." keys).
func PutSettings(ctx context.Context, q Querier, kv map[string]string, now time.Time) error {
	if now.IsZero() {
		return fmt.Errorf("%w: now required", ErrInvalidInput)
	}
	ts := FormatTime(now.UTC().Truncate(time.Second))
	for k, v := range kv {
		if k == "" {
			return fmt.Errorf("%w: empty settings key", ErrInvalidInput)
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO settings ("key", value, updated_at) VALUES (?, ?, ?)
ON CONFLICT ("key") DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, k, v, ts); err != nil {
			return fmt.Errorf("store: put setting %s: %w", k, err)
		}
	}
	return nil
}

// LimitsUpdatedAt is the latest updated_at among "limit." settings; nil when none were ever saved.
func LimitsUpdatedAt(ctx context.Context, q Querier) (*time.Time, error) {
	var s *string
	if err := q.QueryRowContext(ctx, `SELECT MAX(updated_at) FROM settings WHERE "key" LIKE 'limit.%'`).Scan(&s); err != nil {
		return nil, fmt.Errorf("store: limits updated_at: %w", err)
	}
	if s == nil {
		return nil, nil
	}
	t, err := ParseTime(*s)
	if err != nil {
		return nil, fmt.Errorf("store: limits updated_at %q: %w", *s, err)
	}
	return &t, nil
}

// GetMonthlyTarget returns the monthly target or nil when unset/blank.
func GetMonthlyTarget(ctx context.Context, q Querier) (*float64, error) {
	m, err := GetSettingsWithPrefix(ctx, q, KeyMonthlyTarget)
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(m[KeyMonthlyTarget])
	if raw == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, fmt.Errorf("store: setting %s non-numeric %q: %w", KeyMonthlyTarget, raw, err)
	}
	return &f, nil
}
