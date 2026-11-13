// Package store opens the database — the SQLite file at CLOUD0_DB_PATH, or
// Postgres when CLOUD0_DATABASE_URL is set (postgres.go) — runs the embedded
// goose migrations for that dialect and offers the small set of helpers the
// repositories (snapshots, runs, settings, annotations — Phase 1 slice S2)
// build on. SQL stays in the portable subset, so the two dialects differ only
// in Open and in the migrations directory (migrations/ vs migrations_postgres/).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the cgo-free "sqlite" driver

	"github.com/smallTechOrg/cloud0/internal/config"
)

//go:embed migrations/*.sql migrations_postgres/*.sql
var migrationsFS embed.FS

// Store wraps the database handle and the migration provider.
type Store struct {
	DB       *sql.DB
	path     string
	postgres bool
}

// MigrationInfo is one line of `cloud0 migrate status`.
type MigrationInfo struct {
	Version   int64
	Source    string
	Applied   bool
	AppliedAt time.Time // zero when pending
}

// Open opens the database named by target: a Postgres URL
// (postgres://user:pass@host:5432/db?sslmode=require), or a SQLite file
// path, whose parent directory is created if needed and which is opened with
// WAL journaling, a 5 s busy timeout and foreign keys enforced.
func Open(ctx context.Context, target string) (*Store, error) {
	if config.IsPostgresURL(target) {
		return openPostgres(ctx, target)
	}
	path := target
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create directory %s: %w", dir, err)
		}
	}
	dsn := "file:" + path + "?" + url.Values{
		"_pragma": []string{"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(1)"},
	}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One writer at a time keeps SQLite happy; WAL lets readers proceed.
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}
	return &Store{DB: db, path: path}, nil
}

func openPostgres(ctx context.Context, url string) (*Store, error) {
	connector, err := postgresConnector(url)
	if err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", Redact(url), err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: connect %s: %w", Redact(url), err)
	}
	return &Store{DB: db, path: url, postgres: true}, nil
}

// Path is what the store was opened on: the SQLite file, or the Postgres URL
// (with its password; print Redact(Path()) instead).
func (s *Store) Path() string { return s.path }

// Close releases the database handle.
func (s *Store) Close() error { return s.DB.Close() }

// Ping verifies the connection is alive.
func (s *Store) Ping(ctx context.Context) error { return s.DB.PingContext(ctx) }

func (s *Store) provider() (*goose.Provider, error) {
	dir, dialect := "migrations", goose.DialectSQLite3
	if s.postgres {
		dir, dialect = "migrations_postgres", goose.DialectPostgres
	}
	sub, err := fs.Sub(migrationsFS, dir)
	if err != nil {
		return nil, fmt.Errorf("store: migrations fs: %w", err)
	}
	p, err := goose.NewProvider(dialect, s.DB, sub)
	if err != nil {
		return nil, fmt.Errorf("store: goose provider: %w", err)
	}
	return p, nil
}

// Migrate applies every pending migration (`cloud0 migrate up`).
func (s *Store) Migrate(ctx context.Context) ([]MigrationInfo, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	results, err := p.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: migrate up: %w", err)
	}
	// Migration 0002 loosens a CHECK through writable_schema, which a connection
	// that ran it keeps stale; drop idle connections so the next ones reload.
	s.DB.SetMaxIdleConns(0)
	s.DB.SetMaxIdleConns(4)
	out := make([]MigrationInfo, 0, len(results))
	for _, r := range results {
		out = append(out, MigrationInfo{Version: r.Source.Version, Source: filepath.Base(r.Source.Path), Applied: true, AppliedAt: time.Now().UTC()})
	}
	return out, nil
}

// MigrationStatus lists every known migration with its applied time
// (`cloud0 migrate status`).
func (s *Store) MigrationStatus(ctx context.Context) ([]MigrationInfo, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	statuses, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: migrate status: %w", err)
	}
	out := make([]MigrationInfo, 0, len(statuses))
	for _, st := range statuses {
		info := MigrationInfo{Version: st.Source.Version, Source: filepath.Base(st.Source.Path)}
		if st.State == goose.StateApplied {
			info.Applied = true
			info.AppliedAt = st.AppliedAt.UTC()
		}
		out = append(out, info)
	}
	return out, nil
}

// Pending returns the number of migrations not yet applied.
func (s *Store) Pending(ctx context.Context) (int, error) {
	infos, err := s.MigrationStatus(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, i := range infos {
		if !i.Applied {
			n++
		}
	}
	return n, nil
}

// WithTx runs fn inside a transaction, committing on nil and rolling back on
// error or panic.
func (s *Store) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
			return
		}
		err = tx.Commit()
		if err != nil {
			err = fmt.Errorf("store: commit: %w", err)
		}
	}()
	return fn(tx)
}

// InsertID runs an INSERT and returns the generated id, through RETURNING:
// both SQLite (3.35+) and Postgres support it, and Postgres has no
// LastInsertId.
func InsertID(ctx context.Context, q Querier, insert string, args ...any) (int64, error) {
	var id int64
	if err := q.QueryRowContext(ctx, insert+" RETURNING id", args...).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// FormatTime renders a timestamp the way every TEXT column stores it.
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ParseTime reads a timestamp written by FormatTime.
func ParseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

// FormatDate renders a YYYY-MM-DD date.
func FormatDate(t time.Time) string { return t.UTC().Format("2006-01-02") }
