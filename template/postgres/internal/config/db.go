package config

import (
	"strings"

	"github.com/17xande-dev/env"
	"github.com/jackc/pgx/v5/pgxpool"
)

// developmentURL matches the throwaway database in compose.yaml. It is the
// default so that `make run` works on a fresh checkout; a deployment sets
// DATABASE_URL (or DATABASE_URL_FILE) and never relies on it.
const developmentURL = "postgres://app:app@localhost:5432/app?sslmode=disable"

// MinPoolConns is the smallest pool the application can run with: a boot holds
// one connection for the migration lock while migrations use another, so a
// pool of one would wait on itself for ever.
const MinPoolConns = 2

// PoolSizeMessage is the error for a pool smaller than MinPoolConns.
const PoolSizeMessage = "DATABASE_URL " + poolSizeProblem

const poolSizeProblem = "pool_max_conns must be at least 2: one connection holds the migration lock while another runs the migrations"

// DB holds the connection string without exposing it in diagnostics.
type DB struct {
	URL env.Secret
}

// ConnectionString returns the credential for PostgreSQL parsing or connection.
// Do not log the returned string.
func (d DB) ConnectionString() string {
	if !d.URL.IsSet() {
		return developmentURL
	}
	return d.URL.Reveal()
}

func loadDB(r *env.Reader) DB {
	cfg := DB{URL: r.Secret("DATABASE_URL")}
	url := cfg.ConnectionString()
	// Parsing is offline: it validates the string before any connection or
	// migration. The parser's own message can quote the input, so it is not
	// passed on.
	if strings.TrimSpace(url) == "" || strings.ContainsRune(url, 0) {
		r.Fail("DATABASE_URL", "is not a valid PostgreSQL connection string")
	} else if pc, err := pgxpool.ParseConfig(url); err != nil {
		r.Fail("DATABASE_URL", "is not a valid PostgreSQL connection string")
	} else if pc.MaxConns < MinPoolConns {
		r.Fail("DATABASE_URL", poolSizeProblem)
	}
	return cfg
}
