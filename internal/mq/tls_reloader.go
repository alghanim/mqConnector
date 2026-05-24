package mq

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// clientCertReloader caches a parsed client keypair and re-reads it
// from disk when the underlying files change. Wired into the broker
// TLS config via tls.Config.GetClientCertificate so an external cert
// rotator (cert-manager, certbot, an internal CA's renewer) can swap
// the PEM files without bouncing the process — picking up the new
// cert on the next broker reconnect.
//
// Why per-handshake stat instead of a background watcher: broker
// connections handshake rarely (once per connection lifetime — they
// hold the TCP session for hours). A goroutine per connection per
// pipeline doing file-stat polling would burn FDs and CPU without
// shortening the rotation latency, because the only thing that picks
// up a new cert is a fresh handshake anyway. Statting the files at
// handshake time piggybacks on an event we already know is happening.
//
// Cache: keyed on (certPath, keyPath). One reloader per file pair
// across all connectors — the pool already caches connector instances
// per (id, cfg) and BuildTLSConfig is called only at connector
// construction, so we don't need fine-grained per-connection state.
type clientCertReloader struct {
	certPath string
	keyPath  string

	mu       sync.RWMutex
	cert     *tls.Certificate
	certStat time.Time
	keyStat  time.Time
}

// reloaderCache memoizes clientCertReloader instances per cert+key
// pair. The cache is process-wide; pipelines that share the same
// keypair share the same reloader, so a single file rotation lights
// up every consumer at once. Live updates require the cert path to
// be the same byte-for-byte — different connections referencing the
// same logical cert via different paths will load independently.
var reloaderCache sync.Map // string("cert|key") -> *clientCertReloader

func getOrCreateReloader(certPath, keyPath string) (*clientCertReloader, error) {
	key := certPath + "|" + keyPath
	if existing, ok := reloaderCache.Load(key); ok {
		r := existing.(*clientCertReloader)
		// Force a stat + maybe-reload so a recently-rotated cert is
		// picked up even if no handshake has happened yet — keeps
		// the contract symmetric for callers that build the config
		// just before dialing.
		if _, err := r.currentCert(); err != nil {
			return nil, err
		}
		return r, nil
	}
	r := &clientCertReloader{certPath: certPath, keyPath: keyPath}
	if err := r.reload(); err != nil {
		return nil, err
	}
	actual, _ := reloaderCache.LoadOrStore(key, r)
	return actual.(*clientCertReloader), nil
}

func (r *clientCertReloader) reload() error {
	cert, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		return fmt.Errorf("mq tls reloader: load %s/%s: %w", r.certPath, r.keyPath, err)
	}
	certInfo, _ := os.Stat(r.certPath)
	keyInfo, _ := os.Stat(r.keyPath)
	r.mu.Lock()
	r.cert = &cert
	if certInfo != nil {
		r.certStat = certInfo.ModTime()
	}
	if keyInfo != nil {
		r.keyStat = keyInfo.ModTime()
	}
	r.mu.Unlock()
	return nil
}

// currentCert returns the cached cert, reloading from disk if the
// cert or key file has been modified since the last load. A partial
// reload (read the cert but the parse fails — e.g. cert-manager
// half-wrote the file) returns the cached cert; the failure is the
// caller's signal to either retry on the next handshake or surface
// a TLS error. We prefer "keep serving the previous cert" over "fail
// closed" because the previous cert is known-good.
func (r *clientCertReloader) currentCert() (*tls.Certificate, error) {
	certInfo, certErr := os.Stat(r.certPath)
	keyInfo, keyErr := os.Stat(r.keyPath)
	if certErr != nil || keyErr != nil {
		// Files gone — keep serving the cached cert, but tell the
		// caller something is wrong so a hint shows up in the log.
		r.mu.RLock()
		defer r.mu.RUnlock()
		if r.cert != nil {
			return r.cert, nil
		}
		return nil, errors.New("mq tls reloader: cert files not readable and no cached cert")
	}
	r.mu.RLock()
	changed := !certInfo.ModTime().Equal(r.certStat) || !keyInfo.ModTime().Equal(r.keyStat)
	cached := r.cert
	r.mu.RUnlock()
	if !changed {
		return cached, nil
	}
	if err := r.reload(); err != nil {
		// Partial write or read error — keep the previous cert. The
		// next handshake will re-check.
		r.mu.RLock()
		defer r.mu.RUnlock()
		if r.cert != nil {
			return r.cert, nil
		}
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cert, nil
}

// GetClientCertificate is the tls.Config callback. On every
// handshake it checks file mtimes and reloads if rotated.
func (r *clientCertReloader) GetClientCertificate(_ *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	c, err := r.currentCert()
	if err != nil {
		return nil, err
	}
	return c, nil
}
