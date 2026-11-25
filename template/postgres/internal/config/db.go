package config

import (
	"strings"

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
const PoolSizeMessage = "DATABASE_URL pool_max_conns must be at least 2: one connection holds the migration lock while another runs the migrations"

// DB holds the connection string. The string carries a password, so the type
// refuses to print itself: a logged or %v-formatted Config cannot leak it.
type DB struct {
	URL string
}

// String and GoString keep the connection string out of logs and test output.
func (DB) String() string   { return "config.DB{URL: <redacted>}" }
func (DB) GoString() string { return "config.DB{URL: <redacted>}" }

func loadDB(e *env) DB {
	url := e.secret("DATABASE_URL", "DATABASE_URL_FILE")
	file, _ := e.lookup("DATABASE_URL_FILE")
	fileSet := file != ""
	if v, set := e.lookup("DATABASE_URL"); set && v == "" {
		e.reject("DATABASE_URL must not be empty")
	}
	if url == "" && !fileSet {
		url = developmentURL
	}
	// Parsing is offline: it validates the string before any connection or
	// migration. The parser's own message can quote the input, so it is not
	// passed on.
	if strings.TrimSpace(url) == "" || strings.ContainsRune(url, 0) {
		e.reject("DATABASE_URL is not a valid PostgreSQL connection string")
	} else if pc, err := pgxpool.ParseConfig(url); err != nil {
		e.reject("DATABASE_URL is not a valid PostgreSQL connection string")
	} else if pc.MaxConns < MinPoolConns {
		e.reject(PoolSizeMessage)
	}
	return DB{URL: url}
}
