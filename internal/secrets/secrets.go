// Package secrets implements envelope encryption for values that must be
// persisted but the bridge needs to read at runtime (chiefly MQ connection
// passwords). The scheme:
//
//   - The operator supplies a 32-byte master key in the MQC_MASTER_KEY env
//     var, encoded as hex (64 chars) or base64 (44 chars). The key is read
//     once at startup and never logged.
//   - Every ciphertext is AES-256-GCM over a fresh random 12-byte nonce.
//     The stored form is base64( nonce || ciphertext || tag ) prefixed with
//     "v1:" so future key rotations can introduce v2 without breaking old
//     rows.
//   - A constant-prefix sentinel lets us round-trip plaintext and ciphertext
//     in the same column for backwards compatibility: rows written before
//     encryption was enabled stay readable, and Encrypt is a no-op on values
//     that are already encrypted.
//
// This is NOT a replacement for a real KMS. Treat it as defence in depth:
// it stops a casual reader of the SQLite file from seeing every broker
// password in cleartext.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// EncryptedPrefix marks a stored value as ciphertext. The "v1:" component
// is the wire-format version; bumped if the algorithm ever changes.
const EncryptedPrefix = "enc:v1:"

// Service is the value-encryption facade. A nil-valued Service is a valid,
// degrading-gracefully no-op — Encrypt returns the input untouched and
// Decrypt assumes the input is already plaintext. That keeps the code path
// uniform when an operator hasn't configured a master key (e.g. dev mode).
type Service struct {
	aead cipher.AEAD
}

// FromEnv constructs a Service from the MQC_MASTER_KEY env var.
//
//   - empty / unset → returns a nil Service (encryption disabled).
//   - 64 hex chars or 44 base64 chars → 32-byte key for AES-256-GCM.
//   - anything else → error.
func FromEnv() (*Service, error) {
	raw := strings.TrimSpace(os.Getenv("MQC_MASTER_KEY"))
	if raw == "" {
		return nil, nil
	}
	return New(raw)
}

// New builds a Service from a hex or base64 master key.
func New(encoded string) (*Service, error) {
	key, err := decodeKey(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: gcm: %w", err)
	}
	return &Service{aead: aead}, nil
}

func decodeKey(s string) ([]byte, error) {
	if k, err := hex.DecodeString(s); err == nil && len(k) == 32 {
		return k, nil
	}
	if k, err := base64.StdEncoding.DecodeString(s); err == nil && len(k) == 32 {
		return k, nil
	}
	if k, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(k) == 32 {
		return k, nil
	}
	return nil, errors.New("secrets: MQC_MASTER_KEY must be 32 bytes encoded as hex or base64")
}

// Encrypt returns the ciphertext form of plaintext. Idempotent: a value that
// already carries EncryptedPrefix is returned unchanged so it's safe to call
// on rows of mixed provenance. Empty plaintext is returned unchanged so we
// don't accidentally produce ciphertext for empty fields (which the schema
// permits and many connectors expect).
func (s *Service) Encrypt(plaintext string) (string, error) {
	if s == nil || s.aead == nil {
		return plaintext, nil
	}
	if plaintext == "" {
		return "", nil
	}
	if strings.HasPrefix(plaintext, EncryptedPrefix) {
		return plaintext, nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("secrets: random nonce: %w", err)
	}
	ct := s.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return EncryptedPrefix + base64.RawStdEncoding.EncodeToString(ct), nil
}

// Decrypt returns the plaintext form of value. If the value does not carry
// the EncryptedPrefix it is returned unchanged — that handles legacy rows
// written before encryption was enabled. If encryption is disabled but the
// value IS prefixed, Decrypt returns an error so silent data corruption is
// impossible.
func (s *Service) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, EncryptedPrefix) {
		return value, nil
	}
	if s == nil || s.aead == nil {
		return "", errors.New("secrets: encrypted value found but no master key configured")
	}
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, EncryptedPrefix))
	if err != nil {
		return "", fmt.Errorf("secrets: decode ciphertext: %w", err)
	}
	if len(payload) < s.aead.NonceSize() {
		return "", errors.New("secrets: ciphertext too short")
	}
	nonce, ct := payload[:s.aead.NonceSize()], payload[s.aead.NonceSize():]
	pt, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("secrets: decrypt: %w", err)
	}
	return string(pt), nil
}

// Enabled reports whether a usable AEAD is configured.
func (s *Service) Enabled() bool { return s != nil && s.aead != nil }
