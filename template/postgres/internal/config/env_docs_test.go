package config

import (
	"testing"

	"github.com/17xande-dev/env/envtest"
)

func TestEnvironmentDocumentation(t *testing.T) {
	recorder := envtest.NewRecorder(nil)
	if _, err := load(recorder.Lookup); err != nil {
		t.Fatalf("load with default configuration: %v", err)
	}
	keys := recorder.Keys()
	envtest.Documented(t, ".env.example", keys, envtest.EnvExample(t, "../../.env.example"))
	envtest.Documented(t, "README.md", keys, envtest.MarkdownTable(t, "../../README.md", "## Configuration"))
}
