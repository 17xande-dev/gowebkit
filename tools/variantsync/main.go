// Command variantsync keeps the shared files of the two templates in step.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

const (
	sqliteDir   = "template/sqlite"
	postgresDir = "template/postgres"
	repoModule  = "github.com/17xande-dev/gowebkit/"
)

func main() {
	root := flag.String("root", ".", "repository root")
	check := flag.Bool("check", false, "report differences without writing files")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := syncVariants(*root, *check); err != nil {
		fmt.Fprintln(os.Stderr, "variantsync:", err)
		os.Exit(1)
	}
}

// entry is one manifest line: a path the variants own. only names the single
// variant that is meant to have it ("sqlite" or "postgres"), or is empty when
// both must.
type entry struct{ path, only string }

func (e entry) matches(name string) bool {
	return name == e.path || strings.HasSuffix(e.path, "/") && strings.HasPrefix(name, e.path)
}

func readManifest(root *os.Root) ([]entry, error) {
	const name = "template/variants.txt"
	data, err := root.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var owned []entry
	for i, line := range strings.Split(string(data), "\n") {
		text, _, _ := strings.Cut(line, "#")
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		var e entry
		e.path = text
		for _, variant := range []string{"sqlite", "postgres"} {
			if rest, ok := strings.CutPrefix(text, variant+"-only:"); ok {
				e = entry{strings.TrimSpace(rest), variant}
			}
		}
		p := strings.TrimSuffix(e.path, "/")
		if p == "." || !fs.ValidPath(p) || strings.ContainsAny(p, "\\\x00") {
			return nil, fmt.Errorf("%s:%d: invalid relative path %q", name, i+1, text)
		}
		owned = append(owned, e)
	}
	return owned, nil
}

func ownedIndex(name string, owned []entry) int {
	for i, e := range owned {
		if e.matches(name) {
			return i
		}
	}
	return -1
}

// checkOwned reports a declared path that is missing from a variant that should
// have it, present in one that should not, or present in neither. Without this
// a file one variant forgot (a database guide, say) is invisible, because
// variant-owned paths are not compared.
func checkOwned(owned []entry, present []map[string]int) []string {
	var problems []string
	for i, e := range owned {
		in := map[string]bool{sqliteDir: present[i][sqliteDir] > 0, postgresDir: present[i][postgresDir] > 0}
		switch {
		case !in[sqliteDir] && !in[postgresDir]:
			problems = append(problems, fmt.Sprintf("variants.txt: %q matches no file in either variant", e.path))
		case e.only == "":
			for _, dir := range []string{sqliteDir, postgresDir} {
				if !in[dir] {
					problems = append(problems, fmt.Sprintf("variants.txt: %q is declared for both variants but missing from %s (mark it sqlite-only: or postgres-only: if that is intended)", e.path, dir))
				}
			}
		default:
			have, other := sqliteDir, postgresDir
			if e.only == "postgres" {
				have, other = postgresDir, sqliteDir
			}
			if !in[have] {
				problems = append(problems, fmt.Sprintf("variants.txt: %q is marked %s-only but missing from %s", e.path, e.only, have))
			}
			if in[other] {
				problems = append(problems, fmt.Sprintf("variants.txt: %q is marked %s-only but exists in %s", e.path, e.only, other))
			}
		}
	}
	return problems
}

func syncVariants(dir string, check bool) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	owned, err := readManifest(root)
	if err != nil {
		return err
	}
	// Include new files so contributors can run this before staging, but don't
	// carry local, ignored files (such as an environment file) across variants.
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "--full-name", "-z", "--", sqliteDir, postgresDir)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("list template files: %w", err)
	}
	if _, err := regularFile(root, sqliteDir+"/go.mod"); err != nil {
		return fmt.Errorf("SQLite module: %w", err)
	}
	present := make([]map[string]int, len(owned))
	for i := range present {
		present[i] = make(map[string]int)
	}
	type pair struct{ sqlite, postgres fs.FileInfo }
	files := make(map[string]pair)
	for full := range strings.SplitSeq(string(out), "\x00") {
		if full == "" {
			continue
		}
		variant := sqliteDir
		if strings.HasPrefix(full, postgresDir+"/") {
			variant = postgresDir
		}
		rel, ok := strings.CutPrefix(full, variant+"/")
		if !ok {
			return fmt.Errorf("%s is not a template file", full)
		}
		info, err := regularFile(root, full)
		if errors.Is(err, fs.ErrNotExist) {
			// A tracked file can already have been removed from the worktree.
			continue
		}
		if err != nil {
			return err
		}
		if i := ownedIndex(rel, owned); i >= 0 {
			present[i][variant]++
			continue
		}
		p := files[rel]
		if variant == sqliteDir {
			p.sqlite = info
		} else {
			p.postgres = info
		}
		files[rel] = p
	}
	names := make([]string, 0, len(files))
	for rel := range files {
		names = append(names, rel)
	}
	slices.Sort(names)

	var differences int
	for _, rel := range names {
		p := files[rel]
		dst := postgresDir + "/" + rel
		var reason string
		var want []byte
		if p.sqlite == nil {
			reason = "Postgres-only path is not declared in variants.txt"
		} else {
			src, err := root.ReadFile(sqliteDir + "/" + rel)
			if err != nil {
				return err
			}
			want, err = rewrite(rel, src)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			if p.postgres == nil {
				reason = "missing from Postgres"
			} else {
				got, err := root.ReadFile(dst)
				if err != nil {
					return err
				}
				if !bytes.Equal(got, want) {
					reason = "content differs from rewritten SQLite file"
				} else if executable(p.postgres) != executable(p.sqlite) {
					reason = "executable bit differs from SQLite file"
				}
			}
		}
		if reason == "" {
			continue
		}
		differences++
		if check {
			fmt.Printf("%s: %s\n", dst, reason)
		} else if p.sqlite == nil {
			if err := root.Remove(dst); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", dst)
		} else {
			if p.postgres == nil {
				if _, err := regularFile(root, dst); err == nil {
					return fmt.Errorf("%s: not overwriting an ignored Postgres file", dst)
				} else if !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
			if err := writeShared(root, dst, want, p.sqlite); err != nil {
				return err
			}
			fmt.Printf("synced %s\n", dst)
		}
	}
	problems := checkOwned(owned, present)
	for _, p := range problems {
		fmt.Println(p)
	}
	if len(problems) != 0 {
		return fmt.Errorf("%d problems with the variant-owned paths in variants.txt", len(problems))
	}
	if check && differences != 0 {
		return fmt.Errorf("%d shared file differences; run variantsync to sync", differences)
	}
	return nil
}

func regularFile(root *os.Root, name string) (fs.FileInfo, error) {
	if err := checkParents(root, path.Dir(name)); err != nil {
		return nil, err
	}
	info, err := root.Lstat(name)
	if err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: expected a regular file", name)
	}
	return info, err
}

func checkParents(root *os.Root, dir string) error {
	if dir == "." {
		return nil
	}
	if err := checkParents(root, path.Dir(dir)); err != nil {
		return err
	}
	info, err := root.Lstat(dir)
	if err == nil && !info.IsDir() {
		return fmt.Errorf("%s: expected a directory, not a file or link", dir)
	}
	return err
}

func executable(info fs.FileInfo) bool { return info.Mode()&0100 != 0 }

func writeShared(root *os.Root, name string, data []byte, src fs.FileInfo) error {
	dir := path.Dir(name)
	if err := checkParents(root, dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := root.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if _, err := regularFile(root, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Rename instead of truncating: a local hard link must not change the
	// source or a separate variant-owned file.
	f, err := os.CreateTemp(filepath.Join(root.Name(), filepath.FromSlash(dir)), ".variantsync-*")
	if err != nil {
		return err
	}
	tmp := path.Join(dir, filepath.Base(f.Name()))
	defer root.Remove(tmp)
	mode := fs.FileMode(0644)
	if executable(src) {
		mode = 0755
	}
	_, writeErr := f.Write(data)
	chmodErr := f.Chmod(mode)
	if err := errors.Join(writeErr, chmodErr, f.Close()); err != nil {
		return err
	}
	return root.Rename(tmp, name)
}

func rewrite(name string, data []byte) ([]byte, error) {
	const src, dst = repoModule + sqliteDir, repoModule + postgresDir
	if name == "go.mod" {
		f, err := modfile.ParseLax(name, data, nil)
		if err != nil {
			return nil, err
		}
		if f.Module == nil || f.Module.Mod.Path != src {
			return nil, fmt.Errorf("expected module %s", src)
		}
		line := f.Module.Syntax
		end := line.End.Byte
		start := end - len(line.Token[len(line.Token)-1])
		return bytes.Join([][]byte{data[:start], []byte(dst), data[end:]}, nil), nil
	}
	if !strings.HasSuffix(name, ".go") {
		return data, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, data, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	prev := 0
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		if importPath != src && !strings.HasPrefix(importPath, src+"/") {
			continue
		}
		start := fset.Position(spec.Path.Pos()).Offset
		end := fset.Position(spec.Path.End()).Offset
		out.Write(data[prev:start])
		out.WriteString(strconv.Quote(dst + strings.TrimPrefix(importPath, src)))
		prev = end
	}
	out.Write(data[prev:])
	return out.Bytes(), nil
}
