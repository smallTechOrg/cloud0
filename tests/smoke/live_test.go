// Package smoke is the Phase 1 live smoke: `make build`, then a real
// `cloud0 migrate up && cloud0 serve` on an OS-assigned port with a temp DB and
// the real BigQuery credentials from .env, driven over HTTP exactly like the
// UI does: health (incl. the startup dry-run) -> refresh the smoke week ->
// cost table -> embedded UI assets -> JSON log lines.
package smoke

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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/testenv"
)

// defaultSmokeWeek mirrors config.DefaultSmokeWeek (CLOUD0_SMOKE_WEEK unset).
const defaultSmokeWeek = "2026-W42"

type lineBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lineBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lineBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := strings.TrimSpace(b.buf.String())
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func (b *lineBuffer) String() string { return strings.Join(b.Lines(), "\n") }

func childEnv(dbPath string, port int) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		switch strings.SplitN(kv, "=", 2)[0] {
		case "CLOUD0_PORT", "CLOUD0_DB_PATH", "CLOUD0_BIND", "PORT":
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"CLOUD0_DB_PATH="+dbPath,
		"CLOUD0_PORT="+strconv.Itoa(port),
		"CLOUD0_BIND=127.0.0.1",
		"GO111MODULE=on", "GOTOOLCHAIN=local",
	)
}

func runCmd(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := string(out)
		if len(tail) > 4000 {
			tail = tail[len(tail)-4000:]
		}
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, tail)
	}
	return string(out)
}

func getJSON(t *testing.T, client *http.Client, url string, out any) int {
	t.Helper()
	res, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("GET %s: not JSON (status %d): %.500s", url, res.StatusCode, body)
	}
	return res.StatusCode
}

func TestLive_RefreshSmokeWeekEndToEnd(t *testing.T) {
	testenv.RequireBigQuery(t) // loads .env; skips (gate NOT green) only if vars are absent
	root := testenv.RepoRoot(t)

	smokeWeek := os.Getenv("CLOUD0_SMOKE_WEEK")
	if smokeWeek == "" {
		smokeWeek = defaultSmokeWeek
	}

	// Temp DB in a directory we delete explicitly (and assert gone) at the end.
	tmpDir, err := os.MkdirTemp("", "cloud0-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	dbPath := filepath.Join(tmpDir, "cloud0-smoke.db")
	port := testenv.FreePort(t)
	env := childEnv(dbPath, port)

	// 1. make build (pnpm install + pnpm build + sync + go build) -> ./bin/cloud0
	runCmd(t, root, env, "make", "build")
	bin := filepath.Join(root, "bin", "cloud0")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("make build did not produce %s: %v", bin, err)
	}

	// 2. ./bin/cloud0 migrate up
	runCmd(t, root, env, bin, "migrate", "up")

	// 3. ./bin/cloud0 serve
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serve := exec.CommandContext(ctx, bin, "serve")
	serve.Dir = root
	serve.Env = env
	stdout := &lineBuffer{}
	stderr := &lineBuffer{}
	serve.Stdout = stdout
	serve.Stderr = stderr
	if err := serve.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- serve.Wait() }()
	stopped := false
	stop := func() error {
		if stopped {
			return nil
		}
		stopped = true
		_ = serve.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			return err
		case <-time.After(15 * time.Second):
			cancel()
			return fmt.Errorf("serve did not exit within 15s of SIGTERM")
		}
	}
	t.Cleanup(func() { _ = stop() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 150 * time.Second} // refresh has a 120 s server-side timeout

	// 4. wait for health, including bigquery.status == "ok" (startup dry-run)
	var health map[string]any
	deadline := time.Now().Add(90 * time.Second)
	for {
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("serve exited early: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		default:
		}
		health = nil
		res, err := client.Get(base + "/api/v1/health")
		if err == nil {
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			_ = json.Unmarshal(body, &health)
			if bq, ok := health["bigquery"].(map[string]any); ok {
				if bq["status"] == "ok" {
					break
				}
				if bq["status"] == "error" {
					t.Fatalf("startup BigQuery dry-run failed: %v", bq)
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("health never reported bigquery.status==ok within 90s; last health: %v\nstdout:\n%s", health, stdout.String())
		}
		time.Sleep(250 * time.Millisecond)
	}
	if health["ok"] != true || health["db"] != "ok" || health["migrations"] != "current" || health["ui"] != "real" {
		t.Fatalf("health: %v", health)
	}

	// 5. POST /api/v1/refresh for the smoke week
	reqBody, _ := json.Marshal(map[string]string{"mode": "week", "week": smokeWeek})
	start := time.Now()
	res, err := client.Post(base+"/api/v1/refresh", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /refresh: %v", err)
	}
	rawRefresh, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("POST /refresh status %d: %s", res.StatusCode, rawRefresh)
	}
	var refresh struct {
		Run struct {
			ID      int64            `json:"id"`
			Status  string           `json:"status"`
			Error   any              `json:"error"`
			Sources []map[string]any `json:"sources"`
		} `json:"run"`
		Snapshots []struct {
			ID       int64   `json:"id"`
			Week     string  `json:"week"`
			NetTotal float64 `json:"net_total"`
		} `json:"snapshots"`
	}
	if err := json.Unmarshal(rawRefresh, &refresh); err != nil {
		t.Fatalf("refresh body: %v: %s", err, rawRefresh)
	}
	if refresh.Run.Status != "ok" {
		t.Fatalf("run.status = %q, want ok: %s", refresh.Run.Status, rawRefresh)
	}
	if len(refresh.Snapshots) != 1 || refresh.Snapshots[0].Week != smokeWeek {
		t.Fatalf("want one snapshot for %s, got %+v", smokeWeek, refresh.Snapshots)
	}
	t.Logf("refresh %s: run %d ok in %s, snapshot %d net_total %.2f, source %v",
		smokeWeek, refresh.Run.ID, time.Since(start).Round(time.Millisecond), refresh.Snapshots[0].ID,
		refresh.Snapshots[0].NetTotal, refresh.Run.Sources)

	// 6. GET /api/v1/cost/table
	var table struct {
		Columns []struct {
			Key  string `json:"key"`
			Kind string `json:"kind"`
		} `json:"columns"`
		Rows []struct {
			Week  string `json:"week"`
			Cells map[string]struct {
				Net float64 `json:"net"`
			} `json:"cells"`
		} `json:"rows"`
	}
	if code := getJSON(t, client, base+"/api/v1/cost/table?from="+smokeWeek+"&to="+smokeWeek, &table); code != 200 {
		t.Fatalf("GET /cost/table status %d", code)
	}
	if len(table.Rows) != 1 || table.Rows[0].Week != smokeWeek || table.Rows[0].Cells == nil {
		t.Fatalf("cost table for %s: %+v", smokeWeek, table.Rows)
	}
	cells := table.Rows[0].Cells
	if ce := cells["compute_engine"].Net; !(ce > 0) {
		t.Errorf("Compute Engine net = %v, want > 0", ce)
	}
	total := cells["total"].Net
	if !(total > 0) {
		t.Errorf("Total GCP net = %v, want > 0", total)
	}
	var sum float64
	nServices := 0
	for _, c := range table.Columns {
		if c.Kind == "service" {
			sum += cells[c.Key].Net
			nServices++
		}
	}
	if nServices == 0 || math.Abs(total-sum) > 0.005 {
		t.Errorf("Total %.4f != sum of %d service cells %.4f (diff %.6f)", total, nServices, sum, total-sum)
	}
	t.Logf("cost table %s: Compute Engine %.2f, Total %.2f, sum of %d services %.2f",
		smokeWeek, cells["compute_engine"].Net, total, nServices, sum)

	// 7. GET / is the real UI and every linked stylesheet/script is live
	res, err = client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	index, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(index), `data-cloud0-ui="real"`) {
		t.Fatalf("GET / status %d, lacks data-cloud0-ui=\"real\":\n%.500s", res.StatusCode, index)
	}
	css := regexp.MustCompile(`<link[^>]+rel="stylesheet"[^>]+href="([^"]+)"`).FindAllStringSubmatch(string(index), -1)
	js := regexp.MustCompile(`<script[^>]+src="([^"]+)"`).FindAllStringSubmatch(string(index), -1)
	if len(css) == 0 || len(js) == 0 {
		t.Fatalf("index links %d stylesheets and %d scripts; want at least one each", len(css), len(js))
	}
	for _, m := range append(css, js...) {
		href := m[1]
		if strings.HasPrefix(href, "http") {
			continue
		}
		r, err := client.Get(base + href)
		if err != nil {
			t.Fatalf("asset %s: %v", href, err)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 || len(body) == 0 {
			t.Errorf("asset %s: status %d, %d bytes", href, r.StatusCode, len(body))
			continue
		}
		if strings.HasSuffix(href, ".css") && !strings.Contains(string(body), "{") {
			t.Errorf("stylesheet %s has no rules", href)
		}
	}

	// 8. JSON log lines: request + refresh.done (and the rest of the refresh trail)
	time.Sleep(200 * time.Millisecond)
	seen := map[string]bool{}
	for _, line := range stdout.Lines() {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("stdout line is not JSON: %q", line)
			continue
		}
		if msg, ok := m["msg"].(string); ok {
			seen[msg] = true
		}
	}
	for _, msg := range []string{"request", "refresh.start", "source.done", "refresh.done"} {
		if !seen[msg] {
			t.Errorf("no JSON log line with \"msg\":%q in server stdout:\n%s", msg, stdout.String())
		}
	}

	// 9. stop the server, delete the temp DB
	if err := stop(); err != nil {
		t.Errorf("serve exit after SIGTERM: %v\nstderr:\n%s", err, stderr.String())
	}
	if err := os.RemoveAll(tmpDir); err != nil {
		t.Errorf("remove temp DB dir: %v", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("temp DB %s still exists (err=%v)", dbPath, err)
	}
}
