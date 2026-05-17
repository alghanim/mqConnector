package server

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// certReloader holds the most-recently-loaded TLS cert + key pair and
// re-reads them from disk when the underlying files change. Used by
// the HTTPS listener via tls.Config.GetCertificate so a `systemctl
// reload` or external cert-renewer (cert-manager, certbot --post-hook)
// can swap in a new cert without bouncing the process — closing the
// "TLS expired, downtime needed" gap that bit operators on the legacy
// boot-time-load path.
//
// Detection: stat the cert + key files at startup, then again every
// CheckInterval. Any change in mtime triggers a reload attempt. A
// failed reload (file half-written by a renewer, permission error,
// etc.) logs WARN and keeps serving the previous cert — refusing to
// swap to a broken pair is preferable to refusing connections.
type certReloader struct {
	certPath string
	keyPath  string
	logger   *slog.Logger

	mu       sync.RWMutex
	current  *tls.Certificate
	certStat time.Time
	keyStat  time.Time

	// Updated atomically for hot-path lookups so GetCertificate
	// doesn't take the mutex on every handshake.
	cached atomic.Pointer[tls.Certificate]
}

func newCertReloader(certPath, keyPath string, logger *slog.Logger) (*certReloader, error) {
	r := &certReloader{
		certPath: certPath,
		keyPath:  keyPath,
		logger:   logger.With("component", "tls.reloader"),
	}
	if err := r.reload(); err != nil {
		return nil, fmt.Errorf("initial cert load: %w", err)
	}
	return r, nil
}

// reload reads the cert + key from disk and atomically swaps them in.
// Returns an error if the read or parse fails; the caller decides
// whether to surface that or fall back to the previous cert.
func (r *certReloader) reload() error {
	cert, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		return err
	}
	certInfo, _ := os.Stat(r.certPath)
	keyInfo, _ := os.Stat(r.keyPath)

	r.mu.Lock()
	r.current = &cert
	if certInfo != nil {
		r.certStat = certInfo.ModTime()
	}
	if keyInfo != nil {
		r.keyStat = keyInfo.ModTime()
	}
	r.mu.Unlock()
	r.cached.Store(&cert)
	return nil
}

// GetCertificate is the tls.Config callback. Returns the cached cert
// — no lock, no allocation, atomically swapped by reload(). This is
// called on every TLS handshake; performance matters.
func (r *certReloader) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.cached.Load(), nil
}

// Watch polls the cert + key files at interval. Cancels when stop
// closes. A modification on either file triggers a reload; failures
// are logged but don't kill the watcher — the next tick tries again,
// which handles the "file half-written by certbot" race naturally.
func (r *certReloader) Watch(interval time.Duration, stop <-chan struct{}) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.checkAndReload()
		}
	}
}

func (r *certReloader) checkAndReload() {
	certInfo, certErr := os.Stat(r.certPath)
	keyInfo, keyErr := os.Stat(r.keyPath)
	if certErr != nil || keyErr != nil {
		r.logger.Warn("cert file stat failed",
			"cert_err", certErr, "key_err", keyErr)
		return
	}
	r.mu.RLock()
	changed := !certInfo.ModTime().Equal(r.certStat) || !keyInfo.ModTime().Equal(r.keyStat)
	r.mu.RUnlock()
	if !changed {
		return
	}
	if err := r.reload(); err != nil {
		r.logger.Warn("cert reload failed; keeping previous cert", "err", err)
		return
	}
	r.logger.Info("TLS certificate reloaded",
		"cert", r.certPath, "key", r.keyPath)
}
