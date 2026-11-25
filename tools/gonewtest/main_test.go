package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17xande-dev/gowebkit/tools/internal/testrepo"
)

func TestGenerateFromRevision(t *testing.T) {
	t.Parallel()
	root := testrepo.New(t, "repo")
	testrepo.Git(t, root, "add", "--", "LICENSE", "template")
	testrepo.Git(t, root, "commit", "-qm", "Record the template fixture")
	rev := testrepo.Git(t, root, "rev-parse", "HEAD")
	testrepo.Write(t, root, "template/sqlite/value/value.go", "package value\n\nconst Text = \"newer commit\"\n")
	testrepo.Git(t, root, "add", "--", "template/sqlite/value/value.go")
	testrepo.Git(t, root, "commit", "-qm", "Change the sample value")

	// The linked worktree is on a later revision and has uncommitted changes
	// too. Give it a module-local LICENSE so even a directory zip can pass
	// the licence assertion and get as far as testing the revision.
	linked := filepath.Join(t.TempDir(), "linked")
	testrepo.Git(t, root, "worktree", "add", "--quiet", "--detach", linked, "HEAD")
	testrepo.Write(t, linked, "template/sqlite/value/value.go", "package value\n\nconst Text = \"dirty worktree\"\n")
	testrepo.Write(t, linked, "template/sqlite/LICENSE", testrepo.Read(t, root, "LICENSE"))
	out, err := testrepo.Run(t, "-root", linked, "-rev", rev, "-module", "example.com/generated")
	if err != nil || !strings.Contains(out, "checked generated fixture") {
		t.Fatalf("generation from the recorded revision: %v\n%s", err, out)
	}
}

func TestRejectsGeneratedAppWithoutLicense(t *testing.T) {
	t.Parallel()
	root := testrepo.New(t, "repo")
	if err := os.Remove(filepath.Join(root, "LICENSE")); err != nil {
		t.Fatal(err)
	}
	testrepo.Git(t, root, "add", "--", "template")
	testrepo.Git(t, root, "commit", "-qm", "Record a template without its licence")
	out, err := testrepo.Run(t, "-root", root, "--", "go", "list", "-m")
	if err == nil || !strings.Contains(out, "generated app needs a nonempty LICENSE") {
		t.Fatalf("wanted a missing-licence failure, got %v\n%s", err, out)
	}
}

// fakeDocker puts a docker stand-in first on PATH that records each call's
// arguments and working directory, so a test can see what was run without a
// daemon.
func fakeDocker(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$*\" \"$PWD\" >> '" + log + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func composeFixture(t *testing.T, withCompose bool) string {
	t.Helper()
	root := testrepo.New(t, "repo")
	if withCompose {
		testrepo.Write(t, root, "template/sqlite/compose.yaml", "services: {}\n")
	}
	testrepo.Git(t, root, "add", "--", "LICENSE", "template")
	testrepo.Git(t, root, "commit", "-qm", "Record the template fixture")
	return root
}

func TestComposeIsTornDownAfterTheCommand(t *testing.T) {
	for _, tt := range []struct {
		name    string
		command []string
		failed  bool
	}{
		{"succeeding command", []string{"go", "list", "-m"}, false},
		{"failing command", []string{"sh", "-c", "exit 3"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			log := fakeDocker(t)
			root := composeFixture(t, true)
			out, err := testrepo.Run(t, append([]string{"-root", root, "-module", "example.com/generated", "--"}, tt.command...)...)
			if (err != nil) != tt.failed {
				t.Fatalf("run error = %v, want failure %v\n%s", err, tt.failed, out)
			}
			calls := testrepo.Read(t, filepath.Dir(log), "calls.log")
			if !strings.HasPrefix(calls, "compose down -v --remove-orphans|") || strings.Count(calls, "\n") != 1 {
				t.Fatalf("docker calls = %q, want one compose down -v --remove-orphans\n%s", calls, out)
			}
			if dir := strings.TrimSpace(strings.SplitN(calls, "|", 2)[1]); !strings.HasSuffix(dir, "/app") {
				t.Fatalf("compose down ran in %q, want the generated app", dir)
			}
		})
	}
}

func TestNoComposeFileMeansNoDockerCall(t *testing.T) {
	log := fakeDocker(t)
	root := composeFixture(t, false)
	out, err := testrepo.Run(t, "-root", root, "-module", "example.com/generated", "--", "go", "list", "-m")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatalf("docker was called for an app without a compose file: %s", testrepo.Read(t, filepath.Dir(log), "calls.log"))
	}
}

// The module cache is read-only, which makes a plain RemoveAll fail.
func TestRemoveAllDeletesReadOnlyTrees(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work")
	sub := filepath.Join(dir, "modcache", "pkg@v1")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "file.go"), []byte("package p\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "modcache"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := removeAll(dir); err != nil {
		t.Fatalf("removeAll: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory still exists: %v", err)
	}
}

// A real run downloads the pinned gonew into a read-only module cache inside
// its temporary directory; nothing may be left in the temporary root.
func TestRunLeavesNoTemporaryDirectory(t *testing.T) {
	tmp := t.TempDir()
	root := composeFixture(t, false)
	out, err := testrepo.RunEnv(t, []string{"TMPDIR=" + tmp}, "-root", root, "-module", "example.com/generated", "--", "go", "list", "-m")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	left, _ := filepath.Glob(filepath.Join(tmp, "gonewtest-*"))
	if len(left) != 0 {
		t.Fatalf("temporary directories left behind: %v", left)
	}
}

func TestDockerImageIsTaggedAndRemoved(t *testing.T) {
	log := fakeDocker(t)
	root := composeFixture(t, false)
	out, err := testrepo.Run(t, "-root", root, "-module", "example.com/generated", "-docker", "--", "go", "list", "-m")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(testrepo.Read(t, filepath.Dir(log), "calls.log")), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "build -t gonewtest-") || !strings.HasSuffix(strings.SplitN(lines[0], "|", 2)[0], " .") {
		t.Fatalf("want a tagged build, got %q", lines)
	}
	tag := strings.Fields(lines[0])[2]
	if got := strings.SplitN(lines[1], "|", 2)[0]; got != "rmi "+tag {
		t.Fatalf("want the image %s removed, got %q", tag, got)
	}
}
