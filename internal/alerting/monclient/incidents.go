package monclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"google.golang.org/api/option"
	htransport "google.golang.org/api/transport/http"

	"github.com/smallTechOrg/cloud0/internal/alerting"
)

const (
	incMonitoringBase = "https://monitoring.googleapis.com/v3/"
	incReadScope      = "https://www.googleapis.com/auth/monitoring.read"
	incConsoleBase    = "https://console.cloud.google.com/monitoring/alerting/alerts/"
	incidentPageSize  = 100
	incidentMaxPages  = 50
)

// Incidents implements alerting.IncidentClient over the Cloud Monitoring
// `alerts` REST API (projects.alerts.list). It only reads.
type Incidents struct {
	hc   *http.Client
	base string
}

var _ alerting.IncidentClient = (*Incidents)(nil)

// NewIncidentClient builds the client from the READER credentials.
func NewIncidentClient(ctx context.Context, creds Creds) (*Incidents, error) {
	opts := append(append([]option.ClientOption{}, creds.Reader...), option.WithScopes(incReadScope))
	hc, _, err := htransport.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: incident client: %v", alerting.ErrUpstream, err)
	}
	return &Incidents{hc: hc, base: incMonitoringBase}, nil
}

// newIncidentsWith is the test seam (httptest server).
func newIncidentsWith(hc *http.Client, base string) *Incidents {
	return &Incidents{hc: hc, base: strings.TrimRight(base, "/") + "/"}
}

type apiAlert struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	OpenTime  string `json:"openTime"`
	CloseTime string `json:"closeTime"`
	Resource  struct {
		Type   string            `json:"type"`
		Labels map[string]string `json:"labels"`
	} `json:"resource"`
	Metadata struct {
		SystemLabels map[string]any `json:"systemLabels"`
	} `json:"metadata"`
	Metric struct {
		Type string `json:"type"`
	} `json:"metric"`
	Policy struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Severity    string `json:"severity"`
	} `json:"policy"`
}

type incAlertList struct {
	Alerts        []apiAlert `json:"alerts"`
	NextPageToken string     `json:"nextPageToken"`
}

// List returns the project's alerts matching f, newest opened first.
func (c *Incidents) List(ctx context.Context, project string, f alerting.IncidentFilter) ([]alerting.Incident, error) {
	var clauses []string
	switch strings.ToLower(f.State) {
	case "open":
		clauses = append(clauses, `state="OPEN"`)
	case "closed":
		clauses = append(clauses, `state="CLOSED"`)
	}
	if !f.From.IsZero() {
		clauses = append(clauses, fmt.Sprintf(`open_time>="%s"`, f.From.UTC().Format(time.RFC3339)))
	}
	if !f.To.IsZero() {
		// The Monitoring filter language has no strict "<": To is exclusive, so use <= To-1s.
		clauses = append(clauses, fmt.Sprintf(`open_time<="%s"`, f.To.Add(-time.Second).UTC().Format(time.RFC3339)))
	}
	var out []alerting.Incident
	token := ""
	for page := 0; page < incidentMaxPages; page++ {
		q := url.Values{}
		q.Set("pageSize", fmt.Sprint(incidentPageSize))
		q.Set("orderBy", "open_time desc")
		if len(clauses) > 0 {
			q.Set("filter", strings.Join(clauses, " AND "))
		}
		if token != "" {
			q.Set("pageToken", token)
		}
		u := c.base + "projects/" + url.PathEscape(project) + "/alerts?" + q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: list alerts %s: %v", alerting.ErrUpstream, project, err)
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if rerr != nil {
			return nil, fmt.Errorf("%w: read alerts %s: %v", alerting.ErrUpstream, project, rerr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, incMapHTTPError(resp.StatusCode, body, "list alerts "+project)
		}
		var lr incAlertList
		if err := json.Unmarshal(body, &lr); err != nil {
			return nil, fmt.Errorf("%w: decode alerts %s: %v", alerting.ErrUpstream, project, err)
		}
		for _, a := range lr.Alerts {
			inc := convertAlert(project, a)
			if !incMatchesFilter(inc, f) {
				continue
			}
			out = append(out, inc)
		}
		if lr.NextPageToken == "" {
			break
		}
		token = lr.NextPageToken
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OpenedAt.After(out[j].OpenedAt) })
	if out == nil {
		out = []alerting.Incident{}
	}
	return out, nil
}

func incMatchesFilter(i alerting.Incident, f alerting.IncidentFilter) bool {
	if s := strings.ToLower(f.State); s != "" && s != "all" && i.State != s {
		return false
	}
	if !f.From.IsZero() && i.OpenedAt.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && !i.OpenedAt.Before(f.To) {
		return false
	}
	return true
}

func incMapHTTPError(status int, body []byte, what string) error {
	msg := strings.TrimSpace(string(body))
	var e struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	switch {
	case status == http.StatusForbidden && strings.Contains(strings.ToLower(msg), "has not been used"),
		status == http.StatusForbidden && strings.Contains(strings.ToLower(msg), "is disabled"):
		return fmt.Errorf("%w: %s: %s", alerting.ErrAPIDisabled, what, msg)
	case status == http.StatusForbidden, status == http.StatusUnauthorized:
		return fmt.Errorf("%w: %s: %s", alerting.ErrPermissionDenied, what, msg)
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: %s: %s", alerting.ErrNotFound, what, msg)
	}
	return fmt.Errorf("%w: %s: HTTP %d: %s", alerting.ErrUpstream, what, status, msg)
}

func convertAlert(project string, a apiAlert) alerting.Incident {
	id := a.Name
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	inc := alerting.Incident{
		ID:                id,
		Project:           project,
		PolicyName:        a.Policy.Name,
		PolicyDisplayName: a.Policy.DisplayName,
		Severity:          strings.ToUpper(a.Policy.Severity),
		State:             strings.ToLower(a.State),
		ResourceType:      a.Resource.Type,
		ResourceName:      incidentResourceName(a),
		URL:               ConsoleIncidentURL(project, id),
	}
	if inc.State != "open" && inc.State != "closed" {
		if a.CloseTime != "" {
			inc.State = "closed"
		} else {
			inc.State = "open"
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, a.OpenTime); err == nil {
		inc.OpenedAt = t.UTC()
	}
	if a.CloseTime != "" {
		if t, err := time.Parse(time.RFC3339Nano, a.CloseTime); err == nil {
			t = t.UTC()
			inc.ClosedAt = &t
		}
	}
	inc.Summary = inc.PolicyDisplayName
	if a.Metric.Type != "" {
		inc.Summary += " (" + a.Metric.Type + ")"
	}
	return inc
}

// ConsoleIncidentURL builds the GCP console link at request time.
func ConsoleIncidentURL(project, id string) string {
	return incConsoleBase + url.PathEscape(id) + "?project=" + url.QueryEscape(project)
}

func incidentResourceName(a apiAlert) string {
	if v, ok := a.Metadata.SystemLabels["name"].(string); ok && v != "" {
		return v
	}
	l := a.Resource.Labels
	for _, k := range []string{"service_name", "database_id", "instance_name", "instance_id", "url_map_name", "host", "check_id"} {
		if v := l[k]; v != "" {
			if k == "database_id" {
				if i := strings.Index(v, ":"); i >= 0 {
					v = v[i+1:]
				}
			}
			return v
		}
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		if k != "project_id" && k != "location" && k != "zone" && k != "region" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return l[keys[0]]
	}
	return ""
}
