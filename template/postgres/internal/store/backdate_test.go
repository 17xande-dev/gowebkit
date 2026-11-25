package store_test

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/db"
	"github.com/jackc/pgx/v5/pgconn"
)

func removeUserRow(t *testing.T, d *db.DB, id int64) {
	t.Helper()
	tag, err := d.Pool().Exec(t.Context(), `DELETE FROM users WHERE id = $1`, id)
	requireOneRow(t, tag, err)
}

func disableUserRow(t *testing.T, d *db.DB, id int64) {
	t.Helper()
	tag, err := d.Pool().Exec(t.Context(), `UPDATE users SET disabled = true WHERE id = $1`, id)
	requireOneRow(t, tag, err)
}

// backdate rewrites a session's times directly, as an old row would have them.
func backdate(t *testing.T, d *db.DB, token string, created, lastSeen time.Time) {
	t.Helper()
	digest := sha256.Sum256([]byte(token))
	tag, err := d.Pool().Exec(t.Context(), `UPDATE sessions SET created_at = $1, last_seen = $2 WHERE token_hash = $3`,
		created.UTC(), lastSeen.UTC(), digest[:])
	requireOneRow(t, tag, err)
}

func requireOneRow(t *testing.T, tag pgconn.CommandTag, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if n := tag.RowsAffected(); n != 1 {
		t.Fatalf("updated %d rows, want exactly one", n)
	}
}

// rawRows renders each session row as text, the token digest in hex and the
// times at microsecond precision in UTC, so the contract can look for a digest
// and compare rows without knowing the column types.
func rawRows(t *testing.T, d *db.DB) []string {
	t.Helper()
	rows, err := d.Pool().Query(t.Context(), `SELECT encode(token_hash, 'hex'), user_id, created_at, last_seen
		FROM sessions ORDER BY token_hash`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	const layout = "2006-01-02T15:04:05.000000Z"
	var result []string
	for rows.Next() {
		var hash string
		var userID int64
		var created, lastSeen time.Time
		if err := rows.Scan(&hash, &userID, &created, &lastSeen); err != nil {
			t.Fatal(err)
		}
		result = append(result, fmt.Sprintf("%s\t%d\t%s\t%s", hash, userID, created.UTC().Format(layout), lastSeen.UTC().Format(layout)))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
