package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/17xande-dev/sqlitekit/goosestrict"
)

func TestRollbackOneKeepsBaseline(t *testing.T) {
	ctx := t.Context()
	d, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, err := goosestrict.NewProvider(d, fstest.MapFS{
		"0001_baseline.sql": {Data: []byte(`-- +goose Up
CREATE TABLE baseline (id INTEGER PRIMARY KEY) STRICT;
INSERT INTO baseline VALUES (1);
-- +goose Down
DROP TABLE baseline;`)},
		"0002_latest.sql": {Data: []byte(`-- +goose Up
CREATE TABLE latest (id INTEGER PRIMARY KEY) STRICT;
-- +goose Down
DROP TABLE latest;`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rollbackOne(ctx, p); err != nil {
		t.Fatalf("roll back second migration: %v", err)
	}
	if current, _, err := p.GetVersions(ctx); err != nil || current != 1 {
		t.Fatalf("current version = %d, want baseline: %v", current, err)
	}
	var id int
	if err := d.QueryRowContext(ctx, "SELECT id FROM baseline").Scan(&id); err != nil || id != 1 {
		t.Fatalf("baseline row missing: id=%d, error=%v", id, err)
	}
}
