// Command gonewtest exercises generation from a committed module snapshot.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

const (
	pinnedGonew = "v0.52.0"
	testVersion = "v0.0.0-snapshot"
)

type options struct {
	root, rev, template, destination, gonew string
	docker                                  bool
	command                                 []string
}

func main() {
	var opts options
	flag.StringVar(&opts.root, "root", ".", "Git repository root")
	flag.StringVar(&opts.rev, "rev", "HEAD", "Git commit, ref or revision")
	flag.StringVar(&opts.template, "template", "template/sqlite", "repository-relative template directory")
	flag.StringVar(&opts.destination, "module", "example.com/app", "generated module path")
	flag.StringVar(&opts.gonew, "gonew-version", pinnedGonew, "gonew version")
	flag.BoolVar(&opts.docker, "docker", false, "also run docker build in the generated app")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Usage: gonewtest [flags] [-- command [args...]]")
		flag.PrintDefaults()
	}
	flag.Parse()
	opts.command = flag.Args()
	if len(opts.command) == 0 {
		opts.command = []string{"make", "gate"}
	}
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "gonewtest:", err)
		os.Exit(1)
	}
}

func run(opts options) (err error) {
	if opts.template == "." || !fs.ValidPath(opts.template) || strings.ContainsAny(opts.template, "\\\x00") {
		return fmt.Errorf("invalid repository-relative template directory %q", opts.template)
	}
	if err := module.CheckPath(opts.destination); err != nil {
		return err
	}
	root, err := filepath.Abs(opts.root)
	if err != nil {
		return err
	}
	commit, err := git(root, "rev-parse", "--verify", "--end-of-options", opts.rev+"^{commit}")
	if err != nil {
		return err
	}
	rev := strings.TrimSpace(string(commit))
	when, err := git(root, "show", "-s", "--format=%cI", rev)
	if err != nil {
		return err
	}
	stamp, err := time.Parse(time.RFC3339, strings.TrimSpace(string(when)))
	if err != nil {
		return err
	}
	gomod, err := git(root, "cat-file", "blob", rev+":"+opts.template+"/go.mod")
	if err != nil {
		return err
	}
	src := modfile.ModulePath(gomod)
	if src == opts.destination {
		return fmt.Errorf("destination must differ from the source module %s", src)
	}
	if err := module.Check(src, testVersion); err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "gonewtest-")
	if err != nil {
		return err
	}
	// The module cache inside work is read-only, so a plain RemoveAll would
	// fail and, unreported, strand about 14 MB in the temporary root on every run.
	defer func() { err = errors.Join(err, removeAll(work)) }()

	// CreateFromVCS requires .git to be the repository's Git directory.
	// A local, checkout-free clone supplies that even for a linked worktree.
	// It shares objects and reads the resolved commit, never working files.
	repo := filepath.Join(work, "repo")
	if _, err := git(root, "clone", "--quiet", "--no-checkout", "--shared", "--", root, repo); err != nil {
		return err
	}
	proxy := filepath.Join(work, "proxy")
	mv := module.Version{Path: src, Version: testVersion}
	if err := makeProxy(proxy, repo, rev, opts.template, mv, gomod, stamp); err != nil {
		return err
	}
	env := append(os.Environ(), "GOWORK=off")
	gonewEnv, err := proxyEnv(work, proxy, src, env)
	if err != nil {
		return err
	}
	app := filepath.Join(work, "app")
	if err := command(work, gonewEnv, "go", "run", "golang.org/x/tools/cmd/gonew@"+opts.gonew, src+"@"+testVersion, opts.destination, app); err != nil {
		return err
	}
	if err := verifyApp(app, src, opts.destination); err != nil {
		return err
	}
	runErr := command(app, env, opts.command...)
	var rmiErr error
	if runErr == nil && opts.docker {
		// Tag the image so it can be removed again; an untagged one would
		// pile up on a developer's machine.
		tag := "gonewtest-" + strings.ToLower(strings.TrimPrefix(filepath.Base(work), "gonewtest-"))
		runErr = command(app, env, "docker", "build", "-t", tag, ".")
		if runErr == nil {
			rmiErr = command(app, env, "docker", "rmi", tag)
		}
	}
	// A gate may start the app's compose services and keep them running for a
	// developer's next run. Nothing here is a developer's: the generated app is
	// deleted on exit, so its containers and volumes must go with it, also when
	// the command failed.
	return errors.Join(runErr, rmiErr, composeDown(app, env))
}

// removeAll deletes dir after making everything under it writable: the Go
// module cache is created read-only on purpose.
func removeAll(dir string) error {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSymlink == 0 {
			if info, err := d.Info(); err == nil {
				_ = os.Chmod(p, info.Mode().Perm()|0o700)
			}
		}
		return nil
	})
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove temporary directory: %w", err)
	}
	return nil
}

var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// composeTimeout bounds the teardown so a stuck daemon cannot hang the run.
const composeTimeout = 2 * time.Minute

// composeDown removes the generated app's compose project, volumes included.
// An app without a compose file started nothing and is left alone.
func composeDown(app string, env []string) error {
	found := false
	for _, name := range composeFiles {
		if _, err := os.Stat(filepath.Join(app, name)); err == nil {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), composeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "down", "-v", "--remove-orphans")
	cmd.Dir = app
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose down: %w", err)
	}
	return nil
}

func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func makeProxy(proxy, repo, rev, template string, mv module.Version, gomod []byte, stamp time.Time) error {
	escaped, err := module.EscapePath(mv.Path)
	if err != nil {
		return err
	}
	dir := filepath.Join(proxy, filepath.FromSlash(escaped), "@v")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	info, err := json.Marshal(struct {
		Version string
		Time    time.Time
	}{mv.Version, stamp.UTC()})
	if err != nil {
		return err
	}
	for name, content := range map[string][]byte{
		"list":               []byte(mv.Version + "\n"),
		mv.Version + ".mod":  gomod,
		mv.Version + ".info": info,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0644); err != nil {
			return err
		}
	}
	dst, err := os.Create(filepath.Join(dir, mv.Version+".zip"))
	if err != nil {
		return err
	}
	zipErr := modzip.CreateFromVCS(dst, mv, repo, rev, template)
	closeErr := dst.Close()
	if zipErr != nil {
		return zipErr
	}
	return closeErr
}

func proxyEnv(work, proxy, source string, env []string) ([]string, error) {
	// Respect GOENV as well as exported settings for public dependencies.
	cmd := exec.Command("go", "env", "-json", "GOPROXY", "GONOSUMDB")
	cmd.Dir = work
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read Go proxy settings: %w", err)
	}
	var settings map[string]string
	if err := json.Unmarshal(out, &settings); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(proxy)}
	proxyURL := strings.ReplaceAll(u.String(), ",", "%2C")
	return append(env,
		"GOPROXY="+proxyURL+","+settings["GOPROXY"],
		"GONOPROXY=none",
		"GONOSUMDB="+source+","+settings["GONOSUMDB"],
		// The synthetic version name is reused on every run.
		"GOMODCACHE="+filepath.Join(work, "modcache"),
	), nil
}

func verifyApp(app, source, destination string) error {
	gomod, err := os.ReadFile(filepath.Join(app, "go.mod"))
	if err != nil {
		return err
	}
	if got := modfile.ModulePath(gomod); got != destination {
		return fmt.Errorf("generated module is %q, want %q", got, destination)
	}
	if licence, err := os.ReadFile(filepath.Join(app, "LICENSE")); err != nil || len(bytes.TrimSpace(licence)) == 0 {
		return fmt.Errorf("generated app needs a nonempty LICENSE")
	}
	return fs.WalkDir(os.DirFS(app), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(name) != ".go" {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(app, name), nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("%s: invalid import: %w", name, err)
			}
			if p == source || strings.HasPrefix(p, source+"/") || strings.HasPrefix(p, "github.com/17xande-dev/gowebkit/template/") {
				return fmt.Errorf("%s: generated app still imports %s", name, p)
			}
		}
		return nil
	})
}

func command(dir string, env []string, args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(args, " "), err)
	}
	return nil
}
