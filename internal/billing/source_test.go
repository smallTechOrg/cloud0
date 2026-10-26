package billing

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/testenv"
	"google.golang.org/api/iterator"
)

// Tolerances and bounds from spec/roadmap.md → S1 gate.
const (
	moneyTolerance   = 0.005
	maxWeekBytes     = 200_000_000 // bytes billed for one week must stay below 200 MB
	minSmokeWeekRows = 100
)

// realSource builds a Source from .env exactly as serve does. Skips with the
// documented message only when the BigQuery variables are genuinely absent.
func realSource(t *testing.T) (*Source, config.Config) {
	t.Helper()
	testenv.RequireBigQuery(t)
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		t.Fatalf("config.Load from .env: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	src, err := New(ctx, &cfg)
	if err != nil {
		t.Fatalf("billing.New: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src, cfg
}

// independentNet runs the single-aggregate reconciliation query from the
// roadmap with the same filters and parameters the grouped query used.
func independentNet(t *testing.T, ctx context.Context, client *bigquery.Client, q Query) (net float64, jobID string) {
	t.Helper()
	sql := "SELECT SUM(cost) + SUM((SELECT IFNULL(SUM(c.amount), 0) FROM UNNEST(credits) AS c)) AS net\n" +
		"FROM `" + q.Table + "`\n" +
		"WHERE DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end\n" +
		"  AND usage_start_time >= @usage_start\n" +
		"  AND usage_start_time <  @usage_end"
	bq := client.Query(sql)
	bq.Parameters = queryParameters(q.Params)
	job, err := bq.Run(ctx)
	if err != nil {
		t.Fatalf("independent query failed to start: %v", err)
	}
	it, err := job.Read(ctx)
	if err != nil {
		t.Fatalf("independent query failed: %v", err)
	}
	var row struct {
		Net bigquery.NullFloat64 `bigquery:"net"`
	}
	if err := it.Next(&row); err != nil {
		t.Fatalf("independent query returned no row: %v", err)
	}
	var extra struct {
		Net bigquery.NullFloat64 `bigquery:"net"`
	}
	if err := it.Next(&extra); err != iterator.Done {
		t.Fatalf("independent query returned more than one row (err=%v)", err)
	}
	if !row.Net.Valid {
		t.Fatal("independent query returned NULL — the window has no billing lines")
	}
	return row.Net.Float64, job.ID()
}

func TestFetch_SmokeWeekReconcilesWithTheIndependentAggregate(t *testing.T) {
	src, cfg := realSource(t)
	week := cfg.SmokeWeek
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	res, err := src.Fetch(ctx, src.Window(week, week))
	if err != nil {
		f := Classify(err)
		t.Fatalf("Fetch(%s): %v [code=%s hint=%s]", week, err, f.Code, f.Hint)
	}

	// Provenance text and params.
	if !strings.Contains(res.Query.SQL, "DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end") {
		t.Error("executed SQL lacks the partition filter")
	}
	if !strings.Contains(res.Query.SQL, "`"+cfg.BillingExportTable+"`") {
		t.Error("executed SQL does not name the configured table")
	}
	start, end := isoweek.Bounds(week)
	p := res.Query.Params
	if !p.UsageStart.Equal(start) || !p.UsageEnd.Equal(end) {
		t.Errorf("usage params [%s, %s) != week bounds [%s, %s)", p.UsageStart, p.UsageEnd, start, end)
	}
	if !p.PartStart.Equal(start.AddDate(0, 0, -cfg.PartitionMarginDays)) || !p.PartEnd.Equal(end.AddDate(0, 0, cfg.PartitionMarginDays)) {
		t.Errorf("partition params [%s, %s] not week bounds ∓ %d day(s)", p.PartStart.Format("2006-01-02"), p.PartEnd.Format("2006-01-02"), cfg.PartitionMarginDays)
	}

	// Job statistics.
	if res.Stats.JobID == "" {
		t.Error("JobID is empty")
	}
	if res.Stats.BytesBilled >= maxWeekBytes {
		t.Errorf("bytes_billed = %d, want < %d — the partition filter is not keeping the scan small", res.Stats.BytesBilled, maxWeekBytes)
	}
	if res.Stats.BytesProcessed <= 0 && !res.Stats.CacheHit {
		t.Errorf("bytes_processed = %d without a cache hit", res.Stats.BytesProcessed)
	}
	if res.Stats.RowCount != len(res.Rows) {
		t.Errorf("RowCount = %d, len(Rows) = %d", res.Stats.RowCount, len(res.Rows))
	}
	if res.Stats.Duration <= 0 || res.FinishedAt.Before(res.StartedAt) {
		t.Errorf("timing not recorded: duration=%s started=%s finished=%s", res.Stats.Duration, res.StartedAt, res.FinishedAt)
	}

	// Rows.
	if len(res.Rows) <= minSmokeWeekRows {
		t.Fatalf("rows = %d, want > %d for the smoke week", len(res.Rows), minSmokeWeekRows)
	}
	var sumNet, sumGross, sumCredits, computeNet float64
	projects := map[string]bool{}
	for i, r := range res.Rows {
		if r.ISOYear != week.Year || r.ISOWeek != week.Week {
			t.Fatalf("row %d is %04d-W%02d, want %s", i, r.ISOYear, r.ISOWeek, week)
		}
		if math.Abs(r.Net-(r.Gross+r.Credits)) > 1e-9 {
			t.Fatalf("row %d: net %f != gross %f + credits %f", i, r.Net, r.Gross, r.Credits)
		}
		if r.Credits > 1e-9 {
			t.Errorf("row %d: credits %f is positive", i, r.Credits)
		}
		if r.Service == "" || r.ProjectID == "" || r.LineItems <= 0 {
			t.Errorf("row %d incomplete: %+v", i, r)
		}
		if r.Service == "Compute Engine" {
			computeNet += r.Net
		}
		sumNet += r.Net
		sumGross += r.Gross
		sumCredits += r.Credits
		projects[r.ProjectID] = true
	}
	if computeNet <= 0 {
		t.Errorf("Compute Engine net = %f, want > 0", computeNet)
	}
	if math.Abs(sumNet-(sumGross+sumCredits)) > moneyTolerance {
		t.Errorf("Σnet %f != Σgross %f + Σcredits %f", sumNet, sumGross, sumCredits)
	}
	// Rows are sorted deterministically so snapshot_rows order is stable.
	for i := 1; i < len(res.Rows); i++ {
		if rowLess(res.Rows[i], res.Rows[i-1]) {
			t.Fatalf("rows not sorted at index %d", i)
		}
	}

	// Reconciliation against the independent single-aggregate query.
	independent, indepJob := independentNet(t, ctx, src.client, res.Query)
	if diff := math.Abs(sumNet - independent); diff > moneyTolerance {
		t.Fatalf("Σnet over grouped rows %.6f != independent SUM(cost)+SUM(credits) %.6f (diff %.6f > %.3f)", sumNet, independent, diff, moneyTolerance)
	}

	t.Logf("week %s: job %s, bytes_processed=%d bytes_billed=%d cache_hit=%v, rows=%d, projects=%d, duration=%s",
		week, res.Stats.JobID, res.Stats.BytesProcessed, res.Stats.BytesBilled, res.Stats.CacheHit, len(res.Rows), len(projects), res.Stats.Duration.Round(time.Millisecond))
	t.Logf("week %s: reconciled total net=%.6f (gross=%.6f credits=%.6f) vs independent %.6f (job %s), Compute Engine net=%.2f",
		week, sumNet, sumGross, sumCredits, independent, indepJob, computeNet)
}

// wrongTableInRealDataset is the configured export's project.dataset with a
// table that does not exist — a wrong-table typo. Built at runtime so no real
// identifier is committed. (With dataset access BigQuery answers 404
// notFound; without it, it answers a hedged 403 — see wrongDataset.)
func wrongTableInRealDataset(t *testing.T, cfg config.Config) string {
	t.Helper()
	parts := strings.SplitN(cfg.BillingExportTable, ".", 3)
	if len(parts) != 3 {
		t.Fatalf("CLOUD0_BILLING_EXPORT_TABLE is not project.dataset.table")
	}
	return parts[0] + "." + parts[1] + ".cloud0_no_such_table"
}

// wrongDataset is a dataset that does not exist in the job project.
func wrongDataset(cfg config.Config) string {
	return cfg.BigQueryProject + ".cloud0_no_such_dataset.cloud0_no_such_table"
}

func TestFetch_WrongTableClassifiesAsTableNotFound(t *testing.T) {
	src, cfg := realSource(t)
	missing := wrongTableInRealDataset(t, cfg)
	wrong, err := NewWithClient(src.client, missing, cfg.PartitionMarginDays)
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := wrong.Fetch(ctx, wrong.Window(cfg.SmokeWeek, cfg.SmokeWeek))
	if err == nil {
		t.Fatalf("Fetch against a missing table succeeded with %d rows", len(res.Rows))
	}
	f := Classify(err)
	if f.Code != CodeTableNotFound {
		t.Fatalf("Code = %q, want %q (err: %v)", f.Code, CodeTableNotFound, err)
	}
	if !strings.Contains(f.Hint, "CLOUD0_BILLING_EXPORT_TABLE") {
		t.Errorf("hint %q does not name CLOUD0_BILLING_EXPORT_TABLE", f.Hint)
	}
	if f.Message == "" || len(f.Message) > MaxMessageLen {
		t.Errorf("message length %d", len(f.Message))
	}
	// The SQL is still returned so the failed run can be recorded with provenance.
	if !strings.Contains(res.Query.SQL, "`"+missing+"`") {
		t.Error("Result.Query not populated on failure")
	}
	if len(res.Rows) != 0 {
		t.Error("rows returned on failure")
	}
	t.Logf("wrong table → code=%s hint=%q", f.Code, f.Hint)
}

func TestFetch_WrongDatasetIsAHedged403AndTheHintNamesTheTableVariable(t *testing.T) {
	src, cfg := realSource(t)
	wrong, err := NewWithClient(src.client, wrongDataset(cfg), cfg.PartitionMarginDays)
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err = wrong.Fetch(ctx, wrong.Window(cfg.SmokeWeek, cfg.SmokeWeek))
	if err == nil {
		t.Fatal("Fetch against a missing dataset succeeded")
	}
	f := Classify(err)
	// BigQuery does not disclose existence without dataset access, so this is
	// a permission error by the provider's own account — but the hint must
	// still send the user to check the table variable.
	if f.Code != CodePermissionDenied && f.Code != CodeTableNotFound {
		t.Fatalf("Code = %q, want permission_denied or table_not_found (err: %v)", f.Code, err)
	}
	if !strings.Contains(f.Hint, "CLOUD0_BILLING_EXPORT_TABLE") {
		t.Errorf("hint %q does not name CLOUD0_BILLING_EXPORT_TABLE", f.Hint)
	}
	t.Logf("wrong dataset → code=%s", f.Code)
}

func TestFetch_ExpiredDeadlineClassifiesAsTimeout(t *testing.T) {
	src, cfg := realSource(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	_, err := src.Fetch(ctx, src.Window(cfg.SmokeWeek, cfg.SmokeWeek))
	if err == nil {
		t.Fatal("Fetch with an expired deadline succeeded")
	}
	if f := Classify(err); f.Code != CodeTimeout {
		t.Fatalf("Code = %q, want %q (err: %v)", f.Code, CodeTimeout, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error does not wrap context.DeadlineExceeded: %v", err)
	}
}

func TestDryRun_SucceedsAndReportsBytesForTheSmokeWeek(t *testing.T) {
	src, cfg := realSource(t)
	// Pin "now" so the dry run targets the smoke week (last complete week
	// relative to the injected clock) — a week known to have data.
	src.Now = func() time.Time { return cfg.SmokeWeek.End().Add(72 * time.Hour) }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dr, err := src.DryRun(ctx)
	if err != nil {
		f := Classify(err)
		t.Fatalf("DryRun: %v [code=%s hint=%s]", err, f.Code, f.Hint)
	}
	if dr.Week != cfg.SmokeWeek {
		t.Errorf("DryRun week = %s, want %s", dr.Week, cfg.SmokeWeek)
	}
	if dr.BytesProcessed <= 0 {
		t.Fatalf("BytesProcessed = %d, want > 0", dr.BytesProcessed)
	}
	if dr.BytesProcessed >= maxWeekBytes {
		t.Errorf("dry run would process %d bytes, want < %d", dr.BytesProcessed, maxWeekBytes)
	}
	if !strings.Contains(dr.Query.SQL, "DATE(_PARTITIONTIME) BETWEEN") {
		t.Error("dry-run SQL lacks the partition filter")
	}
	t.Logf("dry run for %s would process %d bytes", dr.Week, dr.BytesProcessed)
}

func TestDryRun_WrongTableClassifiesAsTableNotFound(t *testing.T) {
	src, cfg := realSource(t)
	wrong, err := NewWithClient(src.client, wrongTableInRealDataset(t, cfg), cfg.PartitionMarginDays)
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err = wrong.DryRun(ctx)
	if err == nil {
		t.Fatal("DryRun against a missing table succeeded")
	}
	if f := Classify(err); f.Code != CodeTableNotFound {
		t.Fatalf("Code = %q, want table_not_found (err: %v)", f.Code, err)
	}
}

func TestNew_MissingKeyFileClassifiesAsCredentialsMissingWithoutLeakingThePath(t *testing.T) {
	secretDir := filepath.Join(t.TempDir(), "very-secret-location")
	cfg := config.Config{
		CredentialsPath:     filepath.Join(secretDir, "missing-key.json"),
		Projects:            []string{"my-project"},
		BillingExportTable:  placeholderTable,
		BigQueryProject:     "my-project",
		PartitionMarginDays: 1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	src, err := New(ctx, &cfg)
	if err == nil {
		_ = src.Close()
		t.Fatal("New succeeded with a non-existent key file")
	}
	f := Classify(err)
	if f.Code != CodeCredentialsMissing {
		t.Fatalf("Code = %q, want credentials_missing (err: %v)", f.Code, err)
	}
	if strings.Contains(err.Error(), "very-secret-location") || strings.Contains(f.Message, "very-secret-location") {
		t.Errorf("error leaks the credentials path: %v", err)
	}
	if !strings.Contains(f.Hint, "GOOGLE_APPLICATION_CREDENTIALS") {
		t.Errorf("hint %q does not name GOOGLE_APPLICATION_CREDENTIALS", f.Hint)
	}
}

func TestNew_RejectsAnInvalidTableBeforeTouchingTheNetwork(t *testing.T) {
	cfg := config.Config{
		Projects:            []string{"my-project"},
		BillingExportTable:  "`my-project.my_dataset.t`",
		BigQueryProject:     "my-project",
		PartitionMarginDays: 1,
	}
	src, err := New(context.Background(), &cfg)
	if err == nil {
		_ = src.Close()
		t.Fatal("New accepted a table name with backticks")
	}
	if !errors.Is(err, ErrInvalidTable) {
		t.Fatalf("error %v is not ErrInvalidTable", err)
	}
}
