package budgets

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/smallTechOrg/cloud0/internal/alerting"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

const (
	// Multiplier is the head-room over the observed average.
	Multiplier = 1.2
	// WeeksWanted is how many complete weeks are averaged at most.
	WeeksWanted = 4
	// MinServiceMonthly is the monthly net (before the multiplier) under which
	// a service gets no suggestion.
	MinServiceMonthly = 50.0
	// RoundStep is the rounding step of suggested amounts.
	RoundStep = 100.0
)

// SuggestedServices are the services that get a per-service suggestion; the
// first alias is the display name, the others match snapshot service names.
var SuggestedServices = [][]string{
	{"Compute Engine"},
	{"Networking"},
	{"Cloud SQL"},
	{"Cloud Run"},
	{"Gemini API", "Generative Language API"},
}

// Suggester implements alerting.SuggestionProvider.
type Suggester struct {
	st        *store.Store
	targetKey string
	now       func() time.Time
	budgets   alerting.BudgetClient
}

var _ alerting.SuggestionProvider = (*Suggester)(nil)

// NewSuggester builds the provider. targetKey is the setting holding the
// monthly total target (store.KeyMonthlyTarget, "target.monthly_total").
func NewSuggester(st *store.Store, targetKey string, now func() time.Time) alerting.SuggestionProvider {
	return newSuggester(st, targetKey, now, nil)
}

// NewSuggesterWithBudgets also marks suggestions whose scope already has a
// managed budget (Exists).
func NewSuggesterWithBudgets(st *store.Store, targetKey string, now func() time.Time, bc alerting.BudgetClient) alerting.SuggestionProvider {
	return newSuggester(st, targetKey, now, bc)
}

func newSuggester(st *store.Store, targetKey string, now func() time.Time, bc alerting.BudgetClient) *Suggester {
	if now == nil {
		now = time.Now
	}
	return &Suggester{st: st, targetKey: targetKey, now: now, budgets: bc}
}

// roundStep rounds to the nearest step, never below one step for a positive value.
func roundStep(v float64) float64 {
	if v <= 0 {
		return 0
	}
	r := math.Round(v/RoundStep) * RoundStep
	if r < RoundStep {
		r = RoundStep
	}
	return r
}

// Suggestions computes the account suggestion followed by the service ones.
func (s *Suggester) Suggestions(ctx context.Context) ([]alerting.Suggestion, error) {
	last := isoweek.LastComplete(s.now())
	first := last
	for i := 1; i < WeeksWanted; i++ {
		first = first.Prev()
	}
	snaps, err := store.LatestPerWeek(ctx, s.st.DB, first, last)
	if err != nil {
		return nil, err
	}
	weeks := make([]isoweek.Week, 0, len(snaps))
	ids := make([]int64, 0, len(snaps))
	for w := range snaps {
		weeks = append(weeks, w)
	}
	sort.Slice(weeks, func(i, j int) bool { return isoweek.Compare(weeks[i], weeks[j]) < 0 })
	for _, w := range weeks {
		ids = append(ids, snaps[w].ID)
	}
	rows, err := store.RowsForSnapshots(ctx, s.st.DB, ids, store.ScopeAll)
	if err != nil {
		return nil, err
	}

	var accountNet float64
	type svcAgg struct {
		id, name string
		net      float64
	}
	svc := map[string]*svcAgg{}
	for _, w := range weeks {
		for _, r := range rows[snaps[w].ID] {
			accountNet += r.Net
			for _, aliases := range SuggestedServices {
				if matchService(r.Service, aliases) {
					a := svc[aliases[0]]
					if a == nil {
						a = &svcAgg{name: aliases[0]}
						svc[aliases[0]] = a
					}
					a.net += r.Net
					if a.id == "" {
						a.id = NormalizeServiceID(r.ServiceID)
					}
				}
			}
		}
	}
	n := len(weeks)

	existing := map[string]bool{}
	if s.budgets != nil {
		bl, err := s.budgets.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, b := range bl {
			if b.Managed {
				existing[scopeKey(b.ServiceIDs)] = true
			}
		}
	}

	out := []alerting.Suggestion{s.account(ctx, accountNet, n, existing)}
	for _, aliases := range SuggestedServices {
		a := svc[aliases[0]]
		if a == nil || a.id == "" || n == 0 {
			continue
		}
		avg := a.net / float64(n)
		if avg*30/7 < MinServiceMonthly {
			continue
		}
		amount := roundStep(Multiplier * avg * 30 / 7)
		out = append(out, alerting.Suggestion{
			Scope: "service", ServiceID: a.id, ServiceName: a.name, Amount: amount,
			Basis:  basis("snapshots_avg", n, avg),
			Draft:  draft("cloud0-"+slug(a.name)+"-monthly", amount, []string{a.id}),
			Exists: existing[scopeKey([]string{a.id})],
		})
	}
	return out, nil
}

func (s *Suggester) account(ctx context.Context, net float64, n int, existing map[string]bool) alerting.Suggestion {
	avg := 0.0
	if n > 0 {
		avg = net / float64(n)
	}
	sg := alerting.Suggestion{Scope: "account", ServiceName: "All services", Exists: existing[scopeKey(nil)]}
	if target := s.target(ctx); target != nil && *target > 0 {
		sg.Amount = *target
		sg.Basis = alerting.SuggestionBasis{
			Method: "monthly_total_setting", WeeksUsed: n, AvgWeekly: avg, Multiplier: 1,
			Explanation: fmt.Sprintf("Monthly target setting (%s) = %.2f", s.targetKey, *target),
		}
	} else if n == 0 {
		sg.Basis = alerting.SuggestionBasis{
			Method: "none", Multiplier: Multiplier,
			Explanation: "No complete week has been stored yet: refresh the weekly cost review first, or set a monthly target.",
		}
	} else {
		sg.Amount = roundStep(Multiplier * avg * 30 / 7)
		sg.Basis = basis("snapshots_avg", n, avg)
	}
	sg.Draft = draft("cloud0-account-monthly", sg.Amount, nil)
	return sg
}

func (s *Suggester) target(ctx context.Context) *float64 {
	m, err := store.GetSettingsWithPrefix(ctx, s.st.DB, s.targetKey)
	if err != nil {
		return nil
	}
	raw := strings.TrimSpace(m[s.targetKey])
	f, err := strconv.ParseFloat(raw, 64)
	if raw == "" || err != nil {
		return nil
	}
	return &f
}

func basis(method string, n int, avg float64) alerting.SuggestionBasis {
	exp := fmt.Sprintf("%.1f × average weekly net cost of the last %d complete week(s) (%.2f) × 30/7", Multiplier, n, avg)
	if n < WeeksWanted {
		exp += fmt.Sprintf("; only %d of %d weeks have stored snapshots, so the estimate is less reliable", n, WeeksWanted)
	}
	return alerting.SuggestionBasis{Method: method, WeeksUsed: n, AvgWeekly: avg, Multiplier: Multiplier, Explanation: exp}
}

func draft(name string, amount float64, services []string) alerting.BudgetDraft {
	return alerting.BudgetDraft{
		DisplayName: name, Amount: amount, Projects: []string{}, ServiceIDs: append([]string{}, services...),
		Thresholds: DefaultThresholds(), Severity: "ERROR",
	}
}

func matchService(name string, aliases []string) bool {
	for _, a := range aliases {
		if strings.EqualFold(strings.TrimSpace(name), a) {
			return true
		}
	}
	return false
}

func scopeKey(serviceIDs []string) string {
	c := make([]string, 0, len(serviceIDs))
	for _, id := range serviceIDs {
		c = append(c, NormalizeServiceID(id))
	}
	sort.Strings(c)
	return strings.Join(c, ",")
}

func slug(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), "-")
}
