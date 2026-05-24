package secrets

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvProvider_Empty(t *testing.T) {
	t.Setenv("MQC_MASTER_KEY", "")
	t.Setenv("MQC_MASTER_KEYS", "")
	keys, err := EnvProvider{}.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected no keys, got %d", len(keys))
	}
}

func TestEnvProvider_SingleKey(t *testing.T) {
	t.Setenv("MQC_MASTER_KEY", strings.Repeat("a", 64))
	t.Setenv("MQC_MASTER_KEYS", "")
	keys, err := EnvProvider{}.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Version != 1 {
		t.Fatalf("expected one v1 key, got %#v", keys)
	}
}

func TestEnvProvider_MultiKeyWinsOverSingle(t *testing.T) {
	t.Setenv("MQC_MASTER_KEY", "should-be-ignored")
	t.Setenv("MQC_MASTER_KEYS", "v1="+strings.Repeat("a", 64)+",v2="+strings.Repeat("b", 64))
	keys, err := EnvProvider{}.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestFileProvider_BareSingleKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte("# top comment\n"+strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := FileProvider{Path: path}.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(keys) != 1 || keys[0].Version != 1 {
		t.Fatalf("expected single v1 key, got %#v", keys)
	}
}

func TestFileProvider_MultiKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys")
	body := "# header\n" +
		"v1=" + strings.Repeat("a", 64) + "\n" +
		"v2=" + strings.Repeat("b", 64) + " # rotation candidate\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := FileProvider{Path: path}.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestFileProvider_MissingFile(t *testing.T) {
	_, err := FileProvider{Path: "/no/such/file"}.Load(context.Background())
	if err == nil {
		t.Fatalf("expected error for missing file")
	}
}

func TestFileProvider_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k")
	_ = os.WriteFile(path, []byte("# nothing here\n\n"), 0o600)
	_, err := FileProvider{Path: path}.Load(context.Background())
	if err == nil {
		t.Fatal("expected error for empty key file")
	}
}

func TestVaultProvider_ReadsKVv2Secret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/secret/data/mqc/master" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Vault-Token") != "abc.123" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"v1":"` +
			strings.Repeat("a", 64) +
			`","v2":"` + strings.Repeat("b", 64) + `","unrelated":"ignored"}}}`))
	}))
	defer srv.Close()

	p := &VaultProvider{
		Address: srv.URL,
		Token:   "abc.123",
		Mount:   "secret",
		Path:    "mqc/master",
		HTTP:    srv.Client(),
	}
	keys, err := p.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d (%#v)", len(keys), keys)
	}
	versions := map[int]bool{}
	for _, k := range keys {
		versions[k.Version] = true
	}
	if !versions[1] || !versions[2] {
		t.Fatalf("expected v1+v2, got %#v", versions)
	}
}

func TestVaultProvider_BadCredentialsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
	}))
	defer srv.Close()
	p := &VaultProvider{
		Address: srv.URL, Token: "wrong", Mount: "secret", Path: "mqc",
		HTTP: srv.Client(),
	}
	_, err := p.Load(context.Background())
	if err == nil {
		t.Fatal("expected error on permission denied")
	}
}

func TestVaultProvider_MissingSecretIs404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	p := &VaultProvider{
		Address: srv.URL, Token: "t", Mount: "secret", Path: "missing",
		HTTP: srv.Client(),
	}
	_, err := p.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got %v", err)
	}
}

func TestFromProvider_ConstructsServiceFromVault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"data":{"v1":"` + strings.Repeat("a", 64) + `"}}}`))
	}))
	defer srv.Close()
	p := &VaultProvider{
		Address: srv.URL, Token: "t", Mount: "s", Path: "p", HTTP: srv.Client(),
	}
	svc, err := FromProvider(context.Background(), p)
	if err != nil {
		t.Fatalf("FromProvider: %v", err)
	}
	if !svc.Enabled() {
		t.Fatalf("expected enabled service")
	}
	if svc.Current() != 1 {
		t.Fatalf("expected current=1, got %d", svc.Current())
	}
	// Round-trip a value.
	ct, err := svc.Encrypt("hello")
	if err != nil {
		t.Fatal(err)
	}
	pt, err := svc.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if pt != "hello" {
		t.Fatalf("round-trip mismatch: %q", pt)
	}
}
