// Package testenv holds test-only helpers: loading the repo-root .env when
// variables are absent, skipping with the documented message when the
// BigQuery variables are missing, a temp SQLite file per test and an
// OS-assigned free port.
package testenv

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/joho/godotenv"
)

// SkipMessage is the exact text printed when the BigQuery variables are absent.
const SkipMessage = "CLOUD0_BILLING_EXPORT_TABLE not set — fill .env (see .env.example) to run real BigQuery tests"

// BigQueryVars must all be present for a real-BigQuery test to run.
var BigQueryVars = []string{"CLOUD0_BILLING_EXPORT_TABLE", "CLOUD0_BIGQUERY_PROJECT", "CLOUD0_GCP_PROJECTS"}

var (
	rootOnce sync.Once
	rootDir  string
	rootErr  error
	envOnce  sync.Once
	envErr   error
)

// RepoRoot walks up from the working directory to the directory holding go.mod.
func RepoRoot(t testing.TB) string {
	t.Helper()
	rootOnce.Do(func() {
		dir, err := os.Getwd()
		if err != nil {
			rootErr = err
			return
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				rootDir = dir
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				rootErr = errors.New("testenv: go.mod not found above " + dir)
				return
			}
			dir = parent
		}
	})
	if rootErr != nil {
		t.Fatalf("%v", rootErr)
	}
	return rootDir
}

// LoadDotEnv loads <repo root>/.env once per process without overriding
// variables already set. A missing .env is not an error; values are never
// printed.
func LoadDotEnv(t testing.TB) {
	t.Helper()
	root := RepoRoot(t)
	envOnce.Do(func() {
		path := filepath.Join(root, ".env")
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return
			}
			envErr = err
			return
		}
		envErr = godotenv.Load(path)
	})
	if envErr != nil {
		t.Fatalf("testenv: load .env: %v", envErr)
	}
}

// HasBigQuery reports whether every BigQuery variable is set (after LoadDotEnv).
func HasBigQuery(t testing.TB) bool {
	t.Helper()
	LoadDotEnv(t)
	for _, v := range BigQueryVars {
		if os.Getenv(v) == "" {
			return false
		}
	}
	return true
}

// RequireBigQuery loads .env and skips the test with SkipMessage when the
// BigQuery variables are genuinely absent. A skip never passes a gate.
func RequireBigQuery(t testing.TB) {
	t.Helper()
	if !HasBigQuery(t) {
		t.Skip(SkipMessage)
	}
}

// TempDB returns a path for a fresh SQLite file inside t.TempDir(); the
// directory (and the WAL/SHM side files) is removed when the test ends.
func TempDB(t testing.TB) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "cloud0-test.db")
}

// FreePort asks the OS for an unused TCP port on 127.0.0.1.
func FreePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("testenv: free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}
