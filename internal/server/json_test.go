package server

import (
	"net/http/httptest"
	"testing"
)

// Pins the contract: list handlers must emit `[]`, not `null`, on empty.
// A regression here breaks every Svelte page that does `.map()` on the
// fetch result without a null guard.
func TestWriteJSONList_NilSliceBecomesEmptyArray(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil any", nil, "[]\n"},
		{"nil typed slice", []*struct{ X int }(nil), "[]\n"},
		{"empty typed slice", []string{}, "[]\n"},
		{"non-empty slice", []int{1, 2}, "[1,2]\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeJSONList(rec, 200, tc.in)
			if rec.Body.String() != tc.want {
				t.Errorf("got %q, want %q", rec.Body.String(), tc.want)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Errorf("content-type = %q", ct)
			}
		})
	}
}
