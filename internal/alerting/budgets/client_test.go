package budgets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/api/option"

	"github.com/smallTechOrg/cloud0/internal/alerting"
)

func TestClient_NotConfiguredWithoutOptions(t *testing.T) {
	c, err := New(context.Background(), "ABCDEF-123456-ABCDEF", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d := alerting.BudgetDraft{DisplayName: "cloud0-x", Amount: 10}
	name := "billingAccounts/ABCDEF-123456-ABCDEF/budgets/u1"
	if _, err := c.List(ctx); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Errorf("List: %v", err)
	}
	if _, err := c.Get(ctx, name); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Errorf("Get: %v", err)
	}
	if _, err := c.Create(ctx, d, nil); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Errorf("Create: %v", err)
	}
	if _, err := c.Update(ctx, name, d, nil); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Errorf("Update: %v", err)
	}
	if err := c.Delete(ctx, name); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Errorf("Delete: %v", err)
	}
	if _, err := c.Body(d, nil); err != nil {
		t.Errorf("Body must work unconfigured: %v", err)
	}
}

func TestClient_RejectsForeignAndMalformedNames(t *testing.T) {
	c, _ := New(context.Background(), "ABCDEF-123456-ABCDEF", "proj-a")
	for _, n := range []string{"", "billingAccounts/OTHER/budgets/u1", "billingAccounts/ABCDEF-123456-ABCDEF/budgets/../x", "projects/p/budgets/u"} {
		if err := c.Delete(context.Background(), n); !errors.Is(err, alerting.ErrInvalidParams) {
			t.Errorf("Delete(%q) = %v", n, err)
		}
	}
	if _, err := c.Create(context.Background(), alerting.BudgetDraft{DisplayName: "no-prefix", Amount: 1}, nil); !errors.Is(err, alerting.ErrInvalidParams) {
		t.Errorf("Create without prefix: %v", err)
	}
	if _, err := New(context.Background(), "bad/id", ""); err == nil {
		t.Error("bad account id accepted")
	}
}

func fake(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(context.Background(), "ACC-1", "proj-a", option.WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	c.base = srv.URL
	return c
}

const sampleBudget = `{"name":"billingAccounts/ACC-1/budgets/u1","displayName":"Example-budget",
"amount":{"specifiedAmount":{"currencyCode":"INR","units":"5000"}},
"budgetFilter":{"projects":["projects/123"],"services":["services/AAAA-BBBB-CCCC"],"calendarPeriod":"MONTH"},
"thresholdRules":[{"thresholdPercent":0.5},{"thresholdPercent":0.9,"spendBasis":"CURRENT_SPEND"},{"thresholdPercent":1,"spendBasis":"FORECASTED_SPEND"}]}`

func TestClient_ListPaginatesAndMapsView(t *testing.T) {
	var calls int
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/billingAccounts/ACC-1/budgets" || r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
		if r.URL.Query().Get("pageToken") == "" {
			io.WriteString(w, `{"budgets":[`+sampleBudget+`],"nextPageToken":"p2"}`)
			return
		}
		io.WriteString(w, `{"budgets":[{"name":"billingAccounts/ACC-1/budgets/u2","displayName":"cloud0-x","amount":{"lastPeriodAmount":{}},"notificationsRule":{"disableDefaultIamRecipients":true,"monitoringNotificationChannels":["projects/p/notificationChannels/1"]}}]}`)
	})
	got, err := c.List(context.Background())
	if err != nil || calls != 2 || len(got) != 2 {
		t.Fatalf("List = %v %d %v", got, calls, err)
	}
	v := got[0]
	if v.Managed || v.AdoptShape != alerting.ShapeBudgetBoilerplate || v.Amount == nil || *v.Amount != 5000 || v.Currency != "INR" ||
		len(v.Projects) != 1 || v.Projects[0] != "123" || len(v.ServiceIDs) != 1 || !v.DefaultIAMRecipients || len(v.Warnings) != 1 {
		t.Errorf("view0 = %+v", v)
	}
	if len(v.Thresholds) != 3 || v.Thresholds[0].Percent != 50 || v.Thresholds[0].Basis != BasisCurrent || v.Thresholds[2].Basis != BasisForecast || v.Thresholds[1].Percent != 90 {
		t.Errorf("thresholds = %+v", v.Thresholds)
	}
	w := got[1]
	if !w.Managed || w.AmountKind != "last_period" || w.Amount != nil || w.DefaultIAMRecipients || len(w.ChannelNames) != 1 || len(w.Warnings) != 0 {
		t.Errorf("view1 = %+v", w)
	}
}

func TestClient_CreateUpdateDeleteRequests(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.Method {
		case http.MethodPost:
			if r.URL.Path != "/billingAccounts/ACC-1/budgets" || !json.Valid(b) {
				t.Errorf("create: %s %s", r.URL, b)
			}
			io.WriteString(w, sampleBudget)
		case http.MethodPatch:
			if r.URL.Path != "/billingAccounts/ACC-1/budgets/u1" || r.URL.Query().Get("updateMask") != updateMask {
				t.Errorf("update: %s", r.URL)
			}
			io.WriteString(w, sampleBudget)
		case http.MethodDelete:
			io.WriteString(w, `{}`)
		}
	})
	ctx := context.Background()
	d := alerting.BudgetDraft{DisplayName: "cloud0-test-x", Amount: 100}
	if _, err := c.Create(ctx, d, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Update(ctx, "billingAccounts/ACC-1/budgets/u1", d, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "billingAccounts/ACC-1/budgets/u1"); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ErrorClassification(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{403, `{"error":{"message":"denied"}}`, alerting.ErrPermissionDenied},
		{403, `{"error":{"message":"off","details":[{"reason":"SERVICE_DISABLED"}]}}`, alerting.ErrAPIDisabled},
		{404, `{"error":{"message":"gone"}}`, alerting.ErrNotFound},
		{400, `{"error":{"message":"bad"}}`, alerting.ErrInvalidParams},
		{500, `oops`, alerting.ErrUpstream},
	}
	for _, tc := range cases {
		c := fake(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		})
		if _, err := c.Get(context.Background(), "billingAccounts/ACC-1/budgets/u1"); !errors.Is(err, tc.want) {
			t.Errorf("status %d: %v, want %v", tc.status, err, tc.want)
		}
	}
}

func TestClient_ProjectNumbersMappedToIDs(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, sampleBudget) })
	c.WithProjectNames(map[string]string{"123": "proj-a"})
	v, err := c.Get(context.Background(), "billingAccounts/ACC-1/budgets/u1")
	if err != nil || len(v.Projects) != 1 || v.Projects[0] != "proj-a" {
		t.Fatalf("view = %+v, %v", v.Projects, err)
	}
}
