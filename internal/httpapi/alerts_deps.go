package httpapi

import "github.com/smallTechOrg/cloud0/internal/alerting"

// AlertsDeps is everything the /alerts and /budgets handlers need. main builds
// it with the reader client always and the writer client when
// CLOUD0_ALERTS_CREDENTIALS is set (Writable). Nil Alerts means the endpoints
// answer 503 alerts_not_configured.
type AlertsDeps struct {
	Writable    bool
	Builder     alerting.Builder
	Policies    alerting.PolicyClient
	Channels    alerting.ChannelClient
	Uptime      alerting.UptimeClient
	Incidents   alerting.IncidentClient
	Snoozes     alerting.SnoozeClient
	Budgets     alerting.BudgetClient
	Engine      alerting.Engine
	Adopter     alerting.Adopter
	Router      alerting.Router
	Coverage    alerting.CoverageProvider
	History     alerting.HistoryProvider
	Suggestions alerting.SuggestionProvider
}
