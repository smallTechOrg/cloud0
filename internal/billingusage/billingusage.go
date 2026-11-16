// Package billingusage derives the Metrics-block cells for which the billing
// export is the source of record (load-balancer hours, LB outbound/inbound
// data, Cloud Run intercontinental egress, Cloud Storage APAC download) from
// one partition-filtered BigQuery query over the detailed billing export.
package billingusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// Source is the run_sources id of this fetch.
const Source = "bigquery_usage"

// RoleHint and API feed gcpsource.Classify for permission / API errors.
const (
	RoleHint = "roles/bigquery.jobUser on CLOUD0_BIGQUERY_PROJECT and READER on the billing export dataset"
	API      = "bigquery.googleapis.com"
)

// Gap texts.
const (
	GapNotBilled  = "not billed"
	HintNotBilled = "ingress is free and not in billing export"
	GapNoData     = "no data"
	HintNoData    = "no billing line items for these SKUs in the window"
)

// QueryTimeout bounds one BigQuery query (spec: 120 s).
const QueryTimeout = 120 * time.Second

var (
	// ErrBadTable is returned for a table name that is not project.dataset.table.
	ErrBadTable = errors.New("billingusage: table must match project.dataset.table")
	// ErrBadWindow is returned for an empty or inverted window.
	ErrBadWindow = errors.New("billingusage: window end must be after start")

	tableRE = regexp.MustCompile(`^[A-Za-z0-9-]+\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)
)

// ValidateTable checks the fully-qualified billing export table name.
func ValidateTable(table string) error {
	if !tableRE.MatchString(table) {
		return ErrBadTable
	}
	return nil
}

// BuildQuery returns the exact SQL executed (and stored as provenance): the
// Phase 1 partition/usage filter, the correlated credit subquery, grouped by
// project, service, SKU and resource, restricted to the SKUs in Rules.
// The caller must validate table with ValidateTable first.
func BuildQuery(table string) string {
	preds := make([]string, len(Rules))
	for i, r := range Rules {
		preds[i] = "       " + r.sqlPredicate()
	}
	return `SELECT
  project.id            AS project_id,
  service.description   AS service,
  sku.description       AS sku,
  resource.name         AS resource_name,
  SUM(usage.amount_in_pricing_units) AS usage_amount,
  ANY_VALUE(usage.pricing_unit)      AS pricing_unit,
  SUM(cost)             AS gross,
  SUM((SELECT IFNULL(SUM(c.amount), 0) FROM UNNEST(credits) AS c)) AS credits
FROM ` + "`" + table + "`" + `
WHERE DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end
  AND usage_start_time >= @usage_start
  AND usage_start_time <  @usage_end
  AND (
` + strings.Join(preds, "\n    OR ") + `
  )
GROUP BY 1, 2, 3, 4`
}

// Params holds the four query parameters.
type Params struct {
	PartStart  string    `json:"part_start"`  // YYYY-MM-DD
	PartEnd    string    `json:"part_end"`    // YYYY-MM-DD
	UsageStart time.Time `json:"usage_start"` // inclusive
	UsageEnd   time.Time `json:"usage_end"`   // exclusive
}

// BuildParams derives the parameters for window w: the usage bounds are the
// window itself, the partition bounds are widened by marginDays each side.
func BuildParams(w gcpsource.Window, marginDays int) Params {
	if marginDays < 0 {
		marginDays = 0
	}
	s, e := w.Start.UTC(), w.End.UTC()
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
	return Params{
		PartStart:  day(s).AddDate(0, 0, -marginDays).Format("2006-01-02"),
		PartEnd:    day(e).AddDate(0, 0, marginDays).Format("2006-01-02"),
		UsageStart: s,
		UsageEnd:   e,
	}
}

// JSON is the params_json stored with the provenance.
func (p Params) JSON() string {
	b, _ := json.Marshal(struct {
		PartStart  string `json:"part_start"`
		PartEnd    string `json:"part_end"`
		UsageStart string `json:"usage_start"`
		UsageEnd   string `json:"usage_end"`
	}{p.PartStart, p.PartEnd, p.UsageStart.Format(time.RFC3339), p.UsageEnd.Format(time.RFC3339)})
	return string(b)
}

// Query returns the BigQuery parameters.
func (p Params) Query() []bigquery.QueryParameter {
	date := func(v string) *bigquery.QueryParameterValue {
		return &bigquery.QueryParameterValue{Type: bigquery.StandardSQLDataType{TypeKind: "DATE"}, Value: v}
	}
	return []bigquery.QueryParameter{
		{Name: "part_start", Value: date(p.PartStart)},
		{Name: "part_end", Value: date(p.PartEnd)},
		{Name: "usage_start", Value: p.UsageStart},
		{Name: "usage_end", Value: p.UsageEnd},
	}
}

// UsageRow is one result row of BuildQuery.
type UsageRow struct {
	ProjectID    bigquery.NullString  `bigquery:"project_id"`
	Service      bigquery.NullString  `bigquery:"service"`
	SKU          bigquery.NullString  `bigquery:"sku"`
	ResourceName bigquery.NullString  `bigquery:"resource_name"`
	Usage        bigquery.NullFloat64 `bigquery:"usage_amount"`
	PricingUnit  bigquery.NullString  `bigquery:"pricing_unit"`
	Gross        bigquery.NullFloat64 `bigquery:"gross"`
	Credits      bigquery.NullFloat64 `bigquery:"credits"`
}

// groupMetrics lists, per group, the metrics of its row in sheet order.
var groupMetrics = []struct {
	group   string
	metrics []string
}{
	{gcpsource.GroupNetworking, []string{gcpsource.MLBHours, gcpsource.MOutboundGB, gcpsource.MInboundGB}},
	{gcpsource.GroupCloudRun, []string{gcpsource.MEgressInterGB}},
	{gcpsource.GroupGCS, []string{gcpsource.MDownloadAPACGB}},
}

func ruleFor(metric string) Rule {
	for _, r := range Rules {
		if r.Metric == metric {
			return r
		}
	}
	return Rule{}
}

// perResourceMetrics lists the per-service metrics of the Cloud Run group in
// column order (rules with PerResource set).
var perResourceMetrics = []string{
	gcpsource.MServiceMemActiveGiBs, gcpsource.MServiceMemIdleGiBs,
	gcpsource.MServiceCPUActiveSec, gcpsource.MServiceCPUIdleSec,
}

// serviceName reduces a billing resource.name to the Cloud Run service name
// (a defensive trim of any "projects/.../services/" style prefix).
func serviceName(resource string) string {
	if i := strings.LastIndex(resource, "/"); i >= 0 {
		resource = resource[i+1:]
	}
	return strings.TrimSpace(resource)
}

// Aggregate classifies rows and returns one MetricRow (Resource "") per
// project and group that has at least one classified project-level billing
// line, followed by one MetricRow per Cloud Run service (Resource = service)
// carrying the active / idle memory and CPU cells. Cells of such a row with
// no matching lines are gaps ("not billed" for inbound, "no data" otherwise),
// never zeros. skipped counts lines whose SKU matched but whose pricing unit
// was unexpected.
func Aggregate(rows []UsageRow, p gcpsource.Provenance) (out []gcpsource.MetricRow, skipped int) {
	type key struct{ project, group string }
	type skey struct{ project, service string }
	sums := map[key]map[string]float64{}
	svcSums := map[skey]map[string]float64{}
	for _, r := range rows {
		rule, ok := Classify(r.Service.StringVal, r.SKU.StringVal)
		if !ok {
			continue
		}
		if r.PricingUnit.StringVal != rule.PricingUnit {
			skipped++
			continue
		}
		if rule.PerResource {
			svc := serviceName(r.ResourceName.StringVal)
			if svc == "" {
				continue
			}
			k := skey{r.ProjectID.StringVal, svc}
			if svcSums[k] == nil {
				svcSums[k] = map[string]float64{}
			}
			svcSums[k][rule.Metric] += r.Usage.Float64
			continue
		}
		k := key{r.ProjectID.StringVal, rule.Group}
		if sums[k] == nil {
			sums[k] = map[string]float64{}
		}
		sums[k][rule.Metric] += r.Usage.Float64
	}
	keys := make([]key, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	order := map[string]int{}
	for i, g := range groupMetrics {
		order[g.group] = i
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].group != keys[j].group {
			return order[keys[i].group] < order[keys[j].group]
		}
		return keys[i].project < keys[j].project
	})
	for _, k := range keys {
		row := gcpsource.MetricRow{Group: k.group, Project: k.project, Cells: map[string]gcpsource.Cell{}}
		for _, g := range groupMetrics {
			if g.group != k.group {
				continue
			}
			for _, m := range g.metrics {
				rule := ruleFor(m)
				if v, ok := sums[k][m]; ok {
					row.Cells[m] = gcpsource.V(v, rule.Unit, p)
				} else if m == gcpsource.MInboundGB {
					row.Cells[m] = gcpsource.G(GapNotBilled, HintNotBilled, rule.Unit, p)
				} else {
					row.Cells[m] = gcpsource.G(GapNoData, HintNoData, rule.Unit, p)
				}
			}
		}
		out = append(out, row)
	}

	skeys := make([]skey, 0, len(svcSums))
	for k := range svcSums {
		skeys = append(skeys, k)
	}
	sort.Slice(skeys, func(i, j int) bool {
		if skeys[i].project != skeys[j].project {
			return skeys[i].project < skeys[j].project
		}
		return skeys[i].service < skeys[j].service
	})
	for _, k := range skeys {
		row := gcpsource.MetricRow{Group: gcpsource.GroupCloudRun, Project: k.project, Resource: k.service, Cells: map[string]gcpsource.Cell{}}
		for _, m := range perResourceMetrics {
			rule := ruleFor(m)
			if v, ok := svcSums[k][m]; ok {
				row.Cells[m] = gcpsource.V(v, rule.Unit, p)
			} else {
				row.Cells[m] = gcpsource.G(GapNoData, HintNoData, rule.Unit, p)
			}
		}
		out = append(out, row)
	}
	return out, skipped
}

// Fetch runs BuildQuery for window w against table (project.dataset.table)
// with the client's job project and returns the billing-usage metric rows.
// It never panics or returns an error: failures are classified into the Result.
func Fetch(ctx context.Context, client *bigquery.Client, table string, w gcpsource.Window, marginDays int, now func() time.Time) gcpsource.Result {
	res := gcpsource.Result{Source: Source, Started: now().UTC()}
	if err := ValidateTable(table); err != nil {
		res.Finish(now().UTC(), err, RoleHint, API)
		res.Hint = "set CLOUD0_BILLING_EXPORT_TABLE to project.dataset.table"
		return res
	}
	if !w.End.After(w.Start) {
		res.Finish(now().UTC(), ErrBadWindow, RoleHint, API)
		return res
	}
	if client == nil {
		res.Finish(now().UTC(), errors.New("billingusage: nil BigQuery client"), RoleHint, API)
		return res
	}
	sql := BuildQuery(table)
	params := BuildParams(w, marginDays)
	prov := gcpsource.Provenance{Source: Source, Query: sql, Aggregation: params.JSON(), Window: w}

	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	rows, err := run(ctx, client, sql, params)
	prov.FetchedAt = now().UTC()
	res.Provenance = []gcpsource.Provenance{prov}
	if err != nil {
		res.Finish(now().UTC(), fmt.Errorf("billingusage: run query: %w", err), RoleHint, API)
		return res
	}
	metrics, skipped := Aggregate(rows, prov)
	res.Metrics = metrics
	if skipped > 0 {
		res.Message = fmt.Sprintf("skipped %d billing lines with an unexpected pricing unit", skipped)
	}
	res.Finish(now().UTC(), nil, RoleHint, API)
	return res
}

func run(ctx context.Context, client *bigquery.Client, sql string, p Params) ([]UsageRow, error) {
	q := client.Query(sql)
	q.Parameters = p.Query()
	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}
	var rows []UsageRow
	for {
		var r UsageRow
		err := it.Next(&r)
		if errors.Is(err, iterator.Done) {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
}
