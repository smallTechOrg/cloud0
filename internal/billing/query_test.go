package billing

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

// specSQL is the query text from spec/architecture.md → "BigQuery query",
// byte for byte, with the table placeholder the spec uses. BuildQuery must
// produce exactly this with the placeholder substituted — provenance shows
// the executed SQL, so no other formatting may be applied.
const specSQL = "SELECT\n" +
	"  EXTRACT(ISOYEAR FROM usage_start_time) AS iso_year,\n" +
	"  EXTRACT(ISOWEEK FROM usage_start_time) AS iso_week,\n" +
	"  project.id            AS project_id,\n" +
	"  service.id            AS service_id,\n" +
	"  service.description   AS service,\n" +
	"  sku.id                AS sku_id,\n" +
	"  sku.description       AS sku,\n" +
	"  resource.name         AS resource_name,\n" +
	"  resource.global_name  AS resource_global_name,\n" +
	"  SUM(cost)             AS gross,\n" +
	"  SUM((SELECT IFNULL(SUM(c.amount), 0) FROM UNNEST(credits) AS c)) AS credits,\n" +
	"  COUNT(*)              AS line_items\n" +
	"FROM `<CLOUD0_BILLING_EXPORT_TABLE>`\n" +
	"WHERE DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end\n" +
	"  AND usage_start_time >= @usage_start\n" +
	"  AND usage_start_time <  @usage_end\n" +
	"GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9"

// placeholderTable is a syntactically valid project.dataset.table that is
// not a real identifier (the repo is public).
const placeholderTable = "my-project.my_dataset.gcp_billing_export_resource_v1_XXXXXX_XXXXXX_XXXXXX"

func utc(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func mustBuild(t *testing.T, w Window, table string) Query {
	t.Helper()
	q, err := BuildQuery(w, table)
	if err != nil {
		t.Fatalf("BuildQuery(%+v, %q): %v", w, table, err)
	}
	return q
}

func TestBuildQuery_EmitsTheSpecSQLVerbatimWithTheTableSubstituted(t *testing.T) {
	q := mustBuild(t, WeekWindow(isoweek.MustParse("2026-W42"), 1), placeholderTable)

	want := strings.Replace(specSQL, "<CLOUD0_BILLING_EXPORT_TABLE>", placeholderTable, 1)
	if q.SQL != want {
		t.Fatalf("SQL differs from spec text.\n--- got ---\n%s\n--- want ---\n%s", q.SQL, want)
	}
	if !strings.Contains(q.SQL, "DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end") {
		t.Error("SQL lacks the partition filter")
	}
	if !strings.Contains(q.SQL, "usage_start_time >= @usage_start") || !strings.Contains(q.SQL, "usage_start_time <  @usage_end") {
		t.Error("SQL lacks the usage window filter")
	}
	if strings.Contains(q.SQL, "<CLOUD0_BILLING_EXPORT_TABLE>") {
		t.Error("placeholder was not substituted")
	}
	if strings.Contains(q.SQL, "JOIN") {
		t.Error("credits must be summed via a correlated subquery, never a JOIN that multiplies cost")
	}
	if q.Table != placeholderTable {
		t.Errorf("Table = %q, want %q", q.Table, placeholderTable)
	}
}

func TestBuildQuery_DerivesParamsForANormalWeekWithOneDayMargin(t *testing.T) {
	q := mustBuild(t, WeekWindow(isoweek.MustParse("2026-W42"), 1), placeholderTable)
	p := q.Params

	// 2026-W42 is Mon 2026-10-12 .. Sun 2026-10-18; exclusive end Mon 2026-10-19.
	if !p.UsageStart.Equal(utc(2026, time.October, 12)) {
		t.Errorf("usage_start = %s, want 2026-10-12T00:00:00Z", p.UsageStart.Format(time.RFC3339))
	}
	if !p.UsageEnd.Equal(utc(2026, time.October, 19)) {
		t.Errorf("usage_end = %s, want 2026-10-19T00:00:00Z", p.UsageEnd.Format(time.RFC3339))
	}
	if !p.PartStart.Equal(utc(2026, time.October, 11)) {
		t.Errorf("part_start = %s, want 2026-10-11", p.PartStart.Format("2006-01-02"))
	}
	if !p.PartEnd.Equal(utc(2026, time.October, 20)) {
		t.Errorf("part_end = %s, want 2026-10-20", p.PartEnd.Format("2006-01-02"))
	}
	if p.UsageStart.Weekday() != time.Monday || p.UsageEnd.Weekday() != time.Monday {
		t.Errorf("usage bounds must be Mondays: %s / %s", p.UsageStart.Weekday(), p.UsageEnd.Weekday())
	}
	if p.UsageStart.Location() != time.UTC || p.UsageEnd.Location() != time.UTC {
		t.Error("usage bounds must be UTC")
	}
}

func TestBuildQuery_HandlesW53AndTheYearRollover(t *testing.T) {
	cases := []struct {
		name                           string
		window                         Window
		usageStart, usageEnd           time.Time
		partStart, partEnd             time.Time
		wantWeeks                      int
		wantFirstWeekStr, wantLastWeek string
	}{
		{
			name:             "W53 of 2026 alone",
			window:           WeekWindow(isoweek.MustParse("2026-W53"), 1),
			usageStart:       utc(2026, time.December, 28),
			usageEnd:         utc(2027, time.January, 4),
			partStart:        utc(2026, time.December, 27),
			partEnd:          utc(2027, time.January, 5),
			wantWeeks:        1,
			wantFirstWeekStr: "2026-W53",
			wantLastWeek:     "2026-W53",
		},
		{
			name:             "range across the year boundary W52..W01",
			window:           Window{From: isoweek.MustParse("2026-W52"), To: isoweek.MustParse("2027-W01"), MarginDays: 1},
			usageStart:       utc(2026, time.December, 21),
			usageEnd:         utc(2027, time.January, 11),
			partStart:        utc(2026, time.December, 20),
			partEnd:          utc(2027, time.January, 12),
			wantWeeks:        3,
			wantFirstWeekStr: "2026-W52",
			wantLastWeek:     "2027-W01",
		},
		{
			name:             "W01 of 2026 starts in 2025",
			window:           WeekWindow(isoweek.MustParse("2026-W01"), 1),
			usageStart:       utc(2025, time.December, 29),
			usageEnd:         utc(2026, time.January, 5),
			partStart:        utc(2025, time.December, 28),
			partEnd:          utc(2026, time.January, 6),
			wantWeeks:        1,
			wantFirstWeekStr: "2026-W01",
			wantLastWeek:     "2026-W01",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := mustBuild(t, c.window, placeholderTable)
			p := q.Params
			if !p.UsageStart.Equal(c.usageStart) || !p.UsageEnd.Equal(c.usageEnd) {
				t.Errorf("usage window = [%s, %s), want [%s, %s)",
					p.UsageStart.Format(time.RFC3339), p.UsageEnd.Format(time.RFC3339),
					c.usageStart.Format(time.RFC3339), c.usageEnd.Format(time.RFC3339))
			}
			if !p.PartStart.Equal(c.partStart) || !p.PartEnd.Equal(c.partEnd) {
				t.Errorf("partition window = [%s, %s], want [%s, %s]",
					p.PartStart.Format("2006-01-02"), p.PartEnd.Format("2006-01-02"),
					c.partStart.Format("2006-01-02"), c.partEnd.Format("2006-01-02"))
			}
			weeks, err := c.window.Weeks()
			if err != nil {
				t.Fatalf("Weeks: %v", err)
			}
			if len(weeks) != c.wantWeeks || weeks[0].String() != c.wantFirstWeekStr || weeks[len(weeks)-1].String() != c.wantLastWeek {
				t.Errorf("Weeks = %v, want %d weeks %s..%s", weeks, c.wantWeeks, c.wantFirstWeekStr, c.wantLastWeek)
			}
		})
	}
}

func TestBuildQuery_AppliesThePartitionMarginOnBothSides(t *testing.T) {
	week := isoweek.MustParse("2026-W42")
	for _, margin := range []int{0, 1, 3} {
		q := mustBuild(t, WeekWindow(week, margin), placeholderTable)
		wantStart := utc(2026, time.October, 12).AddDate(0, 0, -margin)
		wantEnd := utc(2026, time.October, 19).AddDate(0, 0, margin)
		if !q.Params.PartStart.Equal(wantStart) {
			t.Errorf("margin %d: part_start = %s, want %s", margin, q.Params.PartStart.Format("2006-01-02"), wantStart.Format("2006-01-02"))
		}
		if !q.Params.PartEnd.Equal(wantEnd) {
			t.Errorf("margin %d: part_end = %s, want %s", margin, q.Params.PartEnd.Format("2006-01-02"), wantEnd.Format("2006-01-02"))
		}
		// The usage window never moves with the margin.
		if !q.Params.UsageStart.Equal(utc(2026, time.October, 12)) || !q.Params.UsageEnd.Equal(utc(2026, time.October, 19)) {
			t.Errorf("margin %d moved the usage window", margin)
		}
	}
}

func TestBuildQuery_RejectsUnsafeTableNames(t *testing.T) {
	bad := []string{
		"",
		"`my-project.my_dataset.my_table`",
		"my-project.my_dataset.my table",
		"my-project.my_dataset",
		"my-project.my_dataset.my_table` WHERE 1=1 --",
		"my-project.my_dataset.my_table;DROP",
		"my project.my_dataset.my_table",
	}
	for _, table := range bad {
		_, err := BuildQuery(WeekWindow(isoweek.MustParse("2026-W42"), 1), table)
		if err == nil {
			t.Errorf("BuildQuery accepted table %q", table)
			continue
		}
		if !errors.Is(err, ErrInvalidTable) {
			t.Errorf("table %q: error %v is not ErrInvalidTable", table, err)
		}
		if strings.Contains(err.Error(), table) && table != "" {
			t.Errorf("table %q: error message should not echo the raw table text", table)
		}
	}
}

func TestBuildQuery_RejectsInvalidWindows(t *testing.T) {
	cases := []struct {
		name string
		w    Window
	}{
		{"from after to", Window{From: isoweek.MustParse("2026-W43"), To: isoweek.MustParse("2026-W42"), MarginDays: 1}},
		{"negative margin", WeekWindow(isoweek.MustParse("2026-W42"), -1)},
		{"nonexistent week", Window{From: isoweek.Week{Year: 2027, Week: 53}, To: isoweek.Week{Year: 2027, Week: 53}, MarginDays: 1}},
		{"zero window", Window{}},
	}
	for _, c := range cases {
		_, err := BuildQuery(c.w, placeholderTable)
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		if !errors.Is(err, ErrInvalidWindow) {
			t.Errorf("%s: error %v is not ErrInvalidWindow", c.name, err)
		}
	}
}

func TestParams_SerialiseToTheFourProvenanceKeys(t *testing.T) {
	q := mustBuild(t, WeekWindow(isoweek.MustParse("2026-W42"), 1), placeholderTable)

	m := q.Params.Map()
	want := map[string]string{
		"part_start":  "2026-10-11",
		"part_end":    "2026-10-20",
		"usage_start": "2026-10-12T00:00:00Z",
		"usage_end":   "2026-10-19T00:00:00Z",
	}
	if len(m) != 4 {
		t.Fatalf("Map has %d keys, want 4: %v", len(m), m)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("Map[%q] = %q, want %q", k, m[k], v)
		}
	}

	raw, err := json.Marshal(q.Params)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var back map[string]string
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("params JSON is not a flat string object: %v (%s)", err, raw)
	}
	for k, v := range want {
		if back[k] != v {
			t.Errorf("JSON[%q] = %q, want %q", k, back[k], v)
		}
	}
}

func TestQueryParameters_AreTypedDateAndTimestamp(t *testing.T) {
	q := mustBuild(t, WeekWindow(isoweek.MustParse("2026-W42"), 1), placeholderTable)

	params := queryParameters(q.Params)
	if len(params) != 4 {
		t.Fatalf("got %d query parameters, want 4", len(params))
	}
	byName := map[string]any{}
	for _, p := range params {
		byName[p.Name] = p.Value
	}
	for _, name := range []string{"part_start", "part_end"} {
		d, ok := byName[name].(civil.Date)
		if !ok {
			t.Errorf("%s is %T, want civil.Date (BigQuery DATE)", name, byName[name])
			continue
		}
		if name == "part_start" && d.String() != "2026-10-11" {
			t.Errorf("part_start = %s", d)
		}
		if name == "part_end" && d.String() != "2026-10-20" {
			t.Errorf("part_end = %s", d)
		}
	}
	for _, name := range []string{"usage_start", "usage_end"} {
		ts, ok := byName[name].(time.Time)
		if !ok {
			t.Errorf("%s is %T, want time.Time (BigQuery TIMESTAMP)", name, byName[name])
			continue
		}
		if ts.Location() != time.UTC {
			t.Errorf("%s not UTC", name)
		}
	}
}
