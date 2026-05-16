package mq

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// BuildTLSConfig assembles a *tls.Config from the connection's TLS
// fields. Returns (nil, nil) when no TLS material is configured —
// callers should check before passing to a dialer.
//
// Loading is fail-fast: a path that exists but doesn't parse returns
// an error rather than silently producing a half-built tls.Config.
// CertFile and KeyFile must be supplied together (mTLS); supplying
// only one is rejected.
func BuildTLSConfig(t TLSConfig) (*tls.Config, error) {
	if !t.Enabled() {
		return nil, nil
	}
	if (t.CertFile != "") != (t.KeyFile != "") {
		return nil, errors.New("mq tls: cert_file and key_file must be supplied together")
	}

	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: t.InsecureSkipVerify,
	}

	if t.CAFile != "" {
		caPEM, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, fmt.Errorf("mq tls: read ca_file %q: %w", t.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("mq tls: ca_file %q: no certificates found", t.CAFile)
		}
		cfg.RootCAs = pool
	}

	if t.CertFile != "" && t.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("mq tls: load keypair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}
