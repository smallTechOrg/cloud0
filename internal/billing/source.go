package billing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// SourceName is the run_sources.source value for this source.
const SourceName = "bigquery_billing"

// QueryTimeout bounds one BigQuery job (spec/architecture.md → Conventions).
const QueryTimeout = 120 * time.Second

// ErrCredentials wraps client-construction failures (missing key file, no
// Application Default Credentials). The wrapped text never contains the key
// path; Classify maps it to CodeCredentialsMissing.
var ErrCredentials = errors.New("billing: credentials unavailable")

// Source executes the billing query against one export table.
type Source struct {
	client     *bigquery.Client
	table      string
	marginDays int

	// Now supplies the clock for DryRun's week choice; tests inject it.
	Now func() time.Time
}

// New builds a Source from the validated configuration. Credentials come
// from GOOGLE_APPLICATION_CREDENTIALS when set (cfg.CredentialsPath),
// otherwise from Application Default Credentials (attached service account).
func New(ctx context.Context, cfg *config.Config) (*Source, error) {
	if err := ValidateTable(cfg.BillingExportTable); err != nil {
		return nil, err
	}
	if cfg.BigQueryProject == "" {
		return nil, errors.New("billing: CLOUD0_BIGQUERY_PROJECT is empty")
	}
	var opts []option.ClientOption
	if cfg.CredentialsPath != "" {
		opts = append(opts, option.WithCredentialsFile(cfg.CredentialsPath))
	}
	client, err := bigquery.NewClient(ctx, cfg.BigQueryProject, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrCredentials, redactPath(err.Error(), cfg.CredentialsPath))
	}
	return NewWithClient(client, cfg.BillingExportTable, cfg.PartitionMarginDays)
}

// NewWithClient wraps an existing client; used by tests to point the same
// credentials at a different (e.g. non-existent) table.
func NewWithClient(client *bigquery.Client, table string, marginDays int) (*Source, error) {
	if err := ValidateTable(table); err != nil {
		return nil, err
	}
	if marginDays < 0 {
		return nil, fmt.Errorf("%w: margin %d days is negative", ErrInvalidWindow, marginDays)
	}
	return &Source{client: client, table: table, marginDays: marginDays, Now: time.Now}, nil
}

// Close releases the underlying client.
func (s *Source) Close() error { return s.client.Close() }

// Client exposes the underlying BigQuery client so the Phase 2 month-cost and
// billing-usage sources reuse the same connection and credentials. The caller
// must not Close it; Source.Close owns it.
func (s *Source) Client() *bigquery.Client { return s.client }

// Table is the fully-qualified export table this source queries.
func (s *Source) Table() string { return s.table }

// MarginDays is the configured partition margin.
func (s *Source) MarginDays() int { return s.marginDays }

// Window builds the inclusive week range with the configured margin.
func (s *Source) Window(from, to isoweek.Week) Window {
	return Window{From: from, To: to, MarginDays: s.marginDays}
}

// Row is one grouped result line: the finest grain stored in snapshot_rows.
// ResourceName / ResourceGlobalName are nil when the export has no resource
// for the line (the "unattributed" bucket).
type Row struct {
	ISOYear            int
	ISOWeek            int
	ProjectID          string
	ServiceID          string
	Service            string
	SKUID              string
	SKU                string
	ResourceName       *string
	ResourceGlobalName *string
	Gross              float64
	Credits            float64
	Net                float64
	LineItems          int64
}

// Week is the ISO week the row belongs to.
func (r Row) Week() isoweek.Week { return isoweek.Week{Year: r.ISOYear, Week: r.ISOWeek} }

// JobStats are the BigQuery job statistics recorded on run_sources.
type JobStats struct {
	JobID          string
	BytesProcessed int64
	BytesBilled    int64
	CacheHit       bool
	Duration       time.Duration
	RowCount       int
}

// Result is one executed fetch. Query is populated as soon as the SQL was
// built — even when execution fails — so a failed run can still be recorded
// with its provenance; Stats.JobID is set once the job was started.
type Result struct {
	Query      Query
	Rows       []Row
	Stats      JobStats
	StartedAt  time.Time
	FinishedAt time.Time
}

// DryRunResult is the outcome of the startup health check.
type DryRunResult struct {
	Week           isoweek.Week
	Query          Query
	BytesProcessed int64
}

// Fetch runs the billing query for the window with a QueryTimeout deadline
// and returns the grouped rows (sorted by week, project, service, SKU,
// resource) plus job statistics.
func (s *Source) Fetch(ctx context.Context, window Window) (Result, error) {
	q, err := BuildQuery(window, s.table)
	if err != nil {
		return Result{}, err
	}
	res := Result{Query: q, StartedAt: time.Now().UTC()}

	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()

	bq := s.client.Query(q.SQL)
	bq.Parameters = queryParameters(q.Params)
	job, err := bq.Run(ctx)
	if err != nil {
		return res, wrapCtx(ctx, "start query", err)
	}
	res.Stats.JobID = job.ID()

	status, err := job.Wait(ctx)
	if err != nil {
		return res, wrapCtx(ctx, "wait for query", err)
	}
	if err := status.Err(); err != nil {
		return res, fmt.Errorf("billing: query failed: %w", err)
	}
	res.Stats.BytesProcessed, res.Stats.BytesBilled, res.Stats.CacheHit = statistics(status)

	it, err := job.Read(ctx)
	if err != nil {
		return res, wrapCtx(ctx, "read results", err)
	}
	rows, err := readRows(it)
	if err != nil {
		return res, wrapCtx(ctx, "decode results", err)
	}
	sort.Slice(rows, func(i, j int) bool { return rowLess(rows[i], rows[j]) })

	res.Rows = rows
	res.Stats.RowCount = len(rows)
	res.FinishedAt = time.Now().UTC()
	res.Stats.Duration = res.FinishedAt.Sub(res.StartedAt)
	return res, nil
}

// DryRun validates credentials, table and permissions without scanning: it
// dry-runs the one-week query for the last complete week (relative to Now)
// and reports the bytes it would process.
func (s *Source) DryRun(ctx context.Context) (DryRunResult, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	week := isoweek.LastComplete(now().UTC())
	q, err := BuildQuery(WeekWindow(week, s.marginDays), s.table)
	if err != nil {
		return DryRunResult{}, err
	}
	out := DryRunResult{Week: week, Query: q}

	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()

	bq := s.client.Query(q.SQL)
	bq.Parameters = queryParameters(q.Params)
	bq.DryRun = true
	job, err := bq.Run(ctx)
	if err != nil {
		return out, wrapCtx(ctx, "dry run", err)
	}
	status := job.LastStatus()
	if status == nil {
		return out, errors.New("billing: dry run returned no status")
	}
	if err := status.Err(); err != nil {
		return out, fmt.Errorf("billing: dry run failed: %w", err)
	}
	out.BytesProcessed, _, _ = statistics(status)
	return out, nil
}

// queryParameters types the four parameters for BigQuery: DATE for the
// partition bounds (civil.Date), TIMESTAMP for the usage bounds (time.Time).
func queryParameters(p Params) []bigquery.QueryParameter {
	return []bigquery.QueryParameter{
		{Name: ParamPartStart, Value: civil.DateOf(p.PartStart.UTC())},
		{Name: ParamPartEnd, Value: civil.DateOf(p.PartEnd.UTC())},
		{Name: ParamUsageStart, Value: p.UsageStart.UTC()},
		{Name: ParamUsageEnd, Value: p.UsageEnd.UTC()},
	}
}

// bqRow mirrors the SELECT list; nullable columns use the Null types so a
// NULL never fails decoding.
type bqRow struct {
	ISOYear            bigquery.NullInt64   `bigquery:"iso_year"`
	ISOWeek            bigquery.NullInt64   `bigquery:"iso_week"`
	ProjectID          bigquery.NullString  `bigquery:"project_id"`
	ServiceID          bigquery.NullString  `bigquery:"service_id"`
	Service            bigquery.NullString  `bigquery:"service"`
	SKUID              bigquery.NullString  `bigquery:"sku_id"`
	SKU                bigquery.NullString  `bigquery:"sku"`
	ResourceName       bigquery.NullString  `bigquery:"resource_name"`
	ResourceGlobalName bigquery.NullString  `bigquery:"resource_global_name"`
	Gross              bigquery.NullFloat64 `bigquery:"gross"`
	Credits            bigquery.NullFloat64 `bigquery:"credits"`
	LineItems          bigquery.NullInt64   `bigquery:"line_items"`
}

func readRows(it *bigquery.RowIterator) ([]Row, error) {
	rows := make([]Row, 0, int(it.TotalRows))
	for {
		var r bqRow
		err := it.Next(&r)
		if err == iterator.Done {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, r.toRow())
	}
}

func (r bqRow) toRow() Row {
	gross, credits := r.Gross.Float64, r.Credits.Float64
	return Row{
		ISOYear:            int(r.ISOYear.Int64),
		ISOWeek:            int(r.ISOWeek.Int64),
		ProjectID:          r.ProjectID.StringVal,
		ServiceID:          r.ServiceID.StringVal,
		Service:            r.Service.StringVal,
		SKUID:              r.SKUID.StringVal,
		SKU:                r.SKU.StringVal,
		ResourceName:       nullable(r.ResourceName),
		ResourceGlobalName: nullable(r.ResourceGlobalName),
		Gross:              gross,
		Credits:            credits,
		Net:                gross + credits,
		LineItems:          r.LineItems.Int64,
	}
}

func nullable(s bigquery.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.StringVal
	return &v
}

// rowLess orders rows by (week, project, service, SKU, resource) so the
// stored snapshot_rows are deterministic.
func rowLess(a, b Row) bool {
	if a.ISOYear != b.ISOYear {
		return a.ISOYear < b.ISOYear
	}
	if a.ISOWeek != b.ISOWeek {
		return a.ISOWeek < b.ISOWeek
	}
	if a.ProjectID != b.ProjectID {
		return a.ProjectID < b.ProjectID
	}
	if a.Service != b.Service {
		return a.Service < b.Service
	}
	if a.SKU != b.SKU {
		return a.SKU < b.SKU
	}
	if a.SKUID != b.SKUID {
		return a.SKUID < b.SKUID
	}
	if an, bn := deref(a.ResourceName), deref(b.ResourceName); an != bn {
		return an < bn
	}
	return deref(a.ResourceGlobalName) < deref(b.ResourceGlobalName)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// statistics extracts bytes processed / billed and cache hit from a job status.
func statistics(status *bigquery.JobStatus) (processed, billed int64, cacheHit bool) {
	if status == nil || status.Statistics == nil {
		return 0, 0, false
	}
	processed = status.Statistics.TotalBytesProcessed
	if qs, ok := status.Statistics.Details.(*bigquery.QueryStatistics); ok && qs != nil {
		billed = qs.TotalBytesBilled
		cacheHit = qs.CacheHit
		if processed == 0 {
			processed = qs.TotalBytesProcessed
		}
	}
	return processed, billed, cacheHit
}

// wrapCtx attaches the context error (deadline/cancel) when the context is
// done so Classify sees a timeout rather than a transport error.
func wrapCtx(ctx context.Context, stage string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("billing: %s: %w (%v)", stage, ctxErr, err)
	}
	return fmt.Errorf("billing: %s: %w", stage, err)
}

// redactPath removes the credentials file location from a message so it is
// never logged or stored; the env var name stands in for it.
func redactPath(msg, path string) string {
	for _, p := range []string{path, os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")} {
		if p != "" {
			msg = strings.ReplaceAll(msg, p, "$GOOGLE_APPLICATION_CREDENTIALS")
		}
	}
	return msg
}
