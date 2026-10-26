package billingusage

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

var w42 = gcpsource.Window{
	Start: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
}

func fixedNow() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }

func TestBuildQuery_HasPartitionFilterCreditSubqueryAndSKUFilter(t *testing.T) {
	q := BuildQuery("p-1.ds.tbl")
	for _, want := range []string{
		"FROM `p-1.ds.tbl`",
		"WHERE DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end",
		"AND usage_start_time >= @usage_start",
		"AND usage_start_time <  @usage_end",
		"SUM((SELECT IFNULL(SUM(c.amount), 0) FROM UNNEST(credits) AS c)) AS credits",
		"SUM(usage.amount_in_pricing_units) AS usage_amount",
		"ANY_VALUE(usage.pricing_unit)      AS pricing_unit",
		"SUM(cost)             AS gross",
		"project.id            AS project_id",
		"resource.name         AS resource_name",
		"GROUP BY 1, 2, 3, 4",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q\n%s", want, q)
		}
	}
	if strings.Contains(strings.ToUpper(q), "JOIN UNNEST") {
		t.Error("query must not LEFT JOIN UNNEST(credits)")
	}
	for _, r := range Rules {
		if !strings.Contains(q, "'"+r.Pattern+"'") {
			t.Errorf("query does not restrict to rule pattern %q", r.Pattern)
		}
	}
	if q != BuildQuery("p-1.ds.tbl") {
		t.Error("BuildQuery is not deterministic")
	}
}

func TestValidateTable_RejectsInjectionAndAcceptsQualifiedName(t *testing.T) {
	for _, ok := range []string{"proj-1.dataset_x.table_1", "a.b.c"} {
		if err := ValidateTable(ok); err != nil {
			t.Errorf("ValidateTable(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ds.tbl", "p.d.t; DROP", "p.d.t`", "p.d-x.t", "p.d.t.x"} {
		if err := ValidateTable(bad); !errors.Is(err, ErrBadTable) {
			t.Errorf("ValidateTable(%q) = %v, want ErrBadTable", bad, err)
		}
	}
}

func TestRules_PatternsAreSafeSQLLiteralsAndCoverEveryMetric(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Rules {
		for _, s := range append([]string{r.Pattern}, r.Services...) {
			if strings.ContainsAny(s, `'"\`+"`") || s == "" {
				t.Errorf("unsafe or empty literal %q", s)
			}
		}
		if r.Why == "" || r.Unit == "" || r.PricingUnit == "" {
			t.Errorf("rule %s incomplete", r.Metric)
		}
		seen[r.Metric] = true
	}
	for _, m := range []string{gcpsource.MLBHours, gcpsource.MOutboundGB, gcpsource.MInboundGB, gcpsource.MEgressInterGB, gcpsource.MDownloadAPACGB} {
		if !seen[m] {
			t.Errorf("no rule for %s", m)
		}
	}
}

// Real SKU descriptions observed in the detailed export for ISO 2026-W42.
func TestClassify_MapsRealSKUDescriptionsToCells(t *testing.T) {
	cases := []struct {
		service, sku, metric string // metric "" = not classified
	}{
		{"Networking", "Cloud Load Balancer Forwarding Rule Minimum Global", gcpsource.MLBHours},
		{"Networking", "Cloud Load Balancer Forwarding Rule Additional Global", gcpsource.MLBHours},
		{"Compute Engine", "Network Load Balancing: Forwarding Rule Minimum Service Charge in Americas", gcpsource.MLBHours},
		{"Networking", "Global External Application Load Balancer Outbound Data Processing for Iowa (us-central1)", gcpsource.MOutboundGB},
		{"Networking", "Global External Application Load Balancer Inbound Data Processing for Iowa (us-central1)", gcpsource.MInboundGB},
		{"Cloud Run", "Cloud Run Network Internet Data Transfer Out Intercontinental (Excl Oceania, Africa and China)", gcpsource.MEgressInterGB},
		{"Cloud Run", "Cloud Run Network Internet Data Transfer Out Intercontinental from/to Oceania", gcpsource.MEgressInterGB},
		{"Cloud Run", "Cloud Run Network Internet Data Transfer Out Intercontinental to China", gcpsource.MEgressInterGB},
		{"Cloud Storage", "Download APAC", gcpsource.MDownloadAPACGB},
		// Not classified: Google-internal transfer, other destinations, other services.
		{"Cloud Run", "Cloud Run Network Inter Region Data Transfer Out Intercontinental (Excl Oceania)", ""},
		{"Cloud Run", "Cloud Run Network Internet Data Transfer Out North America to North America", ""},
		{"Cloud Storage", "Download Worldwide Destinations (excluding Asia & Australia)", ""},
		{"Networking", "Network Intelligence Center Network Analyzer Resource Hours", ""},
		{"Compute Engine", "Network Internet Data Transfer Out from Mumbai to Apac(Excluding Korea and Indonesia)", ""},
		{"Compute Engine", "Network Inter Zone Data Transfer In", ""},
		{"Cloud Storage", "Download APAC (extra)", ""},
		{"Cloud Run", "Download APAC", ""},
	}
	for _, c := range cases {
		r, ok := Classify(c.service, c.sku)
		got := ""
		if ok {
			got = r.Metric
		}
		if got != c.metric {
			t.Errorf("Classify(%q, %q) = %q, want %q", c.service, c.sku, got, c.metric)
		}
	}
}

func TestBuildParams_WidensPartitionByMarginAndKeepsUsageWindow(t *testing.T) {
	p := BuildParams(w42, 1)
	if p.PartStart != "2026-10-11" || p.PartEnd != "2026-10-20" {
		t.Fatalf("partition = %s..%s", p.PartStart, p.PartEnd)
	}
	if !p.UsageStart.Equal(w42.Start) || !p.UsageEnd.Equal(w42.End) {
		t.Fatalf("usage = %v..%v", p.UsageStart, p.UsageEnd)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(p.JSON()), &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"part_start": "2026-10-11", "part_end": "2026-10-20", "usage_start": "2026-10-12T00:00:00Z", "usage_end": "2026-10-19T00:00:00Z"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("params_json[%s] = %q, want %q", k, m[k], v)
		}
	}
	if q := p.Query(); len(q) != 4 {
		t.Errorf("Query() has %d params", len(q))
	}
	if z := BuildParams(w42, -3); z.PartStart != "2026-10-12" || z.PartEnd != "2026-10-19" {
		t.Errorf("negative margin not clamped: %+v", z)
	}
}

func row(project, service, sku, unit string, v float64) UsageRow {
	var r UsageRow
	r.ProjectID.StringVal, r.ProjectID.Valid = project, true
	r.Service.StringVal, r.Service.Valid = service, true
	r.SKU.StringVal, r.SKU.Valid = sku, true
	r.PricingUnit.StringVal, r.PricingUnit.Valid = unit, true
	r.Usage.Float64, r.Usage.Valid = v, true
	return r
}

func TestAggregate_SumsPerProjectAndEmitsGapsNotZeros(t *testing.T) {
	prov := gcpsource.Provenance{Source: Source, Query: "SQL", Aggregation: "{}"}
	rows := []UsageRow{
		row("p1", "Networking", "Cloud Load Balancer Forwarding Rule Minimum Global", "hour", 100),
		row("p1", "Networking", "Cloud Load Balancer Forwarding Rule Minimum Global", "hour", 68),
		row("p1", "Networking", "Global External Application Load Balancer Outbound Data Processing for Iowa (us-central1)", "gibibyte", 0.2),
		row("p1", "Networking", "Global External Application Load Balancer Outbound Data Processing for Iowa (us-central1)", "gibibyte", 0.05),
		row("p1", "Cloud Run", "Cloud Run Network Internet Data Transfer Out Intercontinental to China", "gibibyte", 0.5),
		row("p1", "Cloud Run", "Cloud Run Network Internet Data Transfer Out Intercontinental from/to Oceania", "gibibyte", 0.25),
		row("p2", "Cloud Storage", "Download APAC", "gibibyte", 0.09),
		row("p2", "Cloud Storage", "Download APAC", "mebibyte", 99), // unexpected unit → skipped
		row("p2", "Cloud Storage", "Regional Standard Class A Operations", "count", 27),
	}
	out, skipped := Aggregate(rows, prov)
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if len(out) != 3 {
		t.Fatalf("rows = %d, want 3: %+v", len(out), out)
	}
	if out[0].Group != gcpsource.GroupNetworking || out[1].Group != gcpsource.GroupCloudRun || out[2].Group != gcpsource.GroupGCS {
		t.Fatalf("group order = %s,%s,%s", out[0].Group, out[1].Group, out[2].Group)
	}
	net := out[0]
	if net.Project != "p1" || net.Resource != "" {
		t.Errorf("networking row = %q/%q", net.Project, net.Resource)
	}
	assertVal(t, net.Cells[gcpsource.MLBHours], 168, "hours")
	assertVal(t, net.Cells[gcpsource.MOutboundGB], 0.25, "GB")
	in := net.Cells[gcpsource.MInboundGB]
	if in.Value != nil || in.Gap != GapNotBilled || in.Hint != HintNotBilled || in.Unit != "GB" {
		t.Errorf("inbound cell = %+v, want not-billed gap", in)
	}
	assertVal(t, out[1].Cells[gcpsource.MEgressInterGB], 0.75, "GB")
	assertVal(t, out[2].Cells[gcpsource.MDownloadAPACGB], 0.09, "GB")
	if out[2].Project != "p2" || len(out[2].Cells) != 1 {
		t.Errorf("gcs row = %+v", out[2])
	}
	if net.Cells[gcpsource.MLBHours].Provenance.Query != "SQL" {
		t.Error("cell provenance not carried")
	}
}

func TestAggregate_MissingNonInboundMetricIsNoDataGap(t *testing.T) {
	out, _ := Aggregate([]UsageRow{row("p1", "Networking", "Global External Application Load Balancer Inbound Data Processing for Iowa (us-central1)", "gibibyte", 0.1)}, gcpsource.Provenance{})
	if len(out) != 1 {
		t.Fatalf("rows = %d", len(out))
	}
	for _, m := range []string{gcpsource.MLBHours, gcpsource.MOutboundGB} {
		c := out[0].Cells[m]
		if c.Value != nil || c.Gap != GapNoData {
			t.Errorf("%s = %+v, want no-data gap", m, c)
		}
	}
	assertVal(t, out[0].Cells[gcpsource.MInboundGB], 0.1, "GB")
}

func TestAggregate_EmptyInputYieldsNoRows(t *testing.T) {
	out, skipped := Aggregate(nil, gcpsource.Provenance{})
	if len(out) != 0 || skipped != 0 {
		t.Errorf("out=%v skipped=%d", out, skipped)
	}
}

func TestFetch_InvalidTableFailsWithoutCallingBigQuery(t *testing.T) {
	res := Fetch(context.Background(), nil, "bad`table", w42, 1, fixedNow)
	if res.Source != Source || res.Status != gcpsource.StatusFailed || !strings.Contains(res.Hint, "CLOUD0_BILLING_EXPORT_TABLE") {
		t.Errorf("result = %+v", res)
	}
	if res.Started.IsZero() || res.Finished.IsZero() || len(res.Metrics) != 0 {
		t.Errorf("result not stamped or has metrics: %+v", res)
	}
}

func TestFetch_InvertedWindowFails(t *testing.T) {
	res := Fetch(context.Background(), nil, "p.d.t", gcpsource.Window{Start: w42.End, End: w42.Start}, 1, fixedNow)
	if res.Status != gcpsource.StatusFailed || !strings.Contains(res.Message, "window") {
		t.Errorf("result = %+v", res)
	}
}

func assertVal(t *testing.T, c gcpsource.Cell, want float64, unit string) {
	t.Helper()
	if c.Value == nil {
		t.Fatalf("cell is gap %q, want %v", c.Gap, want)
	}
	if math.Abs(*c.Value-want) > 1e-9 || c.Unit != unit {
		t.Errorf("cell = %v %s, want %v %s", *c.Value, c.Unit, want, unit)
	}
}
