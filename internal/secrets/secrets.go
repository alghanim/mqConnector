// Package secrets implements envelope encryption for values that must be
// persisted but the bridge needs to read at runtime (chiefly MQ connection
// passwords). The scheme:
//
//   - The operator supplies one or more 32-byte master keys, each encoded
//     as hex (64 chars) or base64 (44 chars). Single-key deploys use
//     MQC_MASTER_KEY. Multi-version deploys use MQC_MASTER_KEYS, a
//     comma-separated list of `vN=key` pairs where the highest version is
//     the current.
//   - Every ciphertext is AES-256-GCM over a fresh random 12-byte nonce.
//     The stored form is `enc:v{N}:` || base64(nonce || ciphertext || tag).
//     N is the version of the key that produced it.
//   - Encrypt always uses the CURRENT (highest-version) key. Decrypt
//     parses N from the prefix and selects the matching key, so rows
//     written under older keys still decrypt cleanly. Rotation is
//     therefore live: an operator adds v2, restarts (or calls
//     AddKey at runtime), and writes go out under v2 while v1 rows
//     keep round-tripping. Rewrap can later re-encrypt a ciphertext
//     under the current key.
//   - A constant-prefix sentinel lets us round-trip plaintext and
//     ciphertext in the same column for backwards compatibility: rows
//     written before encryption was enabled stay readable, and Encrypt
//     is a no-op on values that already carry the EncryptedPrefix.
//
// This is NOT a replacement for a real KMS. Treat it as defence in depth:
// it stops a casual reader of the SQLite file from seeing every broker
// password in cleartext, and gives you a clean rotation path without a
// restart-driven all-or-nothing migration.
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
	"strconv"
	"strings"
	"sync"
)

// EncryptedPrefix is the literal byte sequence that marks the start of
// a stored ciphertext. The "v" inside is decorative — the actual
// version is parsed from "enc:v{N}:" with {N} an integer.
const EncryptedPrefix = "enc:"

// Service is the value-encryption facade. A nil-valued Service is a
// valid, degrading-gracefully no-op — Encrypt returns the input
// untouched and Decrypt assumes the input is already plaintext. That
// keeps the code path uniform when an operator hasn't configured a
// master key (e.g. dev mode).
//
// Concurrency: AddKey takes a write lock; Encrypt / Decrypt / Rewrap
// take a read lock. Read paths fight only with rotations, not with
// each other, so steady-state throughput is unaffected.
type Service struct {
	mu       sync.RWMutex
	keys     map[int]cipher.AEAD // version → AEAD
	current  int                 // highest version in keys
}

// FromEnv constructs a Service from MQC_MASTER_KEY (single-version) or
// MQC_MASTER_KEYS (multi-version).
//
//   - both empty → nil Service (encryption disabled).
//   - MQC_MASTER_KEY only → v1 keyed with that value.
//   - MQC_MASTER_KEYS → comma-separated list of `v{N}=key`. The highest
//     N becomes the current. Whitespace is tolerated. Repeated versions
//     are an error.
//
// If both are set, MQC_MASTER_KEYS wins (the multi-version form is
// strictly more general; MQC_MASTER_KEY stays for back-compat).
func FromEnv() (*Service, error) {
	if multi := strings.TrimSpace(os.Getenv("MQC_MASTER_KEYS")); multi != "" {
		return parseMultiEnv(multi)
	}
	raw := strings.TrimSpace(os.Getenv("MQC_MASTER_KEY"))
	if raw == "" {
		return nil, nil
	}
	return New(raw)
}

// New builds a Service from a single hex/base64 master key as version 1.
// For multi-version use, see FromEnv or NewWithKeys.
func New(encoded string) (*Service, error) {
	return NewWithKeys(map[int]string{1: encoded})
}

// NewWithKeys builds a Service from a map of version → encoded key.
// At least one entry is required. The highest version becomes the
// current (used for all Encrypt calls).
func NewWithKeys(encoded map[int]string) (*Service, error) {
	if len(encoded) == 0 {
		return nil, errors.New("secrets: at least one key required")
	}
	s := &Service{keys: map[int]cipher.AEAD{}}
	for v, k := range encoded {
		if v < 1 {
			return nil, fmt.Errorf("secrets: key version must be >= 1, got %d", v)
		}
		aead, err := buildAEAD(k)
		if err != nil {
			return nil, fmt.Errorf("secrets: v%d: %w", v, err)
		}
		s.keys[v] = aead
		if v > s.current {
			s.current = v
		}
	}
	return s, nil
}

// AddKey installs a new key at the given version. If `version` is
// higher than the current, it becomes the new current — i.e. all
// future Encrypt calls go out under this key. Idempotent for the
// exact same version+key pair; an attempt to overwrite a version
// with a different key is rejected (rotation should be additive, not
// rewrite-in-place).
func (s *Service) AddKey(version int, encoded string) error {
	if s == nil {
		return errors.New("secrets: service is nil")
	}
	if version < 1 {
		return fmt.Errorf("secrets: version must be >= 1, got %d", version)
	}
	aead, err := buildAEAD(encoded)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.keys[version]; ok {
		// Refuse to silently overwrite. Test seal/open against the new
		// AEAD with a fixed nonce — if both produce the same ciphertext,
		// the keys are identical and this is a no-op.
		if !sameAEADKey(existing, aead) {
			return fmt.Errorf("secrets: v%d already exists with a different key", version)
		}
		return nil
	}
	s.keys[version] = aead
	if version > s.current {
		s.current = version
	}
	return nil
}

// Rotate generates a fresh 32-byte key, installs it as the next
// version (current+1), and returns the new key encoded as hex so the
// operator can persist it to config/secrets store. Convenience for
// the admin endpoint; equivalent to calling AddKey with a freshly
// generated key.
func (s *Service) Rotate() (newVersion int, encodedKey string, err error) {
	if s == nil {
		return 0, "", errors.New("secrets: service is nil")
	}
	var key [32]byte
	if _, err := io.ReadFull(rand.Reader, key[:]); err != nil {
		return 0, "", fmt.Errorf("secrets: random key: %w", err)
	}
	encoded := hex.EncodeToString(key[:])
	s.mu.Lock()
	next := s.current + 1
	s.mu.Unlock()
	if err := s.AddKey(next, encoded); err != nil {
		return 0, "", err
	}
	return next, encoded, nil
}

// Current returns the version that Encrypt will use right now. Useful
// for admin tooling that wants to confirm rotation succeeded.
func (s *Service) Current() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Versions returns the sorted list of known key versions. Sorted so
// the admin UI / status endpoint can render them in a stable order.
func (s *Service) Versions() []int {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]int, 0, len(s.keys))
	for v := range s.keys {
		out = append(out, v)
	}
	// Inline insertion sort — len is tiny.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// Encrypt returns the ciphertext form of plaintext under the current
// key. Idempotent on already-encrypted values, no-op on empty
// plaintext (so the schema's optional fields stay distinguishable
// from a real empty result).
func (s *Service) Encrypt(plaintext string) (string, error) {
	if s == nil {
		return plaintext, nil
	}
	if plaintext == "" {
		return "", nil
	}
	if strings.HasPrefix(plaintext, EncryptedPrefix) {
		return plaintext, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	aead := s.keys[s.current]
	if aead == nil {
		return plaintext, nil
	}
	return seal(aead, s.current, plaintext)
}

// Decrypt returns the plaintext form of value. Legacy plaintext rows
// (no prefix) pass through unchanged. If the value carries a version
// the Service doesn't know, we return an error rather than silently
// returning the ciphertext — that's almost certainly a configuration
// mismatch the operator needs to see.
func (s *Service) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, EncryptedPrefix) {
		return value, nil
	}
	if s == nil {
		return "", errors.New("secrets: encrypted value found but no master key configured")
	}
	version, payload, err := parseCiphertext(value)
	if err != nil {
		return "", err
	}
	s.mu.RLock()
	aead := s.keys[version]
	s.mu.RUnlock()
	if aead == nil {
		return "", fmt.Errorf("secrets: no key configured for v%d (rotation lost?)", version)
	}
	if len(payload) < aead.NonceSize() {
		return "", errors.New("secrets: ciphertext too short")
	}
	nonce, ct := payload[:aead.NonceSize()], payload[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("secrets: decrypt: %w", err)
	}
	return string(pt), nil
}

// Rewrap upgrades a stored value to ciphertext under the current key.
// Behaviour by input:
//
//   - empty                              → empty (no-op)
//   - service disabled                   → input unchanged (no-op)
//   - plaintext (no prefix)              → encrypted under current key
//   - ciphertext at the current version  → unchanged (fast path)
//   - ciphertext at an older version     → decrypt + re-encrypt under
//                                          current
//
// Callers use Rewrap as the rotation migration primitive: "for every
// row, Rewrap and persist the result". The fast path means a second
// run is effectively free.
func (s *Service) Rewrap(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if s == nil || !s.Enabled() {
		return value, nil
	}
	if !strings.HasPrefix(value, EncryptedPrefix) {
		// Plaintext — pull it up to current.
		return s.Encrypt(value)
	}
	version, _, err := parseCiphertext(value)
	if err != nil {
		return "", err
	}
	if version == s.Current() {
		return value, nil
	}
	plaintext, err := s.Decrypt(value)
	if err != nil {
		return "", err
	}
	return s.Encrypt(plaintext)
}

// Enabled reports whether a usable AEAD is configured.
func (s *Service) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.keys) > 0
}

// ─── internal helpers ────────────────────────────────────────────────

func buildAEAD(encoded string) (cipher.AEAD, error) {
	key, err := decodeKey(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return aead, nil
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

func seal(aead cipher.AEAD, version int, plaintext string) (string, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("secrets: random nonce: %w", err)
	}
	ct := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return fmt.Sprintf("enc:v%d:%s", version, base64.RawStdEncoding.EncodeToString(ct)), nil
}

// parseCiphertext returns (version, payload-bytes, err) for a value of
// the form "enc:v{N}:{base64}". The leading "enc:" prefix must have
// been checked by the caller.
func parseCiphertext(value string) (int, []byte, error) {
	body := strings.TrimPrefix(value, EncryptedPrefix)
	// body == "v{N}:{base64}"
	i := strings.IndexByte(body, ':')
	if i <= 1 || body[0] != 'v' {
		return 0, nil, errors.New("secrets: malformed ciphertext header")
	}
	version, err := strconv.Atoi(body[1:i])
	if err != nil || version < 1 {
		return 0, nil, errors.New("secrets: invalid version in ciphertext header")
	}
	payload, err := base64.RawStdEncoding.DecodeString(body[i+1:])
	if err != nil {
		return 0, nil, fmt.Errorf("secrets: decode ciphertext: %w", err)
	}
	return version, payload, nil
}

// parseMultiEnv parses the MQC_MASTER_KEYS form `v1=hex,v2=hex,...`.
func parseMultiEnv(s string) (*Service, error) {
	entries := strings.Split(s, ",")
	keys := map[int]string{}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		eq := strings.IndexByte(e, '=')
		if eq <= 1 || e[0] != 'v' {
			return nil, fmt.Errorf("secrets: MQC_MASTER_KEYS entry %q must be of the form vN=hex", e)
		}
		version, err := strconv.Atoi(e[1:eq])
		if err != nil || version < 1 {
			return nil, fmt.Errorf("secrets: MQC_MASTER_KEYS entry %q: bad version", e)
		}
		if _, dup := keys[version]; dup {
			return nil, fmt.Errorf("secrets: MQC_MASTER_KEYS: v%d appears twice", version)
		}
		keys[version] = strings.TrimSpace(e[eq+1:])
	}
	return NewWithKeys(keys)
}

// sameAEADKey returns true iff two AEAD instances produce the same
// ciphertext for the same plaintext under the same nonce. AES-GCM is
// deterministic given nonce + plaintext + key, so this is a reliable
// equality check without exposing the key bytes.
func sameAEADKey(a, b cipher.AEAD) bool {
	if a.NonceSize() != b.NonceSize() {
		return false
	}
	nonce := make([]byte, a.NonceSize())
	pt := []byte("mqconnector-aead-equality-probe")
	ca := a.Seal(nil, nonce, pt, nil)
	cb := b.Seal(nil, nonce, pt, nil)
	if len(ca) != len(cb) {
		return false
	}
	var diff byte
	for i := range ca {
		diff |= ca[i] ^ cb[i]
	}
	return diff == 0
}
