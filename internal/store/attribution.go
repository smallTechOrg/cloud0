package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Settings keys used by Phase 3 (spec/api.md → Attribution additions →
// Settings keys). No seed rows: absent means the documented default.
const (
	KeyAttributionAllocate = "attribution.allocate"            // none | even | proportional
	KeyAttributionAliases  = "attribution.seed.aliases"        // JSON object alias → product
	KeyRightsizingCPUPct   = "rightsizing.cpu_pct"             // default 15
	KeyRightsizingWeeks    = "rightsizing.weeks"               // default 3
	KeyRightsizingStaleDay = "rightsizing.stale_snapshot_days" // default 30
	KeyRightsizingIdleRun  = "rightsizing.idle_run_min_net"    // default 1
)

// AccountTargetProduct is the `targets.product` value of the account-wide target.
const AccountTargetProduct = "*"

// MaxAttributionRules caps one PUT /attribution/rules body.
const MaxAttributionRules = 500

// Allowed enumerations, mirrored by CHECK constraints in migration 0005.
var (
	ruleFields  = map[string]bool{"label": true, "project": true, "resource_name": true, "sku": true, "service": true}
	ruleOps     = map[string]bool{"equals": true, "contains": true, "prefix": true, "regex": true}
	ruleSources = map[string]bool{"seed": true, "user": true, "proposed": true}

	productNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	monthPattern       = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
)

// ValidProductName reports whether s is a legal product/environment name.
func ValidProductName(s string) bool { return productNamePattern.MatchString(s) }

// AttributionRule is one row of `attribution_rules` (spec/api.md → Rule).
// The engine package converts it to its own type; store stays free of it.
type AttributionRule struct {
	ID          int64
	Priority    int
	MatchField  string // label | project | resource_name | sku | service
	MatchOp     string // equals | contains | prefix | regex
	MatchKey    string // label key; "" unless MatchField == "label"
	MatchValue  string
	Product     string // "" when none
	Environment string // "" when none
	Shared      bool
	Enabled     bool
	Source      string // seed | user | proposed
	Note        *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Validate checks the structural rules shared with the CHECK constraints plus
// the action rules of the contract (regex compilation is the engine's job).
func (r AttributionRule) Validate() error {
	switch {
	case r.Priority < 0:
		return fmt.Errorf("%w: priority must be >= 0", ErrInvalidInput)
	case !ruleFields[r.MatchField]:
		return fmt.Errorf("%w: unknown match field %q", ErrInvalidInput, r.MatchField)
	case !ruleOps[r.MatchOp]:
		return fmt.Errorf("%w: unknown match op %q", ErrInvalidInput, r.MatchOp)
	case !ruleSources[r.Source]:
		return fmt.Errorf("%w: unknown source %q", ErrInvalidInput, r.Source)
	case strings.TrimSpace(r.MatchValue) == "":
		return fmt.Errorf("%w: match value is empty", ErrInvalidInput)
	case (r.MatchField == "label") != (r.MatchKey != ""):
		return fmt.Errorf("%w: match key is required for, and only for, field label", ErrInvalidInput)
	case r.Product == "" && r.Environment == "" && !r.Shared:
		return fmt.Errorf("%w: action sets nothing", ErrInvalidInput)
	case r.Shared && r.Product != "":
		return fmt.Errorf("%w: shared and product are mutually exclusive", ErrInvalidInput)
	case r.Product != "" && !productNamePattern.MatchString(r.Product):
		return fmt.Errorf("%w: invalid product name %q", ErrInvalidInput, r.Product)
	case r.Environment != "" && !productNamePattern.MatchString(r.Environment):
		return fmt.Errorf("%w: invalid environment name %q", ErrInvalidInput, r.Environment)
	}
	return nil
}

const ruleColumns = "id, priority, match_field, match_op, match_key, match_value, product, environment, shared, enabled, source, note, created_at, updated_at"

// ListAttributionRules returns every stored rule ordered by (priority, id).
func ListAttributionRules(ctx context.Context, q Querier) ([]AttributionRule, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+ruleColumns+` FROM attribution_rules ORDER BY priority, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list attribution rules: %w", err)
	}
	defer rows.Close()
	out := []AttributionRule{}
	for rows.Next() {
		var r AttributionRule
		var shared, enabled int
		var note sql.NullString
		var created, updated string
		if err := rows.Scan(&r.ID, &r.Priority, &r.MatchField, &r.MatchOp, &r.MatchKey, &r.MatchValue, &r.Product, &r.Environment,
			&shared, &enabled, &r.Source, &note, &created, &updated); err != nil {
			return nil, fmt.Errorf("store: list attribution rules: %w", err)
		}
		r.Shared, r.Enabled, r.Note = shared == 1, enabled == 1, fromNullString(note)
		if r.CreatedAt, err = ParseTime(created); err != nil {
			return nil, fmt.Errorf("store: attribution rule %d created_at %q: %w", r.ID, created, err)
		}
		if r.UpdatedAt, err = ParseTime(updated); err != nil {
			return nil, fmt.Errorf("store: attribution rule %d updated_at %q: %w", r.ID, updated, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplaceAttributionRules makes the stored set equal to rules, atomically
// when q is a *sql.Tx (call it inside Store.WithTx): stored ids absent from
// rules are deleted, rules with an existing id are updated in place (id and
// created_at kept), rules with ID 0 are inserted. An unknown non-zero ID, a
// duplicate ID, an invalid rule (error names the 0-based index) or more than
// MaxAttributionRules rules returns ErrInvalidInput before any write. The
// stored rules are returned ordered by (priority, id).
func ReplaceAttributionRules(ctx context.Context, q Querier, rules []AttributionRule, now time.Time) ([]AttributionRule, error) {
	if now.IsZero() {
		return nil, fmt.Errorf("%w: now required", ErrInvalidInput)
	}
	if len(rules) > MaxAttributionRules {
		return nil, fmt.Errorf("%w: %d rules exceeds the limit of %d", ErrInvalidInput, len(rules), MaxAttributionRules)
	}
	existing, err := ListAttributionRules(ctx, q)
	if err != nil {
		return nil, err
	}
	have := make(map[int64]AttributionRule, len(existing))
	for _, r := range existing {
		have[r.ID] = r
	}
	seen := map[int64]bool{}
	for i, r := range rules {
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("rule %d: %w", i, err)
		}
		if r.ID != 0 {
			if _, ok := have[r.ID]; !ok {
				return nil, fmt.Errorf("rule %d: %w: unknown rule id %d", i, ErrInvalidInput, r.ID)
			}
			if seen[r.ID] {
				return nil, fmt.Errorf("rule %d: %w: duplicate rule id %d", i, ErrInvalidInput, r.ID)
			}
			seen[r.ID] = true
		}
	}
	ts := FormatTime(now.UTC().Truncate(time.Second))
	for _, r := range existing {
		if seen[r.ID] {
			continue
		}
		if _, err := q.ExecContext(ctx, `DELETE FROM attribution_rules WHERE id = ?`, r.ID); err != nil {
			return nil, fmt.Errorf("store: delete attribution rule %d: %w", r.ID, err)
		}
	}
	for _, r := range rules {
		if r.ID != 0 {
			_, err = q.ExecContext(ctx, `UPDATE attribution_rules SET priority = ?, match_field = ?, match_op = ?, match_key = ?, match_value = ?, product = ?, environment = ?, shared = ?, enabled = ?, source = ?, note = ?, updated_at = ? WHERE id = ?`,
				r.Priority, r.MatchField, r.MatchOp, r.MatchKey, r.MatchValue, r.Product, r.Environment, b2i(r.Shared), b2i(r.Enabled), r.Source, nullableString(r.Note), ts, r.ID)
		} else {
			_, err = q.ExecContext(ctx, `INSERT INTO attribution_rules (priority, match_field, match_op, match_key, match_value, product, environment, shared, enabled, source, note, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				r.Priority, r.MatchField, r.MatchOp, r.MatchKey, r.MatchValue, r.Product, r.Environment, b2i(r.Shared), b2i(r.Enabled), r.Source, nullableString(r.Note), ts, ts)
		}
		if err != nil {
			return nil, fmt.Errorf("store: write attribution rule: %w", err)
		}
	}
	return ListAttributionRules(ctx, q)
}

// AttributionRulesUpdatedAt is the latest updated_at over all rules (and the
// attribution settings keys); nil when nothing was ever saved.
func AttributionRulesUpdatedAt(ctx context.Context, q Querier) (*time.Time, error) {
	var s *string
	if err := q.QueryRowContext(ctx, `SELECT MAX(u) FROM (SELECT MAX(updated_at) AS u FROM attribution_rules UNION ALL SELECT MAX(updated_at) FROM settings WHERE "key" LIKE 'attribution.%')`).Scan(&s); err != nil {
		return nil, fmt.Errorf("store: attribution updated_at: %w", err)
	}
	if s == nil {
		return nil, nil
	}
	t, err := ParseTime(*s)
	if err != nil {
		return nil, fmt.Errorf("store: attribution updated_at %q: %w", *s, err)
	}
	return &t, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Target is one row of `targets`. Amount nil clears the target from MonthFrom.
type Target struct {
	ID        int64
	Product   string // "*" = account target
	MonthFrom string // YYYY-MM
	Amount    *float64
	CreatedAt time.Time
}

// ValidateTarget checks product (`*` or a product name), month and amount.
func ValidateTarget(t Target) error {
	switch {
	case t.Product != AccountTargetProduct && !productNamePattern.MatchString(t.Product):
		return fmt.Errorf("%w: invalid target product %q", ErrInvalidInput, t.Product)
	case !monthPattern.MatchString(t.MonthFrom):
		return fmt.Errorf("%w: invalid month_from %q", ErrInvalidInput, t.MonthFrom)
	case t.Amount != nil && (math.IsNaN(*t.Amount) || math.IsInf(*t.Amount, 0) || *t.Amount < 0):
		return fmt.Errorf("%w: target amount must be a finite number >= 0", ErrInvalidInput)
	}
	return nil
}

// PutTargets upserts one row per entry keyed by (product, month_from); it
// validates every entry before writing. Run it inside Store.WithTx so a
// multi-entry PUT is all-or-nothing.
func PutTargets(ctx context.Context, q Querier, ts []Target, now time.Time) error {
	if now.IsZero() {
		return fmt.Errorf("%w: now required", ErrInvalidInput)
	}
	if len(ts) == 0 {
		return fmt.Errorf("%w: no targets", ErrInvalidInput)
	}
	for i, t := range ts {
		if err := ValidateTarget(t); err != nil {
			return fmt.Errorf("target %d: %w", i, err)
		}
	}
	created := FormatTime(now.UTC().Truncate(time.Second))
	for _, t := range ts {
		var amt any
		if t.Amount != nil {
			amt = *t.Amount
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO targets (product, month_from, amount, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT (product, month_from) DO UPDATE SET amount = excluded.amount, created_at = excluded.created_at`, t.Product, t.MonthFrom, amt, created); err != nil {
			return fmt.Errorf("store: put target %s %s: %w", t.Product, t.MonthFrom, err)
		}
	}
	return nil
}

// ListTargets returns the full history ordered by (product, month_from).
func ListTargets(ctx context.Context, q Querier) ([]Target, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, product, month_from, amount, created_at FROM targets ORDER BY product, month_from`)
	if err != nil {
		return nil, fmt.Errorf("store: list targets: %w", err)
	}
	defer rows.Close()
	out := []Target{}
	for rows.Next() {
		var t Target
		var amt sql.NullFloat64
		var created string
		if err := rows.Scan(&t.ID, &t.Product, &t.MonthFrom, &amt, &created); err != nil {
			return nil, fmt.Errorf("store: list targets: %w", err)
		}
		if amt.Valid {
			v := amt.Float64
			t.Amount = &v
		}
		if t.CreatedAt, err = ParseTime(created); err != nil {
			return nil, fmt.Errorf("store: target %d created_at %q: %w", t.ID, created, err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// EffectiveTargets resolves, per product, the newest row with MonthFrom <=
// month ("A target applies from month_from until superseded"). Products
// whose newest row has a nil Amount (cleared) are omitted.
func EffectiveTargets(ctx context.Context, q Querier, month string) (map[string]Target, error) {
	if !monthPattern.MatchString(month) {
		return nil, fmt.Errorf("%w: invalid month %q", ErrInvalidInput, month)
	}
	all, err := ListTargets(ctx, q)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].MonthFrom < all[j].MonthFrom })
	best := map[string]Target{}
	for _, t := range all {
		if t.MonthFrom <= month { // YYYY-MM compares lexicographically
			best[t.Product] = t
		}
	}
	for p, t := range best {
		if t.Amount == nil {
			delete(best, p)
		}
	}
	return best, nil
}
