package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"time"

	"mqConnector/internal/storage"
)

// SyslogForwarder pushes audit rows to a SIEM in real time via an
// RFC 5424 syslog message over TCP or UDP. Implements
// storage.AuditSink — registered on AuditRepo.AddSink in main.go.
//
// Buffering: a 1024-deep channel decouples the audit-insert hot
// path from the network. A single writer goroutine drains the
// channel. If the channel fills (SIEM is slow / down), drops are
// logged at WARN. The audit DB row is always retained — syslog is a
// secondary copy, not the source of truth.
//
// Reconnect: the writer keeps a single dialer-managed connection.
// On send failure it logs WARN, sleeps Backoff, and reopens. No
// circuit-breaker yet; the SIEM upstream's own backoff covers most
// real cases.
type SyslogForwarder struct {
	addr     string // network:host:port (e.g. tcp:syslog.svc:514)
	hostname string
	app      string
	logger   *slog.Logger
	backoff  time.Duration

	mu   sync.Mutex
	conn net.Conn

	ch       chan storage.AuditEntry
	stopOnce sync.Once
	stopped  chan struct{}
}

// NewSyslogForwarder parses a destination like "tcp://syslog:514" or
// "udp://1.2.3.4:514" and constructs the forwarder. The caller MUST
// call Start to spawn the writer goroutine and Stop on shutdown.
func NewSyslogForwarder(raw, hostname, app string, logger *slog.Logger) (*SyslogForwarder, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse syslog url: %w", err)
	}
	if u.Scheme != "tcp" && u.Scheme != "udp" {
		return nil, fmt.Errorf("syslog url scheme must be tcp or udp, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("syslog url missing host:port")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if hostname == "" {
		hostname = "mqconnector"
	}
	if app == "" {
		app = "mqconnector"
	}
	return &SyslogForwarder{
		addr:     u.Scheme + "://" + u.Host,
		hostname: hostname,
		app:      app,
		logger:   logger.With("component", "audit.syslog"),
		backoff:  2 * time.Second,
		ch:       make(chan storage.AuditEntry, 1024),
		stopped:  make(chan struct{}),
	}, nil
}

// OnInsert implements storage.AuditSink. Non-blocking — drops on a
// full channel with a WARN log. The audit row is already persisted
// in the DB, so a drop is not data loss, just a SIEM gap.
func (f *SyslogForwarder) OnInsert(e storage.AuditEntry) {
	select {
	case f.ch <- e:
	default:
		f.logger.Warn("syslog channel full, dropping audit row",
			"audit_id", e.ID, "resource", e.Resource)
	}
}

// Start spawns the writer goroutine. Non-blocking. Returns
// immediately; cancel via ctx or Stop.
func (f *SyslogForwarder) Start(ctx context.Context) {
	go f.run(ctx)
}

// Stop signals the writer to exit. Subsequent OnInsert calls just
// fill the channel and get dropped on shutdown, which is the right
// posture — we're tearing down anyway.
func (f *SyslogForwarder) Stop() {
	f.stopOnce.Do(func() { close(f.stopped) })
}

func (f *SyslogForwarder) run(ctx context.Context) {
	defer func() {
		f.mu.Lock()
		if f.conn != nil {
			_ = f.conn.Close()
		}
		f.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.stopped:
			return
		case e := <-f.ch:
			if err := f.write(ctx, e); err != nil {
				f.logger.Warn("syslog write failed, reconnecting",
					"err", err, "audit_id", e.ID)
				f.dropConn()
				select {
				case <-ctx.Done():
					return
				case <-f.stopped:
					return
				case <-time.After(f.backoff):
				}
			}
		}
	}
}

func (f *SyslogForwarder) write(ctx context.Context, e storage.AuditEntry) error {
	c, err := f.ensureConn(ctx)
	if err != nil {
		return err
	}
	msg := f.format(e)
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = c.Write([]byte(msg))
	return err
}

func (f *SyslogForwarder) ensureConn(ctx context.Context) (net.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn != nil {
		return f.conn, nil
	}
	// addr is "tcp://host:port" or "udp://host:port"; split.
	scheme := f.addr[:3]
	host := f.addr[6:] // skip "tcp://" or "udp://"
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, scheme, host)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", f.addr, err)
	}
	f.conn = c
	return c, nil
}

func (f *SyslogForwarder) dropConn() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn != nil {
		_ = f.conn.Close()
		f.conn = nil
	}
}

// format renders an RFC 5424 syslog message. Severity = NOTICE (5),
// facility = LOCAL0 (16), so priority = 16*8 + 5 = 133. Structured
// data (audit row JSON) goes in the message body so SIEMs that
// understand JSON-in-syslog can parse straight away. Newline-
// terminated for line-oriented receivers.
func (f *SyslogForwarder) format(e storage.AuditEntry) string {
	body, _ := json.Marshal(e)
	// <PRI>VERSION TIMESTAMP HOSTNAME APP-NAME PROCID MSGID STRUCTURED-DATA MSG
	// PRI 133 = facility LOCAL0 + severity NOTICE.
	return fmt.Sprintf("<133>1 %s %s %s - %s - %s\n",
		e.At.UTC().Format(time.RFC3339),
		f.hostname,
		f.app,
		"audit",
		string(body),
	)
}
