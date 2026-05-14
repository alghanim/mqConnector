package mq

import (
	"context"
	"testing"
	"time"
)

func TestMemoryConnector_SendReceive(t *testing.T) {
	reg := NewMemoryRegistry(4)
	src := NewMemoryConnector(reg, "q1")
	dst := NewMemoryConnector(reg, "q1")

	ctx := context.Background()
	if err := src.Connect(ctx); err != nil {
		t.Fatalf("src Connect: %v", err)
	}
	if err := dst.Connect(ctx); err != nil {
		t.Fatalf("dst Connect: %v", err)
	}

	for _, m := range [][]byte{[]byte("hello"), []byte("world")} {
		if err := src.SendMessage(ctx, m); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	got1, err := dst.ReceiveMessage(ctx)
	if err != nil || string(got1) != "hello" {
		t.Fatalf("Receive 1: %v / %q", err, got1)
	}
	got2, err := dst.ReceiveMessage(ctx)
	if err != nil || string(got2) != "world" {
		t.Fatalf("Receive 2: %v / %q", err, got2)
	}
}

func TestMemoryConnector_PingBeforeConnect(t *testing.T) {
	c := NewMemoryConnector(NewMemoryRegistry(1), "q")
	if err := c.Ping(context.Background()); err == nil {
		t.Error("expected ErrNotConnected before Connect")
	}
}

func TestMemoryConnector_SendBeforeConnect(t *testing.T) {
	c := NewMemoryConnector(NewMemoryRegistry(1), "q")
	if err := c.SendMessage(context.Background(), []byte("x")); err == nil {
		t.Error("expected ErrNotConnected before Connect")
	}
}

func TestMemoryConnector_DisconnectStopsSends(t *testing.T) {
	c := NewMemoryConnector(NewMemoryRegistry(1), "q")
	_ = c.Connect(context.Background())
	_ = c.Disconnect()
	if err := c.SendMessage(context.Background(), []byte("x")); err == nil {
		t.Error("send after Disconnect should fail")
	}
}

func TestMemoryRegistry_Drain(t *testing.T) {
	reg := NewMemoryRegistry(4)
	c := NewMemoryConnector(reg, "events")
	_ = c.Connect(context.Background())
	for i := 0; i < 3; i++ {
		_ = c.SendMessage(context.Background(), []byte("m"))
	}
	msgs := reg.Drain("events")
	if len(msgs) != 3 {
		t.Errorf("expected 3 drained, got %d", len(msgs))
	}
	// Drain again should be empty.
	if len(reg.Drain("events")) != 0 {
		t.Error("subsequent drain should be empty")
	}
}

func TestMemoryConnector_ReceiveContextCancel(t *testing.T) {
	reg := NewMemoryRegistry(0)
	c := NewMemoryConnector(reg, "q")
	_ = c.Connect(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.ReceiveMessage(ctx); err == nil {
		t.Error("expected ctx-cancelled error on empty queue")
	}
}
