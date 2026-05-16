package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"mqConnector/internal/secrets"
)

func keyHex(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(k)
}

// TestConnections_PasswordEncryptedAtRest confirms that the on-disk row
// stores ciphertext while Get/List still return the original plaintext.
func TestConnections_PasswordEncryptedAtRest(t *testing.T) {
	s := openTestStore(t)
	sealer, err := secrets.New(keyHex(t))
	if err != nil {
		t.Fatal(err)
	}
	s.Connections = s.Connections.WithSealer(sealer)

	ctx := context.Background()
	c := &Connection{
		Name: "ibm-prod", Type: "ibm",
		QueueManager: "QM1", ConnName: "host(1414)", Channel: "DEV.SVRCONN",
		Username: "admin", Password: "hunter2", QueueName: "DEV.Q.1",
	}
	if err := s.Connections.Create(ctx, DefaultTenantID, c); err != nil {
		t.Fatal(err)
	}

	// Raw row in SQLite must NOT contain the plaintext password.
	var rawPW string
	if err := s.DB.QueryRowContext(ctx, `SELECT password FROM connections WHERE id=?`, c.ID).Scan(&rawPW); err != nil {
		t.Fatal(err)
	}
	if rawPW == "hunter2" {
		t.Fatal("password stored in plaintext")
	}
	if !strings.HasPrefix(rawPW, secrets.EncryptedPrefix) {
		t.Errorf("stored value missing encrypted prefix: %q", rawPW)
	}

	// Get must return plaintext.
	got, err := s.Connections.Get(ctx, DefaultTenantID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Password != "hunter2" {
		t.Errorf("Get did not decrypt: %q", got.Password)
	}
}

func TestConnections_LegacyPlaintextRow_StillReadable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Insert directly to mimic a row written before encryption was enabled.
	c := &Connection{Name: "legacy", Type: "rabbitmq", Password: "old-plain"}
	if err := s.Connections.Create(ctx, DefaultTenantID, c); err != nil { // no sealer yet
		t.Fatal(err)
	}

	// Now turn encryption on and read the legacy row back. It must still
	// come through as plaintext — Decrypt sees no prefix and returns the
	// value untouched.
	sealer, _ := secrets.New(keyHex(t))
	s.Connections = s.Connections.WithSealer(sealer)
	got, err := s.Connections.Get(ctx, DefaultTenantID, c.ID)
	if err != nil {
		t.Fatalf("read legacy row: %v", err)
	}
	if got.Password != "old-plain" {
		t.Errorf("legacy password mangled: %q", got.Password)
	}
}

func TestConnections_UpdateRecryptsOnSave(t *testing.T) {
	s := openTestStore(t)
	sealer, _ := secrets.New(keyHex(t))
	s.Connections = s.Connections.WithSealer(sealer)

	ctx := context.Background()
	c := &Connection{Name: "x", Type: "rabbitmq", Password: "v1-secret"}
	_ = s.Connections.Create(ctx, DefaultTenantID, c)

	c.Password = "v2-secret"
	if err := s.Connections.Update(ctx, DefaultTenantID, c); err != nil {
		t.Fatal(err)
	}

	var raw string
	_ = s.DB.QueryRowContext(ctx, `SELECT password FROM connections WHERE id=?`, c.ID).Scan(&raw)
	if !strings.HasPrefix(raw, secrets.EncryptedPrefix) {
		t.Errorf("update did not encrypt: %q", raw)
	}
	if strings.Contains(raw, "v2-secret") {
		t.Errorf("plaintext leaked into stored value: %q", raw)
	}

	got, _ := s.Connections.Get(ctx, DefaultTenantID, c.ID)
	if got.Password != "v2-secret" {
		t.Errorf("read-back wrong: %q", got.Password)
	}
}
