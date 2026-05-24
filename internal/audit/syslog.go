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
// Format selects the wire encoding for outbound audit rows.
type Format int

const (
	// FormatRFC5424 sends the audit row's JSON wrapped in an RFC 5424
	// syslog envelope. Default; widely supported by rsyslog, syslog-ng,
	// and Splunk Universal Forwarder.
	FormatRFC5424 Format = iota
	// FormatCEF sends the audit row in ArcSight Common Event Format,
	// wrapped in an RFC 5424 envelope. Best for ArcSight / QRadar
	// pipelines that key on the CEF schema.
	FormatCEF
)

type SyslogForwarder struct {
	addr         string // network:host:port (e.g. tcp:syslog.svc:514)
	hostname     string
	app          string
	logger       *slog.Logger
	backoff      time.Duration
	outputFormat Format
	version      string // product version, used in CEF header

	mu   sync.Mutex
	conn net.Conn

	ch       chan storage.AuditEntry
	stopOnce sync.Once
	stopped  chan struct{}
}

// SetFormat selects the wire encoding. Default is FormatRFC5424;
// FormatCEF wraps the row as a CEF:0 message in an RFC 5424
// envelope.
func (f *SyslogForwarder) SetFormat(fm Format) { f.outputFormat = fm }

// SetVersion sets the product version embedded in CEF headers. Falls
// back to "dev" when unset.
func (f *SyslogForwarder) SetVersion(v string) {
	if v != "" {
		f.version = v
	}
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

// format renders an audit row as one syslog frame. PRI 133 = facility
// LOCAL0 (16) + severity NOTICE (5); same envelope for both wire
// formats so receivers that strip syslog headers see one consistent
// shape. Body is JSON (RFC 5424) or CEF, selected by SetFormat.
func (f *SyslogForwarder) format(e storage.AuditEntry) string {
	var body string
	switch f.outputFormat {
	case FormatCEF:
		body = formatCEF(e, f.version)
	default:
		j, _ := json.Marshal(e)
		body = string(j)
	}
	// <PRI>VERSION TIMESTAMP HOSTNAME APP-NAME PROCID MSGID STRUCTURED-DATA MSG
	return fmt.Sprintf("<133>1 %s %s %s - %s - %s\n",
		e.At.UTC().Format(time.RFC3339),
		f.hostname,
		f.app,
		"audit",
		body,
	)
}

// formatCEF renders one audit entry as ArcSight Common Event Format.
// Header layout: CEF:0|Vendor|Product|Version|EventClassID|Name|
// Severity|extension. Pipes inside header fields are escaped; equals
// signs and backslashes inside extension values are escaped per spec.
// Severity is mapped from HTTP status:
//
//   - 5xx → 7 (very high)
//   - 4xx → 5 (medium)
//   - else → 3 (low — successful state-changing actions)
//
// CEF receivers (ArcSight ESM, QRadar DSM, Splunk CIM) parse this
// directly. The extension carries the audit row's structured fields
// using standard CEF keys (act, suser, src, request, outcome, msg).
func formatCEF(e storage.AuditEntry, version string) string {
	if version == "" {
		version = "dev"
	}
	severity := 3
	switch {
	case e.Status >= 500:
		severity = 7
	case e.Status >= 400:
		severity = 5
	}
	outcome := "success"
	if e.Status >= 400 {
		outcome = "failure"
	}
	// Extension key/value pairs. CEF extension values escape '=', '\'
	// and newline; bare spaces in values must be allowed (multi-word
	// resource paths come through). Order is stable for grep-ability.
	ext := []struct{ k, v string }{
		{"act", e.Action},
		{"suser", e.Actor},
		{"suid", e.ActorSub},
		{"src", e.RemoteIP},
		{"request", e.Resource},
		{"requestMethod", e.Action},
		{"externalId", e.ID},
		{"cs1Label", "request_id"},
		{"cs1", e.RequestID},
		{"cs2Label", "tenant_id"},
		{"cs2", e.TenantID},
		{"cs3Label", "status"},
		{"cs3", fmt.Sprintf("%d", e.Status)},
		{"outcome", outcome},
	}
	var extBuf string
	for _, kv := range ext {
		if kv.v == "" {
			continue
		}
		if extBuf != "" {
			extBuf += " "
		}
		extBuf += kv.k + "=" + escapeCEFValue(kv.v)
	}
	name := e.Resource
	if name == "" {
		name = "audit"
	}
	return fmt.Sprintf("CEF:0|%s|%s|%s|%s|%s|%d|%s",
		escapeCEFHeader("mqconnector"),
		escapeCEFHeader("mqconnector"),
		escapeCEFHeader(version),
		escapeCEFHeader(e.Action), // EventClassID
		escapeCEFHeader(name),     // Name
		severity,
		extBuf,
	)
}

// escapeCEFHeader escapes characters that would terminate a header
// segment. Per spec: backslash and pipe must be escaped.
func escapeCEFHeader(s string) string {
	r := ""
	for _, c := range s {
		switch c {
		case '\\', '|':
			r += "\\" + string(c)
		default:
			r += string(c)
		}
	}
	return r
}

// escapeCEFValue escapes characters that would break extension
// parsing. Per spec: backslash, equals, and newline must be escaped.
func escapeCEFValue(s string) string {
	r := ""
	for _, c := range s {
		switch c {
		case '\\', '=':
			r += "\\" + string(c)
		case '\n':
			r += "\\n"
		case '\r':
			r += "\\r"
		default:
			r += string(c)
		}
	}
	return r
}
