package httpapi

import (
	"net/http"
	"time"

	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

// ConfigResponse is the body of GET /api/v1/config.
type ConfigResponse struct {
	Projects    []string  `json:"projects"`
	FirstWeek   string    `json:"first_week"`
	CurrentWeek string    `json:"current_week"`
	DefaultWeek string    `json:"default_week"`
	SettleHours int       `json:"settle_hours"`
	Currency    string    `json:"currency"`
	Now         time.Time `json:"now"`
}

// defaultWeek is the last complete week, never before first_week.
func defaultWeek(first, current isoweek.Week) isoweek.Week {
	def := current.Prev()
	if isoweek.Compare(def, first) < 0 {
		return first
	}
	return def
}

func (d Deps) handleConfig(w http.ResponseWriter, _ *http.Request) *APIError {
	now := d.now()
	current := isoweek.Current(now)
	projects := d.Config.Projects
	if projects == nil {
		projects = []string{}
	}
	writeJSON(w, http.StatusOK, ConfigResponse{
		Projects:    projects,
		FirstWeek:   d.Config.FirstWeek.String(),
		CurrentWeek: current.String(),
		DefaultWeek: defaultWeek(d.Config.FirstWeek, current).String(),
		SettleHours: d.Config.SettleHours,
		Currency:    currencyOrDefault(d.Config.Currency),
		Now:         now.Truncate(time.Second),
	})
	return nil
}

// currencyOrDefault keeps hand-built Configs (tests) reporting a valid code.
func currencyOrDefault(c string) string {
	if c == "" {
		return config.DefaultCurrency
	}
	return c
}
