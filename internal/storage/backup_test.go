package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStore_BackupAndRestore proves the disaster-recovery loop end to
// end: snapshot a live store, reopen the snapshot as a fresh store,
// and verify the data made it across. This is the actual safety
// property — that a restore from snapshot produces a usable database.
func TestStore_BackupAndRestore(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Seed something we can assert on after restore.
	c := &Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x"}
	if err := s.Connections.Create(ctx, DefaultTenantID, c); err != nil {
		t.Fatalf("seed: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s.Backup(ctx, dst); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("snapshot is empty")
	}

	// Open the snapshot as a fresh store. modernc/sqlite happily reads
	// a file produced by VACUUM INTO with the same DSN scheme.
	dsn := "file:" + dst + "?_pragma=busy_timeout(5000)"
	restored, err := Open(dsn, 2, 1)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer restored.Close()

	// The seeded row should be there.
	got, err := restored.Connections.Get(ctx, DefaultTenantID, c.ID)
	if err != nil {
		t.Fatalf("Get from restored: %v", err)
	}
	if got.Name != "src" {
		t.Errorf("restored connection mismatch: got %q want %q", got.Name, "src")
	}
}

// TestStore_BackupRefusesOverwrite — we deliberately error rather than
// clobber an existing snapshot. An operator pointing two scheduled
// backups at the same name shouldn't silently lose the older one.
func TestStore_BackupRefusesOverwrite(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	dst := filepath.Join(t.TempDir(), "already.db")
	if err := os.WriteFile(dst, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := s.Backup(ctx, dst)
	if err == nil {
		t.Fatal("expected error when destination exists, got nil")
	}
}

// TestStore_IntegrityCheck_FreshDBIsOK — a freshly migrated database
// must pass integrity_check, otherwise CI would catch a corrupting
// migration before it shipped.
func TestStore_IntegrityCheck_FreshDBIsOK(t *testing.T) {
	s := openTestStore(t)
	rows, err := s.IntegrityCheck(context.Background())
	if err != nil {
		t.Fatalf("IntegrityCheck: %v", err)
	}
	if len(rows) != 1 || rows[0] != "ok" {
		t.Errorf("expected exactly [ok], got %v", rows)
	}
}

// TestPruneBackups_KeepsNewest exercises the rotation policy directly
// (the scheduled-backup worker test path; equivalent code lives in
// cmd/mqconnector/backup.go but the storage package owns the canonical
// implementation).
func TestPruneBackups_KeepsNewest(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"mqconnector-20260101T000000Z.db",
		"mqconnector-20260201T000000Z.db",
		"mqconnector-20260301T000000Z.db",
		"mqconnector-20260401T000000Z.db",
		"unrelated.db", // should not be touched
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneBackups(dir, 2); err != nil {
		t.Fatalf("pruneBackups: %v", err)
	}
	got, _ := os.ReadDir(dir)
	have := map[string]bool{}
	for _, e := range got {
		have[e.Name()] = true
	}
	for _, want := range []string{
		"mqconnector-20260301T000000Z.db",
		"mqconnector-20260401T000000Z.db",
		"unrelated.db",
	} {
		if !have[want] {
			t.Errorf("expected %s to remain", want)
		}
	}
	if have["mqconnector-20260101T000000Z.db"] || have["mqconnector-20260201T000000Z.db"] {
		t.Errorf("expected older snapshots to be pruned, dir: %v", have)
	}
}

// TestBackupWorker_RunsOneTickAndExits — drive the worker with a tiny
// interval, let it write one snapshot, then cancel. Verifies the
// happy path actually produces a file in the configured directory.
func TestBackupWorker_RunsOneTickAndExits(t *testing.T) {
	s := openTestStore(t)
	dir := t.TempDir()
	w := &BackupWorker{
		Store:    s,
		Dir:      dir,
		Interval: time.Hour, // doesn't matter; we cancel right after the startup snapshot
		Keep:     5,
		IsLeader: func() bool { return true },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	// Poll briefly for the startup snapshot, then cancel.
	deadline := time.Now().Add(3 * time.Second)
	var found bool
	for time.Now().Before(deadline) && !found {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			n := e.Name()
			if len(n) > len("mqconnector-") && n[:len("mqconnector-")] == "mqconnector-" {
				found = true
				break
			}
		}
		if !found {
			time.Sleep(20 * time.Millisecond)
		}
	}
	cancel()
	<-done

	if !found {
		t.Fatal("expected at least one snapshot from the startup tick")
	}
}

// TestBackupWorker_SkipsWhenNotLeader — a non-leader replica must not
// write to the shared destination directory.
func TestBackupWorker_SkipsWhenNotLeader(t *testing.T) {
	s := openTestStore(t)
	dir := t.TempDir()
	w := &BackupWorker{
		Store:    s,
		Dir:      dir,
		Interval: time.Hour,
		Keep:     5,
		IsLeader: func() bool { return false },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	w.Run(ctx)

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if n := e.Name(); len(n) > 12 && n[:12] == "mqconnector-" {
			t.Fatalf("non-leader wrote a snapshot: %s", n)
		}
	}
}
