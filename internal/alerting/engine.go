package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/smallTechOrg/cloud0/internal/store"
)

// EngineDeps are the collaborators of the preview/apply engine.
type EngineDeps struct {
	Builder  Builder
	Policies PolicyClient
	Channels ChannelClient
	Uptime   UptimeClient
	Snoozes  SnoozeClient
	Budgets  BudgetClient
	Router   Router
	Store    *store.Store // change_history; nil → apply succeeds with a warning
	Now      func() time.Time
	Actor    string // default "local-user"
}

type engine struct {
	d      EngineDeps
	tokens *engTokens
}

// NewEngine returns the preview/apply Engine. Nothing is written to GCP by Preview.
func NewEngine(deps EngineDeps) Engine {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Actor == "" {
		deps.Actor = "local-user"
	}
	return &engine{d: deps, tokens: newEngTokens()}
}

func engInvalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidParams, fmt.Sprintf(format, a...))
}

// engProjectOf extracts X from "projects/X/...".
func engProjectOf(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) >= 2 && parts[0] == "projects" {
		return parts[1]
	}
	return ""
}

func engPresent(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && string(t) != "null"
}

// engStrict decodes raw into dst rejecting unknown keys; empty raw leaves dst zero.
func engStrict(raw json.RawMessage, dst any) error {
	if !engPresent(raw) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return engInvalid("params: %v", err)
	}
	return nil
}

func engDedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func engMust(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// Preview resolves the desired state, reads the live one, diffs them and
// registers a single-use token. It never writes to GCP.
func (e *engine) Preview(ctx context.Context, req PreviewRequest) (Preview, error) {
	if engPresent(req.Params) && engPresent(req.Desired) {
		return Preview{}, engInvalid("send either params or desired, not both")
	}
	var p *engPending
	var err error
	switch req.Kind {
	case TargetPolicy:
		p, err = e.previewPolicy(ctx, req)
	case TargetUptimeCheck:
		p, err = e.previewUptime(ctx, req)
	case TargetBudget:
		p, err = e.previewBudget(ctx, req)
	case TargetSnooze:
		p, err = e.previewSnooze(ctx, req)
	case TargetChannel:
		p, err = e.previewChannel(ctx, req)
	default:
		return Preview{}, engInvalid("unknown kind %q", req.Kind)
	}
	if err != nil {
		return Preview{}, err
	}
	p.kind, p.action = req.Kind, req.Action
	if req.Action == ActionDelete {
		p.requiresConfirm = true
	}
	p.currentHash = engCurrentHash(p.current)
	p.token, p.hash = engToken(p)
	p.expires = e.d.Now().Add(engTokenTTL)
	p.warnings = engDedupe(p.warnings)
	e.tokens.put(p, e.d.Now())

	changes, err := engDiff(p.current, p.desired, p.extraIgnore)
	if err != nil {
		return Preview{}, err
	}
	return Preview{
		PreviewToken:    p.token,
		ExpiresAt:       p.expires,
		Kind:            p.kind,
		Action:          p.action,
		Target:          p.target,
		Current:         engNullable(p.current),
		Desired:         engNullable(p.desired),
		Diff:            Diff{Changes: changes, Unified: engUnified(p.current, p.desired, p.extraIgnore)},
		Gcloud:          e.gcloud(p),
		Warnings:        p.warnings,
		RequiresConfirm: p.requiresConfirm,
	}, nil
}

func engNullable(raw json.RawMessage) json.RawMessage {
	if !engPresent(raw) {
		return json.RawMessage("null")
	}
	return raw
}

// readCurrent fetches the live resource of p (nil for create). A vanished
// resource is ErrNotFound; Apply maps that to ErrPreviewStale.
func (e *engine) readCurrent(ctx context.Context, p *engPending) (json.RawMessage, error) {
	if p.action == ActionCreate {
		return nil, nil
	}
	switch p.kind {
	case TargetPolicy:
		v, err := e.d.Policies.Get(ctx, p.target)
		if err != nil {
			return nil, err
		}
		return engPolicyRaw(v), nil
	case TargetUptimeCheck:
		v, err := e.d.Uptime.Get(ctx, p.target)
		if err != nil {
			return nil, err
		}
		if engPresent(v.RawJSON) {
			return v.RawJSON, nil
		}
		return engMust(v), nil
	case TargetBudget:
		v, err := e.d.Budgets.Get(ctx, p.target)
		if err != nil {
			return nil, err
		}
		if engPresent(v.RawJSON) {
			return v.RawJSON, nil
		}
		return engMust(v), nil
	case TargetSnooze:
		s, err := e.findSnooze(ctx, p.target)
		if err != nil {
			return nil, err
		}
		return engSnoozeJSON(s), nil
	case TargetChannel:
		c, err := e.findChannel(ctx, p.target)
		if err != nil {
			return nil, err
		}
		return engChannelJSON(c), nil
	}
	return nil, engInvalid("unknown kind %q", p.kind)
}

func engPolicyRaw(v PolicyView) json.RawMessage {
	if engPresent(v.RawJSON) {
		return v.RawJSON
	}
	return engMust(v)
}

func engSnoozeJSON(s Snooze) json.RawMessage {
	return engMust(map[string]any{
		"name":        s.Name,
		"displayName": s.DisplayName,
		"criteria":    map[string]any{"policies": s.PolicyNames},
		"interval":    map[string]any{"startTime": s.StartTime.UTC().Format(time.RFC3339), "endTime": s.EndTime.UTC().Format(time.RFC3339)},
	})
}

func engChannelJSON(c Channel) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(engMust(c), &m) != nil {
		return engMust(c)
	}
	delete(m, "warnings") // derived, not part of the resource
	return engMust(m)
}

func (e *engine) findSnooze(ctx context.Context, name string) (Snooze, error) {
	list, err := e.d.Snoozes.List(ctx, engProjectOf(name))
	if err != nil {
		return Snooze{}, err
	}
	for _, s := range list {
		if s.Name == name {
			return s, nil
		}
	}
	return Snooze{}, fmt.Errorf("%w: snooze %s", ErrNotFound, name)
}

func (e *engine) findChannel(ctx context.Context, name string) (Channel, error) {
	list, err := e.d.Channels.List(ctx, engProjectOf(name))
	if err != nil {
		return Channel{}, err
	}
	for _, c := range list {
		if c.Name == name {
			return c, nil
		}
	}
	return Channel{}, fmt.Errorf("%w: channel %s", ErrNotFound, name)
}

// channelWarnings checks each channel resource name against the live list.
func (e *engine) channelWarnings(ctx context.Context, names []string) []string {
	var out []string
	byProject := map[string][]Channel{}
	failed := map[string]bool{}
	for _, n := range names {
		proj := engProjectOf(n)
		if _, ok := byProject[proj]; !ok && !failed[proj] {
			list, err := e.d.Channels.List(ctx, proj)
			if err != nil {
				failed[proj] = true
				out = append(out, fmt.Sprintf("Could not check the notification channels of %s: %v", proj, err))
				continue
			}
			byProject[proj] = list
		}
		if failed[proj] {
			continue
		}
		var found *Channel
		for i := range byProject[proj] {
			if byProject[proj][i].Name == n {
				found = &byProject[proj][i]
			}
		}
		switch {
		case found == nil:
			out = append(out, fmt.Sprintf("Notification channel %s was not found in %s: notifications would not be delivered.", n, proj))
		case !found.Enabled:
			out = append(out, fmt.Sprintf("Notification channel %q is disabled: notifications would not be delivered.", found.DisplayName))
		case found.VerificationStatus != "VERIFIED" && (found.Type == "email" || found.VerificationStatus == "UNVERIFIED"):
			out = append(out, fmt.Sprintf("Channel %q is not verified: notifications may not be delivered. Open the channel in the console and verify it.", found.DisplayName))
		}
	}
	return out
}

// errIsNotConfigured reports whether err means the alerts credentials are missing.
func engNotConfigured(err error) bool { return errors.Is(err, ErrNotConfigured) }
