package server

import (
	"context"
	"net/http"
	"time"

	"mqConnector/internal/auth"
	"mqConnector/internal/logging"
	"mqConnector/internal/storage"
)

// handleSecretsStatus returns the current key version and the full list
// of configured versions. Read-only — anyone with admin can see this.
// Useful for confirming a rotation actually took effect.
func (s *Server) handleSecretsStatus(w http.ResponseWriter, r *http.Request) {
	if s.sealer == nil || !s.sealer.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":  false,
			"current":  0,
			"versions": []int{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  true,
		"current":  s.sealer.Current(),
		"versions": s.sealer.Versions(),
	})
}

// handleRotateSecrets generates a new key, installs it as the next
// version, and rewraps every stored connection password under it.
// Returns the new key hex so the operator persists it to MQC_MASTER_KEYS
// (or their secrets store of choice). Without that step, a restart
// would lose the new key and every freshly-rewrapped row would become
// unreadable.
//
// Authorisation: this is destructive enough to require the system-admin
// (default-tenant owner). A regular tenant owner can't rotate the
// shared master key.
func (s *Server) handleRotateSecrets(w http.ResponseWriter, r *http.Request) {
	if s.sealer == nil || !s.sealer.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "encryption disabled: set MQC_MASTER_KEY first")
		return
	}

	// Coarse system-admin check (owner of the default tenant). Matches
	// the policy used by the audit verifier; we'll tighten this once
	// the SystemAdmin flag lands.
	tenant := auth.TenantID(r.Context())
	if tenant != storage.DefaultTenantID {
		writeError(w, http.StatusForbidden, "system-admin required")
		return
	}

	prevVersion := s.sealer.Current()
	newVersion, encodedKey, err := s.sealer.Rotate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Rewrap every stored password under the new key. Use a fresh
	// context with a generous timeout so a slow disk doesn't 504 the
	// rotation mid-walk; on cancel, partially-rewrapped rows are still
	// readable (old key is still installed in-memory).
	rwCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rewrapped, skipped, err := s.store.Connections.RewrapPasswords(rwCtx, s.sealer)
	if err != nil {
		logging.FromContext(r.Context()).Warn("rewrap after rotate failed",
			"err", err, "new_version", newVersion)
		writeError(w, http.StatusInternalServerError, "rotate succeeded but rewrap failed: "+err.Error())
		return
	}

	logging.FromContext(r.Context()).Info("secrets rotated",
		"prev_version", prevVersion,
		"new_version", newVersion,
		"rewrapped", rewrapped,
		"skipped", skipped,
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"prev_version":   prevVersion,
		"new_version":    newVersion,
		"key_hex":        encodedKey,
		"rewrapped_rows": rewrapped,
		"skipped_rows":   skipped,
		"note":           "Persist key_hex into MQC_MASTER_KEYS (e.g. v" + itoa(newVersion) + "=...) before restarting.",
	})
}

// itoa avoids the strconv import for a single use.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
