package storage

import "testing"

func TestRewritePlaceholders(t *testing.T) {
	cases := []struct {
		name, in, want string
		d              Dialect
	}{
		{"sqlite passes through", `SELECT * FROM t WHERE a=? AND b=?`, `SELECT * FROM t WHERE a=? AND b=?`, DialectSQLite},
		{"postgres rewrites", `SELECT * FROM t WHERE a=? AND b=?`, `SELECT * FROM t WHERE a=$1 AND b=$2`, DialectPostgres},
		{"single quotes preserved", `WHERE name LIKE 'a?b' AND k=?`, `WHERE name LIKE 'a?b' AND k=$1`, DialectPostgres},
		{"double quotes preserved", `WHERE "col?" = ?`, `WHERE "col?" = $1`, DialectPostgres},
		{"no placeholders", `SELECT 1`, `SELECT 1`, DialectPostgres},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rewritePlaceholders(c.in, c.d)
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestDialectFromDSN(t *testing.T) {
	cases := map[string]Dialect{
		"file:./mqc.db":                        DialectSQLite,
		"":                                     DialectSQLite,
		"mqc.db?_pragma=journal_mode(WAL)":     DialectSQLite,
		"postgres://user:pass@host:5432/db":    DialectPostgres,
		"postgresql://user:pass@host:5432/db":  DialectPostgres,
		"POSTGRES://upper-case-still-detected": DialectPostgres,
	}
	for dsn, want := range cases {
		if got := dialectFromDSN(dsn); got != want {
			t.Errorf("dialectFromDSN(%q) = %q, want %q", dsn, got, want)
		}
	}
}

func TestOpen_PostgresErrorsClearly(t *testing.T) {
	_, err := Open("postgres://x/y", 1, 1)
	if err == nil {
		t.Fatal("expected error for postgres DSN")
	}
}
