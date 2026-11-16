package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/smallTechOrg/cloud0/internal/store"
)

// Apply executes a previously previewed change: single-use token, live state
// re-read (stale → ErrPreviewStale), delete confirmation, the write through the
// right client, then one change_history row.
func (e *engine) Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error) {
	now := e.d.Now()
	p, err := e.tokens.claim(req.PreviewToken, now, req.ConfirmDelete)
	if err != nil {
		return ApplyResult{}, err
	}
	restore := func(err error) {
		if engNotConfigured(err) { // nothing was attempted: let the user retry after configuring
			e.tokens.put(p, now)
		}
	}
	cur, err := e.readCurrent(ctx, p)
	if err != nil {
		restore(err)
		if errors.Is(err, ErrNotFound) {
			return ApplyResult{}, fmt.Errorf("%w: the %s no longer exists", ErrPreviewStale, p.kind)
		}
		return ApplyResult{}, err
	}
	if engCurrentHash(cur) != p.currentHash {
		return ApplyResult{}, fmt.Errorf("%w: the live %s changed since the preview", ErrPreviewStale, p.kind)
	}
	target, resource, err := e.write(ctx, p)
	if err != nil {
		restore(err)
		return ApplyResult{}, err
	}
	res := ApplyResult{OK: true, Kind: p.kind, Action: p.action, Target: target, Resource: engNullable(resource), AppliedAt: now, Warnings: []string{}}
	if p.action == ActionDelete {
		res.Resource = json.RawMessage("null")
	}
	if e.d.Store == nil {
		res.Warnings = append(res.Warnings, "Change history is not configured: this change was not logged.")
		return res, nil
	}
	entry, err := store.InsertChange(ctx, e.d.Store.DB, store.NewChange{
		At: now, Actor: e.d.Actor, Kind: p.kind, Action: p.action, Target: target,
		Before: p.current, After: res.Resource, PreviewHash: p.hash,
	})
	if err != nil {
		// The GCP change already happened: report success with a loud warning.
		res.Warnings = append(res.Warnings, fmt.Sprintf("The change was applied but could not be written to the change history: %v", err))
		return res, nil
	}
	res.ChangeHistoryID = entry.ID
	return res, nil
}

func engViewRaw(raw json.RawMessage, v any) json.RawMessage {
	if engPresent(raw) {
		return raw
	}
	return engMust(v)
}

// write performs the GCP mutation of p and returns the resulting name and resource.
func (e *engine) write(ctx context.Context, p *engPending) (string, json.RawMessage, error) {
	switch p.kind {
	case TargetPolicy:
		var v PolicyView
		var err error
		switch p.action {
		case ActionCreate:
			v, err = e.d.Policies.Create(ctx, p.project, p.policyBody)
		case ActionUpdate:
			v, err = e.d.Policies.Update(ctx, p.target, p.policyBody)
		case ActionEnable, ActionDisable:
			v, err = e.d.Policies.SetEnabled(ctx, p.target, p.enabled)
		case ActionDelete:
			return p.target, nil, e.d.Policies.Delete(ctx, p.target)
		}
		if err != nil {
			return "", nil, err
		}
		return engNameOr(v.Name, p.target), engPolicyRaw(v), nil
	case TargetUptimeCheck:
		var v UptimeCheck
		var err error
		switch p.action {
		case ActionCreate:
			v, err = e.d.Uptime.Create(ctx, p.uptime)
		case ActionUpdate:
			v, err = e.d.Uptime.Update(ctx, p.target, p.uptime)
		case ActionDelete:
			return p.target, nil, e.d.Uptime.Delete(ctx, p.target)
		}
		if err != nil {
			return "", nil, err
		}
		return engNameOr(v.Name, p.target), engViewRaw(v.RawJSON, v), nil
	case TargetBudget:
		var v BudgetView
		var err error
		switch p.action {
		case ActionCreate:
			v, err = e.d.Budgets.Create(ctx, p.budget, p.budgetChans)
		case ActionUpdate:
			v, err = e.d.Budgets.Update(ctx, p.target, p.budget, p.budgetChans)
		case ActionDelete:
			return p.target, nil, e.d.Budgets.Delete(ctx, p.target)
		}
		if err != nil {
			return "", nil, err
		}
		return engNameOr(v.Name, p.target), engViewRaw(v.RawJSON, v), nil
	case TargetSnooze:
		var s Snooze
		var err error
		switch p.action {
		case ActionCreate:
			s, err = e.d.Snoozes.Create(ctx, p.snooze)
		case ActionEnd:
			s, err = e.d.Snoozes.End(ctx, p.target, e.d.Now())
		}
		if err != nil {
			return "", nil, err
		}
		return engNameOr(s.Name, p.target), engSnoozeJSON(s), nil
	case TargetChannel:
		switch p.action {
		case ActionCreate:
			c, err := e.d.Channels.Create(ctx, p.channel)
			if err != nil {
				return "", nil, err
			}
			return engNameOr(c.Name, p.target), engChannelJSON(c), nil
		case ActionDelete:
			return p.target, nil, e.d.Channels.Delete(ctx, p.target)
		}
	}
	return "", nil, engInvalid("unsupported %s %s", p.kind, p.action)
}

func engNameOr(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}
