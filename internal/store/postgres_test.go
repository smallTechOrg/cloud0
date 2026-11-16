package store

import "testing"

func TestRebind_NumbersPlaceholdersOutsideQuotesAndComments(t *testing.T) {
	cases := []struct{ in, want string }{
		{`SELECT 1`, `SELECT 1`},
		{`SELECT * FROM t WHERE a = ? AND b = ?`, `SELECT * FROM t WHERE a = $1 AND b = $2`},
		{`INSERT INTO t (a, b, c) VALUES (?, ?, ?)`, `INSERT INTO t (a, b, c) VALUES ($1, $2, $3)`},
		{`SELECT '?' , ? FROM t`, `SELECT '?' , $1 FROM t`},
		{`SELECT 'it''s ?', ?`, `SELECT 'it''s ?', $1`},
		{`SELECT "odd?col" FROM t WHERE x = ?`, `SELECT "odd?col" FROM t WHERE x = $1`},
		{"SELECT ? -- why?\nFROM t WHERE y = ?", "SELECT $1 -- why?\nFROM t WHERE y = $2"},
		{`SELECT ? /* really? */ , ?`, `SELECT $1 /* really? */ , $2`},
		{`SELECT "key" FROM settings WHERE "key" LIKE ? ESCAPE '\' ORDER BY "key"`, `SELECT "key" FROM settings WHERE "key" LIKE $1 ESCAPE '\' ORDER BY "key"`},
		{`SELECT 'unterminated ?`, `SELECT 'unterminated ?`},
	}
	for _, c := range cases {
		if got := rebind(c.in); got != c.want {
			t.Errorf("rebind(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestRedact_HidesThePasswordOfAPostgresURL(t *testing.T) {
	got := Redact("postgres://cloud0_app:s3cret-value@10.0.0.7:5432/cloud0?sslmode=require")
	if got != "postgres://cloud0_app@10.0.0.7:5432/cloud0" {
		t.Errorf("Redact = %q", got)
	}
	if p := "./data/cloud0.db"; Redact(p) != p {
		t.Errorf("a file path must come back unchanged, got %q", Redact(p))
	}
}
