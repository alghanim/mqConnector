package audit

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"mqConnector/internal/logging"
	"mqConnector/internal/storage"
)

// TestSyslogForwarder_DeliversRFC5424 boots a TCP listener that
// pretends to be a syslog server, points the forwarder at it, and
// verifies one audit row's message arrives in the expected RFC 5424
// shape with JSON-in-MSG.
func TestSyslogForwarder_DeliversRFC5424(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- line
	}()

	sf, err := NewSyslogForwarder("tcp://"+ln.Addr().String(), "test-host", "mqc", logging.New("error", "json"))
	if err != nil {
		t.Fatalf("NewSyslogForwarder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sf.Start(ctx)
	defer sf.Stop()

	sf.OnInsert(storage.AuditEntry{
		ID:       "abc-123",
		TenantID: storage.DefaultTenantID,
		At:       time.Date(2026, 5, 17, 12, 34, 56, 0, time.UTC),
		Actor:    "alice",
		Action:   "POST",
		Resource: "/api/v1/connections",
		Status:   201,
	})

	select {
	case line := <-got:
		// RFC 5424 priority/version prefix.
		if !strings.HasPrefix(line, "<133>1 ") {
			t.Errorf("missing RFC 5424 prefix, got: %q", line)
		}
		// Hostname, app, message-id tokens.
		for _, want := range []string{"test-host", "mqc", "audit", `"id":"abc-123"`, `"resource":"/api/v1/connections"`} {
			if !strings.Contains(line, want) {
				t.Errorf("missing %q in message: %s", want, line)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no syslog message received")
	}
}

// TestSyslogForwarder_DropsOnFullChannel — the OnInsert hook is
// non-blocking. If the network is dead and the buffer fills, drops
// are logged but the audit insert path stays unblocked.
func TestSyslogForwarder_DropsOnFullChannel(t *testing.T) {
	// Use a port nothing is listening on so writes never drain.
	sf, err := NewSyslogForwarder("tcp://127.0.0.1:1", "h", "mqc", logging.New("error", "json"))
	if err != nil {
		t.Fatal(err)
	}
	// Don't Start the writer — channel just fills.
	for i := 0; i < 2000; i++ {
		sf.OnInsert(storage.AuditEntry{ID: "x"})
	}
	// The point is that OnInsert returned all 2000 times without
	// blocking — if it didn't this test would deadlock above. No
	// explicit assertion needed.
}

// TestSyslogForwarder_RejectsBadScheme — only tcp:// and udp:// are
// supported; http:// or a typo must fail at construction so a
// misconfigured deploy fails loudly at boot rather than silently
// dropping audit rows.
func TestSyslogForwarder_RejectsBadScheme(t *testing.T) {
	for _, raw := range []string{"http://foo:514", "syslog.example:514", "tcp://"} {
		_, err := NewSyslogForwarder(raw, "h", "a", nil)
		if err == nil {
			t.Errorf("%q should have errored", raw)
		}
	}
}
