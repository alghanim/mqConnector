// Package loadtest is the storage backend benchmark — not a test in the
// `go test` sense, but a runnable workload that exercises the
// repository surface against either SQLite or Postgres and reports
// p50 / p95 / p99 latency.
//
// Why this lives here, not in scripts/:
//   - It uses the in-tree storage types (Connection, Pipeline, etc.)
//     so the workload tracks the same SQL the production binary issues.
//     A shell script driving the HTTP API would also test the chi
//     router + auth middleware, which is fine for end-to-end perf
//     but bad for "is the storage backend itself the bottleneck?"
//   - It builds as a separate binary via cmd/loadtest, so a regular
//     `go build ./...` doesn't pull it into the main binary.
//
// Workload profile:
//   - N concurrent writers.
//   - Each writer alternates: Create a connection → Get → Update → List
//     → Delete. The mix mirrors the dominant repo paths in production
//     (admin UI editing connections + the pipeline reload loop calling
//     List). Tune via the --mix flag if a specific path needs profiling.
//   - Configurable duration. Default 30s — enough to cross several
//     SQLite WAL checkpoint windows; longer if you're chasing
//     long-tail variance.
//
// Output is a JSON summary (latency percentiles, error counts, ops/s)
// printed to stdout so the runner shell script can diff the two
// backends mechanically. A human-readable table is printed to stderr.
package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"mqConnector/internal/storage"
)

// Workload knobs. Defaults match the audit's "1M msgs / 5min, 4
// tenants, 32 connections" reference but downscaled so a local run
// finishes inside a minute on commodity hardware. The runner script
// is the right place to set production-scale values.
type Config struct {
	Concurrency int
	Duration    time.Duration
	OpsPerCycle int    // How many of each op type per worker cycle. Default 1.
	Label       string // "sqlite" or "postgres" — for the JSON output.
}

// Result is the workload's structured output. Keep this JSON-stable;
// the runner script diffs two of them.
type Result struct {
	Label       string         `json:"label"`
	Concurrency int            `json:"concurrency"`
	Duration    string         `json:"duration"`
	TotalOps    int64          `json:"total_ops"`
	Errors      int64          `json:"errors"`
	OpsPerSec   float64        `json:"ops_per_sec"`
	Latency     LatencyBucket  `json:"latency_ms"`
	PerOp       map[string]any `json:"per_op_latency_ms,omitempty"`
}

// LatencyBucket aggregates p50/p95/p99 + max. All values in
// milliseconds. mean is included so the runner can sanity-check the
// percentile distribution against the average.
type LatencyBucket struct {
	P50  float64 `json:"p50"`
	P95  float64 `json:"p95"`
	P99  float64 `json:"p99"`
	Max  float64 `json:"max"`
	Mean float64 `json:"mean"`
}

// Run executes the workload against the open Store. The Store must
// already be migrated. Returns the JSON-marshalable Result; also
// writes a human table to humanOut (nil silences it).
func Run(ctx context.Context, store *storage.Store, cfg Config, humanOut io.Writer) (*Result, error) {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 8
	}
	if cfg.Duration <= 0 {
		cfg.Duration = 30 * time.Second
	}
	if cfg.OpsPerCycle <= 0 {
		cfg.OpsPerCycle = 1
	}

	// Deadline is the soft stop — workers see ctx.Done and exit.
	runCtx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()

	// Per-op latency buckets so we can report per-CRUD-verb distinctly.
	// Buckets are slice-of-float (one float per op observation) which
	// is the simplest correct shape — sorting at the end gives us the
	// percentiles. Memory: 8 bytes × N ops; for a 30s run with 1k ops/s
	// that's 240 KB.
	type stats struct {
		mu   sync.Mutex
		vals []float64
	}
	buckets := map[string]*stats{
		"create": {},
		"get":    {},
		"update": {},
		"list":   {},
		"delete": {},
	}
	record := func(op string, durMs float64) {
		b := buckets[op]
		b.mu.Lock()
		b.vals = append(b.vals, durMs)
		b.mu.Unlock()
	}

	var (
		totalOps atomic.Int64
		errors   atomic.Int64
		allLat   = &stats{}
	)
	recordAll := func(durMs float64) {
		allLat.mu.Lock()
		allLat.vals = append(allLat.vals, durMs)
		allLat.mu.Unlock()
	}

	// Worker.
	worker := func(workerID int) {
		// Each worker keeps a rolling set of N "active" connection IDs
		// so Get/Update/Delete can target a real row. Pre-seeding
		// avoids the warm-up artifact where every initial op is a
		// Create.
		rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))
		var active []string

		// Seed two rows so the first Get/Update/Delete has targets.
		for i := 0; i < 2; i++ {
			id, err := createOne(runCtx, store, workerID, rng)
			if err != nil {
				continue
			}
			active = append(active, id)
		}

		for {
			if runCtx.Err() != nil {
				return
			}
			// Mix: 1 create, 2 gets, 1 update, 1 list, 1 delete per cycle.
			// Mirrors the admin UI's read-heavy + occasional-write profile.
			steps := []func() (string, error){
				func() (string, error) {
					id, err := createOne(runCtx, store, workerID, rng)
					if err == nil {
						active = append(active, id)
					}
					return "create", err
				},
				func() (string, error) {
					if len(active) == 0 {
						return "get", nil
					}
					_, err := store.Connections.Get(runCtx, storage.DefaultTenantID, active[rng.Intn(len(active))])
					return "get", err
				},
				func() (string, error) {
					if len(active) == 0 {
						return "update", nil
					}
					id := active[rng.Intn(len(active))]
					c := &storage.Connection{
						ID: id, Name: "upd-" + id[:8], Type: "rabbitmq",
						URL: "amqp://x", QueueName: "q",
					}
					return "update", store.Connections.Update(runCtx, storage.DefaultTenantID, c)
				},
				func() (string, error) {
					_, err := store.Connections.List(runCtx, storage.DefaultTenantID)
					return "list", err
				},
				func() (string, error) {
					if len(active) == 0 {
						return "delete", nil
					}
					idx := rng.Intn(len(active))
					id := active[idx]
					err := store.Connections.Delete(runCtx, storage.DefaultTenantID, id)
					if err == nil {
						active = append(active[:idx], active[idx+1:]...)
					}
					return "delete", err
				},
			}
			for _, step := range steps {
				if runCtx.Err() != nil {
					return
				}
				start := time.Now()
				op, err := step()
				ms := float64(time.Since(start).Microseconds()) / 1000
				record(op, ms)
				recordAll(ms)
				totalOps.Add(1)
				if err != nil {
					errors.Add(1)
				}
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(cfg.Concurrency)
	startWall := time.Now()
	for i := 0; i < cfg.Concurrency; i++ {
		i := i
		go func() {
			defer wg.Done()
			worker(i)
		}()
	}
	wg.Wait()
	elapsed := time.Since(startWall)

	// Aggregate.
	res := &Result{
		Label:       cfg.Label,
		Concurrency: cfg.Concurrency,
		Duration:    elapsed.Round(time.Millisecond).String(),
		TotalOps:    totalOps.Load(),
		Errors:      errors.Load(),
		OpsPerSec:   float64(totalOps.Load()) / elapsed.Seconds(),
		Latency:     summarize(allLat.vals),
		PerOp:       map[string]any{},
	}
	// Stable iteration so the JSON diff is deterministic.
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		res.PerOp[k] = summarize(buckets[k].vals)
	}

	if humanOut != nil {
		printHuman(humanOut, res)
	}
	return res, nil
}

// createOne is the connection-create primitive used both for warm-up
// and the create step of the mix. Returns the generated row ID.
func createOne(ctx context.Context, store *storage.Store, workerID int, rng *rand.Rand) (string, error) {
	c := &storage.Connection{
		Name:      fmt.Sprintf("loadtest-w%d-%d", workerID, rng.Int63()),
		Type:      "rabbitmq",
		URL:       "amqp://x",
		QueueName: "q",
	}
	if err := store.Connections.Create(ctx, storage.DefaultTenantID, c); err != nil {
		return "", err
	}
	return c.ID, nil
}

// summarize computes the percentile bucket for a sample. Empty input
// is treated as all-zero (so the JSON has a stable shape even if a
// rare op type produced no observations).
func summarize(vals []float64) LatencyBucket {
	if len(vals) == 0 {
		return LatencyBucket{}
	}
	sort.Float64s(vals)
	pct := func(p float64) float64 {
		idx := int(float64(len(vals)-1) * p)
		return vals[idx]
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return LatencyBucket{
		P50:  pct(0.50),
		P95:  pct(0.95),
		P99:  pct(0.99),
		Max:  vals[len(vals)-1],
		Mean: sum / float64(len(vals)),
	}
}

// printHuman writes a fixed-width table — easy to eyeball when running
// locally; the JSON is for the runner-script diff.
func printHuman(w io.Writer, r *Result) {
	fmt.Fprintf(w, "\n=== %s ===\n", r.Label)
	fmt.Fprintf(w, "concurrency=%d duration=%s ops=%d errors=%d ops/s=%.0f\n",
		r.Concurrency, r.Duration, r.TotalOps, r.Errors, r.OpsPerSec)
	fmt.Fprintf(w, "overall p50=%.2fms p95=%.2fms p99=%.2fms max=%.2fms mean=%.2fms\n",
		r.Latency.P50, r.Latency.P95, r.Latency.P99, r.Latency.Max, r.Latency.Mean)
	// Per-op detail.
	keys := make([]string, 0, len(r.PerOp))
	for k := range r.PerOp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// PerOp values are LatencyBucket structs round-tripped through
		// map[string]any — re-marshal to access the fields uniformly.
		b, _ := json.Marshal(r.PerOp[k])
		var lb LatencyBucket
		_ = json.Unmarshal(b, &lb)
		fmt.Fprintf(w, "  %-7s p50=%6.2fms p95=%6.2fms p99=%6.2fms max=%6.2fms\n",
			k, lb.P50, lb.P95, lb.P99, lb.Max)
	}
}
