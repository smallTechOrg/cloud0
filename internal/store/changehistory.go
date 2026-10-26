package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ChangeEntry is one change_history row (spec/api.md → GET /history).
type ChangeEntry struct {
	ID          int64           `json:"id"`
	At          time.Time       `json:"at"`
	Actor       string          `json:"actor"`
	Kind        string          `json:"kind"`
	Action      string          `json:"action"`
	Target      string          `json:"target"`
	Before      json.RawMessage `json:"before"` // JSON null when absent
	After       json.RawMessage `json:"after"`
	PreviewHash string          `json:"preview_hash"`
}

// NewChange is the input of InsertChange. Before/After may be nil.
type NewChange struct {
	At          time.Time
	Actor       string // default "local-user"
	Kind        string
	Action      string
	Target      string
	Before      json.RawMessage
	After       json.RawMessage
	PreviewHash string
}

// ChangeFilter narrows ListChanges; zero value = everything.
type ChangeFilter struct {
	Kind   string
	Target string
}

// DefaultChangeLimit and MaxChangeLimit bound ListChanges.
const (
	DefaultChangeLimit = 50
	MaxChangeLimit     = 500
)

var secretKey = regexp.MustCompile(`(?i)(token|secret|password|passwd|private_key|auth_key|credential|api_?key|webhook_?url|sensitive)`)

// RedactJSON replaces the value of every object key that looks like a
// credential with "[redacted]". Invalid JSON is replaced entirely. Defence in
// depth: callers should never pass secrets in the first place.
func RedactJSON(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return json.RawMessage(`"[unparseable payload omitted]"`)
	}
	out, err := json.Marshal(redactValue(v))
	if err != nil {
		return json.RawMessage(`"[unparseable payload omitted]"`)
	}
	return out
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if secretKey.MatchString(k) {
				t[k] = "[redacted]"
			} else {
				t[k] = redactValue(x)
			}
		}
		return t
	case []any:
		for i := range t {
			t[i] = redactValue(t[i])
		}
		return t
	}
	return v
}

func nullable(raw json.RawMessage) any {
	r := RedactJSON(raw)
	if r == nil || string(r) == "null" {
		return nil
	}
	return string(r)
}

// InsertChange appends a row (payloads redacted) and returns it with its id.
func InsertChange(ctx context.Context, q Querier, nc NewChange) (ChangeEntry, error) {
	if nc.Kind == "" || nc.Action == "" || nc.Target == "" {
		return ChangeEntry{}, fmt.Errorf("%w: change needs kind, action and target", ErrInvalidInput)
	}
	if nc.At.IsZero() {
		return ChangeEntry{}, fmt.Errorf("%w: change time required", ErrInvalidInput)
	}
	if nc.Actor == "" {
		nc.Actor = "local-user"
	}
	at := nc.At.UTC().Truncate(time.Second)
	res, err := q.ExecContext(ctx, `INSERT INTO change_history (at, actor, kind, action, target, before_json, after_json, preview_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, FormatTime(at), nc.Actor, nc.Kind, nc.Action, nc.Target, nullable(nc.Before), nullable(nc.After), nc.PreviewHash)
	if err != nil {
		return ChangeEntry{}, fmt.Errorf("store: insert change: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ChangeEntry{}, fmt.Errorf("store: insert change id: %w", err)
	}
	return ChangeEntry{ID: id, At: at, Actor: nc.Actor, Kind: nc.Kind, Action: nc.Action, Target: nc.Target,
		Before: jsonOrNull(RedactJSON(nc.Before)), After: jsonOrNull(RedactJSON(nc.After)), PreviewHash: nc.PreviewHash}, nil
}

func jsonOrNull(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}

// ListChanges returns rows newest first. limit ≤ 0 → DefaultChangeLimit; capped at MaxChangeLimit.
func ListChanges(ctx context.Context, q Querier, f ChangeFilter, limit int) ([]ChangeEntry, error) {
	if limit <= 0 {
		limit = DefaultChangeLimit
	}
	if limit > MaxChangeLimit {
		limit = MaxChangeLimit
	}
	var where []string
	var args []any
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.Target != "" {
		where = append(where, "target = ?")
		args = append(args, f.Target)
	}
	sqlText := `SELECT id, at, actor, kind, action, target, before_json, after_json, preview_hash FROM change_history`
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sqlText += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list changes: %w", err)
	}
	defer rows.Close()
	out := []ChangeEntry{}
	for rows.Next() {
		var e ChangeEntry
		var at string
		var before, after *string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Kind, &e.Action, &e.Target, &before, &after, &e.PreviewHash); err != nil {
			return nil, fmt.Errorf("store: list changes: %w", err)
		}
		if e.At, err = ParseTime(at); err != nil {
			return nil, fmt.Errorf("store: change %d at %q: %w", e.ID, at, err)
		}
		e.Before, e.After = json.RawMessage("null"), json.RawMessage("null")
		if before != nil {
			e.Before = json.RawMessage(*before)
		}
		if after != nil {
			e.After = json.RawMessage(*after)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
