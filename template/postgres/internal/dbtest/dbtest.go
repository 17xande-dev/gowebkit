// Package dbtest gives tests a real PostgreSQL schema of their own.
//
// A server has no temporary directory, so isolation is a schema per call: it
// is created empty, the test's connections are pointed at it with search_path,
// and it is dropped afterwards. The tests need TEST_DATABASE_URL (a URL, not a
// key/value string). Without it they skip, unless TEST_REQUIRE_DB=1, which
// `make test` and CI set: then a missing database fails the run, so a skip is
// never mistaken for a pass.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// baseURL returns TEST_DATABASE_URL, or skips or fails the test without it.
func baseURL(t testing.TB) string {
	t.Helper()
	switch required := os.Getenv("TEST_REQUIRE_DB"); required {
	case "", "0", "1":
	default:
		t.Fatalf("TEST_REQUIRE_DB must be 1 or unset, not %q", required)
	}
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		if os.Getenv("TEST_REQUIRE_DB") == "1" {
			t.Fatal("TEST_REQUIRE_DB=1 but TEST_DATABASE_URL is not set; start the test database (make db-up)")
		}
		t.Skip("TEST_DATABASE_URL is not set; skipping database tests")
	}
	return u
}

// NewSchema creates an empty schema, removes it when the test ends, and
// returns the connection string that uses it plus the schema's name.
func NewSchema(t testing.TB) (connString, schema string) {
	t.Helper()
	base := baseURL(t)
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	// The name is random, so a second call in one test, or two packages
	// running at once, can never share (and silently drop) a schema.
	// The creation time is part of the name so a later run can tell which
	// schemas a killed test binary left behind.
	schema = fmt.Sprintf("t_%x_%s", time.Now().Unix(), hex.EncodeToString(random[:]))

	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("TEST_DATABASE_URL must be a postgres:// URL")
	}
	admin, err := pgxpool.New(t.Context(), base)
	if err != nil {
		t.Fatalf("connect to the test database: %v", err)
	}
	defer admin.Close()
	staleOnce.Do(func() {
		if err := dropStaleSchemas(t.Context(), admin, time.Now()); err != nil {
			t.Logf("drop stale test schemas: %v", err)
		}
	})
	if _, err := admin.Exec(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		// The test's context has ended by now; cleanup needs its own.
		ctx := context.WithoutCancel(t.Context())
		pool, err := pgxpool.New(ctx, base)
		if err != nil {
			t.Errorf("connect to drop schema %s: %v", schema, err)
			return
		}
		defer pool.Close()
		if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})
	q := u.Query()
	q.Set("search_path", schema)
	// The schema name doubles as the application name, so a test can tell its
	// own sessions apart in pg_stat_activity from other packages' tests.
	q.Set("application_name", schema)
	u.RawQuery = q.Encode()
	return u.String(), schema
}

// staleAfter is how old a test schema must be before a later run removes it.
// A test binary that is killed cannot drop its schema; nothing running now is
// anywhere near this old.
const staleAfter = time.Hour

var (
	staleOnce  sync.Once
	schemaName = regexp.MustCompile(`^t_([0-9a-f]{1,16})_[0-9a-f]{16}$`)
)

// dropStaleSchemas removes test schemas created more than staleAfter before
// now. It matches only the names NewSchema makes.
func dropStaleSchemas(ctx context.Context, pool *pgxpool.Pool, now time.Time) error {
	rows, err := pool.Query(ctx, `SELECT nspname FROM pg_namespace WHERE nspname ~ '^t_[0-9a-f]+_[0-9a-f]{16}$'`)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, name := range names {
		m := schemaName.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		secs, err := strconv.ParseInt(m[1], 16, 64)
		if err != nil || now.Sub(time.Unix(secs, 0)) <= staleAfter {
			continue
		}
		// The name matched the pattern above, so it is safe to splice.
		if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+name+` CASCADE`); err != nil {
			return err
		}
	}
	return nil
}

// Config points a fresh, empty schema at config.DB without migrating it.
func Config(t testing.TB) config.DB {
	t.Helper()
	connString, _ := NewSchema(t)
	return config.DB{URL: connString}
}

// SetEnv points process configuration at a fresh, empty schema.
func SetEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", Config(t).URL)
	t.Setenv("DATABASE_URL_FILE", "")
}

// Open gives each call its own migrated schema and closes the pool after the test.
func Open(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(t.Context(), Config(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d
}
