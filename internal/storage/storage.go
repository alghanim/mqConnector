// Package storage owns the database, applies migrations on open, and
// exposes typed repositories per domain entity.
//
// Today's backend is SQLite via modernc/sqlite (pure Go, no CGO). The
// Open function dispatches by DSN scheme so a future Postgres backend
// can land behind the same surface — see POSTGRES_MIGRATION.md at the
// repo root for the full migration plan.
package storage

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
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
	switch dialect {
	case DialectPostgres:
		return nil, fmt.Errorf(
			"storage: postgres DSN detected but the postgres backend is not yet wired — see POSTGRES_MIGRATION.md")
	case DialectSQLite:
		// fall through
	default:
		return nil, fmt.Errorf("storage: unsupported dialect for DSN")
	}

	db, err := sql.Open("sqlite", dsn)
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
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{
		DB:           db,
		dialect:      dialect,
		Connections:  &ConnectionRepo{db: db},
		Pipelines:    &PipelineRepo{db: db},
		Stages:       &StageRepo{db: db},
		Transforms:   &TransformRepo{db: db},
		RoutingRules: &RoutingRuleRepo{db: db},
		DLQ:          &DLQRepo{db: db},
		Scripts:      &ScriptRepo{db: db},
		Schemas:      &SchemaRepo{db: db},
		Audit:        &AuditRepo{db: db},
		Tenants:      &TenantRepo{db: db},
		Memberships:  &MembershipRepo{db: db},
		APITokens:    &APITokenRepo{db: db},
		Webhooks:     &WebhookRepo{db: db},
	}, nil
}

// Close closes the underlying database. Safe to call on a nil Store.
func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}
