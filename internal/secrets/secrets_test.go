package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
)

func newKey(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(k)
}

func TestNilService_IsNoOp(t *testing.T) {
	var s *Service
	got, err := s.Encrypt("plaintext")
	if err != nil || got != "plaintext" {
		t.Errorf("nil Encrypt: %q %v", got, err)
	}
	got, err = s.Decrypt("plaintext")
	if err != nil || got != "plaintext" {
		t.Errorf("nil Decrypt: %q %v", got, err)
	}
}

func TestRoundTrip(t *testing.T) {
	s, err := New(newKey(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, plaintext := range []string{
		"hunter2",
		"with spaces and !@#$%^&*",
		strings.Repeat("x", 4096),
		"",
	} {
		ct, err := s.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", plaintext, err)
		}
		if plaintext != "" && !strings.HasPrefix(ct, EncryptedPrefix) {
			t.Errorf("ciphertext missing prefix: %q", ct)
		}
		pt, err := s.Decrypt(ct)
		if err != nil {
			t.Fatalf("Decrypt(%q): %v", ct, err)
		}
		if pt != plaintext {
			t.Errorf("roundtrip: want %q got %q", plaintext, pt)
		}
	}
}

func TestEncrypt_IdempotentOnAlreadyEncrypted(t *testing.T) {
	s, _ := New(newKey(t))
	ct, _ := s.Encrypt("secret")
	ct2, err := s.Encrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if ct != ct2 {
		t.Errorf("double-encrypt should be a no-op: %q vs %q", ct, ct2)
	}
}

func TestEncrypt_DifferentNoncesForSamePlaintext(t *testing.T) {
	s, _ := New(newKey(t))
	a, _ := s.Encrypt("same")
	b, _ := s.Encrypt("same")
	if a == b {
		t.Errorf("expected nonces to differ between calls")
	}
}

func TestDecrypt_PassesThroughPlaintext(t *testing.T) {
	s, _ := New(newKey(t))
	pt, err := s.Decrypt("not encrypted")
	if err != nil || pt != "not encrypted" {
		t.Errorf("unexpected: %q %v", pt, err)
	}
}

func TestDecrypt_EncryptedValueWithoutKeyErrors(t *testing.T) {
	s, _ := New(newKey(t))
	ct, _ := s.Encrypt("secret")

	var nilSvc *Service
	if _, err := nilSvc.Decrypt(ct); err == nil {
		t.Error("expected error when decrypting an encrypted value with no key")
	}
}

func TestDecrypt_TamperedCiphertextErrors(t *testing.T) {
	s, _ := New(newKey(t))
	ct, _ := s.Encrypt("secret")
	tampered := ct[:len(ct)-2] + "AA"
	if _, err := s.Decrypt(tampered); err == nil {
		t.Error("expected GCM auth failure on tampered ciphertext")
	}
}

func TestNew_RejectsShortKeys(t *testing.T) {
	if _, err := New("0123456789abcdef"); err == nil {
		t.Error("16-char hex should be rejected (need 64)")
	}
}

func TestFromEnv_NoKey_ReturnsNilService(t *testing.T) {
	t.Setenv("MQC_MASTER_KEY", "")
	s, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Errorf("empty env should return nil service")
	}
}

func TestFromEnv_GoodKey(t *testing.T) {
	t.Setenv("MQC_MASTER_KEY", newKey(t))
	s, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Enabled() {
		t.Errorf("expected enabled service")
	}
}

// TestRotate_NewCiphertextUnderNewKey confirms that after a rotation
// Encrypt produces output tagged with the new version, while old
// ciphertext still decrypts cleanly under the prior version's key.
func TestRotate_NewCiphertextUnderNewKey(t *testing.T) {
	s, err := New(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	oldCT, _ := s.Encrypt("v1-secret")
	if !strings.HasPrefix(oldCT, "enc:v1:") {
		t.Fatalf("expected enc:v1: prefix, got %q", oldCT)
	}

	newVersion, _, err := s.Rotate()
	if err != nil {
		t.Fatal(err)
	}
	if newVersion != 2 {
		t.Fatalf("expected new version 2, got %d", newVersion)
	}

	newCT, _ := s.Encrypt("v2-secret")
	if !strings.HasPrefix(newCT, "enc:v2:") {
		t.Fatalf("expected enc:v2: prefix, got %q", newCT)
	}

	// Both decrypt under the same Service instance.
	if pt, _ := s.Decrypt(oldCT); pt != "v1-secret" {
		t.Errorf("old ciphertext decrypt: %q", pt)
	}
	if pt, _ := s.Decrypt(newCT); pt != "v2-secret" {
		t.Errorf("new ciphertext decrypt: %q", pt)
	}
}

// TestRewrap_MigratesOldToCurrent re-encrypts a v1 ciphertext under v2
// and confirms the new form decrypts to the same plaintext.
func TestRewrap_MigratesOldToCurrent(t *testing.T) {
	s, err := New(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	oldCT, _ := s.Encrypt("secret")
	if _, _, err := s.Rotate(); err != nil {
		t.Fatal(err)
	}

	rewrapped, err := s.Rewrap(oldCT)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rewrapped, "enc:v2:") {
		t.Fatalf("expected enc:v2: after rewrap, got %q", rewrapped)
	}
	if pt, _ := s.Decrypt(rewrapped); pt != "secret" {
		t.Errorf("rewrap round-trip lost plaintext: %q", pt)
	}
	// Rewrap of an already-current ciphertext is a no-op.
	rewrapped2, _ := s.Rewrap(rewrapped)
	if rewrapped2 != rewrapped {
		t.Errorf("rewrap of current should be no-op")
	}
}

// TestDecrypt_UnknownVersionErrors ensures a ciphertext from a key the
// operator has dropped surfaces as an explicit error, not silent
// corruption.
func TestDecrypt_UnknownVersionErrors(t *testing.T) {
	s, _ := New(newKey(t))
	fake := "enc:v99:" + strings.Repeat("A", 30)
	if _, err := s.Decrypt(fake); err == nil {
		t.Fatal("expected error decrypting unknown version, got nil")
	}
}

// TestFromEnv_Multi parses MQC_MASTER_KEYS into a multi-version service
// with the highest version as current.
func TestFromEnv_Multi(t *testing.T) {
	k1 := newKey(t)
	k2 := newKey(t)
	t.Setenv("MQC_MASTER_KEY", "")
	t.Setenv("MQC_MASTER_KEYS", "v1="+k1+", v3="+k2)
	s, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if s.Current() != 3 {
		t.Fatalf("expected current=3, got %d", s.Current())
	}
	versions := s.Versions()
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 3 {
		t.Fatalf("versions: %v", versions)
	}
}
