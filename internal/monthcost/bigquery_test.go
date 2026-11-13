package monthcost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

const maxBytes = 500 * 1000 * 1000

func bqClient(t *testing.T) (*bigquery.Client, string) {
	t.Helper()
	testenv.LoadDotEnv(t)
	testenv.RequireBigQuery(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := bigquery.NewClient(ctx, os.Getenv("CLOUD0_BIGQUERY_PROJECT"))
	if err != nil {
		t.Fatalf("bigquery.NewClient: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, os.Getenv("CLOUD0_BILLING_EXPORT_TABLE")
}

// TestFetch_October2026MatchesIndependentAggregate is the real-BigQuery
// success criterion from spec/capabilities/month-view.md.
func TestFetch_October2026MatchesIndependentAggregate(t *testing.T) {
	client, table := bqClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	oct, _ := Parse("2026-10")
	fixedNow := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	rows, prov, err := Fetch(ctx, client, table, oct, oct, 1, func() time.Time { return fixedNow })
	if err != nil {
		st, msg, hint := gcpsource.Classify(err, RoleHint, API)
		t.Fatalf("Fetch: %s %s (%s)", st, msg, hint)
	}
	if len(rows) == 0 {
		t.Fatal("Fetch returned no rows for 2026-10")
	}

	// Provenance: exact SQL + params JSON + window + injected fetch time.
	if prov.Source != SourceName || prov.Query != BuildQuery(table) {
		t.Fatalf("provenance source/query mismatch: %q", prov.Source)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(prov.Aggregation), &params); err != nil {
		t.Fatalf("params json: %v", err)
	}
	if params["part_start"] != "2026-09-30" || params["part_end"] != "2026-11-02" ||
		params["usage_start"] != "2026-10-01T00:00:00Z" || params["usage_end"] != "2026-11-01T00:00:00Z" {
		t.Fatalf("params = %v", params)
	}
	if !prov.FetchedAt.Equal(fixedNow) || !prov.Window.Start.Equal(oct.Bounds().Start) || !prov.Window.End.Equal(oct.Bounds().End) {
		t.Fatalf("provenance window/time = %+v", prov)
	}

	var net float64
	byService := map[string]float64{}
	invoiceMonths := map[string]bool{}
	for _, r := range rows {
		if r.Month != "2026-10" {
			t.Fatalf("row outside month: %+v", r)
		}
		if !approx(r.Net, r.Gross+r.Credits) {
			t.Fatalf("net != gross+credits: %+v", r)
		}
		net += r.Net
		byService[r.Service] += r.Net
		invoiceMonths[r.InvoiceMonth] = true
	}

	// Independent single aggregate over the same window (literal SQL, no
	// grouping, no shared parameter code).
	indep := "SELECT SUM(cost) + SUM((SELECT IFNULL(SUM(c.amount), 0) FROM UNNEST(credits) AS c)) AS net\n" +
		"FROM `" + table + "`\n" +
		"WHERE DATE(_PARTITIONTIME) BETWEEN DATE '2026-09-30' AND DATE '2026-11-02'\n" +
		"  AND usage_start_time >= TIMESTAMP '2026-10-01 00:00:00+00'\n" +
		"  AND usage_start_time <  TIMESTAMP '2026-11-01 00:00:00+00'"
	it, err := client.Query(indep).Read(ctx)
	if err != nil {
		t.Fatalf("independent query: %v", err)
	}
	var one struct{ Net float64 }
	if err := it.Next(&one); err != nil {
		t.Fatalf("independent query row: %v", err)
	}
	if !approx(net, one.Net) {
		t.Fatalf("Fetch net %.4f != independent %.4f", net, one.Net)
	}

	// Bytes processed via dry run of the exact Fetch query.
	p, _ := NewParams(oct, oct, 1)
	dq := client.Query(BuildQuery(table))
	dq.Parameters = p.QueryParameters()
	dq.DryRun = true
	job, err := dq.Run(ctx)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	bytes := job.LastStatus().Statistics.TotalBytesProcessed
	if bytes <= 0 || bytes >= maxBytes {
		t.Fatalf("bytes processed = %d, want 0 < b < %d", bytes, maxBytes)
	}

	s := Summary(rows, oct, nil, fixedNow)
	type kv struct {
		k string
		v float64
	}
	var top []kv
	for k, v := range byService {
		top = append(top, kv{k, v})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].v > top[j].v })
	if len(top) > 5 {
		top = top[:5]
	}
	t.Logf("2026-10 month-to-date: net %.2f (independent %.2f), gross %.2f, credits %.2f, projected %.2f, status %s, rows %d, invoice months %v, dry-run bytes %d",
		net, one.Net, s.Gross, s.Credits, s.Projected, s.Status, len(rows), keys(invoiceMonths), bytes)
	for i, e := range top {
		t.Logf("  top%d %-22s %.2f", i+1, e.k, e.v)
	}
}

func TestFetch_MonthBeforeExportStartReturnsNoRows(t *testing.T) {
	client, table := bqClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	jan, _ := Parse("2020-01")
	rows, prov, err := Fetch(ctx, client, table, jan, jan, 1, time.Now)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows for 2020-01, want 0", len(rows))
	}
	if prov.Query == "" {
		t.Fatal("empty provenance")
	}
}

func TestFetch_RejectsBadInputsWithoutCallingBigQuery(t *testing.T) {
	oct, _ := Parse("2026-10")
	sep, _ := Parse("2026-09")
	ctx := context.Background()
	if _, _, err := Fetch(ctx, nil, "bad table; drop", oct, oct, 1, time.Now); !errors.Is(err, ErrInvalidTable) {
		t.Errorf("bad table: err = %v", err)
	}
	if _, _, err := Fetch(ctx, nil, "p.d.t", oct, sep, 1, time.Now); !errors.Is(err, ErrInvalidRange) {
		t.Errorf("from after to: err = %v", err)
	}
	if _, _, err := Fetch(ctx, nil, "p.d.t", oct, oct, -1, time.Now); !errors.Is(err, ErrInvalidRange) {
		t.Errorf("negative margin: err = %v", err)
	}
}

func TestFetch_MissingTableIsClassifiedFailure(t *testing.T) {
	client, table := bqClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	parts := strings.SplitN(table, ".", 3)
	missing := parts[0] + "." + parts[1] + ".cloud0_no_such_table"
	oct, _ := Parse("2026-10")
	_, prov, err := Fetch(ctx, client, missing, oct, oct, 1, time.Now)
	if err == nil {
		t.Fatal("Fetch on a missing table succeeded")
	}
	st, msg, _ := gcpsource.Classify(err, RoleHint, API)
	if st == gcpsource.StatusOK || msg == "" {
		t.Fatalf("Classify = %s %q", st, msg)
	}
	if prov.Query != BuildQuery(missing) {
		t.Fatal("provenance must carry the SQL even on failure")
	}
	t.Logf("missing table classified as %s", st)
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
