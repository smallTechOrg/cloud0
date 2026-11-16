package alerting

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ---- uptime checks ----------------------------------------------------------

func engUptimeBody(d UptimeDraft, managed bool) map[string]any {
	scheme := d.UseSSL
	body := map[string]any{
		"displayName": d.DisplayName,
		"monitoredResource": map[string]any{
			"type":   "uptime_url",
			"labels": map[string]any{"host": d.Host, "project_id": d.Project},
		},
		"httpCheck": map[string]any{
			"requestMethod": "GET",
			"useSsl":        scheme,
			"validateSsl":   scheme,
			"path":          d.Path,
			"port":          d.Port,
		},
		"period":  fmt.Sprintf("%ds", d.PeriodSeconds),
		"timeout": fmt.Sprintf("%ds", d.TimeoutSeconds),
	}
	if d.ContentMatch != "" {
		body["contentMatchers"] = []any{map[string]any{"content": d.ContentMatch, "matcher": "CONTAINS_STRING"}}
	}
	if managed {
		body["userLabels"] = map[string]any{LabelManagedBy: LabelManagedByVal}
	}
	return body
}

func engNormalizeUptime(d UptimeDraft, project string) (UptimeDraft, error) {
	if d.Project == "" {
		d.Project = project
	}
	if d.Project == "" {
		return d, engInvalid("uptime check needs a project")
	}
	if strings.TrimSpace(d.DisplayName) == "" {
		return d, engInvalid("display_name is required")
	}
	if d.Host == "" || strings.ContainsAny(d.Host, "/: ") {
		return d, engInvalid("host must be a bare hostname (no scheme, port or path)")
	}
	if d.Path == "" {
		d.Path = "/"
	}
	if !strings.HasPrefix(d.Path, "/") {
		return d, engInvalid("path must start with /")
	}
	if d.Port == 0 {
		d.Port = 80
		if d.UseSSL {
			d.Port = 443
		}
	}
	if d.Port < 1 || d.Port > 65535 {
		return d, engInvalid("port %d out of range", d.Port)
	}
	if d.PeriodSeconds == 0 {
		d.PeriodSeconds = 60
	}
	switch d.PeriodSeconds {
	case 60, 300, 600, 900:
	default:
		return d, engInvalid("period_seconds must be 60, 300, 600 or 900")
	}
	if d.TimeoutSeconds == 0 {
		d.TimeoutSeconds = 10
	}
	if d.TimeoutSeconds < 1 || d.TimeoutSeconds > 60 {
		return d, engInvalid("timeout_seconds must be 1-60")
	}
	return d, nil
}

func (e *engine) previewUptime(ctx context.Context, req PreviewRequest) (*engPending, error) {
	p := &engPending{kind: TargetUptimeCheck, action: req.Action, target: req.Target, extraIgnore: engUptimeServerKeys}
	if engPresent(req.Desired) {
		return nil, engInvalid("uptime checks are edited with params (UptimeDraft), not desired")
	}
	switch req.Action {
	case ActionCreate, ActionUpdate:
		var d UptimeDraft
		if !engPresent(req.Params) {
			return nil, engInvalid("params (UptimeDraft) are required")
		}
		if err := engStrict(req.Params, &d); err != nil {
			return nil, err
		}
		managed := true
		project := req.Project
		if req.Action == ActionUpdate {
			if req.Target == "" {
				return nil, engInvalid("uptime update needs a target")
			}
			live, err := e.d.Uptime.Get(ctx, req.Target)
			if err != nil {
				return nil, err
			}
			p.current = live.RawJSON
			if !engPresent(p.current) {
				p.current = engMust(live)
			}
			managed = live.Managed
			project = engProjectOf(req.Target)
			if !managed {
				p.warnings = append(p.warnings, "This uptime check is unmanaged (not created by cloud0); it stays unmanaged after the update.")
			}
		} else if req.Target != "" {
			return nil, engInvalid("uptime create takes no target")
		}
		d, err := engNormalizeUptime(d, project)
		if err != nil {
			return nil, err
		}
		p.project, p.uptime = d.Project, d
		p.desired = engMust(engUptimeBody(d, managed))
		if d.PeriodSeconds > 60 {
			p.warnings = append(p.warnings, fmt.Sprintf("Check period is %d s: an outage can go unnoticed for that long.", d.PeriodSeconds))
		}
	case ActionDelete:
		if req.Target == "" {
			return nil, engInvalid("uptime delete needs a target")
		}
		live, err := e.d.Uptime.Get(ctx, req.Target)
		if err != nil {
			return nil, err
		}
		p.project = engProjectOf(req.Target)
		p.current = live.RawJSON
		if !engPresent(p.current) {
			p.current = engMust(live)
		}
		p.warnings = append(p.warnings, "Policies that alert on this check's uptime will stop firing once it is deleted.")
		if !live.Managed {
			p.warnings = append(p.warnings, "This uptime check is unmanaged (not created by cloud0). Deleting it is irreversible.")
		}
	default:
		return nil, engInvalid("action %q is not valid for an uptime check", req.Action)
	}
	return p, nil
}

// ---- budgets ---------------------------------------------------------------

func engDefaultThresholds() []BudgetThreshold {
	return []BudgetThreshold{
		{Percent: 50, Basis: "current_spend"}, {Percent: 80, Basis: "current_spend"},
		{Percent: 100, Basis: "current_spend"}, {Percent: 100, Basis: "forecasted_spend"},
	}
}

func (e *engine) previewBudget(ctx context.Context, req PreviewRequest) (*engPending, error) {
	p := &engPending{kind: TargetBudget, action: req.Action, target: req.Target, project: req.Project}
	if engPresent(req.Desired) {
		return nil, engInvalid("budgets are edited with params (BudgetDraft), not desired")
	}
	switch req.Action {
	case ActionCreate, ActionUpdate:
		if !engPresent(req.Params) {
			return nil, engInvalid("params (BudgetDraft) are required")
		}
		var d BudgetDraft
		if err := engStrict(req.Params, &d); err != nil {
			return nil, err
		}
		if strings.TrimSpace(d.DisplayName) == "" {
			return nil, engInvalid("display_name is required")
		}
		if d.Amount <= 0 {
			return nil, engInvalid("amount must be greater than 0")
		}
		for _, t := range d.Thresholds {
			if t.Percent <= 0 || t.Percent > 1000 || (t.Basis != "current_spend" && t.Basis != "forecasted_spend") {
				return nil, engInvalid("threshold %v/%q is invalid (percent > 0, basis current_spend|forecasted_spend)", t.Percent, t.Basis)
			}
		}
		if len(d.Thresholds) == 0 {
			d.Thresholds = engDefaultThresholds()
		}
		if d.Severity == "" {
			d.Severity = SevError
		}
		managedName := strings.HasPrefix(d.DisplayName, BudgetNamePrefix)
		if req.Action == ActionCreate {
			if req.Target != "" {
				return nil, engInvalid("budget create takes no target")
			}
			if !managedName {
				return nil, engInvalid("display_name must start with %q for a cloud0-managed budget", BudgetNamePrefix)
			}
		} else {
			if req.Target == "" {
				return nil, engInvalid("budget update needs a target")
			}
			live, err := e.d.Budgets.Get(ctx, req.Target)
			if err != nil {
				return nil, err
			}
			p.current = live.RawJSON
			if !engPresent(p.current) {
				p.current = engMust(live)
			}
			if live.Managed && !managedName {
				return nil, engInvalid("a managed budget's display_name must keep the %q prefix", BudgetNamePrefix)
			}
			if !live.Managed {
				p.warnings = append(p.warnings, "This budget is unmanaged (not created by cloud0); it stays unmanaged unless you rename it with the cloud0- prefix.")
			}
		}
		proj := req.Project
		if proj == "" && len(d.Projects) > 0 {
			proj = d.Projects[0]
		}
		p.project = proj
		chans, warns, err := e.d.Router.Resolve(ctx, proj, d.Severity, d.ChannelOverride)
		if err != nil {
			return nil, err
		}
		p.warnings = append(p.warnings, warns...)
		if len(chans) == 0 {
			p.warnings = append(p.warnings, "No monitoring notification channels: budget alerts go to billing admins by email only.")
		}
		p.warnings = append(p.warnings, e.channelWarnings(ctx, chans)...)
		body, err := e.d.Budgets.Body(d, chans)
		if err != nil {
			return nil, err
		}
		p.budget, p.budgetChans, p.desired = d, chans, body
	case ActionDelete:
		if req.Target == "" {
			return nil, engInvalid("budget delete needs a target")
		}
		live, err := e.d.Budgets.Get(ctx, req.Target)
		if err != nil {
			return nil, err
		}
		p.current = live.RawJSON
		if !engPresent(p.current) {
			p.current = engMust(live)
		}
		if !live.Managed {
			p.warnings = append(p.warnings, "This budget is unmanaged (not created by cloud0). Deleting it is irreversible.")
		}
	default:
		return nil, engInvalid("action %q is not valid for a budget", req.Action)
	}
	return p, nil
}

// ---- snoozes ---------------------------------------------------------------

func engSnoozeBody(name string, d SnoozeDraft, start, end time.Time) map[string]any {
	b := map[string]any{
		"displayName": d.DisplayName,
		"criteria":    map[string]any{"policies": d.PolicyNames},
		"interval":    map[string]any{"startTime": start.UTC().Format(time.RFC3339), "endTime": end.UTC().Format(time.RFC3339)},
	}
	if name != "" {
		b["name"] = name
	}
	return b
}

func (e *engine) previewSnooze(ctx context.Context, req PreviewRequest) (*engPending, error) {
	p := &engPending{kind: TargetSnooze, action: req.Action, target: req.Target, project: req.Project}
	if engPresent(req.Desired) {
		return nil, engInvalid("snoozes take params (SnoozeDraft), not desired")
	}
	now := e.d.Now().UTC().Truncate(time.Second)
	switch req.Action {
	case ActionCreate:
		var d SnoozeDraft
		if !engPresent(req.Params) {
			return nil, engInvalid("params (SnoozeDraft) are required")
		}
		if err := engStrict(req.Params, &d); err != nil {
			return nil, err
		}
		if d.Project == "" {
			d.Project = req.Project
		}
		if d.Project == "" {
			return nil, engInvalid("snooze needs a project")
		}
		if strings.TrimSpace(d.DisplayName) == "" {
			return nil, engInvalid("display_name is required")
		}
		if d.DurationSeconds < 300 || d.DurationSeconds > 2592000 {
			return nil, engInvalid("duration_seconds must be 300..2592000")
		}
		if len(d.PolicyNames) == 0 {
			return nil, engInvalid("policy_names must not be empty")
		}
		for _, n := range d.PolicyNames {
			if engProjectOf(n) != d.Project || !strings.Contains(n, "/alertPolicies/") {
				return nil, engInvalid("policy %q is not an alert policy of project %s", n, d.Project)
			}
			if _, err := e.d.Policies.Get(ctx, n); err != nil {
				return nil, err
			}
		}
		start := now
		if d.StartTime != nil {
			start = d.StartTime.UTC().Truncate(time.Second)
		}
		d.StartTime = &start
		end := start.Add(time.Duration(d.DurationSeconds) * time.Second)
		if !end.After(now) {
			return nil, engInvalid("the snooze window is already over")
		}
		p.project, p.snooze = d.Project, d
		p.desired = engMust(engSnoozeBody("", d, start, end))
		p.warnings = append(p.warnings, fmt.Sprintf("Notifications of %d policy(ies) are muted until %s; incidents still open.", len(d.PolicyNames), end.Format(time.RFC3339)))
	case ActionEnd:
		if req.Target == "" {
			return nil, engInvalid("snooze end needs a target")
		}
		s, err := e.findSnooze(ctx, req.Target)
		if err != nil {
			return nil, err
		}
		if !s.EndTime.After(now) {
			return nil, engInvalid("snooze %s has already ended", req.Target)
		}
		p.project = engProjectOf(req.Target)
		p.current = engSnoozeJSON(s)
		end := now
		start := s.StartTime
		if end.Before(start) {
			end = start
		}
		p.desired = engMust(engSnoozeBody(s.Name, SnoozeDraft{DisplayName: s.DisplayName, PolicyNames: s.PolicyNames}, start, end))
	default:
		return nil, engInvalid("action %q is not valid for a snooze", req.Action)
	}
	return p, nil
}

// ---- channels --------------------------------------------------------------

func (e *engine) previewChannel(ctx context.Context, req PreviewRequest) (*engPending, error) {
	p := &engPending{kind: TargetChannel, action: req.Action, target: req.Target, project: req.Project}
	if engPresent(req.Desired) {
		return nil, engInvalid("channels take params (ChannelDraft), not desired")
	}
	switch req.Action {
	case ActionCreate:
		var d ChannelDraft
		if !engPresent(req.Params) {
			return nil, engInvalid("params (ChannelDraft) are required")
		}
		if err := engStrict(req.Params, &d); err != nil {
			return nil, err
		}
		if d.Project == "" {
			d.Project = req.Project
		}
		if d.Project == "" || d.Type == "" || strings.TrimSpace(d.DisplayName) == "" {
			return nil, engInvalid("channel needs project, type and display_name")
		}
		descs, err := e.d.Channels.Descriptors(ctx, d.Project)
		if err != nil {
			return nil, err
		}
		var desc *ChannelDescriptor
		for i := range descs {
			if descs[i].Type == d.Type {
				desc = &descs[i]
			}
		}
		if desc == nil {
			return nil, engInvalid("unknown channel type %q", d.Type)
		}
		if !desc.Creatable {
			hint := desc.Hint
			if hint == "" {
				hint = "create it in the Cloud Console and pick the existing channel"
			}
			return nil, engInvalid("channel type %q cannot be created programmatically: %s", d.Type, hint)
		}
		labels := map[string]string{}
		sens := map[string]string{}
		for k, v := range d.Labels {
			labels[k] = v
		}
		for k, v := range d.SensitiveLabels {
			sens[k] = v
		}
		for _, ls := range desc.Labels {
			if ls.Sensitive {
				if v, ok := labels[ls.Key]; ok { // never keep a secret in the plain labels
					sens[ls.Key] = v
					delete(labels, ls.Key)
				}
			}
			if ls.Required && labels[ls.Key] == "" && sens[ls.Key] == "" {
				return nil, engInvalid("channel label %q is required", ls.Key)
			}
		}
		d.Labels, d.SensitiveLabels = labels, sens
		red := map[string]any{}
		for k := range sens {
			red[k] = "[redacted]"
		}
		body := map[string]any{"type": d.Type, "displayName": d.DisplayName, "labels": labels, "enabled": true}
		if len(red) > 0 {
			body["sensitiveLabels"] = red
		}
		p.project, p.channel, p.desired = d.Project, d, engMust(body)
		if d.Type == "email" {
			p.warnings = append(p.warnings, "A new email channel must be verified in the console before notifications are reliably delivered.")
		}
	case ActionDelete:
		if req.Target == "" {
			return nil, engInvalid("channel delete needs a target")
		}
		c, err := e.findChannel(ctx, req.Target)
		if err != nil {
			return nil, err
		}
		p.project = engProjectOf(req.Target)
		p.current = engChannelJSON(c)
		if routing, err := e.d.Router.Get(ctx); err == nil {
			for sev, names := range routing {
				for _, n := range names {
					if n == req.Target {
						p.warnings = append(p.warnings, fmt.Sprintf("This channel is in the %s routing: update the routing or those alerts will notify nobody.", sev))
					}
				}
			}
		}
		if list, err := e.d.Policies.List(ctx, p.project); err == nil {
			n := 0
			for _, v := range list {
				for _, c := range v.Channels {
					if c.Name == req.Target {
						n++
					}
				}
			}
			if n > 0 {
				p.warnings = append(p.warnings, fmt.Sprintf("%d alert policy(ies) notify this channel and would lose it.", n))
			}
		}
	default:
		return nil, engInvalid("action %q is not valid for a channel", req.Action)
	}
	return p, nil
}
