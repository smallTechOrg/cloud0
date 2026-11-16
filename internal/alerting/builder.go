package alerting

import (
	"encoding/json"
	"fmt"
	"strings"
)

// templateDef is one registered template: its metadata (ParamSpecs, Defaults,
// default Severity …) and the pure function that builds the desired policy.
// Each family lives in its own file and exports one constructor that
// NewBuilder collects: vmDefs (vm_defs.go), errorDefs (error_defs.go),
// dbDefs (db_defs.go). Defs must be pure — no I/O.
type templateDef struct {
	Template Template
	Build    func(p TemplateParams, ctx BuildContext) (Built, error)
}

type builder struct {
	defs  map[string]templateDef
	order []string
}

// NewBuilder returns the pure policy builder over every registered template,
// listed in AllTemplateIDs order (ids with no def yet are skipped).
func NewBuilder() Builder {
	b := &builder{defs: map[string]templateDef{}}
	for _, group := range [][]templateDef{vmDefs(), errorDefs(), dbDefs()} {
		for _, d := range group {
			b.defs[d.Template.ID] = d
		}
	}
	for _, id := range AllTemplateIDs {
		if _, ok := b.defs[id]; ok {
			b.order = append(b.order, id)
		}
	}
	return b
}

func (b *builder) Templates() []Template {
	out := make([]Template, 0, len(b.order))
	for _, id := range b.order {
		out = append(out, b.defs[id].Template)
	}
	return out
}

func (b *builder) Template(id string) (Template, bool) {
	d, ok := b.defs[id]
	return d.Template, ok
}

// Merge applies the template defaults to p and validates against the
// ParamSpecs; errors wrap ErrInvalidParams.
func (b *builder) Merge(t Template, p TemplateParams) (TemplateParams, error) {
	out := t.Defaults
	if p.Threshold != nil {
		out.Threshold = p.Threshold
	}
	if p.DurationSeconds != nil {
		out.DurationSeconds = p.DurationSeconds
	}
	if p.WindowSeconds != nil {
		out.WindowSeconds = p.WindowSeconds
	}
	if p.RateLimitSeconds != nil {
		out.RateLimitSeconds = p.RateLimitSeconds
	}
	if p.Severity != "" {
		out.Severity = p.Severity
	}
	if p.Enabled != nil {
		out.Enabled = p.Enabled
	}
	if p.ChannelOverride != nil {
		out.ChannelOverride = p.ChannelOverride
	}
	if p.Instance != "" {
		out.Instance = p.Instance
	}
	if p.UptimeCheckIDs != nil {
		out.UptimeCheckIDs = p.UptimeCheckIDs
	}
	if out.Severity == "" {
		out.Severity = t.Severity
	}
	valid := false
	for _, s := range Severities {
		if out.Severity == s {
			valid = true
		}
	}
	if !valid {
		return out, fmt.Errorf("%w: severity %q must be one of %s", ErrInvalidParams, out.Severity, strings.Join(Severities, ", "))
	}
	check := func(key string, v *float64) error {
		if v == nil {
			return nil
		}
		for _, s := range t.ParamSpecs {
			if s.Key != key {
				continue
			}
			if s.Min != nil && *v < *s.Min {
				return fmt.Errorf("%w: %s %v is below the minimum %v", ErrInvalidParams, key, *v, *s.Min)
			}
			if s.Max != nil && *v > *s.Max {
				return fmt.Errorf("%w: %s %v is above the maximum %v", ErrInvalidParams, key, *v, *s.Max)
			}
		}
		return nil
	}
	f := func(i *int) *float64 {
		if i == nil {
			return nil
		}
		v := float64(*i)
		return &v
	}
	for _, c := range []struct {
		k string
		v *float64
	}{{"threshold", out.Threshold}, {"duration_seconds", f(out.DurationSeconds)}, {"window_seconds", f(out.WindowSeconds)}, {"rate_limit_seconds", f(out.RateLimitSeconds)}} {
		if err := check(c.k, c.v); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (b *builder) Build(t Template, p TemplateParams, ctx BuildContext) (Built, error) {
	d, ok := b.defs[t.ID]
	if !ok {
		return Built{}, fmt.Errorf("%w: unknown template %q", ErrInvalidParams, t.ID)
	}
	merged, err := b.Merge(d.Template, p)
	if err != nil {
		return Built{}, err
	}
	return d.Build(merged, ctx)
}

// Gcloud renders the equivalent gcloud command (policy JSON passed via a file).
func (b *builder) Gcloud(project, name string, policy json.RawMessage) string {
	body := strings.TrimSpace(string(policy))
	if name == "" {
		return fmt.Sprintf("cat > policy.json <<'EOF'\n%s\nEOF\ngcloud monitoring policies create --project=%s --policy-from-file=policy.json", body, project)
	}
	return fmt.Sprintf("cat > policy.json <<'EOF'\n%s\nEOF\ngcloud monitoring policies update %s --policy-from-file=policy.json", body, name)
}
