// Package billing is cloud0's BigQuery billing source: it builds the one
// parameterised, partition-filtered query over the detailed billing export
// (spec/architecture.md → "BigQuery query"), executes it, returns rows grouped
// by (ISO week, project, service, SKU, resource) with job statistics, and
// classifies provider errors into a code plus a fix hint
// (spec/capabilities/run-log.md).
//
// query.go is pure: no I/O, no clock.
package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

// tablePlaceholder is the token in sqlTemplate replaced by the validated
// table id; it mirrors the placeholder used in spec/architecture.md.
const tablePlaceholder = "<CLOUD0_BILLING_EXPORT_TABLE>"

// sqlTemplate is the exact text from spec/architecture.md → "BigQuery query".
// It is executed and stored verbatim (only the table is substituted), so
// provenance shows the executed SQL. Credits are summed per line item via a
// correlated subquery — never a JOIN, which would multiply cost by the number
// of credits on a line.
const sqlTemplate = "SELECT\n" +
	"  EXTRACT(ISOYEAR FROM usage_start_time) AS iso_year,\n" +
	"  EXTRACT(ISOWEEK FROM usage_start_time) AS iso_week,\n" +
	"  project.id            AS project_id,\n" +
	"  service.id            AS service_id,\n" +
	"  service.description   AS service,\n" +
	"  sku.id                AS sku_id,\n" +
	"  sku.description       AS sku,\n" +
	"  resource.name         AS resource_name,\n" +
	"  resource.global_name  AS resource_global_name,\n" +
	"  TO_JSON_STRING(labels)        AS labels_json,\n" +
	"  TO_JSON_STRING(system_labels) AS system_labels_json,\n" +
	"  SUM(cost)             AS gross,\n" +
	"  SUM((SELECT IFNULL(SUM(c.amount), 0) FROM UNNEST(credits) AS c)) AS credits,\n" +
	"  COUNT(*)              AS line_items\n" +
	"FROM `" + tablePlaceholder + "`\n" +
	"WHERE DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end\n" +
	"  AND usage_start_time >= @usage_start\n" +
	"  AND usage_start_time <  @usage_end\n" +
	"GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11"

// Parameter names as they appear in the SQL and in run_sources.params_json.
const (
	ParamPartStart  = "part_start"
	ParamPartEnd    = "part_end"
	ParamUsageStart = "usage_start"
	ParamUsageEnd   = "usage_end"
)

// dateLayout is the YYYY-MM-DD form used for the DATE parameters in provenance.
const dateLayout = "2006-01-02"

var (
	// ErrInvalidTable means the table id is not a plain project.dataset.table.
	ErrInvalidTable = errors.New("billing: invalid table id (want project.dataset.table, see CLOUD0_BILLING_EXPORT_TABLE in .env.example)")
	// ErrInvalidWindow means the week range or margin is not usable.
	ErrInvalidWindow = errors.New("billing: invalid window")
)

// Window is an inclusive range of ISO weeks plus the partition margin
// (CLOUD0_PARTITION_MARGIN_DAYS) applied on each side of the usage window
// when filtering _PARTITIONTIME.
type Window struct {
	From, To   isoweek.Week
	MarginDays int
}

// WeekWindow is the single-week window used by "Refresh" and the dry run.
func WeekWindow(w isoweek.Week, marginDays int) Window {
	return Window{From: w, To: w, MarginDays: marginDays}
}

// Validate reports whether both weeks exist, From <= To and the margin is >= 0.
func (w Window) Validate() error {
	switch {
	case !w.From.Valid():
		return fmt.Errorf("%w: from %s does not exist", ErrInvalidWindow, w.From)
	case !w.To.Valid():
		return fmt.Errorf("%w: to %s does not exist", ErrInvalidWindow, w.To)
	case isoweek.Compare(w.From, w.To) > 0:
		return fmt.Errorf("%w: from %s is after to %s", ErrInvalidWindow, w.From, w.To)
	case w.MarginDays < 0:
		return fmt.Errorf("%w: margin %d days is negative", ErrInvalidWindow, w.MarginDays)
	}
	return nil
}

// Weeks enumerates every ISO week in the window, ascending — one snapshot is
// written per entry, so weeks with no billing lines still get an empty one.
func (w Window) Weeks() ([]isoweek.Week, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return isoweek.Range(w.From, w.To)
}

// Params are the four named parameters sent with the query. PartStart and
// PartEnd are DATE values (midnight UTC, date part only); UsageStart and
// UsageEnd are TIMESTAMP values, Monday 00:00:00 UTC, [start, end).
type Params struct {
	PartStart  time.Time
	PartEnd    time.Time
	UsageStart time.Time
	UsageEnd   time.Time
}

// Map renders the parameters by name in the provenance form:
// dates as YYYY-MM-DD, timestamps as RFC3339 UTC.
func (p Params) Map() map[string]string {
	return map[string]string{
		ParamPartStart:  p.PartStart.UTC().Format(dateLayout),
		ParamPartEnd:    p.PartEnd.UTC().Format(dateLayout),
		ParamUsageStart: p.UsageStart.UTC().Format(time.RFC3339),
		ParamUsageEnd:   p.UsageEnd.UTC().Format(time.RFC3339),
	}
}

// MarshalJSON encodes Params as the flat object stored in run_sources.params_json.
func (p Params) MarshalJSON() ([]byte, error) { return json.Marshal(p.Map()) }

// Query is the executable statement: the exact SQL text, its parameters and
// the table it targets.
type Query struct {
	SQL    string
	Params Params
	Table  string
}

// ValidateTable checks that table is a plain project.dataset.table id with
// no quoting, whitespace or punctuation that could alter the SQL.
func ValidateTable(table string) error {
	if table == "" {
		return fmt.Errorf("%w: empty", ErrInvalidTable)
	}
	if !config.TablePattern.MatchString(table) {
		return ErrInvalidTable
	}
	return nil
}

// BuildQuery derives the parameters for the window and substitutes the
// validated table into the spec's SQL. It performs no I/O.
//
//	usage_start = Monday 00:00 UTC of window.From
//	usage_end   = Monday 00:00 UTC after window.To (exclusive)
//	part_start  = DATE(usage_start) − MarginDays
//	part_end    = DATE(usage_end)   + MarginDays
func BuildQuery(window Window, table string) (Query, error) {
	if err := window.Validate(); err != nil {
		return Query{}, err
	}
	if err := ValidateTable(table); err != nil {
		return Query{}, err
	}
	usageStart := window.From.Start()
	usageEnd := window.To.End()
	return Query{
		SQL:   strings.Replace(sqlTemplate, tablePlaceholder, table, 1),
		Table: table,
		Params: Params{
			PartStart:  usageStart.AddDate(0, 0, -window.MarginDays),
			PartEnd:    usageEnd.AddDate(0, 0, window.MarginDays),
			UsageStart: usageStart,
			UsageEnd:   usageEnd,
		},
	}, nil
}
