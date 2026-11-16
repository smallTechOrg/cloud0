package alerting

import (
	"context"
	"encoding/json"
	"fmt"
)

func (e *engine) previewPolicy(ctx context.Context, req PreviewRequest) (*engPending, error) {
	p := &engPending{kind: TargetPolicy, action: req.Action, target: req.Target}
	switch req.Action {
	case ActionCreate:
		if req.Target != "" {
			return nil, engInvalid("policy create takes no target")
		}
		if req.Project == "" || req.TemplateID == "" {
			return nil, engInvalid("policy create needs project and template_id")
		}
		if engPresent(req.Desired) {
			return nil, engInvalid("policy create is template-based: send params, not desired")
		}
		var tp TemplateParams
		if err := engStrict(req.Params, &tp); err != nil {
			return nil, err
		}
		body, warns, _, err := e.buildPolicy(ctx, req.Project, req.TemplateID, tp, nil)
		if err != nil {
			return nil, err
		}
		p.project, p.policyBody, p.desired, p.warnings = req.Project, body, body, warns
		if list, err := e.d.Policies.List(ctx, req.Project); err == nil {
			for _, v := range list {
				if v.Managed && v.TemplateID == req.TemplateID && v.Instance == tp.Instance {
					p.warnings = append(p.warnings, fmt.Sprintf("A managed policy from this template already exists (%s): this would create a duplicate; preview an update instead.", v.Name))
				}
			}
		}
	case ActionUpdate:
		if req.Target == "" {
			return nil, engInvalid("policy update needs a target")
		}
		live, err := e.d.Policies.Get(ctx, req.Target)
		if err != nil {
			return nil, err
		}
		p.project = engProjectOf(req.Target)
		p.current = engPolicyRaw(live)
		switch {
		case engPresent(req.Desired):
			if _, err := engDecode(req.Desired); err != nil {
				return nil, err
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(req.Desired, &m); err != nil {
				return nil, engInvalid("desired must be a JSON object")
			}
			if n, ok := m["name"]; ok {
				var s string
				if json.Unmarshal(n, &s) == nil && s != "" && s != req.Target {
					return nil, engInvalid("desired.name %q differs from target %q", s, req.Target)
				}
			}
			p.policyBody, p.desired = req.Desired, req.Desired
			if live.Managed && !engHasManagedLabel(req.Desired) {
				p.warnings = append(p.warnings, "The desired body drops the managed-by=cloud0 label: the policy would become unmanaged and templates would no longer update it.")
			}
			p.warnings = append(p.warnings, e.channelWarnings(ctx, engBodyChannels(req.Desired))...)
			if len(engBodyChannels(req.Desired)) == 0 {
				p.warnings = append(p.warnings, "No notification channels: this policy will notify nobody.")
			}
		case !live.Managed:
			return nil, engInvalid("%s is unmanaged: send desired (raw policy JSON) to edit it; cloud0 never templates unmanaged policies", req.Target)
		default:
			if req.TemplateID != "" && req.TemplateID != live.TemplateID {
				return nil, engInvalid("policy %s was built from template %q, not %q", req.Target, live.TemplateID, req.TemplateID)
			}
			var tp TemplateParams
			if err := engStrict(req.Params, &tp); err != nil {
				return nil, err
			}
			if tp.Instance == "" {
				tp.Instance = live.Instance
			}
			body, warns, _, err := e.buildPolicy(ctx, p.project, live.TemplateID, tp, &live)
			if err != nil {
				return nil, err
			}
			p.policyBody, p.desired, p.warnings = body, body, warns
		}
	case ActionDelete, ActionEnable, ActionDisable:
		if req.Target == "" {
			return nil, engInvalid("policy %s needs a target", req.Action)
		}
		if engPresent(req.Params) || engPresent(req.Desired) {
			return nil, engInvalid("policy %s takes only a target", req.Action)
		}
		live, err := e.d.Policies.Get(ctx, req.Target)
		if err != nil {
			return nil, err
		}
		p.project = engProjectOf(req.Target)
		p.current = engPolicyRaw(live)
		switch req.Action {
		case ActionDelete:
			p.desired = nil
			if !live.Managed {
				p.warnings = append(p.warnings, "This policy is unmanaged (not created by cloud0). Deleting it is irreversible.")
			}
		default:
			want := req.Action == ActionEnable
			p.enabled = want
			var m map[string]any
			if err := json.Unmarshal(p.current, &m); err != nil {
				return nil, fmt.Errorf("alerting: live policy is not an object: %w", err)
			}
			m["enabled"] = want
			p.desired = engMust(m)
			if live.Enabled == want {
				p.warnings = append(p.warnings, fmt.Sprintf("The policy is already %sd: applying changes nothing.", req.Action))
			}
		}
	default:
		return nil, engInvalid("action %q is not valid for a policy", req.Action)
	}
	return p, nil
}

// buildPolicy merges params, resolves channels via the router and builds the body.
func (e *engine) buildPolicy(ctx context.Context, project, tplID string, tp TemplateParams, existing *PolicyView) (json.RawMessage, []string, TemplateParams, error) {
	tpl, ok := e.d.Builder.Template(tplID)
	if !ok {
		return nil, nil, tp, engInvalid("unknown template %q", tplID)
	}
	merged, err := e.d.Builder.Merge(tpl, tp)
	if err != nil {
		return nil, nil, tp, err
	}
	chans, warns, err := e.d.Router.Resolve(ctx, project, merged.Severity, merged.ChannelOverride)
	if err != nil {
		return nil, nil, tp, err
	}
	bctx := BuildContext{Project: project, Channels: chans, Now: e.d.Now(), Existing: existing}
	if tpl.Kind == KindUptime {
		if checks, err := e.d.Uptime.List(ctx, project); err == nil {
			bctx.UptimeChecks = checks
		}
	}
	built, err := e.d.Builder.Build(tpl, merged, bctx)
	if err != nil {
		return nil, nil, tp, err
	}
	warns = append(warns, built.Warnings...)
	body := built.Policy
	if !engHasManagedLabel(body) {
		warns = append(warns, "The built policy lacks the managed-by=cloud0 label: it would not be recognised as managed.")
	}
	enabled := merged.Enabled == nil || *merged.Enabled
	var m map[string]json.RawMessage
	_ = json.Unmarshal(body, &m)
	if raw, ok := m["enabled"]; ok {
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			enabled = b
		}
	}
	if len(engBodyChannels(body)) == 0 {
		warns = append(warns, fmt.Sprintf("No notification channel resolved for severity %s in %s: the policy would notify nobody.", merged.Severity, project))
	}
	if !enabled {
		warns = append(warns, "The policy is created disabled; enable it explicitly to start alerting.")
	}
	warns = append(warns, e.channelWarnings(ctx, engBodyChannels(body))...)
	switch tplID {
	case TplVMMemoryHigh, TplVMDiskHigh:
		warns = append(warns, "Uses Ops Agent metrics: VMs without the Ops Agent are skipped silently (they stay covered for CPU only).")
	case TplSQLMemoryHigh:
		warns = append(warns, "Shared-core Cloud SQL tiers are skipped for memory alerts: their memory utilization reads 100 %.")
	}
	return body, warns, merged, nil
}

func engHasManagedLabel(body json.RawMessage) bool {
	var v struct {
		UserLabels map[string]string `json:"userLabels"`
	}
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	return v.UserLabels[LabelManagedBy] == LabelManagedByVal
}

func engBodyChannels(body json.RawMessage) []string {
	var v struct {
		Channels []string `json:"notificationChannels"`
	}
	_ = json.Unmarshal(body, &v)
	return v.Channels
}
