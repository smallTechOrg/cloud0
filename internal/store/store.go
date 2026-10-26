// Package store opens the SQLite file at CLOUD0_DB_PATH, applies the
// connection pragmas, runs the embedded goose migrations and offers the small
// set of helpers the repositories (snapshots, runs, settings, annotations —
// Phase 1 slice S2) build on. SQL stays in the portable subset so a Postgres
// driver can be swapped in by touching Open and the migrations directory only.
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
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store wraps the database handle and the migration provider.
type Store struct {
	DB   *sql.DB
	path string
}

// MigrationInfo is one line of `cloud0 migrate status`.
type MigrationInfo struct {
	Version   int64
	Source    string
	Applied   bool
	AppliedAt time.Time // zero when pending
}

// Open creates the parent directory if needed and opens the SQLite file with
// WAL journaling, a 5 s busy timeout and foreign keys enforced.
func Open(ctx context.Context, path string) (*Store, error) {
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

// Path is the file the store was opened on.
func (s *Store) Path() string { return s.path }

// Close releases the database handle.
func (s *Store) Close() error { return s.DB.Close() }

// Ping verifies the connection is alive.
func (s *Store) Ping(ctx context.Context) error { return s.DB.PingContext(ctx) }

func (s *Store) provider() (*goose.Provider, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: migrations fs: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, s.DB, sub)
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

// LastInsertID is the one seam that reads the generated id; a Postgres
// driver would switch this to a RETURNING clause.
func LastInsertID(res sql.Result) (int64, error) {
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: last insert id: %w", err)
	}
	return id, nil
}

// FormatTime renders a timestamp the way every TEXT column stores it.
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ParseTime reads a timestamp written by FormatTime.
func ParseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

// FormatDate renders a YYYY-MM-DD date.
func FormatDate(t time.Time) string { return t.UTC().Format("2006-01-02") }
