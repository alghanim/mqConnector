package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mqConnector/internal/secrets"
	"mqConnector/internal/storage"
)

func key(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(k)
}

// seedConn opens the DB with whatever sealer is provided, writes a single
// connection, and returns the row ID + the raw on-disk password column.
func seedConn(t *testing.T, dsn, password string, sealer *secrets.Service) (string, string) {
	t.Helper()
	s, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	s.Connections = s.Connections.WithSealer(sealer)

	c := &storage.Connection{Name: "test", Type: "rabbitmq", URL: "amqp://x", Password: password}
	if err := s.Connections.Create(context.Background(), storage.DefaultTenantID, c); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.DB.QueryRow(`SELECT password FROM connections WHERE id=?`, c.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return c.ID, raw
}

func readDecrypted(t *testing.T, dsn string, sealer *secrets.Service, id string) string {
	t.Helper()
	s, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	s.Connections = s.Connections.WithSealer(sealer)
	c, err := s.Connections.Get(context.Background(), storage.DefaultTenantID, id)
	if err != nil {
		t.Fatal(err)
	}
	return c.Password
}

// TestRotate_PlaintextToEncrypted writes a row with no sealer, runs
// rotate-secrets with --new-key, and asserts the row is now encrypted
// and still decrypts to the original plaintext.
func TestRotate_PlaintextToEncrypted(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "rot.db") + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"

	id, beforeRaw := seedConn(t, dsn, "hunter2", nil)
	if beforeRaw != "hunter2" {
		t.Fatalf("seed should be plaintext, got %q", beforeRaw)
	}

	// Drive the subcommand via its argv shape.
	newKey := key(t)
	cfgPath := writeConfig(t, dir, dsn)
	withArgs(t, []string{"mqconnector", "-config", cfgPath, "-new-key", newKey},
		func() {
			if err := rotateSecrets(); err != nil {
				t.Fatalf("rotateSecrets: %v", err)
			}
		})

	// Raw column must now carry the encrypted prefix.
	var rawAfter string
	mustQuery(t, dsn, `SELECT password FROM connections WHERE id=?`, []any{id}, &rawAfter)
	if !strings.HasPrefix(rawAfter, secrets.EncryptedPrefix) {
		t.Fatalf("row not encrypted after rotate: %q", rawAfter)
	}

	// And it decrypts cleanly under the new key.
	newSealer, _ := secrets.New(newKey)
	if got := readDecrypted(t, dsn, newSealer, id); got != "hunter2" {
		t.Errorf("decrypted = %q, want hunter2", got)
	}
}

// TestRotate_OldToNewKey writes with one key, rotates to another, and
// asserts the new sealer reads it cleanly.
func TestRotate_OldToNewKey(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "rot.db") + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"

	oldKey := key(t)
	newKey := key(t)
	oldSealer, _ := secrets.New(oldKey)
	id, _ := seedConn(t, dsn, "old-secret", oldSealer)

	cfgPath := writeConfig(t, dir, dsn)
	withArgs(t, []string{"mqconnector", "-config", cfgPath, "-old-key", oldKey, "-new-key", newKey},
		func() {
			if err := rotateSecrets(); err != nil {
				t.Fatalf("rotateSecrets: %v", err)
			}
		})

	newSealer, _ := secrets.New(newKey)
	if got := readDecrypted(t, dsn, newSealer, id); got != "old-secret" {
		t.Errorf("new-key decrypt: got %q, want old-secret", got)
	}

	// And the OLD sealer must no longer work.
	if got, err := tryDecryptedAfterRotate(t, dsn, oldSealer, id); err == nil {
		t.Errorf("old-key should fail to decrypt after rotate, got %q", got)
	}
}

// TestRotate_DryRunDoesNotWrite proves --dry-run leaves the on-disk
// ciphertext alone.
func TestRotate_DryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "rot.db") + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"

	oldKey := key(t)
	oldSealer, _ := secrets.New(oldKey)
	id, before := seedConn(t, dsn, "pw", oldSealer)

	newKey := key(t)
	cfgPath := writeConfig(t, dir, dsn)
	withArgs(t, []string{"mqconnector", "-config", cfgPath, "-old-key", oldKey, "-new-key", newKey, "-dry-run"},
		func() {
			if err := rotateSecrets(); err != nil {
				t.Fatalf("rotateSecrets: %v", err)
			}
		})

	var after string
	mustQuery(t, dsn, `SELECT password FROM connections WHERE id=?`, []any{id}, &after)
	if before != after {
		t.Errorf("dry-run mutated row\nbefore=%q\n after=%q", before, after)
	}
}

// TestRotate_RefusesWhenNoKeys catches the "operator typed the wrong
// command" case.
func TestRotate_RefusesWhenNoKeys(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "rot.db") + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	_, _ = seedConn(t, dsn, "pw", nil)

	cfgPath := writeConfig(t, dir, dsn)
	withArgs(t, []string{"mqconnector", "-config", cfgPath},
		func() {
			t.Setenv("MQC_MASTER_KEY", "")
			err := rotateSecrets()
			if err == nil {
				t.Error("expected an error when neither --old-key nor --new-key is set")
			}
		})
}

// ────────────────────── test helpers ────────────────────────────────

func writeConfig(t *testing.T, dir, dsn string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	yaml := `server:
  mode: dev
  listen: "127.0.0.1:0"
  tls:
    enabled: false
storage:
  dsn: "` + dsn + `"
auth:
  data_file: "` + filepath.Join(dir, "auth.json") + `"
  session_ttl: "12h"
logging:
  level: error
  format: text
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// withArgs swaps os.Args around fn() so flag.NewFlagSet sees a clean argv.
func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	saved := os.Args
	os.Args = args
	defer func() { os.Args = saved }()
	fn()
}

func mustQuery(t *testing.T, dsn, query string, args []any, dest ...any) {
	t.Helper()
	s, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.DB.QueryRow(query, args...).Scan(dest...); err != nil {
		t.Fatalf("query: %v", err)
	}
}

func tryDecryptedAfterRotate(t *testing.T, dsn string, sealer *secrets.Service, id string) (string, error) {
	t.Helper()
	s, err := storage.Open(dsn, 4, 2)
	if err != nil {
		return "", err
	}
	defer func() { _ = s.Close() }()
	s.Connections = s.Connections.WithSealer(sealer)
	c, err := s.Connections.Get(context.Background(), storage.DefaultTenantID, id)
	if err != nil {
		return "", err
	}
	return c.Password, nil
}
