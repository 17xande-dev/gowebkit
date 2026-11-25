package store_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
)

func removeUserRow(t *testing.T, d *db.DB, id int64) {
	t.Helper()
	res, err := d.ExecContext(t.Context(), `DELETE FROM users WHERE id = ?`, id)
	requireOneRow(t, res, err)
}

func disableUserRow(t *testing.T, d *db.DB, id int64) {
	t.Helper()
	res, err := d.ExecContext(t.Context(), `UPDATE users SET disabled = 1 WHERE id = ?`, id)
	requireOneRow(t, res, err)
}

func backdate(t *testing.T, d *db.DB, token string, created, lastSeen time.Time) {
	t.Helper()
	const layout = "2006-01-02T15:04:05.000000Z"
	digest := sha256.Sum256([]byte(token))
	res, err := d.ExecContext(t.Context(), `UPDATE sessions SET created_at = ?, last_seen = ? WHERE token_hash = ?`,
		created.UTC().Format(layout), lastSeen.UTC().Format(layout), hex.EncodeToString(digest[:]))
	requireOneRow(t, res, err)
}

func requireOneRow(t *testing.T, res sql.Result, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("updated %d rows, want exactly one: %v", n, err)
	}
}

func rawRows(t *testing.T, d *db.DB) []string {
	t.Helper()
	rows, err := d.QueryContext(t.Context(), `SELECT * FROM sessions ORDER BY token_hash`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var hash, created, lastSeen string
		var userID int64
		if err := rows.Scan(&hash, &userID, &created, &lastSeen); err != nil {
			t.Fatal(err)
		}
		result = append(result, fmt.Sprintf("%s\t%d\t%s\t%s", hash, userID, created, lastSeen))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
