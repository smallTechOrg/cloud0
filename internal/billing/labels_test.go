package billing

import (
	"strings"
	"testing"

	"cloud.google.com/go/bigquery"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

func TestSQLSelectsLabelsAndGroupsByThem(t *testing.T) {
	q := mustBuild(t, WeekWindow(isoweek.MustParse("2026-W42"), 1), placeholderTable)
	for _, want := range []string{
		"TO_JSON_STRING(labels)        AS labels_json",
		"TO_JSON_STRING(system_labels) AS system_labels_json",
		"GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11",
		"SUM((SELECT IFNULL(SUM(c.amount), 0) FROM UNNEST(credits) AS c)) AS credits", // correlated-credit pattern kept
	} {
		if !strings.Contains(q.SQL, want) {
			t.Errorf("SQL lacks %q", want)
		}
	}
	if strings.Contains(strings.ToUpper(q.SQL), "JOIN") {
		t.Error("credits must never be joined")
	}
}

func TestToRowCarriesLabelsAndNullStaysNil(t *testing.T) {
	r := bqRow{
		LabelsJSON:       bigquery.NullString{StringVal: `[{"key":"env","value":"prod"}]`, Valid: true},
		SystemLabelsJSON: bigquery.NullString{},
		Gross:            bigquery.NullFloat64{Float64: 3, Valid: true},
		Credits:          bigquery.NullFloat64{Float64: -1, Valid: true},
	}.toRow()
	if r.LabelsJSON == nil || *r.LabelsJSON != `[{"key":"env","value":"prod"}]` || r.SystemLabelsJSON != nil || r.Net != 2 {
		t.Fatalf("toRow = %+v", r)
	}
}

func TestRowLessBreaksTiesOnLabels(t *testing.T) {
	a := Row{ProjectID: "p", LabelsJSON: strp(`[{"key":"a","value":"1"}]`)}
	b := Row{ProjectID: "p", LabelsJSON: strp(`[{"key":"b","value":"1"}]`)}
	if !rowLess(a, b) || rowLess(b, a) {
		t.Error("labels must give a total order for identical grains")
	}
}

func strp(s string) *string { return &s }
