package mq

import (
	"context"
	"testing"
	"time"
)

// BenchmarkPool_GetCached measures the hot-path cost of resolving an
// already-pooled connector. The pool serialises per-entry access via a mutex,
// so this is also a contention test under -parallel benchmarks.
func BenchmarkPool_GetCached(b *testing.B) {
	pool := NewPool(PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()
	reg := NewMemoryRegistry(1)
	conn := NewMemoryConnector(reg, "q")
	_ = conn.Connect(context.Background())
	pool.InjectForTest("k", conn)

	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, release, err := pool.Get(ctx, "k", Config{Type: TypeRabbitMQ, URL: "amqp://x", QueueName: "q"})
		if err != nil {
			b.Fatal(err)
		}
		release()
	}
}

func BenchmarkPool_SendThroughCached(b *testing.B) {
	pool := NewPool(PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()
	reg := NewMemoryRegistry(b.N + 64)
	conn := NewMemoryConnector(reg, "q")
	_ = conn.Connect(context.Background())
	pool.InjectForTest("k", conn)

	ctx := context.Background()
	body := []byte(`{"hello":"world"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c, release, _ := pool.Get(ctx, "k", Config{})
		_ = c.SendMessage(ctx, body)
		release()
	}
}
