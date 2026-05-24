package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
)

// payloadHash returns the SHA-256 hex digest of the outbound payload.
// The dedup window keys on this hash so two byte-identical payloads
// collide regardless of source-broker delivery semantics (redeliveries,
// at-least-once duplicates). Stage transforms run before the hash, so
// canonicalisation (e.g. JSON → JSON with sorted keys) lets operators
// dedup payloads that arrive in different serial forms by adding a
// canonicalising stage upstream of the implicit dedup step.
func payloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
