package mq

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// generateLeafCert produces a self-signed cert + private key suitable
// for a test, written to PEM files in `dir`. Returns (certPath, keyPath).
// Cheap (ECDSA P-256) so unit tests don't pay for RSA keygen.
func generateLeafCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mqc-test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestBuildTLSConfig_Disabled(t *testing.T) {
	cfg, err := BuildTLSConfig(TLSConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Errorf("expected nil tls.Config when not enabled, got %+v", cfg)
	}
}

func TestBuildTLSConfig_SkipVerifyOnly(t *testing.T) {
	cfg, err := BuildTLSConfig(TLSConfig{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || !cfg.InsecureSkipVerify {
		t.Errorf("expected non-nil cfg with InsecureSkipVerify=true, got %+v", cfg)
	}
}

func TestBuildTLSConfig_FullMTLS(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateLeafCert(t, dir)
	// Reuse the leaf cert as both client cert and CA root for the test.
	cfg, err := BuildTLSConfig(TLSConfig{
		CAFile:   certPath,
		CertFile: certPath,
		KeyFile:  keyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil cfg")
	}
	if cfg.RootCAs == nil {
		t.Error("expected RootCAs populated from ca_file")
	}
	// Client cert is wired via GetClientCertificate (hot reload) instead
	// of cfg.Certificates so a rotated keypair is picked up on the next
	// broker handshake without a process restart.
	if cfg.GetClientCertificate == nil {
		t.Error("expected GetClientCertificate populated from cert_file/key_file")
	}
	if len(cfg.Certificates) != 0 {
		t.Errorf("expected Certificates to be empty (rotation uses GetClientCertificate); got %d", len(cfg.Certificates))
	}
	// And the callback must actually return a usable cert.
	c, err := cfg.GetClientCertificate(nil)
	if err != nil || c == nil || len(c.Certificate) == 0 {
		t.Errorf("GetClientCertificate did not return a usable cert: cert=%v err=%v", c, err)
	}
}

// Supplying only one half of the keypair is a misconfiguration we
// surface as an error rather than silently dropping mTLS.
func TestBuildTLSConfig_PartialKeyPair(t *testing.T) {
	_, err := BuildTLSConfig(TLSConfig{CertFile: "/tmp/c.pem"})
	if err == nil {
		t.Error("expected error for cert-only configuration")
	}
}
