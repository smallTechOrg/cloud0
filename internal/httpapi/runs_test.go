package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRuns_EmptyListAndLimitValidation(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{}, &now)

	res, m, raw := f.doJSON(t, http.MethodGet, "/api/v1/runs", "")
	if res.StatusCode != 200 || !strings.Contains(string(raw), `"runs":[]`) {
		t.Fatalf("empty list = %d %s", res.StatusCode, raw)
	}
	for _, q := range []string{"limit=abc", "limit=0", "limit=-5", "limit=1.5"} {
		res, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/runs?"+q, "")
		expectError(t, res, m, 400, "invalid_request")
	}
	if res, _, _ = f.doJSON(t, http.MethodGet, "/api/v1/runs?limit=500", ""); res.StatusCode != 200 {
		t.Fatalf("limit above max must clamp, got %d", res.StatusCode)
	}
	res, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/runs/42", "")
	expectError(t, res, m, 404, "not_found")
	res, m, _ = f.doJSON(t, http.MethodGet, "/api/v1/runs/x", "")
	expectError(t, res, m, 404, "not_found")
}

func TestRuns_ListIsNewestFirstWithoutSQLAndDetailAddsSQL(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{rows: phase1Rows}, &now)
	first := f.refreshWeek(t, "2026-W41")
	now = now.Add(time.Minute)
	second := f.refreshWeek(t, "2026-W42")
	firstID := int64(num(first["run"].(map[string]any)["id"]))
	secondID := int64(num(second["run"].(map[string]any)["id"]))

	_, m, raw := f.doJSON(t, http.MethodGet, "/api/v1/runs?limit=1", "")
	runs := m["runs"].([]any)
	if len(runs) != 1 || int64(num(runs[0].(map[string]any)["id"])) != secondID {
		t.Fatalf("limit=1 must return the newest run: %v", runs)
	}
	_, m, raw = f.doJSON(t, http.MethodGet, "/api/v1/runs", "")
	runs = m["runs"].([]any)
	if len(runs) != 2 || int64(num(runs[0].(map[string]any)["id"])) != secondID || int64(num(runs[1].(map[string]any)["id"])) != firstID {
		t.Fatalf("runs order = %v", runs)
	}
	if strings.Contains(string(raw), `"sql"`) || strings.Contains(string(raw), `"params"`) {
		t.Fatal("GET /runs must omit sql and params")
	}
	r := runs[0].(map[string]any)
	for _, k := range []string{"id", "trigger", "from", "to", "started_at", "finished_at", "duration_ms", "status", "error", "sources", "snapshot_ids"} {
		if _, ok := r[k]; !ok {
			t.Fatalf("run lacks %q: %v", k, r)
		}
	}
	if ids := r["snapshot_ids"].([]any); len(ids) != 1 || int64(num(ids[0])) != int64(num(second["snapshots"].([]any)[0].(map[string]any)["id"])) {
		t.Fatalf("snapshot_ids = %v", r["snapshot_ids"])
	}
	if r["started_at"] != "2026-10-26T09:01:00Z" || r["status"] != "ok" {
		t.Fatalf("run = %v", r)
	}

	res, d, _ := f.doJSON(t, http.MethodGet, "/api/v1/runs/"+itoa(firstID), "")
	if res.StatusCode != 200 || int64(num(d["id"])) != firstID {
		t.Fatalf("detail = %d %v", res.StatusCode, d)
	}
	src := d["sources"].([]any)[0].(map[string]any)
	if sql, _ := src["sql"].(string); !strings.Contains(sql, "_PARTITIONTIME") {
		t.Fatalf("detail source lacks sql: %v", src)
	}
	params, _ := src["params"].(map[string]any)
	if len(params) != 4 || params["usage_start"] != "2026-10-05T00:00:00Z" {
		t.Fatalf("detail params = %v", src["params"])
	}
}
