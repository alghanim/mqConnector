package auth

import (
	"encoding/json"
	"net/http"
)

// writeJSON is the local response helper. Defined here so the auth package
// stays free of cross-package dependencies (server has its own helper that
// looks identical — this is fine, the duplication is small and intentional).
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
