package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDatabaseDefaultsAndOverrides(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values map[string]string
		want   DB
	}{
		{"defaults", nil, DB{filepath.Join("data", "app.db"), filepath.Join("data", "backups"), 14}},
		{"empty file", map[string]string{"DB_PATH": ""}, DB{filepath.Join("data", "app.db"), filepath.Join("data", "backups"), 14}},
		{"empty backup directory", map[string]string{"BACKUP_DIR": ""}, DB{filepath.Join("data", "app.db"), filepath.Join("data", "backups"), 14}},
		{"empty retention", map[string]string{"BACKUP_KEEP": ""}, DB{filepath.Join("data", "app.db"), filepath.Join("data", "backups"), 14}},
		{"custom file", map[string]string{"DB_PATH": filepath.Join("state", "main.db"), "BACKUP_KEEP": "0"},
			DB{filepath.Join("state", "main.db"), filepath.Join("state", "backups"), 0}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := from(tt.values)
			if err != nil || cfg.DB != tt.want {
				t.Fatalf("database configuration = %+v, error=%v; want %+v", cfg.DB, err, tt.want)
			}
		})
	}
}

func TestInvalidDatabaseConfig(t *testing.T) {
	for _, tt := range []struct{ name, key, value string }{
		{"blank file", "DB_PATH", " "},
		{"in-memory database", "DB_PATH", ":memory:"},
		{"URI", "DB_PATH", "file:app.db"},
		{"negative retention", "BACKUP_KEEP", "-1"},
		{"blank backup directory", "BACKUP_DIR", " "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := from(map[string]string{tt.key: tt.value})
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("expected %s error, got %v", tt.key, err)
			}
		})
	}
}
