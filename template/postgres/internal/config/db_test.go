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
		{"empty", map[string]string{"DATABASE_URL": ""}, developmentURL},
		{"url", map[string]string{"DATABASE_URL": sampleURL}, sampleURL},
		{"url file", map[string]string{"DATABASE_URL_FILE": file}, sampleURL},
		{"url file with empty direct setting", map[string]string{"DATABASE_URL": "", "DATABASE_URL_FILE": file}, sampleURL},
		{"key value form", map[string]string{"DATABASE_URL": "host=example.com user=app dbname=app"}, "host=example.com user=app dbname=app"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := from(tt.values)
			if err != nil || cfg.DB.ConnectionString() != tt.want {
				t.Fatalf("unexpected database connection string, error=%v", err)
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
		{"blank", map[string]string{"DATABASE_URL": "  "}, "DATABASE_URL"},
		{"not a connection string", map[string]string{
			"DATABASE_URL": "postgres://user-sample:sample-secret@example.com:notaport/app",
		}, "DATABASE_URL: is not a valid PostgreSQL connection string"},
		{"a pool of one", map[string]string{"DATABASE_URL": sampleURL + "&pool_max_conns=1"}, "pool_max_conns"},
		{"both", map[string]string{"DATABASE_URL": sampleURL, "DATABASE_URL_FILE": empty}, "DATABASE_URL_FILE: cannot be set together with DATABASE_URL"},
		{"empty file", map[string]string{"DATABASE_URL_FILE": empty}, "DATABASE_URL_FILE"},
		{"missing file", map[string]string{"DATABASE_URL_FILE": filepath.Join(dir, "none")}, "DATABASE_URL_FILE"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := from(tt.values)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one naming %q", err, tt.want)
			}
			for _, part := range []struct{ name, value string }{
				{"username", "user-sample"},
				{"password", "sample-secret"},
				{"host", "example.com"},
				{"port", "notaport"},
			} {
				if strings.Contains(err.Error(), part.value) {
					t.Errorf("the diagnostic quotes the connection %s", part.name)
				}
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
		fmt.Sprint(cfg.DB), fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg), fmt.Sprintf("%#v", cfg.DB),
	} {
		if strings.Contains(rendered, "sample-secret") || !strings.Contains(rendered, "[redacted]") {
			t.Fatal("rendered configuration did not redact the database credential")
		}
	}
}
