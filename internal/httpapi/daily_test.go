package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/smallTechOrg/cloud0/internal/billing"
)

func datedRow(date, week, project, service, sku string, resource *string, gross, credits float64) billing.Row {
	r := fakeRow(week, project, service, sku, resource, gross, credits)
	r.UsageDate = date
	return r
}

// W41 comes from an undated (pre-daily) source; W42 has days; W43 is never fetched.
var dailyRows = []billing.Row{
	fakeRow("2026-W41", "project-a", "Compute Engine", "N1 Core", strp("projects/1/instances/vm-a"), 100, 0),
	datedRow("2026-10-12", "2026-W42", "project-a", "Compute Engine", "N1 Core", strp("projects/1/instances/vm-a"), 100, 0),
	datedRow("2026-10-13", "2026-W42", "project-a", "Compute Engine", "N1 Core", strp("projects/1/instances/vm-a"), 50, 0),
	datedRow("2026-10-13", "2026-W42", "project-b", "Cloud SQL", "DB Hours", strp("projects/2/instances/db"), 60, 0),
	datedRow("2026-10-14", "2026-W42", "project-b", "Networking", "LB Rule", nil, 90, -30),
	datedRow("2026-10-14", "2026-W42", "project-a", "Cloud Monitoring", "Metrics", nil, 5, 0),
}

func dailyFixture(t *testing.T) *fixture {
	t.Helper()
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{rows: dailyRows}, &now)
	f.deps.Attribution = bdDeps()
	f.srv.Close()
	f.srv = httptest.NewServer(NewHandler(f.deps))
	t.Cleanup(f.srv.Close)
	f.refreshWeek(t, "2026-W41")
	f.refreshWeek(t, "2026-W42")
	return f
}

func seriesByKey(t *testing.T, m map[string]any) (map[string]map[string]any, []string) {
	out := map[string]map[string]any{}
	var order []string
	for _, s := range asList(t, m["series"]) {
		sm := asMap(t, s)
		out[sm["key"].(string)] = sm
		order = append(order, sm["key"].(string))
	}
	return out, order
}

func TestDaily_ServiceSeriesDaysGapsAndNotes(t *testing.T) {
	f := dailyFixture(t)
	res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/daily?from=2026-10-05&to=2026-10-25", "")
	if res.StatusCode != 200 {
		t.Fatalf("%d %v", res.StatusCode, m)
	}
	days := asList(t, m["days"])
	if len(days) != 21 || m["by"] != "service" {
		t.Fatalf("days = %d, by = %v", len(days), m["by"])
	}
	d0, d12, d14, d15, d19 := asMap(t, days[0]), asMap(t, days[7]), asMap(t, days[9]), asMap(t, days[10]), asMap(t, days[14])
	if d0["has_daily"] != false || d0["total"] != nil || d0["snapshot_id"] == nil {
		t.Errorf("W41 day (fetched without daily rows) = %v", d0)
	}
	if d12["date"] != "2026-10-12" || !near(num(d12["total"]), 100) || !near(num(d14["total"]), 65) || num(d15["total"]) != 0 {
		t.Errorf("W42 days = %v %v %v", d12, d14, d15)
	}
	if d19["week"] != "2026-W43" || d19["snapshot_id"] != nil || d19["total"] != nil {
		t.Errorf("W43 day (not fetched) = %v", d19)
	}
	if !near(num(m["total"]), 275) {
		t.Errorf("total = %v", m["total"])
	}
	s, order := seriesByKey(t, m)
	if strings.Join(order, ",") != "compute_engine,cloud_sql,networking,svc:Cloud Monitoring" {
		t.Errorf("series order = %v", order)
	}
	ce := asList(t, s["compute_engine"]["values"])
	if ce[0] != nil || !near(num(ce[7]), 100) || !near(num(ce[8]), 50) || num(ce[9]) != 0 || s["compute_engine"]["label"] != "Compute Engine" {
		t.Errorf("compute_engine = %v", s["compute_engine"])
	}
	notes := strings.Join(func() []string {
		var n []string
		for _, x := range asList(t, m["notes"]) {
			n = append(n, x.(string))
		}
		return n
	}(), " | ")
	if !strings.Contains(notes, "2026-W41") || !strings.Contains(notes, "Not fetched yet: 2026-W43") {
		t.Errorf("notes = %s", notes)
	}

	// Weekly snapshot and daily rows agree: W42 Σ days = the Cost table's total.
	_, tbl, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table?from=2026-W42&to=2026-W42", "")
	if wt := num(asMap(t, asMap(t, asMap(t, asList(t, tbl["rows"])[0])["cells"])["total"])["net"]); !near(wt, 275) {
		t.Errorf("weekly total %v ≠ Σ daily 275", wt)
	}
}

func TestDaily_TopOtherProjectProductAndErrors(t *testing.T) {
	f := dailyFixture(t)
	_, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/daily?from=2026-10-12&to=2026-10-18&top=1", "")
	s, order := seriesByKey(t, m)
	if strings.Join(order, ",") != "compute_engine,(other)" || !near(num(s["(other)"]["total"]), 125) || s["(other)"]["kind"] != "other" {
		t.Errorf("top=1 series = %v", order)
	}

	_, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/cost/daily?from=2026-10-12&to=2026-10-18&by=project&scope=project-b", "")
	s, order = seriesByKey(t, m)
	if strings.Join(order, ",") != "project-b" || !near(num(m["total"]), 120) {
		t.Errorf("project/scope series = %v total %v", order, m["total"])
	}

	_, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/cost/daily?from=2026-10-12&to=2026-10-18&by=product", "")
	s, order = seriesByKey(t, m)
	if strings.Join(order, ",") != "alpha,beta,(shared),(unattributed)" || !near(num(s["alpha"]["total"]), 150) || !near(num(s["(shared)"]["total"]), 60) {
		t.Errorf("product series = %v", order)
	}

	_, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/cost/daily?days=7&to=2026-10-18", "")
	if m["from"] != "2026-10-12" || len(asList(t, m["days"])) != 7 {
		t.Errorf("days=7: from %v, %d days", m["from"], len(asList(t, m["days"])))
	}

	for q, code := range map[string]string{
		"from=2026-13-01":               "invalid_date",
		"to=2026-11-01":                 "date_out_of_range",
		"from=2026-10-20&to=2026-10-12": "date_out_of_range",
		"by=sku":                        "invalid_dimension",
		"top=0":                         "invalid_top",
		"scope=elsewhere":               "invalid_scope",
	} {
		res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/daily?"+q, "")
		if res.StatusCode != 400 || asMap(t, m["error"])["code"] != code {
			t.Errorf("%s: %d %v", q, res.StatusCode, m)
		}
	}
}
