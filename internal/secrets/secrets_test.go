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
