// Package testenv holds test-only helpers: loading the repo-root .env when
// variables are absent, skipping with the documented message when the
// BigQuery variables are missing, a fresh database per test (SQLite, or a
// Postgres schema when CLOUD0_TEST_DATABASE_URL is set) and an OS-assigned
// free port.
package testenv

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver TempDB uses to create test schemas
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

// TestDatabaseURLVar names a Postgres server to run the store-backed tests
// against (CI sets it; each test gets its own schema). Unset → SQLite.
const TestDatabaseURLVar = "CLOUD0_TEST_DATABASE_URL"

// Postgres reports whether TempDB hands out Postgres databases.
func Postgres() bool { return os.Getenv(TestDatabaseURLVar) != "" }

var schemaSeq atomic.Int64

// TempDB returns a fresh database for one test, as a store.Open target: a
// SQLite file inside t.TempDir() (removed with its WAL/SHM side files when
// the test ends), or, when CLOUD0_TEST_DATABASE_URL is set, a new schema on
// that Postgres server, selected through the returned URL's search_path and
// dropped when the test ends.
func TempDB(t testing.TB) string {
	t.Helper()
	base := os.Getenv(TestDatabaseURLVar)
	if base == "" {
		return filepath.Join(t.TempDir(), "cloud0-test.db")
	}
	schema := fmt.Sprintf("t_%d_%d_%d", os.Getpid(), time.Now().UnixNano(), schemaSeq.Add(1))
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("testenv: open %s: %v", TestDatabaseURLVar, err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		_ = admin.Close()
		t.Fatalf("testenv: create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "search_path=" + schema
}

// DBEnv is the environment entry that points a cloud0 child process at a
// TempDB target: CLOUD0_DATABASE_URL for a Postgres schema, CLOUD0_DB_PATH
// for a SQLite file.
func DBEnv(target string) string {
	if Postgres() {
		return "CLOUD0_DATABASE_URL=" + target
	}
	return "CLOUD0_DB_PATH=" + target
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
