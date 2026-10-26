package smoke

// Phase 2 live smoke: the roadmap gate's real-API assertions, driven over HTTP
// against a real `cloud0 serve` with a temp DB and the real GCP credentials
// from .env. Refresh mode=all, then the smoke week, then check every Phase 2
// endpoint.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/testenv"
)

type p2Cell struct {
	Value *float64 `json:"value"`
	Gap   *string  `json:"gap"`
}

func postJSON(t *testing.T, client *http.Client, url string, body any, out any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	res, err := client.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("POST %s: not JSON (status %d): %.500s", url, res.StatusCode, b)
	}
	return res.StatusCode
}

func TestLive_Phase2Gate(t *testing.T) {
	testenv.RequireBigQuery(t)
	root := testenv.RepoRoot(t)
	smokeWeek := os.Getenv("CLOUD0_SMOKE_WEEK")
	if smokeWeek == "" {
		smokeWeek = defaultSmokeWeek
	}

	tmpDir, err := os.MkdirTemp("", "cloud0-smoke2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	port := testenv.FreePort(t)
	env := childEnv(filepath.Join(tmpDir, "cloud0-smoke.db"), port)

	// Rebuild the Go binary (the UI was built by `make build` / the Phase 1 smoke).
	runCmd(t, root, env, "make", "build-go")
	bin := filepath.Join(root, "bin", "cloud0")
	runCmd(t, root, env, bin, "migrate", "up")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serve := exec.CommandContext(ctx, bin, "serve")
	serve.Dir = root
	serve.Env = env
	stdout, stderr := &lineBuffer{}, &lineBuffer{}
	serve.Stdout, serve.Stderr = stdout, stderr
	if err := serve.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- serve.Wait() }()
	t.Cleanup(func() {
		_ = serve.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			cancel()
		}
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 400 * time.Second}

	deadline := time.Now().Add(90 * time.Second)
	for {
		var h map[string]any
		if res, err := client.Get(base + "/api/v1/health"); err == nil {
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			_ = json.Unmarshal(b, &h)
			if bq, ok := h["bigquery"].(map[string]any); ok && bq["status"] == "ok" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("health never ok: %v\nstderr:\n%s", h, stderr.String())
		}
		time.Sleep(250 * time.Millisecond)
	}

	// 1. refresh mode=all, then the smoke week (so its metrics/errors exist).
	type runResp struct {
		Run struct {
			ID      int64  `json:"id"`
			Status  string `json:"status"`
			Sources []struct {
				Source string `json:"source"`
				Status string `json:"status"`
			} `json:"sources"`
		} `json:"run"`
		MetricSnapshots []struct {
			Week string `json:"week"`
		} `json:"metric_snapshots"`
		MonthSnapshots []struct {
			Month string `json:"month"`
		} `json:"month_snapshots"`
	}
	var all runResp
	if code := postJSON(t, client, base+"/api/v1/refresh", map[string]string{"mode": "all"}, &all); code != 200 {
		t.Fatalf("refresh all: status %d", code)
	}
	if all.Run.Status != "ok" && all.Run.Status != "partial" {
		t.Fatalf("refresh all run.status = %q", all.Run.Status)
	}
	var week runResp
	if code := postJSON(t, client, base+"/api/v1/refresh", map[string]string{"mode": "week", "week": smokeWeek}, &week); code != 200 {
		t.Fatalf("refresh week: status %d", code)
	}
	if week.Run.Status != "ok" && week.Run.Status != "partial" {
		t.Fatalf("refresh week run.status = %q", week.Run.Status)
	}
	if len(week.MetricSnapshots) != 1 || week.MetricSnapshots[0].Week != smokeWeek {
		t.Fatalf("want one metric snapshot for %s, got %+v", smokeWeek, week.MetricSnapshots)
	}

	// 2. run log: exactly one row per source, the full source set, in the all run.
	wantSources := []string{
		"bigquery_billing", "bigquery_month", "monitoring_vm", "monitoring_cloudsql", "monitoring_cloudrun",
		"monitoring_gcs", "monitoring_logging", "bigquery_usage", "errors_cloudrun", "errors_error_reporting",
		"errors_load_balancer", "errors_nginx",
	}
	var runs struct {
		Runs []runResp_ `json:"runs"`
	}
	if code := getJSON(t, client, base+"/api/v1/runs?limit=10", &runs); code != 200 {
		t.Fatalf("GET /runs: %d", code)
	}
	var allRun *runResp_
	for i := range runs.Runs {
		if runs.Runs[i].ID == all.Run.ID {
			allRun = &runs.Runs[i]
		}
	}
	if allRun == nil {
		t.Fatalf("run %d not in /runs", all.Run.ID)
	}
	got := map[string]int{}
	for _, s := range allRun.Sources {
		got[s.Source]++
	}
	for _, s := range wantSources {
		if got[s] != 1 {
			t.Errorf("run log: source %s has %d rows, want exactly 1 (got %v)", s, got[s], got)
		}
	}
	if len(allRun.Sources) != len(wantSources) {
		t.Errorf("run log: %d source rows, want %d", len(allRun.Sources), len(wantSources))
	}

	// 3. metrics: CPU numeric for >= 1 VM; no cell has both/neither of value/gap; no gap is a 0.
	var metrics struct {
		Groups []struct {
			Group string `json:"group"`
			Rows  []struct {
				Resource string            `json:"resource"`
				Cells    map[string]p2Cell `json:"cells"`
			} `json:"rows"`
		} `json:"groups"`
	}
	if code := getJSON(t, client, base+"/api/v1/metrics/table?week="+smokeWeek, &metrics); code != 200 {
		t.Fatalf("GET /metrics/table: %d", code)
	}
	order := []string{"vm", "cloudsql", "networking", "cloudrun", "gcs", "logging", "errors"}
	if len(metrics.Groups) != len(order) {
		t.Fatalf("metrics groups = %d, want %d", len(metrics.Groups), len(order))
	}
	vmCPU := 0
	for i, g := range metrics.Groups {
		if g.Group != order[i] {
			t.Errorf("group %d = %s, want %s", i, g.Group, order[i])
		}
		for _, r := range g.Rows {
			for k, c := range r.Cells {
				if (c.Value == nil) == (c.Gap == nil) {
					t.Errorf("%s/%s/%s: exactly one of value/gap must be set: %+v", g.Group, r.Resource, k, c)
				}
			}
			if g.Group == "vm" && r.Resource != "" {
				if c := r.Cells["cpu_pct"]; c.Value != nil && *c.Value >= 0 {
					vmCPU++
				}
			}
		}
	}
	if vmCPU < 1 {
		t.Errorf("no VM has a numeric CPU %% for %s", smokeWeek)
	}

	// 4. errors: a Cloud Run row with numeric 4xx.
	var errs struct {
		Rows []struct {
			Source string `json:"source"`
			C4xx   *int64 `json:"c4xx"`
			C5xx   *int64 `json:"c5xx"`
		} `json:"rows"`
	}
	if code := getJSON(t, client, base+"/api/v1/errors/table?week="+smokeWeek, &errs); code != 200 {
		t.Fatalf("GET /errors/table: %d", code)
	}
	numeric := 0
	for _, r := range errs.Rows {
		if r.Source == "cloudrun" && r.C4xx != nil && r.C5xx != nil {
			numeric++
		}
	}
	if numeric < 1 {
		t.Errorf("no Cloud Run row with numeric 4xx and 5xx in %+v", errs.Rows)
	}

	// 5. limits: every non-stub entry has a status; 14 entries.
	var lim struct {
		Limits []struct {
			ID    string `json:"id"`
			Level string `json:"level"`
			Stub  bool   `json:"stub"`
		} `json:"limits"`
	}
	if code := getJSON(t, client, base+"/api/v1/limits?week="+smokeWeek, &lim); code != 200 {
		t.Fatalf("GET /limits: %d", code)
	}
	if len(lim.Limits) != 14 {
		t.Errorf("limits entries = %d, want 14", len(lim.Limits))
	}
	valid := map[string]bool{"ok": true, "near": true, "over": true, "stub": true, "no data": true}
	withStatus := 0
	for _, l := range lim.Limits {
		if !valid[l.Level] {
			t.Errorf("limit %s: unknown level %q", l.ID, l.Level)
		}
		if l.Level == "ok" || l.Level == "near" || l.Level == "over" {
			withStatus++
		}
	}
	if withStatus < 1 {
		t.Errorf("no limit has an ok/near/over status")
	}

	// 6. month roll-up: total = sum of the service rows, for every fetched month.
	var month struct {
		Columns []struct {
			Key  string `json:"key"`
			Kind string `json:"kind"`
		} `json:"columns"`
		Rows []struct {
			Month string `json:"month"`
			Cells map[string]struct {
				Net float64 `json:"net"`
			} `json:"cells"`
		} `json:"rows"`
	}
	if code := getJSON(t, client, base+"/api/v1/cost/month", &month); code != 200 {
		t.Fatalf("GET /cost/month: %d", code)
	}
	fetched := 0
	for _, r := range month.Rows {
		if r.Cells == nil {
			continue
		}
		var sum float64
		for _, c := range month.Columns {
			if c.Kind == "service" {
				sum += r.Cells[c.Key].Net
			}
		}
		if r.Cells["total"].Net > 0 {
			fetched++ // months before the billing export started are legitimately 0
		}
		if total := r.Cells["total"].Net; math.Abs(total-sum) > 0.005 {
			t.Errorf("month %s: total %.4f vs sum of service rows %.4f", r.Month, total, sum)
		}
	}
	if fetched < 1 {
		t.Errorf("no month has a positive total after refresh all")
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t.Logf("refresh all run %d (%s), sources %s; %d VM(s) with CPU; %d numeric Cloud Run rows; %d limits with status; %d months fetched",
		all.Run.ID, all.Run.Status, strings.Join(keys, ","), vmCPU, numeric, withStatus, fetched)
}

type runResp_ struct {
	ID      int64 `json:"id"`
	Sources []struct {
		Source string `json:"source"`
		Status string `json:"status"`
	} `json:"sources"`
}
