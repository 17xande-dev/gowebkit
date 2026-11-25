package dbtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A killed test binary leaves its schema behind. Later runs remove schemas
// older than an hour and nothing else.
func TestDropStaleSchemas(t *testing.T) {
	base := baseURL(t)
	pool, err := pgxpool.New(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	now := time.Now()
	name := func(age time.Duration, suffix int) string {
		return fmt.Sprintf("t_%x_%016x", now.Add(-age).Unix(), suffix)
	}
	stale, fresh := name(3*time.Hour, 1), name(5*time.Minute, 2)
	other := fmt.Sprintf("t_notours_%d", now.UnixNano())
	for _, s := range []string{stale, fresh, other} {
		if _, err := pool.Exec(t.Context(), `CREATE SCHEMA `+s); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(t.Context()), `DROP SCHEMA IF EXISTS `+s+` CASCADE`) })
	}
	if err := dropStaleSchemas(t.Context(), pool, now); err != nil {
		t.Fatal(err)
	}
	exists := func(s string) bool {
		var ok bool
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, s).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if exists(stale) {
		t.Error("the stale schema was not dropped")
	}
	if !exists(fresh) {
		t.Error("a recent schema was dropped")
	}
	if !exists(other) {
		t.Error("a schema with a name NewSchema never makes was dropped")
	}
}
