// Package storage owns the SQLite database, applies migrations on open, and
// exposes typed repositories per domain entity.
package storage

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// Store wraps the database handle and exposes one repository per entity. Its
// zero value is not usable — construct it with Open.
type Store struct {
	DB           *sql.DB
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
}

// Open opens (and migrates) the database at dsn. The caller must Close it.
func Open(dsn string, maxOpen, maxIdle int) (*Store, error) {
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
	}, nil
}

// Close closes the underlying database. Safe to call on a nil Store.
func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}
