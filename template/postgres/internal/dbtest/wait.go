package dbtest

import (
	"context"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/db"
)

// WaitUntilBlocked returns once exactly want sessions of this test's schema are
// waiting on a lock while running a statement that contains fragment, which
// proves a call is blocked on another transaction without sleeping for a fixed
// time. It fails the test if done delivers a value first (the call went ahead
// instead of waiting) or if nothing blocks within the deadline.
func WaitUntilBlocked[T any](t testing.TB, d *db.DB, fragment string, want int, done <-chan T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
	defer cancel()
	for {
		var n int
		// application_name is the schema name (see NewSchema), so other
		// packages' tests running against the same server do not count.
		err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE application_name = current_setting('application_name')
			AND wait_event_type = 'Lock' AND query LIKE '%' || $1 || '%' AND pid <> pg_backend_pid()`, fragment).Scan(&n)
		if err != nil {
			t.Fatalf("inspect sessions: %v", err)
		}
		if n == want {
			return
		}
		select {
		case v := <-done:
			t.Fatalf("the call returned (%v) instead of waiting for the lock (%d blocked of %d)", v, n, want)
		case <-ctx.Done():
			t.Fatalf("%d sessions blocked on %q, want %d", n, fragment, want)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
