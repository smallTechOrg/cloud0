package attribution

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// engMaxRegexLen bounds a rule regex. Together with RE2's linear-time guarantee
// (Go's regexp has no backtracking) and its built-in repeat-count limit this
// keeps a hostile or careless pattern from costing more than O(len(row)).
const engMaxRegexLen = 200

// Field and operator codes (compiled form of the Match strings).
const (
	fLabel = iota
	fProject
	fResource
	fSKU
	fService
)

const (
	oEquals = iota
	oContains
	oPrefix
	oRegex
)

// compiledRule is a validated, normalised rule ready for evaluation.
type compiledRule struct {
	rule     Rule   // normalised copy (lower-cased names, trimmed)
	id       int64  // stored id, or a negative synthetic id for unsaved rules
	field    int    // fLabel…fService
	op       int    // oEquals…oRegex
	key      string // lower-cased label key
	val      string // lower-cased match value (unused for regex)
	re       *regexp.Regexp
	product  string
	env      string
	shared   bool
	desc     string // trail description
	evidence string // static evidence text (non-label rules)
}

// engValidName reports whether s matches ^[a-z0-9][a-z0-9_-]{0,39}$.
func engValidName(s string) bool {
	if len(s) == 0 || len(s) > 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '_' || c == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

func engNormName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func engFieldCode(f string) (int, bool) {
	switch f {
	case FieldLabel:
		return fLabel, true
	case FieldProject:
		return fProject, true
	case FieldResourceName:
		return fResource, true
	case FieldSKU:
		return fSKU, true
	case FieldService:
		return fService, true
	}
	return 0, false
}

func engOpCode(o string) (int, bool) {
	switch o {
	case OpEquals:
		return oEquals, true
	case OpContains:
		return oContains, true
	case OpPrefix:
		return oPrefix, true
	case OpRegex:
		return oRegex, true
	}
	return 0, false
}

func engRuleErr(idx int, format string, a ...any) error {
	return fmt.Errorf("%w: rules[%d]: %s", ErrInvalidRule, idx, fmt.Sprintf(format, a...))
}

// engCompileRule validates one rule. synth is the negative id used when the rule
// has no stored id.
func engCompileRule(idx int, r Rule, synth int64) (compiledRule, error) {
	var c compiledRule
	var ok bool
	if r.Priority < 0 {
		return c, engRuleErr(idx, "priority must be >= 0")
	}
	if c.field, ok = engFieldCode(r.Match.Field); !ok {
		return c, engRuleErr(idx, "unknown match field %q", r.Match.Field)
	}
	if c.op, ok = engOpCode(r.Match.Op); !ok {
		return c, engRuleErr(idx, "unknown match op %q", r.Match.Op)
	}
	if r.Match.Value == "" {
		return c, engRuleErr(idx, "match value must not be empty")
	}
	key := strings.TrimSpace(r.Match.Key)
	if c.field == fLabel && key == "" {
		return c, engRuleErr(idx, "match key is required for field label")
	}
	if c.field != fLabel && key != "" {
		return c, engRuleErr(idx, "match key is only allowed for field label")
	}
	prod, env := engNormName(r.Action.Product), engNormName(r.Action.Environment)
	if prod == "" && env == "" && !r.Action.Shared {
		return c, engRuleErr(idx, "action needs a product, an environment or shared")
	}
	if r.Action.Shared && prod != "" {
		return c, engRuleErr(idx, "shared and product are mutually exclusive")
	}
	if prod != "" && !engValidName(prod) {
		return c, engRuleErr(idx, "invalid product name %q", r.Action.Product)
	}
	if env != "" && !engValidName(env) {
		return c, engRuleErr(idx, "invalid environment name %q", r.Action.Environment)
	}
	if c.op == oRegex {
		if len(r.Match.Value) > engMaxRegexLen {
			return c, engRuleErr(idx, "regex longer than %d characters", engMaxRegexLen)
		}
		re, err := regexp.Compile("(?i)" + r.Match.Value)
		if err != nil {
			return c, engRuleErr(idx, "invalid regex: %v", err)
		}
		c.re = re
	}
	c.rule = r
	c.rule.Match.Key = key
	c.rule.Action = Action{Product: prod, Environment: env, Shared: r.Action.Shared}
	c.key = strings.ToLower(key)
	c.val = strings.ToLower(r.Match.Value)
	c.product, c.env, c.shared = prod, env, r.Action.Shared
	if r.ID != nil && *r.ID > 0 {
		c.id = *r.ID
	} else {
		c.id = synth
	}
	c.desc = engDescribe(c.rule)
	if c.field != fLabel {
		c.evidence = fmt.Sprintf("%s %s %q", r.Match.Field, r.Match.Op, r.Match.Value)
	}
	return c, nil
}

func engDescribe(r Rule) string {
	var b strings.Builder
	b.WriteString(r.Match.Field)
	if r.Match.Field == FieldLabel {
		b.WriteByte(' ')
		b.WriteString(r.Match.Key)
	}
	fmt.Fprintf(&b, " %s %q → ", r.Match.Op, r.Match.Value)
	b.WriteString(engActionText(r.Action))
	return b.String()
}

func engActionText(a Action) string {
	parts := make([]string, 0, 2)
	if a.Shared {
		parts = append(parts, "shared")
	}
	if a.Product != "" {
		parts = append(parts, "product "+a.Product)
	}
	if a.Environment != "" {
		parts = append(parts, "environment "+a.Environment)
	}
	return strings.Join(parts, ", ")
}

// engOrderRules returns the indexes of the enabled rules in (priority, id) order;
// rules without a stored id sort after stored ones, then by input position.
func engOrderRules(rules []Rule) []int {
	idx := make([]int, 0, len(rules))
	for i, r := range rules {
		if r.Enabled {
			idx = append(idx, i)
		}
	}
	key := func(i int) int64 {
		if id := rules[i].ID; id != nil && *id > 0 {
			return *id
		}
		return 1<<62 + int64(i)
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := rules[idx[a]], rules[idx[b]]
		if ra.Priority != rb.Priority {
			return ra.Priority < rb.Priority
		}
		return key(idx[a]) < key(idx[b])
	})
	return idx
}

// ---- matching ---------------------------------------------------------------

// engLastSegment returns the part after the final '/', or s itself.
func engLastSegment(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func engIsASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func engLowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// engContainsFold reports whether s contains lowerSub (already lower-case),
// ignoring case, without allocating for ASCII input.
func engContainsFold(s, lowerSub string) bool {
	if len(lowerSub) == 0 {
		return true
	}
	if !engIsASCII(s) || !engIsASCII(lowerSub) {
		return strings.Contains(strings.ToLower(s), lowerSub)
	}
	n := len(lowerSub)
	for i := 0; i+n <= len(s); i++ {
		j := 0
		for j < n && engLowerByte(s[i+j]) == lowerSub[j] {
			j++
		}
		if j == n {
			return true
		}
	}
	return false
}

func engHasPrefixFold(s, lowerPre string) bool {
	if engIsASCII(s) && engIsASCII(lowerPre) {
		return len(s) >= len(lowerPre) && strings.EqualFold(s[:len(lowerPre)], lowerPre)
	}
	return strings.HasPrefix(strings.ToLower(s), lowerPre)
}

// test applies the rule's operator to one candidate string.
func (c *compiledRule) test(s string) bool {
	switch c.op {
	case oEquals:
		return strings.EqualFold(s, c.val)
	case oContains:
		return engContainsFold(s, c.val)
	case oPrefix:
		return engHasPrefixFold(s, c.val)
	default:
		return c.re.MatchString(s)
	}
}

// engLabelValue looks key up case-insensitively. An exact match wins; otherwise
// the lexicographically smallest case-variant key, so the result never depends
// on map iteration order.
func engLabelValue(m map[string]string, key string) (string, bool) {
	if len(m) == 0 {
		return "", false
	}
	if v, ok := m[key]; ok {
		return v, true
	}
	best, bestKey, found := "", "", false
	for k, v := range m {
		if strings.EqualFold(k, key) && (!found || k < bestKey) {
			best, bestKey, found = v, k, true
		}
	}
	return best, found
}

// match reports whether the rule matches the row. detail is the value that was
// tested (the label value, the matching resource candidate, or the field), used
// for explain; for a miss it is the value that failed.
func (c *compiledRule) match(r *Row) (ok bool, detail string) {
	switch c.field {
	case fLabel:
		v, present := engLabelValue(r.Labels, c.key)
		if !present {
			return false, ""
		}
		return c.test(v), v
	case fProject:
		return c.test(r.ProjectID), r.ProjectID
	case fSKU:
		return c.test(r.SKU), r.SKU
	case fService:
		return c.test(r.Service), r.Service
	}
	// resource_name: resource_name or resource_global_name, full string or
	// last path segment.
	detail = r.ResourceName
	if detail == "" {
		detail = r.ResourceGlobalName
	}
	for _, s := range [2]string{r.ResourceName, r.ResourceGlobalName} {
		if s == "" {
			continue
		}
		if c.test(s) {
			return true, s
		}
		if seg := engLastSegment(s); seg != s && seg != "" && c.test(seg) {
			return true, s
		}
	}
	return false, detail
}

// ---- built-in label rules ---------------------------------------------------

// engBuiltinProduct reads the `product` label; shared when it equals "shared".
func engBuiltinProduct(r *Row) (product string, shared bool, raw string, ok bool) {
	v, present := engLabelValue(r.Labels, ProductLabelKey)
	if !present {
		return "", false, "", false
	}
	n := engNormName(v)
	if n == SharedLabelValue {
		return "", true, v, true
	}
	if !engValidName(n) {
		return "", false, v, false
	}
	return n, false, v, true
}

// engBuiltinEnv reads `env`, then `environment`.
func engBuiltinEnv(r *Row) (env, key, raw string, ok bool) {
	for _, k := range EnvLabelKeys {
		if v, present := engLabelValue(r.Labels, k); present {
			if n := engNormName(v); engValidName(n) {
				return n, k, v, true
			}
		}
	}
	return "", "", "", false
}
