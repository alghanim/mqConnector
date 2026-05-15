package leadership

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB opens a fresh SQLite database in the test temp dir. The
// pragmas mirror what storage.Open uses so behaviour matches production.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "leadership.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(4)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestLease_SoloAcquires(t *testing.T) {
	db := openTestDB(t)
	l := New(db, "a", time.Second, nil)
	l.attempt(context.Background())
	if !l.IsLeader() {
		t.Fatal("solo lease should win")
	}
}

func TestLease_OneOfTwoWins(t *testing.T) {
	db := openTestDB(t)
	a := New(db, "a", time.Second, nil)
	b := New(db, "b", time.Second, nil)
	a.attempt(context.Background())
	b.attempt(context.Background())
	// Exactly one of them is leader.
	if a.IsLeader() == b.IsLeader() {
		t.Errorf("expected exactly one leader, got a=%v b=%v", a.IsLeader(), b.IsLeader())
	}
}

func TestLease_RenewalKeepsHold(t *testing.T) {
	db := openTestDB(t)
	a := New(db, "a", 200*time.Millisecond, nil)
	b := New(db, "b", 200*time.Millisecond, nil)
	a.attempt(context.Background())
	if !a.IsLeader() {
		t.Fatal("a should win the empty race")
	}
	// a renews well within the ttl; b should keep losing.
	time.Sleep(50 * time.Millisecond)
	a.attempt(context.Background())
	b.attempt(context.Background())
	if !a.IsLeader() {
		t.Error("a should still hold after a fast renewal")
	}
	if b.IsLeader() {
		t.Error("b shouldn't take over before a's lease expires")
	}
}

func TestLease_TakeoverAfterExpiry(t *testing.T) {
	db := openTestDB(t)
	a := New(db, "a", 100*time.Millisecond, nil)
	b := New(db, "b", 100*time.Millisecond, nil)
	a.attempt(context.Background())
	if !a.IsLeader() {
		t.Fatal("a should be leader initially")
	}
	// Sleep past a's lease without renewing.
	time.Sleep(150 * time.Millisecond)
	b.attempt(context.Background())
	if !b.IsLeader() {
		t.Error("b should have taken over after a's lease expired")
	}
}

func TestLease_ConcurrentClaimsExactlyOneWins(t *testing.T) {
	db := openTestDB(t)
	const N = 8
	leases := make([]*Lease, N)
	for i := 0; i < N; i++ {
		leases[i] = New(db, "replica-"+itoa(i), time.Second, nil)
	}
	var wg sync.WaitGroup
	for _, l := range leases {
		l := l
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.attempt(context.Background())
		}()
	}
	wg.Wait()
	winners := 0
	for _, l := range leases {
		if l.IsLeader() {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("expected exactly one winner, got %d", winners)
	}
}

func TestLease_RunStopsOnContextCancel(t *testing.T) {
	db := openTestDB(t)
	l := New(db, "stopper", 200*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = l.Run(ctx)
		close(done)
	}()
	// Let it acquire.
	time.Sleep(80 * time.Millisecond)
	if !l.IsLeader() {
		t.Fatal("Run should have acquired leadership")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if l.IsLeader() {
		t.Error("Run should release on shutdown")
	}
}

func TestLease_ChangesEmitsOnRoleFlip(t *testing.T) {
	db := openTestDB(t)
	a := New(db, "a", 100*time.Millisecond, nil)
	a.attempt(context.Background())
	select {
	case s := <-a.Changes():
		if !s.IsLeader {
			t.Errorf("expected leader=true on acquire, got %+v", s)
		}
	case <-time.After(time.Second):
		t.Fatal("no change event on acquire")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [4]byte
	n := 0
	for i > 0 {
		buf[n] = byte('0' + i%10)
		i /= 10
		n++
	}
	// reverse
	for j, k := 0, n-1; j < k; j, k = j+1, k-1 {
		buf[j], buf[k] = buf[k], buf[j]
	}
	return string(buf[:n])
}
