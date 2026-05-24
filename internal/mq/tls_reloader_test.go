package mq

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// genKeypair writes a freshly-generated self-signed ECDSA cert + key
// to the given paths. Used by the reload tests to simulate a cert
// rotation — every call produces a fresh keypair so the post-rotation
// fingerprint is distinguishable from the pre-rotation one.
func genKeypair(t *testing.T, certPath, keyPath, cn string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template,
		&priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClientCertReloader_LoadsCert(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "client.crt")
	key := filepath.Join(dir, "client.key")
	genKeypair(t, cert, key, "v1")

	r, err := getOrCreateReloader(cert, key)
	if err != nil {
		t.Fatalf("getOrCreateReloader: %v", err)
	}
	c, err := r.GetClientCertificate(nil)
	if err != nil {
		t.Fatalf("GetClientCertificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if leaf.Subject.CommonName != "v1" {
		t.Fatalf("expected CN=v1, got %s", leaf.Subject.CommonName)
	}
}

func TestClientCertReloader_PicksUpRotation(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "client.crt")
	key := filepath.Join(dir, "client.key")
	genKeypair(t, cert, key, "v1")

	r, err := getOrCreateReloader(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	c1, _ := r.GetClientCertificate(nil)
	cn1 := commonNameOf(t, c1)

	// Wait long enough that mtime resolution definitely advances,
	// then rotate. macOS has 1s mtime granularity on some FSes.
	time.Sleep(1100 * time.Millisecond)
	genKeypair(t, cert, key, "v2")

	c2, err := r.GetClientCertificate(nil)
	if err != nil {
		t.Fatalf("post-rotation GetClientCertificate: %v", err)
	}
	cn2 := commonNameOf(t, c2)
	if cn2 == cn1 {
		t.Fatalf("reloader did not pick up rotation: still serving %s", cn1)
	}
	if cn2 != "v2" {
		t.Fatalf("expected CN=v2 after rotation, got %s", cn2)
	}
}

func TestClientCertReloader_CachedAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "c.crt")
	key := filepath.Join(dir, "c.key")
	genKeypair(t, cert, key, "x")

	r1, _ := getOrCreateReloader(cert, key)
	r2, _ := getOrCreateReloader(cert, key)
	if r1 != r2 {
		t.Fatalf("expected same reloader instance for the same paths")
	}
}

func TestBuildTLSConfig_WiresGetClientCertificate(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "c.crt")
	key := filepath.Join(dir, "c.key")
	genKeypair(t, cert, key, "wired")

	cfg, err := BuildTLSConfig(TLSConfig{
		CertFile: cert,
		KeyFile:  key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil tls.Config")
	}
	if cfg.GetClientCertificate == nil {
		t.Fatal("GetClientCertificate not wired — rotation would not work")
	}
	if len(cfg.Certificates) != 0 {
		t.Fatalf("expected empty Certificates (rotation uses GetClientCertificate); got %d", len(cfg.Certificates))
	}
}

func commonNameOf(t *testing.T, c *tls.Certificate) string {
	t.Helper()
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.Subject.CommonName
}
