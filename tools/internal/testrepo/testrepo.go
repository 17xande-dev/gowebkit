// Package testrepo builds disposable Git repositories for command tests.
package testrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// New copies a fixture tree and initializes a repository around it.
func New(t *testing.T, fixture string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", fixture))); err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "init", "--quiet")
	return dir
}

// Git runs Git with a test-only identity and returns its trimmed output.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{
		"-c", "user.name=Fixture", "-c", "user.email=fixture@example.com",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=.git/no-hooks",
	}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// GitAs is Git with the given author and committer identity.
func GitAs(t *testing.T, dir, name, email string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{
		"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "core.hooksPath=.git/no-hooks",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Run invokes a real command built from the current test package.
func Run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return RunEnv(t, nil, args...)
}

// RunEnv is Run with extra environment entries (KEY=value), which win over the
// inherited environment. An entry without "=" removes that variable.
func RunEnv(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", append([]string{"run", "."}, args...)...)
	drop := map[string]bool{}
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		drop[k] = true
	}
	cmd.Env = []string{"GOWORK=off"}
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if !drop[k] {
			cmd.Env = append(cmd.Env, e)
		}
	}
	for _, e := range env {
		if strings.Contains(e, "=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Write changes a file in a disposable repository.
func Write(t *testing.T, dir, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

// Read returns a file's contents from a disposable repository.
func Read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
