package billingusage

import (
	"context"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

// TestFetch_RealW42MatchesSheetAndScansUnder200MB runs the real query for
// ISO 2026-W42 and compares with the sheet (LB hours 168, outbound 0.28,
// inbound 0.1, intercontinental 0.86, APAC 0.09). The sheet used manual day
// boundaries, so volumes are checked within a band around the sheet value.
func TestFetch_RealW42MatchesSheetAndScansUnder200MB(t *testing.T) {
	testenv.RequireBigQuery(t)
	table := os.Getenv("CLOUD0_BILLING_EXPORT_TABLE")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client, err := bigquery.NewClient(ctx, os.Getenv("CLOUD0_BIGQUERY_PROJECT"))
	if err != nil {
		t.Fatalf("bigquery.NewClient: %v", err)
	}
	defer client.Close()

	// Dry run of the exact query + params: bytes processed must stay tiny.
	q := client.Query(BuildQuery(table))
	q.Parameters = BuildParams(w42, 1).Query()
	q.DryRun = true
	job, err := q.Run(ctx)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	st := job.LastStatus()
	if st == nil || st.Statistics == nil {
		t.Fatal("dry run returned no statistics")
	}
	bytes := st.Statistics.TotalBytesProcessed
	t.Logf("dry run: %d bytes processed (%.1f MB)", bytes, float64(bytes)/1e6)
	if bytes <= 0 || bytes >= 200_000_000 {
		t.Fatalf("bytes processed = %d, want 0 < b < 200 MB", bytes)
	}

	res := Fetch(ctx, client, table, w42, 1, time.Now)
	if res.Status != gcpsource.StatusOK {
		t.Fatalf("status %s: %s (hint %s)", res.Status, res.Message, res.Hint)
	}
	if res.Source != Source || len(res.Provenance) != 1 || res.Provenance[0].Query != BuildQuery(table) || res.Provenance[0].Aggregation != BuildParams(w42, 1).JSON() {
		t.Fatalf("provenance = %+v", res.Provenance)
	}

	totals := map[string]float64{}
	for _, r := range res.Metrics {
		if r.Resource != "" {
			t.Errorf("row %s/%s has resource %q, want group total", r.Group, r.Project, r.Resource)
		}
		for m, c := range r.Cells {
			if c.Provenance.Query == "" || c.Provenance.FetchedAt.IsZero() {
				t.Errorf("%s/%s cell %s missing provenance", r.Group, r.Project, m)
			}
			if c.Value != nil {
				totals[m] += *c.Value
			}
		}
	}
	checks := []struct {
		metric        string
		sheet, lo, hi float64
	}{
		{gcpsource.MLBHours, 168, 167.99, 168.01},
		{gcpsource.MOutboundGB, 0.28, 0.20, 0.35},
		{gcpsource.MInboundGB, 0.10, 0.08, 0.12},
		{gcpsource.MEgressInterGB, 0.86, 0.80, 0.92},
		{gcpsource.MDownloadAPACGB, 0.09, 0.03, 0.10},
	}
	for _, c := range checks {
		got := totals[c.metric]
		t.Logf("W42 %-28s = %.4f (sheet %.2f)", c.metric, got, c.sheet)
		if got < c.lo || got > c.hi {
			t.Errorf("%s = %.4f, want in [%.2f, %.2f] (sheet %.2f)", c.metric, got, c.lo, c.hi, c.sheet)
		}
	}
}
