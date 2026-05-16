package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mqConnector/internal/storage"
)

func openStore(t *testing.T) *storage.Store {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "arc.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	s, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedAudit(t *testing.T, s *storage.Store, n int, atFn func(i int) time.Time) {
	t.Helper()
	for i := 0; i < n; i++ {
		e := &storage.AuditEntry{
			At:       atFn(i),
			Actor:    "alice",
			ActorSub: "u1",
			Action:   "POST",
			Resource: "/api/v1/connections",
			Status:   201,
		}
		if err := s.Audit.Insert(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestArchive_WritesJSONLAndPrunes(t *testing.T) {
	s := openStore(t)
	dir := t.TempDir()
	old := time.Now().Add(-2 * time.Hour).UTC()
	recent := time.Now().UTC()

	// Three old + two recent rows.
	seedAudit(t, s, 3, func(i int) time.Time { return old.Add(time.Duration(i) * time.Second) })
	seedAudit(t, s, 2, func(i int) time.Time { return recent.Add(time.Duration(i) * time.Second) })

	arc := New(s.Audit, dir, time.Hour, time.Hour, nil)
	moved, err := arc.Archive(context.Background(), time.Now().Add(-time.Hour).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if moved != 3 {
		t.Errorf("expected 3 rows archived, got %d", moved)
	}

	// JSONL contents: should be three lines, one per row.
	files, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	if len(files) == 0 {
		t.Fatal("no archive files produced")
	}
	lineCount := 0
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			lineCount++
			var got storage.AuditEntry
			if err := json.Unmarshal(sc.Bytes(), &got); err != nil {
				t.Errorf("malformed JSONL line: %s", sc.Text())
			}
			if got.Actor != "alice" {
				t.Errorf("unexpected actor: %s", got.Actor)
			}
		}
		_ = f.Close()
	}
	if lineCount != 3 {
		t.Errorf("expected 3 lines across archives, got %d", lineCount)
	}

	// Pruned from the table.
	_, total, _ := s.Audit.List(context.Background(), storage.AuditFilter{}, 1, 50)
	if total != 2 {
		t.Errorf("expected 2 rows remaining after prune, got %d", total)
	}
}

func TestArchive_NoOpWhenEmpty(t *testing.T) {
	s := openStore(t)
	dir := t.TempDir()
	arc := New(s.Audit, dir, time.Hour, time.Hour, nil)
	n, err := arc.Archive(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("empty table should archive 0 rows, got %d", n)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 0 {
		t.Errorf("no archive files should exist, got %v", files)
	}
}

func TestArchive_PerDayFile(t *testing.T) {
	s := openStore(t)
	dir := t.TempDir()
	// Two rows on different UTC days.
	day1 := time.Date(2026, 5, 14, 23, 30, 0, 0, time.UTC)
	day2 := time.Date(2026, 5, 15, 0, 30, 0, 0, time.UTC)
	for _, at := range []time.Time{day1, day2} {
		_ = s.Audit.Insert(context.Background(), &storage.AuditEntry{
			At: at, Actor: "a", Action: "POST", Resource: "/x", Status: 200,
		})
	}

	arc := New(s.Audit, dir, time.Hour, time.Hour, nil)
	cutoff := time.Now().UTC()
	if _, err := arc.Archive(context.Background(), cutoff); err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{"audit-2026-05-14.jsonl", "audit-2026-05-15.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, expected)); err != nil {
			t.Errorf("missing per-day file %s: %v", expected, err)
		}
	}
}

func TestArchive_RunRespectsCancel(t *testing.T) {
	s := openStore(t)
	dir := t.TempDir()
	arc := New(s.Audit, dir, time.Hour, 10*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		arc.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after cancel")
	}
}

func TestArchive_DisabledWhenDirEmpty(t *testing.T) {
	s := openStore(t)
	arc := New(s.Audit, "", time.Hour, time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		arc.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
		// no-op archiver should return immediately
	case <-time.After(200 * time.Millisecond):
		t.Error("disabled archiver did not return promptly")
	}
}

func TestArchive_JSONLLinesAreNewlineDelimited(t *testing.T) {
	s := openStore(t)
	dir := t.TempDir()
	old := time.Now().Add(-2 * time.Hour).UTC()
	seedAudit(t, s, 5, func(i int) time.Time { return old.Add(time.Duration(i) * time.Second) })

	arc := New(s.Audit, dir, time.Hour, time.Hour, nil)
	if _, err := arc.Archive(context.Background(), time.Now().Add(-time.Hour).UTC()); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	b, _ := os.ReadFile(files[0])
	if !strings.HasSuffix(string(b), "\n") {
		t.Error("archive should be newline-terminated")
	}
	if strings.Count(string(b), "\n") != 5 {
		t.Errorf("expected 5 newline-delimited lines, got %d", strings.Count(string(b), "\n"))
	}
}
