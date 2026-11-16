package alerting

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ProjectFailure is one project whose incidents could not be read.
type ProjectFailure struct {
	Project string
	Err     error
}

// History computes weekly alert history from an IncidentClient.
type History struct {
	incidents  IncidentClient
	projects   []string
	weekWindow func(week string) (start, end time.Time, err error)
	now        func() time.Time
}

var _ HistoryProvider = (*History)(nil)

const maxHistoryResources = 5

// NewHistoryProvider returns the HistoryProvider. weekWindow resolves an ISO
// week id to [start, end); now is used only to bound open durations.
func NewHistoryProvider(incidents IncidentClient, projects []string,
	weekWindow func(week string) (start, end time.Time, err error), now func() time.Time) *History {
	if now == nil {
		now = time.Now
	}
	return &History{incidents: incidents, projects: projects, weekWindow: weekWindow, now: now}
}

// History implements HistoryProvider. A failing project fails the call (the
// rows of the healthy projects are still returned alongside the error); use
// Run to get the per-project failures separately.
func (h *History) History(ctx context.Context, week string) ([]HistoryRow, error) {
	rows, failed, err := h.Run(ctx, week)
	if err != nil {
		return nil, err
	}
	if len(failed) > 0 {
		return rows, fmt.Errorf("%w: incidents unavailable for %d project(s): %v", ErrUpstream, len(failed), failed[0].Err)
	}
	return rows, nil
}

// Run returns the rows plus per-project failures. err is set only when the week
// is invalid (wraps ErrInvalidParams).
func (h *History) Run(ctx context.Context, week string) ([]HistoryRow, []ProjectFailure, error) {
	start, end, err := h.weekWindow(week)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: week %q: %v", ErrInvalidParams, week, err)
	}
	var rows []HistoryRow
	var failed []ProjectFailure
	for _, p := range h.projects {
		incs, err := h.incidents.List(ctx, p, IncidentFilter{From: start, To: end})
		if err != nil {
			failed = append(failed, ProjectFailure{Project: p, Err: err})
			continue
		}
		rows = append(rows, aggregateHistory(p, incs, start, end)...)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Opened != rows[j].Opened {
			return rows[i].Opened > rows[j].Opened
		}
		return strings.ToLower(rows[i].PolicyDisplayName) < strings.ToLower(rows[j].PolicyDisplayName)
	})
	if rows == nil {
		rows = []HistoryRow{}
	}
	return rows, failed, nil
}

func severityRank(s string) int {
	switch strings.ToUpper(s) {
	case SevCritical:
		return 3
	case SevError:
		return 2
	case SevWarning:
		return 1
	}
	return 0
}

func aggregateHistory(project string, incs []Incident, start, end time.Time) []HistoryRow {
	byPolicy := map[string]*HistoryRow{}
	seenRes := map[string]map[string]bool{}
	var order []string
	for _, in := range incs {
		if in.OpenedAt.Before(start) || !in.OpenedAt.Before(end) {
			continue
		}
		key := in.PolicyName
		if key == "" {
			key = "display:" + in.PolicyDisplayName
		}
		r := byPolicy[key]
		if r == nil {
			r = &HistoryRow{
				Project: project, PolicyName: in.PolicyName, PolicyDisplayName: in.PolicyDisplayName,
				Resources: []string{}, FirstOpenedAt: in.OpenedAt, LastOpenedAt: in.OpenedAt,
			}
			byPolicy[key] = r
			seenRes[key] = map[string]bool{}
			order = append(order, key)
		}
		r.Opened++
		if in.State == "open" {
			r.StillOpen++
		}
		if severityRank(in.Severity) > severityRank(r.Severity) {
			r.Severity = strings.ToUpper(in.Severity)
		}
		if in.OpenedAt.Before(r.FirstOpenedAt) {
			r.FirstOpenedAt = in.OpenedAt
		}
		if in.OpenedAt.After(r.LastOpenedAt) {
			r.LastOpenedAt = in.OpenedAt
		}
		if in.ResourceName != "" && !seenRes[key][in.ResourceName] && len(r.Resources) < maxHistoryResources {
			seenRes[key][in.ResourceName] = true
			r.Resources = append(r.Resources, in.ResourceName)
		}
	}
	out := make([]HistoryRow, 0, len(order))
	for _, k := range order {
		r := byPolicy[k]
		sort.Strings(r.Resources)
		out = append(out, *r)
	}
	return out
}
