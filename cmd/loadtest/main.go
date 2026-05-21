// Command loadtest drives the storage repository surface against either
// SQLite or Postgres and reports latency percentiles. Used to compare
// the two backends fairly: same workload, same Go binary, just a
// different DSN.
//
// Invocation:
//
//	# Local SQLite (default path: ./loadtest.db, removed after run)
//	go run ./cmd/loadtest --backend=sqlite --duration=30s --concurrency=8
//
//	# Local Postgres (operator runs the container themselves)
//	docker run -d --rm -p 5432:5432 -e POSTGRES_PASSWORD=mqc postgres:16
//	go run ./cmd/loadtest \
//	  --backend=postgres \
//	  --dsn='postgres://postgres:mqc@localhost:5432/postgres?sslmode=disable' \
//	  --duration=30s --concurrency=8
//
// Output: JSON to stdout (machine-readable, the runner script diffs
// two of these), human-readable table to stderr.
//
// Not run from `go test`. The intent is a build-and-run binary the
// operator invokes once before declaring a Postgres rollout
// production-ready; pinning it into the regular test suite would
// pay the setup cost every time `go test ./...` runs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mqConnector/internal/storage"
	"mqConnector/internal/storage/loadtest"
)

func main() {
	var (
		backend     = flag.String("backend", "sqlite", "sqlite|postgres")
		dsn         = flag.String("dsn", "", "DSN; defaults vary by backend")
		concurrency = flag.Int("concurrency", 8, "concurrent writers")
		duration    = flag.Duration("duration", 30*time.Second, "test duration")
		label       = flag.String("label", "", "label for the JSON output; defaults to backend name")
		jsonOnly    = flag.Bool("json-only", false, "suppress the human-readable table on stderr")
	)
	flag.Parse()

	// DSN defaults so a bare `go run ./cmd/loadtest` produces useful
	// output without forcing the operator to pick a path.
	if *dsn == "" {
		switch *backend {
		case "sqlite":
			tmp, err := os.MkdirTemp("", "mqc-loadtest-*")
			if err != nil {
				fail("mkdir temp: %v", err)
			}
			// File:-prefixed DSN with the modernc.org/sqlite tuning
			// the production binary uses. Same pragmas → same fsync
			// behaviour → fair comparison.
			path := filepath.Join(tmp, "loadtest.db")
			*dsn = "file:" + path +
				"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
			defer os.RemoveAll(tmp)
		case "postgres":
			fail("--backend=postgres requires --dsn=postgres://...")
		default:
			fail("--backend must be sqlite or postgres")
		}
	}
	if *label == "" {
		*label = *backend
	}

	// Open + migrate. The Store constructor runs migrations
	// transparently — first call sets up the schema.
	store, err := storage.Open(*dsn, 16, 8)
	if err != nil {
		fail("storage.Open: %v", err)
	}
	defer store.Close()

	// SIGINT-aware context so an operator who hits Ctrl-C gets a
	// partial report instead of nothing.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "interrupt — stopping workers")
		cancel()
	}()

	var humanOut *os.File
	if !*jsonOnly {
		humanOut = os.Stderr
	}

	res, err := loadtest.Run(ctx, store, loadtest.Config{
		Concurrency: *concurrency,
		Duration:    *duration,
		Label:       *label,
	}, humanOut)
	if err != nil {
		fail("loadtest.Run: %v", err)
	}

	// JSON to stdout for the runner-script diff.
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		fail("encode json: %v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "loadtest: "+format+"\n", args...)
	os.Exit(1)
}
