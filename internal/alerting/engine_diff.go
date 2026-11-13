package alerting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Server-generated fields never shown in a diff and never hashed as "desired".
var engServerKeys = map[string]bool{
	"creationRecord": true, "mutationRecord": true, "etag": true,
	"createTime": true, "updateTime": true,
}

// Extra server-defaulted keys of an uptime check that UptimeDraft cannot set.
var engUptimeServerKeys = map[string]bool{
	"checkerType": true, "isInternal": true, "internalCheckers": true,
	"selectedRegions": true, "headers": true, "maskHeaders": true,
	"pingConfig": true, "acceptedResponseStatusCodes": true, "authInfo": true,
	"contentType": true, "customContentType": true, "body": true,
	"serviceAgentAuthentication": true, "logCheckFailures": true,
}

// engDecode parses raw JSON into a generic value (nil for empty / null).
func engDecode(raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %v", ErrInvalidParams, err)
	}
	return v, nil
}

// engNormalize removes server-generated fields: the top-level "name", the
// "name" of each condition, and the keys in engServerKeys (and extra) at any depth.
func engNormalize(v any, extra map[string]bool) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	for k, x := range m {
		if k == "name" {
			continue
		}
		out[k] = engStrip(x, extra, k == "conditions")
	}
	for k := range out {
		if engServerKeys[k] || extra[k] {
			delete(out, k)
		}
	}
	return out
}

func engStrip(v any, extra map[string]bool, dropName bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			if engServerKeys[k] || extra[k] || (dropName && k == "name") {
				continue
			}
			out[k] = engStrip(x, extra, k == "conditions")
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = engStrip(x, extra, dropName)
		}
		return out
	}
	return v
}

// engCanonical marshals v with sorted keys (encoding/json sorts map keys).
func engCanonical(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// DiffJSON computes the flat path diff between two JSON documents, ignoring
// server-generated fields. Paths are dotted with [i] array indices.
func DiffJSON(before, after json.RawMessage) ([]DiffChange, error) {
	return engDiff(before, after, nil)
}

func engDiff(before, after json.RawMessage, extra map[string]bool) ([]DiffChange, error) {
	b, err := engDecode(before)
	if err != nil {
		return nil, err
	}
	a, err := engDecode(after)
	if err != nil {
		return nil, err
	}
	changes := []DiffChange{}
	nb, na := engNormalize(b, extra), engNormalize(a, extra)
	// A create/delete diffs against an empty object so changes are per path.
	if _, ok := na.(map[string]any); ok && nb == nil {
		nb = map[string]any{}
	}
	if _, ok := nb.(map[string]any); ok && na == nil {
		na = map[string]any{}
	}
	walkDiff("", nb, na, &changes)
	return changes, nil
}

func engJoin(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func engRaw(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage("null")
	}
	return engCanonical(v)
}

func walkDiff(path string, b, a any, out *[]DiffChange) {
	switch bt := b.(type) {
	case map[string]any:
		if at, ok := a.(map[string]any); ok {
			keys := map[string]bool{}
			for k := range bt {
				keys[k] = true
			}
			for k := range at {
				keys[k] = true
			}
			sorted := make([]string, 0, len(keys))
			for k := range keys {
				sorted = append(sorted, k)
			}
			sort.Strings(sorted)
			for _, k := range sorted {
				bv, bok := bt[k]
				av, aok := at[k]
				p := engJoin(path, k)
				switch {
				case bok && !aok:
					*out = append(*out, DiffChange{Path: p, Op: "remove", Before: engRaw(bv), After: json.RawMessage("null")})
				case !bok && aok:
					*out = append(*out, DiffChange{Path: p, Op: "add", Before: json.RawMessage("null"), After: engRaw(av)})
				default:
					walkDiff(p, bv, av, out)
				}
			}
			return
		}
	case []any:
		if at, ok := a.([]any); ok {
			n := len(bt)
			if len(at) > n {
				n = len(at)
			}
			for i := 0; i < n; i++ {
				p := fmt.Sprintf("%s[%d]", path, i)
				switch {
				case i >= len(at):
					*out = append(*out, DiffChange{Path: p, Op: "remove", Before: engRaw(bt[i]), After: json.RawMessage("null")})
				case i >= len(bt):
					*out = append(*out, DiffChange{Path: p, Op: "add", Before: json.RawMessage("null"), After: engRaw(at[i])})
				default:
					walkDiff(p, bt[i], at[i], out)
				}
			}
			return
		}
	}
	if reflect.DeepEqual(b, a) {
		return
	}
	p := path
	if p == "" {
		p = "$"
	}
	op := "change"
	switch {
	case b == nil:
		op = "add"
	case a == nil:
		op = "remove"
	}
	*out = append(*out, DiffChange{Path: p, Op: op, Before: engRaw(b), After: engRaw(a)})
}

// engPretty renders a normalised value as indented JSON lines.
func engPretty(raw json.RawMessage, extra map[string]bool) []string {
	v, err := engDecode(raw)
	if err != nil || v == nil {
		return nil
	}
	b, err := json.MarshalIndent(engNormalize(v, extra), "", "  ")
	if err != nil {
		return nil
	}
	return strings.Split(string(b), "\n")
}

// engUnified renders a unified line diff (3 lines of context) of the pretty
// JSON of before and after. Empty string when identical.
func engUnified(before, after json.RawMessage, extra map[string]bool) string {
	a, b := engPretty(before, extra), engPretty(after, extra)
	type op struct {
		kind byte // ' ', '-', '+'
		text string
	}
	var ops []op
	// LCS table.
	n, m := len(a), len(b)
	if n*m > 4_000_000 {
		for _, l := range a {
			ops = append(ops, op{'-', l})
		}
		for _, l := range b {
			ops = append(ops, op{'+', l})
		}
	} else {
		lcs := make([][]int, n+1)
		for i := range lcs {
			lcs[i] = make([]int, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if a[i] == b[j] {
					lcs[i][j] = lcs[i+1][j+1] + 1
				} else if lcs[i+1][j] >= lcs[i][j+1] {
					lcs[i][j] = lcs[i+1][j]
				} else {
					lcs[i][j] = lcs[i][j+1]
				}
			}
		}
		i, j := 0, 0
		for i < n && j < m {
			switch {
			case a[i] == b[j]:
				ops = append(ops, op{' ', a[i]})
				i++
				j++
			case lcs[i+1][j] >= lcs[i][j+1]:
				ops = append(ops, op{'-', a[i]})
				i++
			default:
				ops = append(ops, op{'+', b[j]})
				j++
			}
		}
		for ; i < n; i++ {
			ops = append(ops, op{'-', a[i]})
		}
		for ; j < m; j++ {
			ops = append(ops, op{'+', b[j]})
		}
	}
	changed := false
	for _, o := range ops {
		if o.kind != ' ' {
			changed = true
			break
		}
	}
	if !changed {
		return ""
	}
	const ctx = 3
	keep := make([]bool, len(ops))
	for i, o := range ops {
		if o.kind == ' ' {
			continue
		}
		for k := i - ctx; k <= i+ctx; k++ {
			if k >= 0 && k < len(ops) {
				keep[k] = true
			}
		}
	}
	var sb strings.Builder
	sb.WriteString("--- current\n+++ desired\n")
	// Line numbers per op for hunk headers.
	aLine, bLine := make([]int, len(ops)+1), make([]int, len(ops)+1)
	for i, o := range ops {
		aLine[i+1], bLine[i+1] = aLine[i], bLine[i]
		if o.kind != '+' {
			aLine[i+1]++
		}
		if o.kind != '-' {
			bLine[i+1]++
		}
	}
	for i := 0; i < len(ops); {
		if !keep[i] {
			i++
			continue
		}
		j := i
		for j < len(ops) && keep[j] {
			j++
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", aLine[i]+1, aLine[j]-aLine[i], bLine[i]+1, bLine[j]-bLine[i])
		for k := i; k < j; k++ {
			sb.WriteByte(ops[k].kind)
			sb.WriteByte(' ')
			sb.WriteString(ops[k].text)
			sb.WriteByte('\n')
		}
		i = j
	}
	return strings.TrimRight(sb.String(), "\n")
}
