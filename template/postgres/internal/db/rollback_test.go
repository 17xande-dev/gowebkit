package db_test

import (
	"testing"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/db"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/dbtest"
	"github.com/jackc/pgx/v5/stdlib"
)

// Every migration needs a working Down; the command refuses the baseline, so
// the round trip goes through goose directly.
func TestBaselineMigrationRoundTrips(t *testing.T) {
	pool, err := db.Connect(t.Context(), dbtest.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	p, err := db.Provider(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		var n int
		err := sqlDB.QueryRowContext(t.Context(), `SELECT count(*) FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name IN ('users', 'sessions', 'setup')`).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for i, step := range []struct {
		name string
		run  func() error
		want int
	}{
		{"up", func() error { _, err := p.Up(t.Context()); return err }, 3},
		{"down", func() error { _, err := p.Down(t.Context()); return err }, 0},
		{"up again", func() error { _, err := p.Up(t.Context()); return err }, 3},
	} {
		if err := step.run(); err != nil {
			t.Fatalf("step %d (%s): %v", i, step.name, err)
		}
		if got := count(); got != step.want {
			t.Fatalf("after %s there are %d application tables, want %d", step.name, got, step.want)
		}
	}
}
