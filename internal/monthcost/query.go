package monthcost

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

// tablePattern is the spec's fully-qualified table shape
// (spec/architecture.md → BigQuery query).
var tablePattern = regexp.MustCompile(`^[A-Za-z0-9-]+\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)

var (
	// ErrInvalidTable means the table id is not a plain project.dataset.table.
	ErrInvalidTable = errors.New("monthcost: invalid table id (want project.dataset.table, see CLOUD0_BILLING_EXPORT_TABLE)")
	// ErrInvalidRange means from is after to or the margin is negative.
	ErrInvalidRange = errors.New("monthcost: invalid month range")
)

// BuildQuery returns the exact SQL executed (and stored as provenance) for
// the month roll-up: the Phase 1 shape grouped by UTC calendar month,
// invoice month, project and service. Credits are summed per line item via
// the correlated subquery, never a JOIN. The table is substituted verbatim;
// Fetch validates it first.
func BuildQuery(table string) string {
	return "SELECT\n" +
		"  FORMAT_TIMESTAMP('%Y-%m', usage_start_time, 'UTC') AS month,\n" +
		"  invoice.month         AS invoice_month,\n" +
		"  project.id            AS project_id,\n" +
		"  service.id            AS service_id,\n" +
		"  service.description   AS service,\n" +
		"  SUM(cost)             AS gross,\n" +
		"  SUM((SELECT IFNULL(SUM(c.amount), 0) FROM UNNEST(credits) AS c)) AS credits\n" +
		"FROM `" + table + "`\n" +
		"WHERE DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end\n" +
		"  AND usage_start_time >= @usage_start\n" +
		"  AND usage_start_time <  @usage_end\n" +
		"GROUP BY 1, 2, 3, 4, 5"
}

// Params are the four named query parameters.
//
//	usage_start = from.Start(), usage_end = to.Next().Start() (exclusive)
//	part_start  = DATE(usage_start) − margin, part_end = DATE(usage_end) + margin
type Params struct {
	PartStart  time.Time
	PartEnd    time.Time
	UsageStart time.Time
	UsageEnd   time.Time
}

// NewParams derives the parameters for the inclusive month range.
func NewParams(from, to Month, marginDays int) (Params, error) {
	if to.Before(from) {
		return Params{}, fmt.Errorf("%w: from %s is after to %s", ErrInvalidRange, from, to)
	}
	if marginDays < 0 {
		return Params{}, fmt.Errorf("%w: margin %d days is negative", ErrInvalidRange, marginDays)
	}
	us, ue := from.Start(), to.Next().Start()
	return Params{
		PartStart:  us.AddDate(0, 0, -marginDays),
		PartEnd:    ue.AddDate(0, 0, marginDays),
		UsageStart: us,
		UsageEnd:   ue,
	}, nil
}

// Map renders the parameters as stored in provenance: dates YYYY-MM-DD,
// timestamps RFC3339 UTC.
func (p Params) Map() map[string]string {
	return map[string]string{
		"part_start":  p.PartStart.UTC().Format("2006-01-02"),
		"part_end":    p.PartEnd.UTC().Format("2006-01-02"),
		"usage_start": p.UsageStart.UTC().Format(time.RFC3339),
		"usage_end":   p.UsageEnd.UTC().Format(time.RFC3339),
	}
}

// JSON is the params_json provenance string.
func (p Params) JSON() string {
	b, _ := json.Marshal(p.Map()) // map[string]string cannot fail to marshal
	return string(b)
}

// QueryParameters are the typed BigQuery parameters (DATE and TIMESTAMP).
func (p Params) QueryParameters() []bigquery.QueryParameter {
	return []bigquery.QueryParameter{
		{Name: "part_start", Value: civil.DateOf(p.PartStart.UTC())},
		{Name: "part_end", Value: civil.DateOf(p.PartEnd.UTC())},
		{Name: "usage_start", Value: p.UsageStart.UTC()},
		{Name: "usage_end", Value: p.UsageEnd.UTC()},
	}
}
