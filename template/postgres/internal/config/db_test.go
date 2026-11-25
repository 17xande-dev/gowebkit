package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleURL = "postgres://app_user:sample-secret@example.com:5432/app?sslmode=require"

func TestDatabaseDefaultsAndOverrides(t *testing.T) {
	file := filepath.Join(t.TempDir(), "url")
	if err := os.WriteFile(file, []byte(sampleURL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"defaults to the development database", nil, developmentURL},
		{"url", map[string]string{"DATABASE_URL": sampleURL}, sampleURL},
		{"url file", map[string]string{"DATABASE_URL_FILE": file}, sampleURL},
		{"key value form", map[string]string{"DATABASE_URL": "host=example.com user=app dbname=app"}, "host=example.com user=app dbname=app"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := from(tt.values)
			if err != nil || cfg.DB.URL != tt.want {
				t.Fatalf("database url = %q, error=%v; want %q", cfg.DB.URL, err, tt.want)
			}
		})
	}
}

func TestInvalidDatabaseConfig(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"empty", map[string]string{"DATABASE_URL": ""}, "DATABASE_URL"},
		{"blank", map[string]string{"DATABASE_URL": "  "}, "DATABASE_URL"},
		{"not a connection string", map[string]string{"DATABASE_URL": "postgres://app:sample-secret@example.com:notaport/app"}, "DATABASE_URL"},
		{"a pool of one", map[string]string{"DATABASE_URL": sampleURL + "&pool_max_conns=1"}, "pool_max_conns"},
		{"both", map[string]string{"DATABASE_URL": sampleURL, "DATABASE_URL_FILE": empty}, "only one"},
		{"empty file", map[string]string{"DATABASE_URL_FILE": empty}, "DATABASE_URL_FILE"},
		{"missing file", map[string]string{"DATABASE_URL_FILE": filepath.Join(dir, "none")}, "DATABASE_URL_FILE"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := from(tt.values)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one naming %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "sample-secret") {
				t.Fatalf("the error quotes the password: %v", err)
			}
		})
	}
}

// A configuration is printed by diagnostics and failed tests; the password in
// the connection string must not come out with it.
func TestDatabaseConfigNeverPrintsThePassword(t *testing.T) {
	cfg, err := from(map[string]string{"DATABASE_URL": sampleURL})
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{
		fmt.Sprint(cfg.DB), fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg.DB),
	} {
		if strings.Contains(rendered, "sample-secret") {
			t.Fatalf("rendered configuration contains the password: %s", rendered)
		}
	}
}
