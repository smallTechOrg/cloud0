// Package scaffold is the cross-process boot test of the scaffold gate: build
// the binary → migrate up / status on a temp DB → serve on a free port passed
// as CLOUD0_PORT → health says {"ok":true,"ui":"real"} → GET / is the real
// UI with live CSS/JS → one JSON log line with "msg":"request".
package scaffold

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/testenv"
)

// requiredPlaceholders keep the boot test independent of a filled .env: the
// scaffold server never contacts BigQuery, so any syntactically valid values do.
var requiredPlaceholders = map[string]string{
	"CLOUD0_GCP_PROJECTS":         "my-project-a,my-project-b",
	"CLOUD0_BILLING_EXPORT_TABLE": "my-project.my_dataset.my_table",
	"CLOUD0_BIGQUERY_PROJECT":     "my-project",
}

func goBinary() string {
	if g := os.Getenv("GO"); g != "" {
		return g
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}

func childEnv(dbPath string, port int) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		k := strings.SplitN(kv, "=", 2)[0]
		switch k {
		case "CLOUD0_PORT", "CLOUD0_DB_PATH", "CLOUD0_BIND", "PORT":
			continue
		}
		env = append(env, kv)
	}
	for k, v := range requiredPlaceholders {
		if os.Getenv(k) == "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env,
		"CLOUD0_DB_PATH="+dbPath,
		"CLOUD0_PORT="+strconv.Itoa(port),
		"CLOUD0_BIND=127.0.0.1",
		"GO111MODULE=on", "GOTOOLCHAIN=local",
	)
	return env
}

func runCmd(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// lineBuffer collects the child's stdout and lets tests search it safely.
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
	return strings.Split(strings.TrimSpace(b.buf.String()), "\n")
}

func TestBoot_BuildMigrateServeHealthUIAndLogs(t *testing.T) {
	testenv.LoadDotEnv(t)
	root := testenv.RepoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "internal", "webui", "dist", "index.html")); err != nil {
		t.Fatalf("internal/webui/dist/index.html is missing — run `make web-build` first so the real UI is embedded (%v)", err)
	}

	dbPath := testenv.TempDB(t)
	port := testenv.FreePort(t)
	env := childEnv(dbPath, port)
	bin := filepath.Join(t.TempDir(), "cloud0")

	// 1. build
	runCmd(t, root, env, goBinary(), "build", "-o", bin, "./cmd/cloud0")
	if out := runCmd(t, root, env, bin, "version"); !strings.HasPrefix(out, "cloud0 ") {
		t.Fatalf("version output %q", out)
	}

	// 2. migrate up + status on the temp DB
	up := runCmd(t, root, env, bin, "migrate", "up")
	if !strings.Contains(up, "0001_init.sql") {
		t.Fatalf("migrate up did not report 0001_init.sql:\n%s", up)
	}
	status := runCmd(t, root, env, bin, "migrate", "status")
	if !strings.Contains(status, "0001_init.sql") || strings.Contains(status, "Pending") {
		t.Fatalf("migrate status should list 0001_init.sql as applied:\n%s", status)
	}
	if !strings.Contains(status, dbPath) {
		t.Fatalf("migrate status should name the temp DB %s:\n%s", dbPath, status)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("temp DB was not created at %s: %v", dbPath, err)
	}

	// 3. serve on the free port
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serve := exec.CommandContext(ctx, bin, "serve")
	serve.Dir = root
	serve.Env = env
	stdout := &lineBuffer{}
	var stderr bytes.Buffer
	serve.Stdout = stdout
	serve.Stderr = &stderr
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
	client := &http.Client{Timeout: 5 * time.Second}
	var health map[string]any
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("serve exited early: %v\nstdout:\n%s\nstderr:\n%s", err, strings.Join(stdout.Lines(), "\n"), stderr.String())
		default:
		}
		res, err := client.Get(base + "/api/v1/health")
		if err == nil {
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode == 200 {
				if err := json.Unmarshal(body, &health); err != nil {
					t.Fatalf("health is not JSON: %q", body)
				}
				break
			}
			t.Logf("health %d: %s", res.StatusCode, body)
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become healthy on %s\nstdout:\n%s\nstderr:\n%s", base, strings.Join(stdout.Lines(), "\n"), stderr.String())
		}
		time.Sleep(150 * time.Millisecond)
	}

	// 4. health
	if health["ok"] != true || health["ui"] != "real" {
		t.Fatalf(`health must be {"ok":true,"ui":"real"}, got %v`, health)
	}
	if health["db"] != "ok" || health["migrations"] != "current" {
		t.Fatalf("health db/migrations: %v", health)
	}
	// Phase 1 wired the startup BigQuery dry-run: the field is an object
	// ({status, checked_at, ...}); it may be "ok" or "error" depending on
	// credentials, but it must be present and never null.
	if v, ok := health["bigquery"].(map[string]any); !ok || v["status"] == nil {
		t.Fatalf("health.bigquery must be an object with a status: %v", health)
	}

	// 5. GET / is the real UI with live assets
	res, err := client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	index, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(index), `data-cloud0-ui="real"`) {
		t.Fatalf("GET / status %d, body lacks data-cloud0-ui=\"real\":\n%.500s", res.StatusCode, index)
	}
	if res.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("index Cache-Control %q, want no-cache", res.Header.Get("Cache-Control"))
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
			t.Fatalf("asset %s: status %d, %d bytes", href, r.StatusCode, len(body))
		}
		if strings.HasPrefix(href, "/_next/static/") && !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
			t.Errorf("asset %s Cache-Control %q, want immutable", href, r.Header.Get("Cache-Control"))
		}
		if strings.HasSuffix(href, ".css") && !strings.Contains(string(body), "{") {
			t.Fatalf("stylesheet %s has no rules", href)
		}
	}

	// 6. config endpoint and the API 404 envelope
	res, err = client.Get(base + "/api/v1/config")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	_ = json.NewDecoder(res.Body).Decode(&cfg)
	res.Body.Close()
	if res.StatusCode != 200 || cfg["first_week"] == nil || cfg["current_week"] == nil || cfg["default_week"] == nil {
		t.Fatalf("config: %d %v", res.StatusCode, cfg)
	}
	res, err = client.Get(base + "/api/v1/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	var notFound map[string]any
	_ = json.NewDecoder(res.Body).Decode(&notFound)
	res.Body.Close()
	if res.StatusCode != 404 || notFound["error"] == nil {
		t.Fatalf("unknown API path: %d %v", res.StatusCode, notFound)
	}

	// 7. one JSON request log line per request, with the documented fields
	time.Sleep(100 * time.Millisecond)
	var requestLines []map[string]any
	for _, line := range stdout.Lines() {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdout line is not JSON: %q", line)
		}
		if m["msg"] == "request" {
			requestLines = append(requestLines, m)
		}
	}
	if len(requestLines) == 0 {
		t.Fatalf("no JSON log line with \"msg\":\"request\":\n%s", strings.Join(stdout.Lines(), "\n"))
	}
	for _, k := range []string{"time", "level", "request_id", "method", "path", "status", "duration_ms", "bytes_out", "remote_addr"} {
		if _, ok := requestLines[0][k]; !ok {
			t.Errorf("request log line lacks %q: %v", k, requestLines[0])
		}
	}
	var sawListening bool
	for _, line := range stdout.Lines() {
		if strings.Contains(line, `"msg":"listening"`) && strings.Contains(line, `"ui":"real"`) {
			sawListening = true
		}
		if strings.Contains(line, "sa-key") || strings.Contains(line, ".json") {
			t.Errorf("log line looks like it leaks a credentials path: %s", line)
		}
	}
	if !sawListening {
		t.Errorf("no listening log line with ui=real")
	}

	// 8. graceful shutdown on SIGTERM
	if err := serve.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve exited with %v after SIGTERM\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not exit within 15s of SIGTERM")
	}
}

func TestBoot_ServeRefusesToStartWithPendingMigrations(t *testing.T) {
	testenv.LoadDotEnv(t)
	root := testenv.RepoRoot(t)
	dbPath := testenv.TempDB(t)
	env := childEnv(dbPath, testenv.FreePort(t))
	bin := filepath.Join(t.TempDir(), "cloud0")
	runCmd(t, root, env, goBinary(), "build", "-o", bin, "./cmd/cloud0")

	cmd := exec.Command(bin, "serve")
	cmd.Dir = root
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("serve started with an unmigrated DB:\n%s", out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 3 {
		t.Fatalf("serve exit = %v, want exit status 3\n%s", err, out)
	}
	if !strings.Contains(string(out), "pending migration") || !strings.Contains(string(out), "migrate up") {
		t.Fatalf("serve did not explain the pending migration:\n%s", out)
	}
}

func TestBoot_ServeExits2OnMissingRequiredConfig(t *testing.T) {
	root := testenv.RepoRoot(t)
	bin := filepath.Join(t.TempDir(), "cloud0")
	buildEnv := append(os.Environ(), "GO111MODULE=on", "GOTOOLCHAIN=local")
	runCmd(t, root, buildEnv, goBinary(), "build", "-o", bin, "./cmd/cloud0")

	cmd := exec.Command(bin, "serve")
	cmd.Dir = t.TempDir() // no .env here
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "CLOUD0_DB_PATH=" + testenv.TempDB(t)}
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("serve exit = %v, want exit status 2\n%s", err, out)
	}
	for _, v := range []string{"CLOUD0_GCP_PROJECTS", "CLOUD0_BILLING_EXPORT_TABLE", "CLOUD0_BIGQUERY_PROJECT", ".env.example"} {
		if !strings.Contains(string(out), v) {
			t.Errorf("error output does not mention %s:\n%s", v, out)
		}
	}
}
