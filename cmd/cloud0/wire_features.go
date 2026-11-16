package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/option"

	"github.com/smallTechOrg/cloud0/internal/alerting"
	"github.com/smallTechOrg/cloud0/internal/alerting/budgets"
	"github.com/smallTechOrg/cloud0/internal/alerting/monclient"
	"github.com/smallTechOrg/cloud0/internal/attribution"
	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/httpapi"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// buildAlerts wires the /alerts and /budgets endpoints. Reads use the reader
// credentials (ADC); every write uses CLOUD0_ALERTS_CREDENTIALS. A client that
// cannot be built is logged and leaves its endpoints answering with the
// classified error; the server always starts.
func buildAlerts(ctx context.Context, cfg config.Config, st *store.Store, logger *slog.Logger, onClose func(func())) *httpapi.AlertsDeps {
	creds := monclient.CredsFromEnv()
	writable := creds.Writer != nil
	fail := func(what string, err error) *httpapi.AlertsDeps {
		logger.Error("alerts."+what, slog.String("error", err.Error()))
		return nil
	}

	policies, err := monclient.NewPolicies(ctx, creds)
	if err != nil {
		return fail("policies_client", err)
	}
	channels, err := monclient.NewChannels(ctx, creds)
	if err != nil {
		return fail("channels_client", err)
	}
	uptime, err := monclient.NewUptimeClient(ctx, creds)
	if err != nil {
		return fail("uptime_client", err)
	}
	snoozes, err := monclient.NewSnoozeClient(ctx, creds)
	if err != nil {
		return fail("snooze_client", err)
	}
	incidents, err := monclient.NewIncidentClient(ctx, creds)
	if err != nil {
		return fail("incidents_client", err)
	}

	var budgetOpts []option.ClientOption
	if p := strings.TrimSpace(os.Getenv(monclient.EnvAlertsCredentials)); p != "" {
		budgetOpts = append(budgetOpts, option.WithCredentialsFile(p))
	}
	quotaProject := ""
	if len(cfg.Projects) > 0 {
		quotaProject = cfg.Projects[0]
	}
	bc, err := budgets.New(ctx, cfg.BillingAccountID, quotaProject, budgetOpts...)
	if err != nil {
		return fail("budgets_client", err)
	}
	bc = bc.WithCurrency(cfg.Currency).WithProjectNames(projectNumbers(ctx, cfg.Projects, logger))

	builder := alerting.NewBuilder()
	router := alerting.NewRouter(st, channels, cfg.Projects)
	engine := alerting.NewEngine(alerting.EngineDeps{
		Builder: builder, Policies: policies, Channels: channels, Uptime: uptime, Snoozes: snoozes,
		Budgets: bc, Router: router, Store: st, Now: time.Now,
	})
	adopter := alerting.NewAdopter(alerting.AdoptDeps{
		Builder: builder, Policies: policies, Budgets: bc, Engine: engine, Router: router,
		Projects: cfg.Projects, Now: time.Now,
	})

	deps := &httpapi.AlertsDeps{
		Writable: writable, Builder: builder, Policies: policies, Channels: channels, Uptime: uptime,
		Incidents: incidents, Snoozes: snoozes, Budgets: bc, Engine: engine, Adopter: adopter, Router: router,
		History: alerting.NewHistoryProvider(incidents, cfg.Projects, func(week string) (time.Time, time.Time, error) {
			w, err := isoweek.Parse(week)
			if err != nil {
				return time.Time{}, time.Time{}, err
			}
			s, e := isoweek.Bounds(w)
			return s, e, nil
		}, time.Now),
		Suggestions: budgets.NewSuggesterWithBudgets(st, "target.monthly_total", time.Now, bc),
	}
	if inv, err := monclient.NewInventory(ctx, cfg.Projects, creds, policies); err != nil {
		logger.Error("alerts.inventory_client", slog.String("error", err.Error()))
	} else {
		deps.Coverage = alerting.NewCoverageProvider(inv)
		onClose(func() { _ = inv.Close() })
	}
	logger.Info("alerts.ready", slog.Bool("writable", writable))
	return deps
}

// projectNumbers maps project numbers to ids (the Budgets API returns numbers).
func projectNumbers(ctx context.Context, projects []string, logger *slog.Logger) map[string]string {
	out := map[string]string{}
	svc, err := cloudresourcemanager.NewService(ctx)
	if err != nil {
		logger.Warn("alerts.project_numbers", slog.String("error", err.Error()))
		return out
	}
	for _, id := range projects {
		p, err := svc.Projects.Get("projects/" + id).Context(ctx).Do()
		if err != nil {
			logger.Warn("alerts.project_number", slog.String("project", id), slog.String("error", err.Error()))
			continue
		}
		out[strings.TrimPrefix(p.Name, "projects/")] = id
	}
	return out
}

// buildAttribution wires the Phase 3 endpoints.
func buildAttribution(ctx context.Context, cfg config.Config, logger *slog.Logger, onClose func(func())) *httpapi.AttributionDeps {
	deps := &httpapi.AttributionDeps{
		NewEngine: attribution.NewEngine,
		Seeder:    attribution.NewSeeder(),
		Allocator: attribution.NewAllocator(),
		Labels:    attribution.NewLabelSuggester(),
		Detector:  attribution.NewDetector(),
		Proposer:  attribution.NoopProposer{},
	}
	if cfg.OpenRouterAPIKey != "" {
		deps.Proposer = attribution.NewOpenRouterProposer(cfg.OpenRouterAPIKey, cfg.LLMModel, nil)
	}
	if inv, closeFn, err := attribution.NewInventory(ctx); err != nil {
		logger.Error("attribution.inventory_client", slog.String("error", err.Error()))
	} else {
		deps.Inventory = inv
		onClose(func() { _ = closeFn() })
	}
	return deps
}
