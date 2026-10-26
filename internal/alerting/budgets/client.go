package budgets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	htransport "google.golang.org/api/transport/http"

	"github.com/smallTechOrg/cloud0/internal/alerting"
)

const (
	defaultBase = "https://billingbudgets.googleapis.com/v1"
	scope       = "https://www.googleapis.com/auth/cloud-platform"
)

var (
	accountPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	namePattern    = regexp.MustCompile(`^billingAccounts/([A-Za-z0-9-]+)/budgets/[A-Za-z0-9-]+$`)
)

// Client implements alerting.BudgetClient. Create it with New.
type Client struct {
	account  string
	currency string
	base     string
	quota    string            // sent as x-goog-user-project
	names    map[string]string // project number -> project id
	hc       *http.Client      // nil = not configured
}

var _ alerting.BudgetClient = (*Client)(nil)

// New builds a client for one billing account. quotaProject is sent as the
// user/quota project (the Budgets API requires one). With no options the
// client is "not configured": List/Get/Create/Update/Delete return
// alerting.ErrNotConfigured, Body still works.
func New(ctx context.Context, billingAccountID, quotaProject string, opts ...option.ClientOption) (*Client, error) {
	if !accountPattern.MatchString(billingAccountID) {
		return nil, invalid("billing account id %q is not valid", billingAccountID)
	}
	c := &Client{account: billingAccountID, currency: DefaultCurrency, base: defaultBase, quota: quotaProject}
	if len(opts) == 0 {
		return c, nil
	}
	all := append([]option.ClientOption{option.WithScopes(scope)}, opts...)
	hc, _, err := htransport.NewClient(ctx, all...)
	if err != nil {
		return nil, fmt.Errorf("budgets: http client: %w", err)
	}
	c.hc = hc
	return c, nil
}

// WithCurrency sets the currency code used by Body (config.Currency).
func (c *Client) WithCurrency(code string) *Client {
	if code != "" {
		c.currency = strings.ToUpper(code)
	}
	return c
}

// WithProjectNames tells the client how to turn the project numbers the
// Budgets API returns into project ids (number -> id). Unmapped numbers are
// shown as returned. Body accepts either form on input.
func (c *Client) WithProjectNames(numberToID map[string]string) *Client {
	c.names = numberToID
	return c
}

func (c *Client) view(raw json.RawMessage) (alerting.BudgetView, error) {
	v, err := ViewFromJSON(raw)
	for i, p := range v.Projects {
		if id, ok := c.names[p]; ok {
			v.Projects[i] = id
		}
	}
	return v, err
}

// Body implements alerting.BudgetClient.
func (c *Client) Body(d alerting.BudgetDraft, channelNames []string) (json.RawMessage, error) {
	return Body(d, channelNames, c.currency)
}

func (c *Client) checkName(name string) error {
	m := namePattern.FindStringSubmatch(name)
	if m == nil || m[1] != c.account {
		return invalid("budget name %q is not a budget of this billing account", name)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body []byte, out any) error {
	if c.hc == nil {
		return alerting.ErrNotConfigured
	}
	u := c.base + "/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return fmt.Errorf("budgets: request: %w", err)
	}
	if c.quota != "" {
		req.Header.Set("X-Goog-User-Project", c.quota)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			return fmt.Errorf("%w: token: %v", alerting.ErrUpstream, re.ErrorCode)
		}
		return fmt.Errorf("%w: budgets %s: %v", alerting.ErrUpstream, method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("%w: read budgets response: %v", alerting.ErrUpstream, err)
	}
	if resp.StatusCode/100 != 2 {
		return classify(resp.StatusCode, data)
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%w: decode budgets response: %v", alerting.ErrUpstream, err)
		}
	}
	return nil
}

func classify(status int, data []byte) error {
	msg := strings.TrimSpace(string(data))
	var env struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	disabled := false
	if json.Unmarshal(data, &env) == nil && env.Error.Message != "" {
		msg = env.Error.Message
		for _, d := range env.Error.Details {
			if d.Reason == "SERVICE_DISABLED" {
				disabled = true
			}
		}
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	switch {
	case disabled:
		return fmt.Errorf("%w: billingbudgets.googleapis.com: %s", alerting.ErrAPIDisabled, msg)
	case status == http.StatusForbidden || status == http.StatusUnauthorized:
		return fmt.Errorf("%w: %s", alerting.ErrPermissionDenied, msg)
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: %s", alerting.ErrNotFound, msg)
	case status == http.StatusBadRequest:
		return fmt.Errorf("%w: %s", alerting.ErrInvalidParams, msg)
	}
	return fmt.Errorf("%w: budgets API HTTP %d: %s", alerting.ErrUpstream, status, msg)
}

// List returns every budget of the billing account.
func (c *Client) List(ctx context.Context) ([]alerting.BudgetView, error) {
	out := []alerting.BudgetView{}
	token := ""
	for page := 0; page < 50; page++ {
		q := url.Values{"pageSize": {"100"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var resp struct {
			Budgets       []json.RawMessage `json:"budgets"`
			NextPageToken string            `json:"nextPageToken"`
		}
		if err := c.do(ctx, http.MethodGet, "billingAccounts/"+c.account+"/budgets", q, nil, &resp); err != nil {
			return nil, err
		}
		for _, raw := range resp.Budgets {
			v, err := c.view(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	return out, nil
}

// Get returns one budget by full name.
func (c *Client) Get(ctx context.Context, name string) (alerting.BudgetView, error) {
	if err := c.checkName(name); err != nil {
		return alerting.BudgetView{}, err
	}
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, name, nil, nil, &raw); err != nil {
		return alerting.BudgetView{}, err
	}
	return c.view(raw)
}

// Create makes a new managed budget (display name must carry the cloud0- prefix).
func (c *Client) Create(ctx context.Context, d alerting.BudgetDraft, channelNames []string) (alerting.BudgetView, error) {
	if !strings.HasPrefix(d.DisplayName, alerting.BudgetNamePrefix) {
		return alerting.BudgetView{}, invalid("managed budget display_name must start with %q", alerting.BudgetNamePrefix)
	}
	body, err := c.Body(d, channelNames)
	if err != nil {
		return alerting.BudgetView{}, err
	}
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodPost, "billingAccounts/"+c.account+"/budgets", nil, body, &raw); err != nil {
		return alerting.BudgetView{}, err
	}
	return c.view(raw)
}

// updateMask lists the fields Body renders, so an update replaces them all.
const updateMask = "displayName,amount,budgetFilter,thresholdRules,notificationsRule"

// Update replaces display name, amount, filter, thresholds and notifications.
func (c *Client) Update(ctx context.Context, name string, d alerting.BudgetDraft, channelNames []string) (alerting.BudgetView, error) {
	if err := c.checkName(name); err != nil {
		return alerting.BudgetView{}, err
	}
	body, err := c.Body(d, channelNames)
	if err != nil {
		return alerting.BudgetView{}, err
	}
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodPatch, name, url.Values{"updateMask": {updateMask}}, body, &raw); err != nil {
		return alerting.BudgetView{}, err
	}
	return c.view(raw)
}

// Delete removes a budget by full name.
func (c *Client) Delete(ctx context.Context, name string) error {
	if err := c.checkName(name); err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, name, nil, nil, nil)
}

// ViewFromJSON converts a REST budget into the wire view.
func ViewFromJSON(raw json.RawMessage) (alerting.BudgetView, error) {
	var b struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Amount      struct {
			SpecifiedAmount  *restMoney      `json:"specifiedAmount"`
			LastPeriodAmount json.RawMessage `json:"lastPeriodAmount"`
		} `json:"amount"`
		BudgetFilter struct {
			Projects []string `json:"projects"`
			Services []string `json:"services"`
		} `json:"budgetFilter"`
		ThresholdRules []struct {
			ThresholdPercent float64 `json:"thresholdPercent"`
			SpendBasis       string  `json:"spendBasis"`
		} `json:"thresholdRules"`
		NotificationsRule struct {
			Channels   []string `json:"monitoringNotificationChannels"`
			DisableIAM bool     `json:"disableDefaultIamRecipients"`
		} `json:"notificationsRule"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return alerting.BudgetView{}, fmt.Errorf("%w: decode budget: %v", alerting.ErrUpstream, err)
	}
	v := alerting.BudgetView{
		Name:                 b.Name,
		DisplayName:          b.DisplayName,
		Managed:              strings.HasPrefix(b.DisplayName, alerting.BudgetNamePrefix),
		AmountKind:           "specified",
		Projects:             []string{},
		ServiceIDs:           []string{},
		Thresholds:           []alerting.BudgetThreshold{},
		ChannelNames:         []string{},
		DefaultIAMRecipients: !b.NotificationsRule.DisableIAM,
		Warnings:             []string{},
		RawJSON:              append(json.RawMessage(nil), raw...),
	}
	if m := b.Amount.SpecifiedAmount; m != nil {
		units, _ := strconv.ParseInt(m.Units, 10, 64)
		amt := float64(units) + float64(m.Nanos)/1e9
		v.Amount, v.Currency = &amt, m.CurrencyCode
	} else {
		v.AmountKind = "last_period"
	}
	for _, p := range b.BudgetFilter.Projects {
		v.Projects = append(v.Projects, strings.TrimPrefix(p, "projects/"))
	}
	v.ServiceIDs = append(v.ServiceIDs, b.BudgetFilter.Services...)
	for _, r := range b.ThresholdRules {
		basis := BasisCurrent
		if r.SpendBasis == "FORECASTED_SPEND" {
			basis = BasisForecast
		}
		v.Thresholds = append(v.Thresholds, alerting.BudgetThreshold{Percent: roundPct(r.ThresholdPercent * 100), Basis: basis})
	}
	v.ChannelNames = append(v.ChannelNames, b.NotificationsRule.Channels...)
	if len(v.ChannelNames) == 0 {
		v.Warnings = append(v.Warnings, "No monitoring notification channels: alerts go to billing admins by email only.")
	}
	if !v.Managed {
		v.AdoptShape = alerting.ShapeBudgetBoilerplate
	}
	return v, nil
}

func roundPct(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }
