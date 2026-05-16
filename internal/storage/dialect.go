package storage

import (
	"strconv"
	"strings"
)

// rewritePlaceholders converts SQLite's `?` positional placeholders to
// Postgres's `$1, $2, ...` form. Repository methods are written
// against the SQLite dialect today; once the Postgres backend lands
// they'll dispatch through this helper at query time.
//
// The function is intentionally conservative: it scans character by
// character and only rewrites bare `?` (not literals inside quoted
// strings, not `??`). Quote-handling tracks both single quotes and
// double quotes so a SQL fragment like `WHERE name LIKE 'a?b'` is
// left alone.
//
// Cost: O(n) over the SQL string. Repos that use static query strings
// can memoise the rewrite trivially; the test in dialect_test.go
// asserts correctness over a small fixture set.
func rewritePlaceholders(sql string, dialect Dialect) string {
	if dialect != DialectPostgres {
		return sql
	}
	var b strings.Builder
	b.Grow(len(sql) + 8)
	idx := 1
	inSingle, inDouble := false, false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			b.WriteByte(c)
		case c == '"' && !inSingle:
			inDouble = !inDouble
			b.WriteByte(c)
		case c == '?' && !inSingle && !inDouble:
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(idx))
			idx++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
