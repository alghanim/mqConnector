// Package storage owns the database, applies migrations on open, and
// exposes typed repositories per domain entity.
//
// Today's backend is SQLite via modernc/sqlite (pure Go, no CGO). The
// Open function dispatches by DSN scheme so a future Postgres backend
// can land behind the same surface — see POSTGRES_MIGRATION.md at the
// repo root for the full migration plan.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // Postgres driver speaking database/sql
	_ "modernc.org/sqlite"             // SQLite driver (pure-Go)
)

// Dialect names the underlying SQL flavour. Repository methods that use
// dialect-specific SQL (e.g. INSERT OR IGNORE vs ON CONFLICT) branch on
// this. Set once by Open and exposed via Store.Dialect().
type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

// Store wraps the database handle and exposes one repository per entity. Its
// zero value is not usable — construct it with Open.
type Store struct {
	DB           *sql.DB
	dialect      Dialect
	Connections  *ConnectionRepo
	Pipelines    *PipelineRepo
	Stages       *StageRepo
	Transforms   *TransformRepo
	RoutingRules *RoutingRuleRepo
	DLQ          *DLQRepo
	Scripts      *ScriptRepo
	Schemas      *SchemaRepo
	Audit        *AuditRepo
	Tenants      *TenantRepo
	Memberships  *MembershipRepo
	APITokens    *APITokenRepo
	Webhooks     *WebhookRepo
	Plugins      *PluginRepo
}

// Dialect reports which SQL flavour the underlying connection speaks.
// Useful for repo methods that need dialect-specific SQL.
func (s *Store) Dialect() Dialect {
	if s == nil {
		return ""
	}
	return s.dialect
}

// dialectFromDSN picks the dialect based on the DSN's scheme prefix.
// Unknown / empty schemes default to SQLite — the historical behaviour.
func dialectFromDSN(dsn string) Dialect {
	low := strings.ToLower(dsn)
	if strings.HasPrefix(low, "postgres://") || strings.HasPrefix(low, "postgresql://") {
		return DialectPostgres
	}
	return DialectSQLite
}

// Open opens (and migrates) the database at dsn. The caller must Close it.
//
// DSN dispatch:
//   - "postgres://…" or "postgresql://…" → Postgres backend (not yet
//     wired; see POSTGRES_MIGRATION.md). Returns a clear error so the
//     operator knows their configuration was understood but the
//     driver isn't compiled in.
//   - anything else (including "file:…" and bare paths) → SQLite via
//     the modernc/sqlite pure-Go driver.
func Open(dsn string, maxOpen, maxIdle int) (*Store, error) {
	dialect := dialectFromDSN(dsn)
	var driverName string
	switch dialect {
	case DialectPostgres:
		driverName = "pgx"
	case DialectSQLite:
		driverName = "sqlite"
	default:
		return nil, fmt.Errorf("storage: unsupported dialect for DSN")
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	if maxOpen > 0 {
		db.SetMaxOpenConns(maxOpen)
	}
	if maxIdle > 0 {
		db.SetMaxIdleConns(maxIdle)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := migrate(db, dialect); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Wrap *sql.DB so repos see placeholder-rewritten queries
	// transparently. Each repo holds *dbWrap; call sites are
	// unchanged.
	wrap := &dbWrap{db: db, dialect: dialect}
	return &Store{
		DB:           db,
		dialect:      dialect,
		Connections:  &ConnectionRepo{db: wrap},
		Pipelines:    &PipelineRepo{db: wrap},
		Stages:       &StageRepo{db: wrap},
		Transforms:   &TransformRepo{db: wrap},
		RoutingRules: &RoutingRuleRepo{db: wrap},
		DLQ:          &DLQRepo{db: wrap},
		Scripts:      &ScriptRepo{db: wrap},
		Schemas:      &SchemaRepo{db: wrap},
		Audit:        &AuditRepo{db: wrap},
		Tenants:      &TenantRepo{db: wrap},
		Memberships:  &MembershipRepo{db: wrap},
		APITokens:    &APITokenRepo{db: wrap},
		Webhooks:     &WebhookRepo{db: wrap},
		Plugins:      &PluginRepo{db: wrap},
	}, nil
}

// Close closes the underlying database. Safe to call on a nil Store.
func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

// Backup writes a consistent snapshot of the database to dst. Uses
// SQLite's VACUUM INTO under the hood, which is atomic and works
// while writers are active — readers and writers see no interruption.
// The produced file is itself a valid SQLite database, openable by
// the same driver. dst must be on the same filesystem as a writable
// temp dir; the function does NOT create parent directories.
//
// Only meaningful on the SQLite backend. Postgres callers receive an
// error so the operator hits this in dev rather than trusting a silent
// no-op.
func (s *Store) Backup(ctx context.Context, dst string) error {
	if s == nil || s.DB == nil {
		return errors.New("storage: backup called on nil store")
	}
	if s.dialect != DialectSQLite {
		return errors.New("storage: Backup only supports SQLite")
	}
	if strings.TrimSpace(dst) == "" {
		return errors.New("storage: backup destination required")
	}
	// Refuse to overwrite an existing file — the operator may not
	// realise they're clobbering a previous snapshot.
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("storage: backup destination already exists: %s", dst)
	}
	// VACUUM INTO doesn't accept bound parameters, so we escape the
	// path by replacing single quotes (filenames containing them are
	// rare; the escape covers the corner case rather than rejecting it).
	escaped := strings.ReplaceAll(dst, "'", "''")
	_, err := s.DB.ExecContext(ctx, "VACUUM INTO '"+escaped+"'")
	if err != nil {
		return fmt.Errorf("storage: VACUUM INTO: %w", err)
	}
	return nil
}

// IntegrityCheck runs SQLite's PRAGMA integrity_check and returns the
// rows it produces. A healthy database returns exactly one row "ok";
// anything else is corruption and the operator should restore from
// backup before any further writes. PRAGMA quick_check is faster but
// less thorough; we prefer the full check for the on-demand admin
// endpoint, which is rare-enough to afford the cost.
func (s *Store) IntegrityCheck(ctx context.Context) ([]string, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("storage: integrity check on nil store")
	}
	if s.dialect != DialectSQLite {
		return nil, errors.New("storage: IntegrityCheck only supports SQLite")
	}
	rows, err := s.DB.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return nil, fmt.Errorf("integrity_check: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
