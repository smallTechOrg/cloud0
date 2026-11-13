package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

func errRow(project, svc string, c2, c4, c5 int64, by map[string]int64) gcpsource.ErrorRow {
	return gcpsource.ErrorRow{Source: gcpsource.ErrSrcCloudRun, Project: project, Service: svc,
		C2xx: i64(c2), C3xx: i64(0), C4xx: i64(c4), C5xx: i64(c5), ByCode: by, Provenance: p2prov("errors_cloudrun")}
}

// seedErrorBatch writes an error batch for a week under an existing run id.
func seedErrorBatch(t *testing.T, f *fixture, week string, runID int64, rows []gcpsource.ErrorRow) {
	t.Helper()
	_, err := store.InsertErrorRows(context.Background(), f.store.DB, store.NewErrorBatch{
		Week: isoweek.MustParse(week), FetchedAt: phase1Now, StatusAtFetch: "final", RunID: runID, Rows: rows})
	if err != nil {
		t.Fatal(err)
	}
}

func rowByService(t *testing.T, rows []any, source, svc string) map[string]any {
	t.Helper()
	for _, r := range rows {
		m := asMap(t, r)
		if m["source"] == source && m["service"] == svc {
			return m
		}
	}
	t.Fatalf("no %s row %q in %v", source, svc, rows)
	return nil
}

func TestErrorsTable_TriageChangeAndOrdering(t *testing.T) {
	f := p2Fixture(t)
	run := int64(num(asMap(t, f.refreshBody(t, `{"mode":"all"}`)["run"])["id"]))
	// Replace the week's stored batch with a newer one carrying known shapes, plus the prior week.
	seedErrorBatch(t, f, "2026-W40", run, []gcpsource.ErrorRow{
		errRow("project-a", "api", 100000, 10, 2, map[string]int64{"404": 10, "502": 2}),
		errRow("project-a", "scanned", 100000, 11000, 0, map[string]int64{"404": 11000}),
	})
	seedErrorBatch(t, f, "2026-W41", run, []gcpsource.ErrorRow{
		errRow("project-a", "quiet", 100000, 0, 0, map[string]int64{}),
		errRow("project-a", "scanned", 100000, 11550, 0, map[string]int64{"404": 11550}),
		errRow("project-a", "api", 10000, 40, 26, map[string]int64{"404": 5, "401": 35, "502": 26}),
		errRow("project-a", "blip", 100000, 0, 1, map[string]int64{"500": 1}),
		errRow("project-a", "newsvc", 2970, 0, 30, map[string]int64{"503": 30}),
		{Source: gcpsource.ErrSrcCloudRun, Project: "project-b", Service: "gap", Gap: "no data", Hint: "no requests", Provenance: p2prov("errors_cloudrun")},
	})
	_, tbl, _ := f.doJSON(t, http.MethodGet, "/api/v1/errors/table?week=2026-W41", "")
	rows := asList(t, tbl["rows"])
	if len(rows) != 6 {
		t.Fatalf("rows = %d", len(rows))
	}
	sum := asMap(t, tbl["summary"])
	if num(sum["needs_attention"]) != 2 || num(sum["watch"]) != 1 || num(sum["noise"]) != 1 || num(sum["healthy"]) != 2 {
		t.Fatalf("summary = %v", sum)
	}
	if h, _ := sum["headline"].(string); !strings.Contains(h, "newsvc") || !strings.Contains(h, "503") {
		t.Fatalf("headline = %q", sum["headline"])
	}

	api := rowByService(t, rows, "cloudrun", "api")
	if api["verdict"] != "needs_attention" || num(api["rank"]) != 2 {
		t.Fatalf("api = %v", api)
	}
	if r := asList(t, api["reasons"]); len(r) == 0 || !strings.HasPrefix(r[0].(string), "26 server errors (502 ×26)") {
		t.Fatalf("api reasons = %v", api["reasons"])
	}
	ch := asMap(t, api["change"])
	if ch["prev_week"] != "2026-W40" || num(ch["prev_5xx"]) != 2 || num(ch["ratio_5xx"]) != 13 || ch["prev_client_4xx"] == nil || num(asMap(t, ch["prev_by_code"])["502"]) != 2 {
		t.Fatalf("api change = %v", ch)
	}
	rates := asMap(t, api["rates"])
	if num(rates["requests"]) != 10066 || num(rates["rate_5xx"]) < 0.0025 || num(rates["rate_404"]) <= 0 || num(rates["rate_client_4xx"]) <= 0 {
		t.Fatalf("api rates = %v", rates)
	}

	if nv := rowByService(t, rows, "cloudrun", "newsvc"); nv["verdict"] != "needs_attention" || num(nv["rank"]) != 1 || nv["change"] != nil {
		t.Fatalf("newsvc (no prev row) = %v", nv)
	}
	sc := rowByService(t, rows, "cloudrun", "scanned")
	if sc["verdict"] != "noise" || num(sc["rank"]) != 4 || sc["change"] == nil {
		t.Fatalf("scanned = %v", sc)
	}
	if bl := rowByService(t, rows, "cloudrun", "blip"); bl["verdict"] != "watch" || num(bl["rank"]) != 3 {
		t.Fatalf("blip = %v", bl)
	}
	if q := rowByService(t, rows, "cloudrun", "quiet"); q["verdict"] != "healthy" || num(q["rank"]) != 0 {
		t.Fatalf("quiet = %v", q)
	}
	// ordering is attention (1: newsvc 30, 2: api 26), watch (3), noise (4).
	if g := rowByService(t, rows, "cloudrun", "gap"); g["verdict"] != "healthy" || g["change"] != nil || asMap(t, g["rates"])["requests"] != nil {
		t.Fatalf("gap = %v", g)
	}

	// Week without a stored prior week: change is null and the verdict still works.
	_, w40, _ := f.doJSON(t, http.MethodGet, "/api/v1/errors/table?week=2026-W40", "")
	for _, r := range asList(t, w40["rows"]) {
		if asMap(t, r)["change"] != nil {
			t.Fatalf("W40 row has change without a W39 batch: %v", r)
		}
	}
	// Empty week: zero summary, no rows.
	_, empty, _ := f.doJSON(t, http.MethodGet, "/api/v1/errors/table?week=2026-W30", "")
	es := asMap(t, empty["summary"])
	if num(es["needs_attention"])+num(es["watch"])+num(es["noise"])+num(es["healthy"]) != 0 || es["headline"] == "" || len(asList(t, empty["rows"])) != 0 {
		t.Fatalf("empty = %v", empty)
	}
}

func TestErrorsTable_FixtureRowsGetVerdicts(t *testing.T) {
	f := p2Fixture(t)
	f.refreshBody(t, `{"mode":"all"}`)
	_, tbl, _ := f.doJSON(t, http.MethodGet, "/api/v1/errors/table", "")
	rows := asList(t, tbl["rows"])
	sum := asMap(t, tbl["summary"])
	if int(num(sum["needs_attention"])+num(sum["watch"])+num(sum["noise"])+num(sum["healthy"])) != len(rows) {
		t.Fatalf("summary %v does not add up to %d rows", sum, len(rows))
	}
	cr := rowByService(t, rows, "cloudrun", "svc-a")
	if cr["verdict"] != "watch" || num(cr["rank"]) != 2 || cr["change"] != nil {
		t.Fatalf("svc-a = %v", cr)
	}
	er := rowByService(t, rows, "error_reporting", "svc-a")
	if er["verdict"] != "watch" {
		t.Fatalf("error reporting = %v", er)
	}
}
