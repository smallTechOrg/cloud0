package monthcost

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"google.golang.org/api/iterator"
)

// SourceName is the provenance / run_sources source id.
const SourceName = "bigquery_month"

// RoleHint and API are what callers pass to gcpsource.Classify.
const (
	RoleHint = "roles/bigquery.jobUser + dataset READER"
	API      = "bigquery.googleapis.com"
)

// QueryTimeout bounds one BigQuery job (spec/architecture.md → Conventions).
const QueryTimeout = 120 * time.Second

// Row is one (month, invoice month, project, service) total. Net = Gross +
// Credits (credits are negative). Empty strings stand for NULL columns.
type Row struct {
	Month        string // usage month, "YYYY-MM" UTC
	InvoiceMonth string // invoice.month as exported, "YYYYMM"
	ProjectID    string
	ServiceID    string
	Service      string
	Gross        float64
	Credits      float64
	Net          float64
}

type bqRow struct {
	Month        bigquery.NullString  `bigquery:"month"`
	InvoiceMonth bigquery.NullString  `bigquery:"invoice_month"`
	ProjectID    bigquery.NullString  `bigquery:"project_id"`
	ServiceID    bigquery.NullString  `bigquery:"service_id"`
	Service      bigquery.NullString  `bigquery:"service"`
	Gross        bigquery.NullFloat64 `bigquery:"gross"`
	Credits      bigquery.NullFloat64 `bigquery:"credits"`
}

// Fetch runs the month query for the inclusive range [from, to] and returns
// rows sorted by month, project, service, invoice month. The provenance
// (exact SQL, params JSON, window, fetch time from now) is returned even on
// failure once the inputs are valid. Errors are for the caller to classify
// with gcpsource.Classify(err, RoleHint, API).
func Fetch(ctx context.Context, client *bigquery.Client, table string, from, to Month, marginDays int, now func() time.Time) ([]Row, gcpsource.Provenance, error) {
	if !tablePattern.MatchString(table) {
		return nil, gcpsource.Provenance{}, ErrInvalidTable
	}
	p, err := NewParams(from, to, marginDays)
	if err != nil {
		return nil, gcpsource.Provenance{}, err
	}
	if now == nil {
		now = time.Now
	}
	sql := BuildQuery(table)
	prov := gcpsource.Provenance{
		Source:      SourceName,
		Query:       sql,
		Aggregation: p.JSON(),
		Window:      gcpsource.Window{Start: p.UsageStart, End: p.UsageEnd},
		FetchedAt:   now().UTC(),
	}
	if client == nil {
		return nil, prov, errors.New("monthcost: nil BigQuery client")
	}

	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	q := client.Query(sql)
	q.Parameters = p.QueryParameters()
	it, err := q.Read(ctx)
	if err != nil {
		return nil, prov, fmt.Errorf("monthcost: run query: %w", err)
	}
	var rows []Row
	for {
		var r bqRow
		err := it.Next(&r)
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, prov, fmt.Errorf("monthcost: read results: %w", err)
		}
		row := Row{
			Month:        r.Month.StringVal,
			InvoiceMonth: r.InvoiceMonth.StringVal,
			ProjectID:    r.ProjectID.StringVal,
			ServiceID:    r.ServiceID.StringVal,
			Service:      r.Service.StringVal,
			Gross:        r.Gross.Float64,
			Credits:      r.Credits.Float64,
		}
		row.Net = row.Gross + row.Credits
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Month != b.Month {
			return a.Month < b.Month
		}
		if a.ProjectID != b.ProjectID {
			return a.ProjectID < b.ProjectID
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.ServiceID != b.ServiceID {
			return a.ServiceID < b.ServiceID
		}
		return a.InvoiceMonth < b.InvoiceMonth
	})
	return rows, prov, nil
}
