package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17xande-dev/gowebkit/tools/internal/testrepo"
)

func TestCopyRespectsManifest(t *testing.T) {
	root := testrepo.New(t, "pair")
	testrepo.Write(t, root, "template/sqlite/local.txt", "Ignored local input.\n")
	before := testrepo.Read(t, root, "template/postgres/internal/db/value.txt")
	if out, err := testrepo.Run(t, "-root", root); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	if got := testrepo.Read(t, root, "template/postgres/internal/db/value.txt"); got != before {
		t.Fatalf("overwrote owned path: %q", got)
	}
	requireMissing(t, root, "template/postgres/internal/db/sqlite-only.txt")
	requireMissing(t, root, "template/postgres/local.txt")
	if got := testrepo.Read(t, root, "template/postgres/postgres-only.txt"); got != "Postgres-specific input.\n" {
		t.Fatalf("changed Postgres-only owned path: %q", got)
	}
}

func TestCopyRewritesModuleAndImportsOnly(t *testing.T) {
	root := testrepo.New(t, "pair")
	if out, err := testrepo.Run(t, "-root", root); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	src := testrepo.Read(t, root, "template/sqlite/sample.go")
	want := strings.Replace(src, `"`+repoModule+sqliteDir+`/internal/value"`, `"`+repoModule+postgresDir+`/internal/value"`, 1)
	if got := testrepo.Read(t, root, "template/postgres/sample.go"); got != want {
		t.Fatalf("want only the import rewritten:\n%s\ngot:\n%s", want, got)
	}
	if got := testrepo.Read(t, root, "template/postgres/go.mod"); !strings.Contains(got, "module "+repoModule+postgresDir+" // Template module.") {
		t.Fatalf("want the module directive rewritten, got:\n%s", got)
	}
	if out, err := testrepo.Run(t, "-root", root, "-check"); err != nil {
		t.Fatalf("check after sync: %v\n%s", err, out)
	}
}

func TestCheckReportsMissingSharedFile(t *testing.T) {
	root := testrepo.New(t, "pair")
	if out, err := testrepo.Run(t, "-root", root); err != nil {
		t.Fatalf("initial sync: %v\n%s", err, out)
	}
	const dst = "template/postgres/shared.txt"
	testrepo.Git(t, root, "add", "--", dst)
	if err := os.Remove(filepath.Join(root, dst)); err != nil {
		t.Fatal(err)
	}
	if out, err := testrepo.Run(t, "-root", root, "-check"); err == nil ||
		!strings.Contains(out, dst+": missing from Postgres") {
		t.Fatalf("wanted a missing-file report, got %v\n%s", err, out)
	}
	requireMissing(t, root, dst)
}

func TestCheckReportsDriftAndUndeclaredPostgresFile(t *testing.T) {
	root := testrepo.New(t, "pair")
	out, err := testrepo.Run(t, "-root", root, "-check")
	if err == nil {
		t.Fatal("check accepted drift")
	}
	for _, want := range []string{
		"template/postgres/sample.go: content differs",
		"template/postgres/extra.txt: Postgres-only path is not declared",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if got := testrepo.Read(t, root, "template/postgres/sample.go"); got != "package main\n" {
		t.Fatalf("check changed the worktree: %q", got)
	}
	if out, err := testrepo.Run(t, "-root", root); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	requireMissing(t, root, "template/postgres/extra.txt")
}

func TestCopyDoesNotOverwriteIgnoredFile(t *testing.T) {
	root := testrepo.New(t, "pair")
	testrepo.Write(t, root, ".gitignore", "/template/postgres/shared.txt\n")
	const dst, local = "template/postgres/shared.txt", "Local contents.\n"
	testrepo.Write(t, root, dst, local)
	out, err := testrepo.Run(t, "-root", root)
	if err == nil || !strings.Contains(out, dst+": not overwriting an ignored Postgres file") {
		t.Fatalf("wanted an ignored-file conflict, got %v\n%s", err, out)
	}
	if got := testrepo.Read(t, root, dst); got != local {
		t.Fatalf("overwrote ignored file: %q", got)
	}
}

func requireMissing(t *testing.T, root, name string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s: expected no file, got %v", name, err)
	}
}

// A declared path that exists in only one variant is how a forgotten file (a CI
// workflow, a guide) went unnoticed: owned paths are not compared, only listed.
func TestCheckReportsOwnedPathInOnlyOneVariant(t *testing.T) {
	for _, tt := range []struct {
		name, manifest, want string
	}{
		{"unmarked, missing from SQLite", "postgres-only.txt\n",
			`"postgres-only.txt" is declared for both variants but missing from template/sqlite`},
		{"marked postgres-only but in SQLite too", "postgres-only: postgres-only.txt\npostgres-only: shared.txt\n",
			`"shared.txt" is marked postgres-only but exists in template/sqlite`},
		{"marked sqlite-only but missing", "sqlite-only: absent.txt\npostgres-only: postgres-only.txt\n",
			`"absent.txt" matches no file in either variant`},
		{"marked sqlite-only but only in Postgres", "sqlite-only: postgres-only.txt\n",
			`"postgres-only.txt" is marked sqlite-only but missing from template/sqlite`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := testrepo.New(t, "pair")
			testrepo.Write(t, filepath.Join(root, "template"), "variants.txt", "internal/db/\n"+tt.manifest)
			for _, args := range [][]string{{"-check"}, {}} {
				out, err := testrepo.Run(t, append([]string{"-root", root}, args...)...)
				if err == nil || !strings.Contains(out, tt.want) {
					t.Fatalf("args %v: wanted %q, got %v\n%s", args, tt.want, err, out)
				}
			}
		})
	}
	t.Run("a marked path in the right variant is accepted", func(t *testing.T) {
		root := testrepo.New(t, "pair")
		if out, err := testrepo.Run(t, "-root", root); err != nil {
			t.Fatalf("sync: %v\n%s", err, out)
		}
		if out, err := testrepo.Run(t, "-root", root, "-check"); err != nil {
			t.Fatalf("check: %v\n%s", err, out)
		}
	})
}
