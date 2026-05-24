package storage

import (
	"context"
	"database/sql"
)

// dbWrap is a thin adapter around *sql.DB that rewrites `?`
// placeholders to the target dialect on every query. Repos hold
// *dbWrap instead of *sql.DB so existing call sites stay verbatim —
// only the field type and the construction path change.
//
// The wrapping is intentionally narrow. We expose only the
// database/sql methods the repos actually use; anything beyond
// that surfaces here as a compile error rather than silently
// bypassing the rewrite. The cost is one extra string scan per
// query (rewritePlaceholders is O(n)); the trade-off is one
// place where dialect divergence is handled instead of 200.
type dbWrap struct {
	db      *sql.DB
	dialect Dialect
}

func (d *dbWrap) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.db.ExecContext(ctx, rewritePlaceholders(query, d.dialect), args...)
}

func (d *dbWrap) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.db.QueryContext(ctx, rewritePlaceholders(query, d.dialect), args...)
}

func (d *dbWrap) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.db.QueryRowContext(ctx, rewritePlaceholders(query, d.dialect), args...)
}

// Exec / Query / QueryRow without context. A handful of legacy
// call sites use these (PRAGMA fan-out, test helpers); supplying
// them keeps the conversion mechanical.
func (d *dbWrap) Exec(query string, args ...any) (sql.Result, error) {
	return d.db.Exec(rewritePlaceholders(query, d.dialect), args...)
}

func (d *dbWrap) Query(query string, args ...any) (*sql.Rows, error) {
	return d.db.Query(rewritePlaceholders(query, d.dialect), args...)
}

func (d *dbWrap) QueryRow(query string, args ...any) *sql.Row {
	return d.db.QueryRow(rewritePlaceholders(query, d.dialect), args...)
}

// BeginTx returns a wrapped *txWrap so transactional SQL also gets
// the placeholder rewrite. Repos that already use BeginTx — Stages,
// Transforms, RoutingRules, Tenants — store the returned value
// without caring about the underlying type.
func (d *dbWrap) BeginTx(ctx context.Context, opts *sql.TxOptions) (*txWrap, error) {
	tx, err := d.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &txWrap{tx: tx, dialect: d.dialect}, nil
}

// Underlying gives callers access to the raw *sql.DB for paths the
// wrapper deliberately doesn't cover (migrate's pinned connection,
// PRAGMA control, the conn pool's stats endpoints). Treat with
// care — anything that bypasses Underlying must handle dialect
// differences itself.
func (d *dbWrap) Underlying() *sql.DB { return d.db }

func (d *dbWrap) Close() error { return d.db.Close() }

func (d *dbWrap) Conn(ctx context.Context) (*sql.Conn, error) {
	return d.db.Conn(ctx)
}

// Stats forwards through; identical semantics to the underlying
// *sql.DB.Stats(). Exposed for the /api/health endpoint.
func (d *dbWrap) Stats() sql.DBStats { return d.db.Stats() }

// Tx is the storage package's exported handle for a wrapped
// transaction. It is the same type repos accept on their *Tx-aware
// methods (StageRepo.ReplaceForPipelineTx, etc.); the server-layer
// apply-revision helper calls Store.BeginTx to obtain one and threads
// it through the per-table replacements so all four writes commit (or
// roll back) together.
//
// Internally this is exactly the same struct as txWrap was before
// being exported. Aliasing keeps the original `tx *txWrap` parameter
// names elsewhere in the package compiling without churn — and gives
// external callers an opaque exported handle.
type Tx = txWrap

// txWrap mirrors dbWrap for transactions. Each tx-bound call goes
// through the same placeholder rewrite. Kept internal-named because
// most call sites in this package still refer to it as txWrap; the
// public name is Tx (an alias above).
type txWrap struct {
	tx      *sql.Tx
	dialect Dialect
}

func (t *txWrap) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, rewritePlaceholders(query, t.dialect), args...)
}

func (t *txWrap) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, rewritePlaceholders(query, t.dialect), args...)
}

func (t *txWrap) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, rewritePlaceholders(query, t.dialect), args...)
}

func (t *txWrap) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.Exec(rewritePlaceholders(query, t.dialect), args...)
}

func (t *txWrap) Query(query string, args ...any) (*sql.Rows, error) {
	return t.tx.Query(rewritePlaceholders(query, t.dialect), args...)
}

func (t *txWrap) QueryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRow(rewritePlaceholders(query, t.dialect), args...)
}

func (t *txWrap) Commit() error   { return t.tx.Commit() }
func (t *txWrap) Rollback() error { return t.tx.Rollback() }
