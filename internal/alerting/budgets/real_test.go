package budgets

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/option"

	"github.com/smallTechOrg/cloud0/internal/alerting"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

// realClient builds a client with the alerts credentials. It skips only when
// CLOUD0_ALERTS_CREDENTIALS is unset; any other missing variable fails.
func realClient(t *testing.T) (*Client, string) {
	t.Helper()
	testenv.LoadDotEnv(t)
	cred := os.Getenv("CLOUD0_ALERTS_CREDENTIALS")
	if cred == "" {
		t.Skip("CLOUD0_ALERTS_CREDENTIALS not set")
	}
	acct := os.Getenv("CLOUD0_BILLING_ACCOUNT_ID")
	projects := strings.Split(os.Getenv("CLOUD0_GCP_PROJECTS"), ",")
	main := strings.TrimSpace(projects[0])
	if acct == "" || main == "" {
		t.Fatal("CLOUD0_BILLING_ACCOUNT_ID and CLOUD0_GCP_PROJECTS must be set")
	}
	c, err := New(context.Background(), acct, main, option.WithCredentialsFile(cred))
	if err != nil {
		t.Fatal(err)
	}
	return c, main
}

func TestReal_ListReturnsPreExistingBudget(t *testing.T) {
	c, _ := realClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	list, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found bool
	for _, b := range list {
		if b.Managed || b.Amount == nil || *b.Amount != 5000 || b.AmountKind != "specified" {
			continue
		}
		found = true
		if len(b.Projects) != 1 || b.Projects[0] == "" || b.Currency != "INR" || b.AdoptShape != alerting.ShapeBudgetBoilerplate || len(b.Thresholds) == 0 {
			t.Errorf("pre-existing budget shape: projects=%d currency=%s adopt=%s thresholds=%d", len(b.Projects), b.Currency, b.AdoptShape, len(b.Thresholds))
		}
	}
	if !found {
		t.Fatalf("no unmanaged 5000 INR budget among %d budgets", len(list))
	}
}

func TestReal_ServiceIDsComputeEngineAndCloudRun(t *testing.T) {
	testenv.RequireBigQuery(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	bq, err := bigquery.NewClient(ctx, os.Getenv("CLOUD0_BIGQUERY_PROJECT"))
	if err != nil {
		t.Fatal(err)
	}
	defer bq.Close()
	l, err := NewBQServiceLister(bq, os.Getenv("CLOUD0_BILLING_EXPORT_TABLE"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ids, missing, err := ServiceIDs(ctx, l, "Compute Engine", "Cloud Run")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing services: %v", missing)
	}
	for n, id := range ids {
		if !strings.HasPrefix(id, "services/") || len(id) != len("services/XXXX-XXXX-XXXX") {
			t.Errorf("%s id shape wrong (len %d)", n, len(id))
		}
	}
}

func TestReal_CreateGetUpdateDelete(t *testing.T) {
	c, main := realClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	name := alerting.TestPrefix + "budget-" + time.Now().UTC().Format("150405")
	d := alerting.BudgetDraft{DisplayName: name, Amount: 100, Projects: []string{main}}
	created, err := c.Create(ctx, d, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Delete(context.Background(), created.Name); err != nil {
			t.Errorf("LOUD: cleanup failed to delete test budget %s: %v", created.Name, err)
		}
	})
	if !created.Managed || created.Amount == nil || *created.Amount != 100 || created.Currency != "INR" || len(created.Projects) != 1 || len(created.Thresholds) != 4 {
		t.Fatalf("created = %+v", created)
	}

	got, err := c.Get(ctx, created.Name)
	if err != nil || got.DisplayName != name || got.Name != created.Name {
		t.Fatalf("Get = %+v, %v", got, err)
	}

	d.Amount = 150.5
	upd, err := c.Update(ctx, created.Name, d, nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if upd.Amount == nil || *upd.Amount != 150.5 {
		t.Fatalf("updated amount = %v", upd.Amount)
	}
	again, err := c.Get(ctx, created.Name)
	if err != nil || again.Amount == nil || *again.Amount != 150.5 {
		t.Fatalf("Get after update = %+v, %v", again, err)
	}
}
