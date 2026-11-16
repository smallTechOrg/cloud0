package attribution

import "fmt"

// engine is the default Engine: immutable after NewEngine, so Evaluate and
// Explain are pure and safe for concurrent use.
type engine struct {
	rules []compiledRule // enabled rules in evaluation order
}

var _ Engine = (*engine)(nil)

// NewEngine is the EngineFactory. It validates every rule (enabled or not,
// errors wrap ErrInvalidRule and carry the 0-based index into rules) and
// orders the enabled ones by (priority, id). Rules without a stored id get a
// negative synthetic id (-(index+1)) in evaluations and trails.
func NewEngine(rules []Rule) (Engine, error) {
	compiled := make([]compiledRule, len(rules))
	for i, r := range rules {
		c, err := engCompileRule(i, r, -int64(i+1))
		if err != nil {
			return nil, err
		}
		compiled[i] = c
	}
	order := engOrderRules(rules)
	e := &engine{rules: make([]compiledRule, 0, len(order))}
	for _, i := range order {
		e.rules = append(e.rules, compiled[i])
	}
	return e, nil
}

var _ EngineFactory = NewEngine

// Rules returns the compiled (normalised) enabled rules in evaluation order.
func (e *engine) Rules() []Rule {
	out := make([]Rule, len(e.rules))
	for i := range e.rules {
		r := e.rules[i].rule
		if r.ID == nil || *r.ID <= 0 {
			r.ID = nil
		} else {
			id := *r.ID
			r.ID = &id
		}
		out[i] = r
	}
	return out
}

func engPtr(v int64) *int64 { return &v }

// Evaluate resolves product/shared and environment independently; the first
// deciding rule of each dimension wins, built-in label rules first.
func (e *engine) Evaluate(r Row) Evaluation {
	ev := Evaluation{Evidence: []string{}}
	pDone, eDone := false, false

	if p, shared, raw, ok := engBuiltinProduct(&r); ok {
		pDone = true
		ev.Product, ev.Shared, ev.RuleID = p, shared, engPtr(BuiltinRuleID)
		ev.Evidence = append(ev.Evidence, "label "+ProductLabelKey+"="+raw)
	}
	if env, key, raw, ok := engBuiltinEnv(&r); ok {
		eDone = true
		ev.Environment, ev.EnvironmentRuleID = env, engPtr(BuiltinRuleID)
		ev.Evidence = append(ev.Evidence, "label "+key+"="+raw)
	}
	for i := 0; i < len(e.rules) && !(pDone && eDone); i++ {
		c := &e.rules[i]
		wantP := !pDone && (c.product != "" || c.shared)
		wantE := !eDone && c.env != ""
		if !wantP && !wantE {
			continue
		}
		ok, detail := c.match(&r)
		if !ok {
			continue
		}
		text := c.evidence
		if c.field == fLabel {
			text = "label " + c.rule.Match.Key + "=" + detail
		}
		if wantP {
			pDone = true
			ev.Product, ev.Shared, ev.RuleID = c.product, c.shared, engPtr(c.id)
		}
		if wantE {
			eDone = true
			ev.Environment, ev.EnvironmentRuleID = c.env, engPtr(c.id)
		}
		ev.Evidence = append(ev.Evidence, text)
	}
	return ev
}

func engStrp(s string) *string { return &s }

// Explain lists every enabled rule in evaluation order up to and including the
// last deciding rule (all of them when nothing decides), built-ins first.
func (e *engine) Explain(r Row) []TrailStep {
	steps := make([]TrailStep, 0, len(e.rules)+2)
	pDone, eDone := false, false
	last := -1

	// built-in product
	{
		_, shared, raw, ok := engBuiltinProduct(&r)
		st := TrailStep{RuleID: BuiltinRuleID, Builtin: true, Description: "label product"}
		switch {
		case ok:
			pDone = true
			st.Matched, st.Applied = true, engStrp(KindProduct)
			if shared {
				st.Applied = engStrp(KindShared)
			}
			st.Reason = "label product=" + raw
			last = len(steps)
		case raw != "":
			st.Reason = fmt.Sprintf("label 'product'=%q is not a valid product name", raw)
		default:
			st.Reason = "no label 'product'"
		}
		steps = append(steps, st)
	}
	// built-in environment
	{
		_, key, raw, ok := engBuiltinEnv(&r)
		st := TrailStep{RuleID: BuiltinRuleID, Builtin: true, Description: "label env|environment"}
		if ok {
			eDone = true
			st.Matched, st.Applied = true, engStrp(KindEnvironment)
			st.Reason = "label " + key + "=" + raw
			last = len(steps)
		} else {
			st.Reason = "no usable label 'env' or 'environment'"
		}
		steps = append(steps, st)
	}

	for i := 0; i < len(e.rules) && !(pDone && eDone); i++ {
		c := &e.rules[i]
		prio := c.rule.Priority
		st := TrailStep{RuleID: c.id, Priority: &prio, Description: c.desc}
		ok, detail := c.match(&r)
		st.Matched = ok
		switch {
		case !ok && c.field == fLabel && detail == "":
			st.Reason = fmt.Sprintf("no label %q", c.rule.Match.Key)
		case !ok && c.field == fResource && detail == "":
			st.Reason = "row has no resource name"
		case !ok:
			st.Reason = fmt.Sprintf("%s %q does not match", engFieldText(c), detail)
		default:
			st.Reason = fmt.Sprintf("%s %q matches", engFieldText(c), detail)
			wantP := !pDone && (c.product != "" || c.shared)
			wantE := !eDone && c.env != ""
			switch {
			case wantP:
				pDone = true
				st.Applied = engStrp(KindProduct)
				if c.shared {
					st.Applied = engStrp(KindShared)
				}
				if wantE {
					eDone = true
					st.Reason += "; also decides environment"
				}
			case wantE:
				st.Applied = engStrp(KindEnvironment)
				eDone = true
			default:
				st.Reason += "; dimension already decided by an earlier rule"
			}
			if st.Applied != nil {
				last = len(steps)
			}
		}
		steps = append(steps, st)
	}
	if last >= 0 {
		steps = steps[:last+1]
	}
	return steps
}

func engFieldText(c *compiledRule) string {
	if c.field == fLabel {
		return "label " + c.rule.Match.Key
	}
	return c.rule.Match.Field + " →"[:0]
}
