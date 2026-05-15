package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders_CSPHasFreshNoncePerRequest(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	collect := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		csp := rec.Header().Get("Content-Security-Policy")
		i := strings.Index(csp, "'nonce-")
		if i < 0 {
			t.Fatalf("CSP missing nonce directive: %q", csp)
		}
		end := strings.Index(csp[i:], "'")
		end2 := strings.Index(csp[i+1:], "'")
		_ = end
		return csp[i+1 : i+1+end2]
	}
	a, b := collect(), collect()
	if a == "" || b == "" {
		t.Fatalf("empty nonce: a=%q b=%q", a, b)
	}
	if a == b {
		t.Errorf("nonces must differ per request, got %q twice", a)
	}
}

func TestInjectCSPNonce_RewritesInlineScripts(t *testing.T) {
	body := []byte(`<html>
<head>
  <script>console.log("inline 1")</script>
  <script type="module" src="/_app/start.js"></script>
  <script>
    (function () { /* inline 2 */ })();
  </script>
</head>
</html>`)
	out := injectCSPNonce(body, "ABCDEFG")
	// Two inline openers → two nonces. The src= module script must NOT be touched.
	gotNonces := bytes.Count(out, []byte(`nonce="ABCDEFG"`))
	if gotNonces != 2 {
		t.Errorf("expected 2 nonce attributes, got %d in:\n%s", gotNonces, out)
	}
	if !bytes.Contains(out, []byte(`<script type="module" src="/_app/start.js">`)) {
		t.Errorf("external script tag was modified: %s", out)
	}
}

func TestInjectCSPNonce_EmptyNonceIsPassthrough(t *testing.T) {
	body := []byte(`<script>x</script>`)
	if !bytes.Equal(injectCSPNonce(body, ""), body) {
		t.Errorf("empty nonce should be a no-op")
	}
}
