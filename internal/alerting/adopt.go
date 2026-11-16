package alerting

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// AdoptDeps are the collaborators of the Adopter.
type AdoptDeps struct {
	Builder  Builder
	Policies PolicyClient
	Budgets  BudgetClient
	Engine   Engine
	Router   Router
	Projects []string
	Now      func() time.Time
}

type adopter struct{ AdoptDeps }

// NewAdopter returns the Adopter implementing the frozen shape → template mapping.
func NewAdopter(deps AdoptDeps) Adopter {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &adopter{deps}
}

func isUptimeShape(s string) bool { return s == ShapeUptimeBackend || s == ShapeUptimeTwoHours }

func filtersOf(p PolicyView) string {
	var b strings.Builder
	for _, c := range p.Conditions {
		b.WriteString(c.Filter)
		b.WriteByte('\n')
	}
	return b.String()
}

// detectShape classifies an unmanaged policy by its metric signature plus
// name hint (case-insensitive); "" when it matches no known shape.
func detectShape(p PolicyView) string {
	if p.Managed {
		return ""
	}
	if p.AdoptShape != "" {
		return p.AdoptShape
	}
	name := strings.ToLower(p.DisplayName)
	filters := filtersOf(p)
	switch {
	case strings.Contains(filters, "uptime_check") && (strings.Contains(name, "backend down") || strings.Contains(name, "down for 2 hours")):
		if strings.Contains(name, "2 hours") {
			return ShapeUptimeTwoHours
		}
		return ShapeUptimeBackend
	case strings.Contains(filters, "cpu/utilization") && strings.Contains(name, "cpu spike"):
		return ShapeZeroCPUSpike
	case strings.Contains(filters, "memory") && strings.Contains(name, "memory load"):
		return ShapeHighMemory
	}
	return ""
}

func (a *adopter) Preview(ctx context.Context, req AdoptRequest) (AdoptPreview, error) {
	switch req.Shape {
	case ShapeZeroCPUSpike:
		return a.vmShape(ctx, req, TplVMCPUHigh, 80, 300)
	case ShapeHighMemory:
		return a.vmShape(ctx, req, TplVMMemoryHigh, 85, 900)
	case ShapeUptimeBackend, ShapeUptimeTwoHours:
		return a.uptimeShape(ctx, req)
	case ShapeBudgetBoilerplate:
		return a.budgetShape(ctx, req)
	}
	return AdoptPreview{}, fmt.Errorf("%w: unknown adopt shape %q", ErrInvalidParams, req.Shape)
}

// unmanaged lists the unmanaged policies of every project that match shape ok.
func (a *adopter) listPolicies(ctx context.Context) ([]PolicyView, error) {
	var all []PolicyView
	for _, p := range a.Projects {
		l, err := a.Policies.List(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("alerting: list policies of %s: %w", p, err)
		}
		all = append(all, l...)
	}
	return all, nil
}

// resolveOld finds the unmanaged policy for shape: explicit Target (validated)
// or the single detected one.
func (a *adopter) resolveOld(ctx context.Context, all []PolicyView, req AdoptRequest, match func(string) bool) (PolicyView, error) {
	if req.Target != "" {
		p, err := a.Policies.Get(ctx, req.Target)
		if err != nil {
			return PolicyView{}, err
		}
		if p.Managed {
			return PolicyView{}, fmt.Errorf("%w: %s is already managed by cloud0", ErrInvalidParams, req.Target)
		}
		if !match(detectShape(p)) {
			return PolicyView{}, fmt.Errorf("%w: %s does not match shape %s", ErrInvalidParams, req.Target, req.Shape)
		}
		return p, nil
	}
	var cands []PolicyView
	for _, p := range all {
		if match(detectShape(p)) {
			cands = append(cands, p)
		}
	}
	switch len(cands) {
	case 0:
		return PolicyView{}, fmt.Errorf("%w: no unmanaged policy matches shape %s", ErrNotFound, req.Shape)
	case 1:
		return cands[0], nil
	}
	return PolicyView{}, fmt.Errorf("%w: %d policies match shape %s; pass target", ErrInvalidParams, len(cands), req.Shape)
}

func projectOf(p PolicyView) string {
	if p.Project != "" {
		return p.Project
	}
	return channelProject(p.Name)
}

// managedEquivalent finds the managed policy of template/instance in project.
func managedEquivalent(all []PolicyView, project, tpl, instance string) (PolicyView, bool) {
	for _, p := range all {
		if p.Managed && p.TemplateID == tpl && p.Instance == instance && projectOf(p) == project {
			return p, true
		}
	}
	return PolicyView{}, false
}

func (a *adopter) policyPreview(ctx context.Context, all []PolicyView, project, tpl string, params TemplateParams) (Preview, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return Preview{}, err
	}
	pr := PreviewRequest{Kind: TargetPolicy, Action: ActionCreate, Project: project, TemplateID: tpl, Params: raw}
	if ex, ok := managedEquivalent(all, project, tpl, params.Instance); ok {
		pr.Action, pr.Target = ActionUpdate, ex.Name
	}
	return a.Engine.Preview(ctx, pr)
}

func disableFollowUp(p PolicyView) AdoptFollowUp {
	return AdoptFollowUp{Action: ActionDisable, Kind: TargetPolicy, Target: p.Name,
		Note: fmt.Sprintf("Disable %q only after the replacement is applied; cloud0 never deletes it for you.", p.DisplayName)}
}

func (a *adopter) vmShape(ctx context.Context, req AdoptRequest, tpl string, threshold float64, dur int) (AdoptPreview, error) {
	all, err := a.listPolicies(ctx)
	if err != nil {
		return AdoptPreview{}, err
	}
	old, err := a.resolveOld(ctx, all, req, func(s string) bool { return s == req.Shape })
	if err != nil {
		return AdoptPreview{}, err
	}
	project := projectOf(old)
	pv, err := a.policyPreview(ctx, all, project, tpl, TemplateParams{Threshold: &threshold, DurationSeconds: &dur})
	if err != nil {
		return AdoptPreview{}, err
	}
	notes := []string{fmt.Sprintf("Replacement covers every VM in %s, including future ones, not just the one the old policy named.", project)}
	for _, c := range old.Conditions {
		if c.DurationSeconds != nil && *c.DurationSeconds == 0 {
			notes = append(notes, fmt.Sprintf("The old policy has duration 0 s (fires on a single sample); the replacement requires %d min.", dur/60))
			break
		}
	}
	if tpl == TplVMMemoryHigh {
		notes = append(notes, "Memory uses Ops Agent metrics: VMs without the agent are not covered.")
	}
	return AdoptPreview{Shape: req.Shape, Replaces: []string{old.Name}, Previews: []Preview{pv},
		FollowUp: []AdoptFollowUp{disableFollowUp(old)}, Notes: notes}, nil
}

func (a *adopter) uptimeShape(ctx context.Context, req AdoptRequest) (AdoptPreview, error) {
	all, err := a.listPolicies(ctx)
	if err != nil {
		return AdoptPreview{}, err
	}
	old, err := a.resolveOld(ctx, all, req, func(s string) bool {
		if req.Target != "" {
			return isUptimeShape(s)
		}
		return s == req.Shape
	})
	if err != nil {
		return AdoptPreview{}, err
	}
	project := projectOf(old)
	replaces := []PolicyView{old}
	for _, p := range all {
		if p.Name != old.Name && projectOf(p) == project && isUptimeShape(detectShape(p)) {
			replaces = append(replaces, p)
		}
	}
	var previews []Preview
	for _, s := range []struct {
		inst, sev string
		dur       int
	}{{"warning", SevWarning, 600}, {"critical", SevCritical, 7200}} {
		dur := s.dur
		pv, err := a.policyPreview(ctx, all, project, TplUptimeDown, TemplateParams{Instance: s.inst, Severity: s.sev, DurationSeconds: &dur})
		if err != nil {
			return AdoptPreview{}, err
		}
		previews = append(previews, pv)
	}
	out := AdoptPreview{Shape: req.Shape, Previews: previews, Notes: []string{
		"One managed uptime rule with escalation: WARNING after 10 min down, CRITICAL after 2 h down.",
		"Both previews are created whichever uptime shape was chosen; apply each independently.",
	}}
	for _, p := range replaces {
		out.Replaces = append(out.Replaces, p.Name)
		out.FollowUp = append(out.FollowUp, disableFollowUp(p))
	}
	if len(replaces) == 1 {
		out.Notes = append(out.Notes, "Only one of the two old uptime policies exists; the other is not present.")
	}
	return out, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func budgetSlug(display string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(display), "-"), "-")
	s = strings.TrimPrefix(s, BudgetNamePrefix)
	if s == "" {
		s = "account-monthly"
	}
	return BudgetNamePrefix + s
}

func (a *adopter) budgetShape(ctx context.Context, req AdoptRequest) (AdoptPreview, error) {
	budgets, err := a.Budgets.List(ctx)
	if err != nil {
		return AdoptPreview{}, fmt.Errorf("alerting: list budgets: %w", err)
	}
	var old BudgetView
	if req.Target != "" {
		old, err = a.Budgets.Get(ctx, req.Target)
		if err != nil {
			return AdoptPreview{}, err
		}
		if old.Managed {
			return AdoptPreview{}, fmt.Errorf("%w: %s is already managed by cloud0", ErrInvalidParams, req.Target)
		}
	} else {
		var cands []BudgetView
		for _, b := range budgets {
			if !b.Managed {
				cands = append(cands, b)
			}
		}
		switch len(cands) {
		case 0:
			return AdoptPreview{}, fmt.Errorf("%w: no unmanaged budget", ErrNotFound)
		case 1:
			old = cands[0]
		default:
			return AdoptPreview{}, fmt.Errorf("%w: %d unmanaged budgets; pass target", ErrInvalidParams, len(cands))
		}
	}
	if old.Amount == nil || *old.Amount <= 0 {
		return AdoptPreview{}, fmt.Errorf("%w: budget %s has no fixed amount to copy", ErrInvalidParams, old.Name)
	}
	draft := BudgetDraft{
		DisplayName:          budgetSlug(old.DisplayName),
		Amount:               *old.Amount,
		Projects:             append([]string{}, old.Projects...),
		ServiceIDs:           append([]string{}, old.ServiceIDs...),
		Thresholds:           append([]BudgetThreshold{}, old.Thresholds...),
		Severity:             SevError,
		DefaultIAMRecipients: boolPtr(true),
	}
	if len(draft.Thresholds) == 0 {
		draft.Thresholds = nil
	}
	var notes []string
	if a.Router != nil && len(a.Projects) > 0 {
		chans, warns, err := a.Router.Resolve(ctx, a.Projects[0], SevError, nil)
		if err != nil {
			return AdoptPreview{}, err
		}
		draft.ChannelOverride = chans
		notes = append(notes, warns...)
	}
	pr := PreviewRequest{Kind: TargetBudget, Action: ActionCreate}
	for _, b := range budgets {
		if b.Managed && b.DisplayName == draft.DisplayName {
			pr.Action, pr.Target = ActionUpdate, b.Name
			break
		}
	}
	if pr.Params, err = json.Marshal(draft); err != nil {
		return AdoptPreview{}, err
	}
	pv, err := a.Engine.Preview(ctx, pr)
	if err != nil {
		return AdoptPreview{}, err
	}
	notes = append(notes, "The original budget is left untouched; delete it in the Cloud Billing console once the managed copy is in place, or keep both.")
	return AdoptPreview{Shape: ShapeBudgetBoilerplate, Replaces: []string{old.Name}, Previews: []Preview{pv}, Notes: notes}, nil
}

func boolPtr(b bool) *bool { return &b }
