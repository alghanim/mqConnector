package mq

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseType(t *testing.T) {
	tests := map[string]Type{
		"ibm":      TypeIBM,
		"IBM":      TypeIBM,
		"ibmmq":    TypeIBM,
		"rabbit":   TypeRabbitMQ,
		"rabbitmq": TypeRabbitMQ,
		"amqp":     TypeRabbitMQ,
		"kafka":    TypeKafka,
		"KAFKA":    TypeKafka,
	}
	for in, expected := range tests {
		got, err := ParseType(in)
		if err != nil {
			t.Errorf("ParseType(%q) error: %v", in, err)
			continue
		}
		if got != expected {
			t.Errorf("ParseType(%q) = %s, want %s", in, got, expected)
		}
	}
	if _, err := ParseType("snafu"); err == nil {
		t.Error("expected error on unknown type")
	}
}

func TestNew_UnknownType(t *testing.T) {
	if _, err := New(Config{Type: "garbage"}); err == nil {
		t.Error("expected error on unknown type")
	}
}

// fakeConn is a Connector used to test the pool without a real broker.
type fakeConn struct {
	connectCalls    int
	disconnectCalls int
	pingErr         error
}

func (f *fakeConn) Connect(_ context.Context) error               { f.connectCalls++; return nil }
func (f *fakeConn) Disconnect() error                             { f.disconnectCalls++; return nil }
func (f *fakeConn) SendMessage(_ context.Context, _ []byte) error { return nil }
func (f *fakeConn) ReceiveMessage(_ context.Context) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeConn) Ping(_ context.Context) error { return f.pingErr }

func TestPool_GetReusesEntry(t *testing.T) {
	p := NewPool(PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer p.Close()

	// Manually insert a fake entry to bypass the real factory.
	p.mu.Lock()
	p.entries["id1"] = &poolEntry{conn: &fakeConn{}}
	p.mu.Unlock()

	conn, release, err := p.Get(context.Background(), "id1", Config{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	release()
	conn2, release2, err := p.Get(context.Background(), "id1", Config{})
	if err != nil {
		t.Fatalf("Get 2: %v", err)
	}
	release2()
	if conn != conn2 {
		t.Error("expected the same connector instance on second Get")
	}
}

func TestPool_ReleaseDisconnects(t *testing.T) {
	p := NewPool(PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer p.Close()

	f := &fakeConn{}
	p.mu.Lock()
	p.entries["x"] = &poolEntry{conn: f}
	p.mu.Unlock()

	p.Release("x")
	if f.disconnectCalls != 1 {
		t.Errorf("expected 1 disconnect, got %d", f.disconnectCalls)
	}
	if p.Size() != 0 {
		t.Errorf("expected size 0 after release, got %d", p.Size())
	}
}

func TestPool_IdleEviction(t *testing.T) {
	p := NewPool(PoolOptions{IdleTimeout: time.Millisecond, HealthInterval: time.Hour})
	defer p.Close()

	f := &fakeConn{}
	p.mu.Lock()
	p.entries["x"] = &poolEntry{conn: f, lastUsed: time.Now().Add(-time.Hour)}
	p.mu.Unlock()

	p.runSweep()

	if p.Size() != 0 {
		t.Errorf("expected eviction, size %d", p.Size())
	}
}

func TestPool_HealthCheckEvictsBadConn(t *testing.T) {
	p := NewPool(PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer p.Close()

	f := &fakeConn{pingErr: errors.New("dead")}
	p.mu.Lock()
	p.entries["x"] = &poolEntry{conn: f, lastUsed: time.Now()}
	p.mu.Unlock()

	p.runSweep()

	if p.Size() != 0 {
		t.Errorf("expected eviction of unhealthy connector, size %d", p.Size())
	}
}
