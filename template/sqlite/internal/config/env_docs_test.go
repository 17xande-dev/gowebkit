package config

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestEnvironmentDocumentation(t *testing.T) {
	// Instrument the same lookup used by Load, including the variant's DB
	// settings and both names in each value-or-file secret.
	keys := make(map[string]bool)
	if _, err := load(func(key string) (string, bool) {
		keys[key] = true
		return "", false
	}); err != nil {
		t.Fatalf("load with default configuration: %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("config did not read any environment variables")
	}

	for _, doc := range []struct {
		name, row string
	}{
		{".env.example", `^(?:#[ \t]*)?([A-Z][A-Z0-9_]*)=`},
		{"README.md", "^\\|[ \t]*`([A-Z][A-Z0-9_]*)`[ \t]*\\|"},
	} {
		t.Run(doc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", doc.name))
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if doc.name == "README.md" {
				_, section, ok := strings.Cut(text, "\n## Configuration\n")
				if !ok {
					t.Fatal("missing Configuration heading")
				}
				text, _, _ = strings.Cut(section, "\n## ")
			}
			row := regexp.MustCompile(doc.row)
			documented := make(map[string]bool)
			for line := range strings.SplitSeq(text, "\n") {
				if match := row.FindStringSubmatch(line); match != nil {
					key := match[1]
					if documented[key] {
						t.Errorf("%s is documented more than once", key)
					}
					documented[key] = true
				}
			}
			for _, key := range slices.Sorted(maps.Keys(keys)) {
				if !documented[key] {
					t.Errorf("%s is read by config but is not documented", key)
				}
			}
			for _, key := range slices.Sorted(maps.Keys(documented)) {
				if !keys[key] {
					t.Errorf("%s is documented but is not read by config", key)
				}
			}
		})
	}
}
