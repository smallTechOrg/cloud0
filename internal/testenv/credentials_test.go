package testenv

import (
	"context"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"
)

// TestCredentialsAlive proves the key referenced by .env is present AND alive
// by running one minimal real BigQuery query (SELECT 1). It skips only when
// the BigQuery variables are genuinely absent.
func TestCredentialsAlive(t *testing.T) {
	RequireBigQuery(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	project := os.Getenv("CLOUD0_BIGQUERY_PROJECT")
	client, err := bigquery.NewClient(ctx, project)
	if err != nil {
		t.Fatalf("bigquery.NewClient: %v (check GOOGLE_APPLICATION_CREDENTIALS in .env)", err)
	}
	defer client.Close()

	q := client.Query("SELECT 1 AS one")
	job, err := q.Run(ctx)
	if err != nil {
		t.Fatalf("SELECT 1 failed to start: %v (the service account in .env may lack roles/bigquery.jobUser on CLOUD0_BIGQUERY_PROJECT)", err)
	}
	it, err := job.Read(ctx)
	if err != nil {
		t.Fatalf("SELECT 1 failed: %v", err)
	}
	var row []bigquery.Value
	if err := it.Next(&row); err != nil {
		t.Fatalf("SELECT 1 returned no row: %v", err)
	}
	if len(row) != 1 || row[0] != int64(1) {
		t.Fatalf("SELECT 1 returned %v, want [1]", row)
	}
	if err := it.Next(&row); err != iterator.Done {
		t.Fatalf("SELECT 1 returned more than one row (err=%v)", err)
	}
	t.Logf("BigQuery credentials alive: job %s completed", job.ID())
}
