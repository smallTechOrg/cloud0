package store

import (
	"context"
	"database/sql/driver"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Postgres support (CLOUD0_DATABASE_URL). Every query in this package is
// written once, with SQLite's "?" placeholders and the portable SQL subset;
// the connector below rewrites "?" to "$1, $2, …" on the way to pgx, so
// repositories, callers and their transactions need no dialect switches.

// IsPostgresURL reports whether target is a Postgres connection URL rather
// than a SQLite file path.
func IsPostgresURL(target string) bool {
	return strings.HasPrefix(target, "postgres://") || strings.HasPrefix(target, "postgresql://")
}

// Redact hides the password of a Postgres URL (file paths come back as is),
// for logs and `cloud0 migrate` output.
func Redact(target string) string {
	if !IsPostgresURL(target) {
		return target
	}
	cfg, err := pgx.ParseConfig(target)
	if err != nil {
		return "postgres://(unparseable URL)"
	}
	return "postgres://" + cfg.User + "@" + cfg.Host + ":" + strconv.Itoa(int(cfg.Port)) + "/" + cfg.Database
}

func postgresConnector(url string) (driver.Connector, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	return rebindConnector{stdlib.GetConnector(*cfg)}, nil
}

type rebindConnector struct{ inner driver.Connector }

func (c rebindConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return rebindConn{conn}, nil
}

func (c rebindConnector) Driver() driver.Driver { return c.inner.Driver() }

// rebindConn forwards to pgx's connection with every query rebound.
type rebindConn struct{ inner driver.Conn }

func (c rebindConn) Prepare(query string) (driver.Stmt, error) { return c.inner.Prepare(rebind(query)) }
func (c rebindConn) Close() error                              { return c.inner.Close() }

// Begin is required by driver.Conn; database/sql calls BeginTx.
func (c rebindConn) Begin() (driver.Tx, error) { return c.inner.Begin() }

func (c rebindConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if p, ok := c.inner.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, rebind(query))
	}
	return c.inner.Prepare(rebind(query))
}

func (c rebindConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.inner.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c rebindConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := c.inner.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, rebind(query), args)
	}
	return nil, driver.ErrSkip
}

func (c rebindConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if q, ok := c.inner.(driver.QueryerContext); ok {
		return q.QueryContext(ctx, rebind(query), args)
	}
	return nil, driver.ErrSkip
}

func (c rebindConn) Ping(ctx context.Context) error {
	if p, ok := c.inner.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c rebindConn) CheckNamedValue(nv *driver.NamedValue) error {
	if n, ok := c.inner.(driver.NamedValueChecker); ok {
		return n.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

func (c rebindConn) ResetSession(ctx context.Context) error {
	if r, ok := c.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c rebindConn) IsValid() bool {
	if v, ok := c.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// rebind turns each "?" placeholder into "$1", "$2", … in order, leaving
// question marks inside quoted strings, quoted identifiers and comments alone.
func rebind(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 0
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch {
		case ch == '\'' || ch == '"':
			end := strings.IndexByte(query[i+1:], ch)
			if end < 0 {
				b.WriteString(query[i:])
				return b.String()
			}
			b.WriteString(query[i : i+end+2])
			i += end + 1
		case ch == '-' && i+1 < len(query) && query[i+1] == '-':
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				b.WriteString(query[i:])
				return b.String()
			}
			b.WriteString(query[i : i+end])
			i += end - 1
		case ch == '/' && i+1 < len(query) && query[i+1] == '*':
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				b.WriteString(query[i:])
				return b.String()
			}
			b.WriteString(query[i : i+end+4])
			i += end + 3
		case ch == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}
