package server

import (
	"encoding/json"
	"net/http"
	"reflect"
)

// writeJSON serialises body as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

// writeJSONList serialises a slice (typically a list-handler result) as JSON
// with the given status. Unlike writeJSON, a nil slice is emitted as `[]`
// rather than `null`, so JS callers can safely `.map()` / `.length` over
// the result without a null guard. Pass anything other than a slice and it
// falls back to writeJSON.
func writeJSONList(w http.ResponseWriter, status int, body any) {
	if body == nil {
		writeJSON(w, status, []any{})
		return
	}
	v := reflect.ValueOf(body)
	if v.Kind() == reflect.Slice && v.IsNil() {
		// Empty slice of the right element type so the JSON shape is `[]`.
		writeJSON(w, status, reflect.MakeSlice(v.Type(), 0, 0).Interface())
		return
	}
	writeJSON(w, status, body)
}

// writeError emits a {"error":"..."} envelope.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// decodeJSON binds the request body into v. Returns an error suitable for
// 400 responses.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
