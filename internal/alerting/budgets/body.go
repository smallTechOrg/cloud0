// Package budgets implements alerting.BudgetClient over the Cloud Billing
// Budgets REST API (https://billingbudgets.googleapis.com/v1) plus the
// service-id lookup and the amount suggester.
package budgets

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/smallTechOrg/cloud0/internal/alerting"
)

// MaxChannels is the Budgets API limit of monitoring channels per budget.
const MaxChannels = 5

// DefaultCurrency is used when the client is not told otherwise.
const DefaultCurrency = "INR"

// Spend bases as stored in alerting.BudgetThreshold.Basis.
const (
	BasisCurrent  = "current_spend"
	BasisForecast = "forecasted_spend"
)

// DefaultThresholds are used when a draft has none: 50/80/100 % of current
// spend plus 100 % of forecasted spend.
func DefaultThresholds() []alerting.BudgetThreshold {
	return []alerting.BudgetThreshold{
		{Percent: 50, Basis: BasisCurrent},
		{Percent: 80, Basis: BasisCurrent},
		{Percent: 100, Basis: BasisCurrent},
		{Percent: 100, Basis: BasisForecast},
	}
}

// Credits treatment: INCLUDE_ALL_CREDITS means credits are subtracted from
// the gross cost before comparing with thresholds, i.e. the budget tracks
// NET cost, the same figure cloud0 shows everywhere (net of credits).
const creditsTreatment = "INCLUDE_ALL_CREDITS"

type restBudget struct {
	DisplayName       string            `json:"displayName"`
	Amount            restAmount        `json:"amount"`
	BudgetFilter      restFilter        `json:"budgetFilter"`
	ThresholdRules    []restThreshold   `json:"thresholdRules"`
	NotificationsRule restNotifications `json:"notificationsRule"`
}

type restAmount struct {
	SpecifiedAmount *restMoney `json:"specifiedAmount,omitempty"`
}

type restMoney struct {
	CurrencyCode string `json:"currencyCode"`
	Units        string `json:"units"`
	Nanos        int32  `json:"nanos,omitempty"`
}

type restFilter struct {
	Projects             []string `json:"projects,omitempty"`
	Services             []string `json:"services,omitempty"`
	CalendarPeriod       string   `json:"calendarPeriod"`
	CreditTypesTreatment string   `json:"creditTypesTreatment"`
}

type restThreshold struct {
	ThresholdPercent float64 `json:"thresholdPercent"`
	SpendBasis       string  `json:"spendBasis"`
}

type restNotifications struct {
	MonitoringNotificationChannels []string `json:"monitoringNotificationChannels,omitempty"`
	DisableDefaultIamRecipients    bool     `json:"disableDefaultIamRecipients"`
	SchemaVersion                  string   `json:"schemaVersion"`
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", alerting.ErrInvalidParams, fmt.Sprintf(format, a...))
}

// Body renders the REST budget JSON for a draft. Pure: no I/O.
func Body(d alerting.BudgetDraft, channelNames []string, currency string) (json.RawMessage, error) {
	if currency == "" {
		currency = DefaultCurrency
	}
	name := strings.TrimSpace(d.DisplayName)
	if name == "" {
		return nil, invalid("budget display_name is required")
	}
	if len(name) > 60 {
		return nil, invalid("budget display_name is longer than 60 characters")
	}
	if math.IsNaN(d.Amount) || math.IsInf(d.Amount, 0) || d.Amount <= 0 {
		return nil, invalid("budget amount must be > 0")
	}
	if len(channelNames) > MaxChannels {
		return nil, invalid("a budget supports at most %d notification channels, got %d", MaxChannels, len(channelNames))
	}
	for _, c := range channelNames {
		if !strings.HasPrefix(c, "projects/") || !strings.Contains(c, "/notificationChannels/") {
			return nil, invalid("notification channel %q is not a projects/<p>/notificationChannels/<id> name", c)
		}
	}
	disableIAM := d.DefaultIAMRecipients != nil && !*d.DefaultIAMRecipients
	if disableIAM && len(channelNames) == 0 {
		return nil, invalid("default IAM recipients cannot be disabled without at least one notification channel")
	}

	units := int64(math.Floor(d.Amount))
	nanos := int32(math.Round((d.Amount - float64(units)) * 1e9))
	if nanos >= 1e9 {
		units++
		nanos = 0
	}

	var projects []string
	for _, p := range d.Projects {
		p = strings.TrimPrefix(strings.TrimSpace(p), "projects/")
		if p == "" {
			return nil, invalid("empty project in budget filter")
		}
		projects = append(projects, "projects/"+p)
	}
	var services []string
	for _, s := range d.ServiceIDs {
		s = strings.TrimPrefix(strings.TrimSpace(s), "services/")
		if s == "" {
			return nil, invalid("empty service id in budget filter")
		}
		services = append(services, "services/"+s)
	}

	th := d.Thresholds
	if len(th) == 0 {
		th = DefaultThresholds()
	}
	rules := make([]restThreshold, 0, len(th))
	for _, t := range th {
		if math.IsNaN(t.Percent) || t.Percent <= 0 || t.Percent > 1000 {
			return nil, invalid("threshold percent %v out of range (0, 1000]", t.Percent)
		}
		var basis string
		switch t.Basis {
		case BasisCurrent, "":
			basis = "CURRENT_SPEND"
		case BasisForecast:
			basis = "FORECASTED_SPEND"
		default:
			return nil, invalid("threshold basis %q must be current_spend or forecasted_spend", t.Basis)
		}
		rules = append(rules, restThreshold{ThresholdPercent: t.Percent / 100, SpendBasis: basis})
	}

	b := restBudget{
		DisplayName: name,
		Amount:      restAmount{SpecifiedAmount: &restMoney{CurrencyCode: currency, Units: strconv.FormatInt(units, 10), Nanos: nanos}},
		BudgetFilter: restFilter{
			Projects: projects, Services: services,
			CalendarPeriod: "MONTH", CreditTypesTreatment: creditsTreatment,
		},
		ThresholdRules: rules,
		NotificationsRule: restNotifications{
			MonitoringNotificationChannels: channelNames,
			DisableDefaultIamRecipients:    disableIAM,
			SchemaVersion:                  "1.0",
		},
	}
	out, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("budgets: render body: %w", err)
	}
	return out, nil
}
