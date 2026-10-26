package alerting

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/smallTechOrg/cloud0/internal/store"
)

const (
	routingKeyPrefix   = "alerts.routing."
	routingTarget      = "alerts.routing"
	defaultChannelName = "Alert"
)

type router struct {
	store    *store.Store
	channels ChannelClient
	projects []string
	now      func() time.Time
}

// NewRouter returns the severity → channels Router backed by settings keys
// alerts.routing.<critical|error|warning> (comma-separated channel names).
func NewRouter(st *store.Store, channels ChannelClient, projects []string) Router {
	return &router{store: st, channels: channels, projects: projects, now: time.Now}
}

func routingKey(sev string) string { return routingKeyPrefix + strings.ToLower(sev) }

func validSeverity(s string) bool {
	for _, v := range Severities {
		if v == s {
			return true
		}
	}
	return false
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// channelProject extracts "proj-a" from "projects/proj-a/notificationChannels/1".
func channelProject(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) >= 2 && parts[0] == "projects" {
		return parts[1]
	}
	return ""
}

func (r *router) stored(ctx context.Context) (map[string]string, error) {
	kv, err := store.GetSettingsWithPrefix(ctx, r.store.DB, routingKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("alerting: read routing: %w", err)
	}
	return kv, nil
}

// defaultChannels is the single email channel named "Alert" in the first
// project, or nothing (logged) when there is not exactly one.
func (r *router) defaultChannels(ctx context.Context) []string {
	if len(r.projects) == 0 {
		return nil
	}
	list, err := r.channels.List(ctx, r.projects[0])
	if err != nil {
		slog.Warn("alerting: default routing: cannot list channels", "project", r.projects[0], "err", err)
		return nil
	}
	var found []string
	for _, c := range list {
		if c.Type == "email" && c.DisplayName == defaultChannelName {
			found = append(found, c.Name)
		}
	}
	if len(found) != 1 {
		slog.Warn("alerting: default routing: expected exactly one email channel named Alert", "project", r.projects[0], "found", len(found))
		return nil
	}
	return found
}

func (r *router) Get(ctx context.Context) (map[string][]string, error) {
	kv, err := r.stored(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	var def []string
	defLoaded := false
	for _, sev := range Severities {
		if v, ok := kv[routingKey(sev)]; ok {
			out[sev] = splitList(v)
			continue
		}
		if !defLoaded {
			def, defLoaded = r.defaultChannels(ctx), true
		}
		out[sev] = append([]string{}, def...)
	}
	return out, nil
}

func (r *router) projectSet() map[string]bool {
	m := map[string]bool{}
	for _, p := range r.projects {
		m[p] = true
	}
	return m
}

func (r *router) Put(ctx context.Context, routing map[string][]string) error {
	for k := range routing {
		if !validSeverity(k) {
			return fmt.Errorf("%w: unknown severity %q (want %s)", ErrInvalidParams, k, strings.Join(Severities, ", "))
		}
	}
	known := map[string]Channel{}
	for _, p := range r.projects {
		list, err := r.channels.List(ctx, p)
		if err != nil {
			return fmt.Errorf("alerting: list channels of %s: %w", p, err)
		}
		for _, c := range list {
			known[c.Name] = c
		}
	}
	projects := r.projectSet()
	kv := map[string]string{}
	clean := map[string][]string{}
	for _, sev := range Severities {
		names, ok := routing[sev]
		if !ok || len(names) == 0 {
			return fmt.Errorf("%w: routing for %s needs at least one channel", ErrInvalidParams, sev)
		}
		seen := map[string]bool{}
		for _, n := range names {
			n = strings.TrimSpace(n)
			if strings.Contains(n, ",") || n == "" {
				return fmt.Errorf("%w: invalid channel name %q for %s", ErrInvalidParams, n, sev)
			}
			if seen[n] {
				return fmt.Errorf("%w: duplicate channel %s for %s", ErrInvalidParams, n, sev)
			}
			seen[n] = true
			if !projects[channelProject(n)] {
				return fmt.Errorf("%w: channel %s is not in a configured project", ErrInvalidParams, n)
			}
			if _, ok := known[n]; !ok {
				return fmt.Errorf("%w: channel %s does not exist", ErrInvalidParams, n)
			}
			clean[sev] = append(clean[sev], n)
		}
		kv[routingKey(sev)] = strings.Join(clean[sev], ",")
	}
	before, _ := r.Get(ctx)
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(clean)
	now := r.now()
	return r.store.WithTx(ctx, func(tx *sql.Tx) error {
		if err := store.PutSettings(ctx, tx, kv, now); err != nil {
			return err
		}
		_, err := store.InsertChange(ctx, tx, store.NewChange{At: now, Kind: TargetRouting, Action: ActionUpdate,
			Target: routingTarget, Before: beforeJSON, After: afterJSON})
		return err
	})
}

func (r *router) Resolve(ctx context.Context, project, severity string, override []string) ([]string, []string, error) {
	names := override
	if len(names) == 0 {
		if !validSeverity(severity) {
			return nil, nil, fmt.Errorf("%w: unknown severity %q", ErrInvalidParams, severity)
		}
		all, err := r.Get(ctx)
		if err != nil {
			return nil, nil, err
		}
		names = all[severity]
	}
	var out, warns []string
	var inProject map[string]Channel
	for _, n := range names {
		if p := channelProject(n); p != project {
			warns = append(warns, fmt.Sprintf("channel %s belongs to project %q, skipped for %s", n, p, project))
			continue
		}
		if inProject == nil {
			list, err := r.channels.List(ctx, project)
			if err != nil {
				return nil, nil, fmt.Errorf("alerting: list channels of %s: %w", project, err)
			}
			inProject = map[string]Channel{}
			for _, c := range list {
				inProject[c.Name] = c
			}
		}
		c, ok := inProject[n]
		if !ok {
			warns = append(warns, fmt.Sprintf("channel %s was not found in %s", n, project))
		} else if c.Type == "email" && c.VerificationStatus != "VERIFIED" {
			warns = append(warns, fmt.Sprintf("channel %s is unverified — Google sends a verification email", n))
		}
		out = append(out, n)
	}
	return out, warns, nil
}
