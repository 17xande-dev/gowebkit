package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17xande-dev/gowebkit/tools/internal/testrepo"
)

// The tests use invented words. The real private list is never read: every
// run points the tool at a file the test wrote, or at none.
const (
	fakeWord = "zorbleword"
	fakeApp  = "quuxapp"
	// Built at run time so this file does not trip the rules it tests.
	strayDomain = "corp." + "example.org"
	strayIP     = "8.8." + "4.4"
)

// env isolates a run from the developer's own configuration.
type env struct {
	t        *testing.T
	patterns string // path of the private file, or ""
	extra    []string
}

func newEnv(t *testing.T, privateLines string) *env {
	t.Helper()
	e := &env{t: t}
	if privateLines != "" {
		e.patterns = filepath.Join(t.TempDir(), "private.txt")
		testrepo.Write(t, filepath.Dir(e.patterns), "private.txt", privateLines)
	}
	return e
}

func (e *env) run(root string, args ...string) (string, error) {
	e.t.Helper()
	vars := []string{
		"GOWEBKIT_HYGIENE_PATTERNS", "HYGIENE_REQUIRE_PRIVATE", // removed
		"XDG_CONFIG_HOME=" + e.t.TempDir(), // no default file exists here
	}
	vars = append(vars, e.extra...)
	args = append([]string{"-root", root}, args...)
	if e.patterns != "" {
		args = append(args, "-patterns", e.patterns)
	}
	return testrepo.RunEnv(e.t, vars, args...)
}

func (e *env) wantMatch(root, location, label string) string {
	e.t.Helper()
	out, err := e.run(root)
	if err == nil || !strings.Contains(out, location) || !strings.Contains(out, label) {
		e.t.Fatalf("wanted %q and %q in a failure, got %v\n%s", location, label, err, out)
	}
	return out
}

func fixture(t *testing.T) string {
	t.Helper()
	root := testrepo.New(t, "repo")
	testrepo.Git(t, root, "add", "--", "note.txt", "examples.txt")
	testrepo.Git(t, root, "commit", "-qm", "Record public examples")
	return root
}

const private = "# fake list\n" + fakeWord + "\n\\b" + fakeApp + "\\b\n"

func TestPrivateWordInTrackedFile(t *testing.T) {
	root := fixture(t)
	testrepo.Write(t, root, "note.txt", "Label:\n"+fakeWord+"\n")
	e := newEnv(t, private)
	out := e.wantMatch(root, "file note.txt:2:", "private pattern line 2")
	if strings.Contains(out, " patch:") || strings.Contains(out, fakeWord) {
		t.Fatalf("an uncommitted file is not in a patch, and the output must not repeat the word:\n%s", out)
	}
}

func TestPrivateWordInOldCommitDiff(t *testing.T) {
	root := fixture(t)
	testrepo.Write(t, root, "note.txt", fakeWord+"\n")
	testrepo.Git(t, root, "commit", "-qm", "Record a sample note", "--", "note.txt")
	old := testrepo.Git(t, root, "rev-parse", "HEAD")
	testrepo.Write(t, root, "note.txt", "Public note.\n")
	testrepo.Git(t, root, "commit", "-qm", "Replace the sample note", "--", "note.txt")
	out := newEnv(t, private).wantMatch(root, "commit "+old+" patch:", "private pattern line 2")
	if strings.Contains(out, "file note.txt:") {
		t.Fatalf("the tracked note no longer has the word:\n%s", out)
	}
}

func TestPrivateWordInCommitMessage(t *testing.T) {
	root := fixture(t)
	testrepo.Git(t, root, "commit", "--allow-empty", "-qm", "Sample message", "-m", fakeWord)
	id := testrepo.Git(t, root, "rev-parse", "HEAD")
	newEnv(t, private).wantMatch(root, "commit "+id+" message:3:", "private pattern line 2")
}

func TestPrivateWordInFileName(t *testing.T) {
	root := fixture(t)
	testrepo.Write(t, root, fakeWord+"-notes.txt", "Public text.\n")
	testrepo.Git(t, root, "add", "--", fakeWord+"-notes.txt")
	testrepo.Git(t, root, "commit", "-qm", "Add a notes file")
	testrepo.Git(t, root, "rm", "-q", "--", fakeWord+"-notes.txt")
	testrepo.Git(t, root, "commit", "-qm", "Remove the notes file")
	// Removed from the tree: only the old patch header still names it.
	newEnv(t, private).wantMatch(root, " patch:", "private pattern line 2")
	// Present in the tree: the tracked name itself is checked.
	testrepo.Write(t, root, fakeWord+"-again.txt", "Public text.\n")
	testrepo.Git(t, root, "add", "--", fakeWord+"-again.txt")
	if out := newEnv(t, private).wantMatch(root, "file "+strings.Repeat("*", len(fakeWord))+"-again.txt name:1:", "private pattern line 2"); strings.Contains(out, fakeWord) {
		t.Fatalf("a file or ref name must be masked in the output:\n%s", out)
	}
}

func TestPrivateWordInBranchName(t *testing.T) {
	root := fixture(t)
	testrepo.Git(t, root, "branch", fakeWord+"-work")
	if out := newEnv(t, private).wantMatch(root, "ref refs/heads/"+strings.Repeat("*", len(fakeWord))+"-work:1:", "private pattern line 2"); strings.Contains(out, fakeWord) {
		t.Fatalf("a file or ref name must be masked in the output:\n%s", out)
	}
}

func TestNoticeWithoutPrivateList(t *testing.T) {
	root := fixture(t)
	out, err := newEnv(t, "").run(root)
	if err != nil || !strings.Contains(out, "private pattern list not loaded: generic rules only") {
		t.Fatalf("want a clean run with the notice: %v\n%s", err, out)
	}
	// The generic rules still run.
	testrepo.Write(t, root, "note.txt", "Write to person@"+strayDomain+"\n")
	out, err = newEnv(t, "").run(root)
	if err == nil || !strings.Contains(out, "file note.txt:1:") {
		t.Fatalf("the generic rules did not run: %v\n%s", err, out)
	}
}

func TestDefaultPrivateFileLocation(t *testing.T) {
	root := fixture(t)
	testrepo.Write(t, root, "note.txt", fakeWord+"\n")
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "gowebkit"), 0o755); err != nil {
		t.Fatal(err)
	}
	testrepo.Write(t, filepath.Join(cfg, "gowebkit"), "hygiene-private.txt", private)
	out, err := testrepo.RunEnv(t, []string{"GOWEBKIT_HYGIENE_PATTERNS", "XDG_CONFIG_HOME=" + cfg}, "-root", root)
	if err == nil || !strings.Contains(out, "file note.txt:1:") {
		t.Fatalf("the default file under XDG_CONFIG_HOME was not used: %v\n%s", err, out)
	}
	// The variable wins over the default, and is the same file here.
	path := filepath.Join(cfg, "gowebkit", "hygiene-private.txt")
	out, err = testrepo.RunEnv(t, []string{"GOWEBKIT_HYGIENE_PATTERNS=" + path, "XDG_CONFIG_HOME=" + t.TempDir()}, "-root", root)
	if err == nil || !strings.Contains(out, "file note.txt:1:") {
		t.Fatalf("$GOWEBKIT_HYGIENE_PATTERNS was not used: %v\n%s", err, out)
	}
}

func TestRequirePrivateFailsWithoutList(t *testing.T) {
	root := fixture(t)
	e := newEnv(t, "")
	if out, err := e.run(root, "-require-private"); err == nil || !strings.Contains(out, "required but not loaded") {
		t.Fatalf("-require-private must fail without a list: %v\n%s", err, out)
	}
	e.extra = []string{"HYGIENE_REQUIRE_PRIVATE=1"}
	if out, err := e.run(root); err == nil || !strings.Contains(out, "required but not loaded") {
		t.Fatalf("HYGIENE_REQUIRE_PRIVATE=1 must fail without a list: %v\n%s", err, out)
	}
	e.extra = []string{"HYGIENE_REQUIRE_PRIVATE=maybe"}
	if out, err := e.run(root); err == nil || !strings.Contains(out, "must be 0, 1 or empty") {
		t.Fatalf("a bad value must fail: %v\n%s", err, out)
	}
	// With a list it passes, and an empty list does not count.
	if out, err := newEnv(t, private).run(root, "-require-private"); err != nil {
		t.Fatalf("a loaded list satisfies -require-private: %v\n%s", err, out)
	}
	if out, err := newEnv(t, "# only a comment\n").run(root, "-require-private"); err == nil {
		t.Fatalf("an empty list must not satisfy -require-private:\n%s", out)
	}
}

func TestMissingPatternFileFails(t *testing.T) {
	root := fixture(t)
	missing := filepath.Join(t.TempDir(), "absent.txt")
	out, err := testrepo.RunEnv(t, []string{"GOWEBKIT_HYGIENE_PATTERNS=" + missing, "XDG_CONFIG_HOME=" + t.TempDir()}, "-root", root)
	if err == nil || !strings.Contains(out, "private pattern file") {
		t.Fatalf("a missing file named by the environment must fail: %v\n%s", err, out)
	}
	out, err = testrepo.RunEnv(t, []string{"GOWEBKIT_HYGIENE_PATTERNS", "XDG_CONFIG_HOME=" + t.TempDir()}, "-root", root, "-patterns", missing)
	if err == nil || !strings.Contains(out, "private pattern file") {
		t.Fatalf("a missing -patterns file must fail: %v\n%s", err, out)
	}
}

func TestInvalidPrivateRuleNamesTheLine(t *testing.T) {
	root := fixture(t)
	out, err := newEnv(t, "ok\n(unclosed"+fakeWord+"\n").run(root)
	if err == nil || !strings.Contains(out, ":2: not a valid regular expression") || strings.Contains(out, fakeWord) {
		t.Fatalf("want the line number and not the rule: %v\n%s", err, out)
	}
}

func TestIdentifierTokensAndUnicodeAreNormalised(t *testing.T) {
	cases := map[string]string{
		"snake":       fakeApp + "_service",
		"kebab":       "my-" + fakeApp,
		"camel":       "my" + strings.ToUpper(fakeApp[:1]) + fakeApp[1:],
		"upper":       strings.ToUpper(fakeApp) + "Handler",
		"fullwidth":   "ＺＯＲＢＬＥＷＯＲＤ",
		"zero width":  "zor\u200bble\u2060word",
		"soft hyphen": "zorble\u00adword",
		// Look-alike letters from other scripts, and a combining mark.
		"cyrillic":            "z\u043erblew\u043erd",
		"greek":               "z\u03bfrbl\u03b5w\u03bfrd",
		"cyrillic capitals":   "Z\u041erbl\u0415W\u041eRD",
		"combining diaeresis": "zo\u0308rbleword",
		"precomposed accent":  "z\u00f6rbleword",
		"combining on a rule": "q\u0301uuxapp",
		// A letter written as an escape.
		"percent escape": "%7aorbleword",
		"html decimal":   "&#122;orbleword",
		"html hex":       "&#x7a;orbleword",
		"backslash x":    `\x7aorbleword`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			root := fixture(t)
			testrepo.Write(t, root, "note.txt", "x := "+content+"\n")
			newEnv(t, private).wantMatch(root, "file note.txt:1:", "private pattern line")
		})
	}
	t.Run("word boundary still applies to plain text", func(t *testing.T) {
		root := fixture(t)
		testrepo.Write(t, root, "note.txt", fakeApp+"kit\n") // \b rule: not a token boundary
		out, err := newEnv(t, private).run(root)
		if err != nil {
			t.Fatalf("a word-bounded rule must not match inside a longer word: %v\n%s", err, out)
		}
	})
}

func TestAllowlistScopeAndPlaceholders(t *testing.T) {
	root := fixture(t)
	testrepo.Write(t, root, "note.txt", fakeWord+"\n")
	allow := "allowlist.txt"
	testrepo.Write(t, root, allow, "file\tnote.txt\t\"zorble\\x77ord\"\n")
	e := newEnv(t, private)
	if out, err := e.run(root, "-allowlist", allow); err != nil {
		t.Fatalf("exception and placeholders should pass: %v\n%s", err, out)
	}
	testrepo.Write(t, root, "examples.txt", fakeWord+"\n")
	out, err := e.run(root, "-allowlist", allow)
	if err == nil || !strings.Contains(out, "file examples.txt:1:") || strings.Contains(out, "file note.txt:") {
		t.Fatalf("exception escaped its scope: %v\n%s", err, out)
	}
}

func TestGenericRules(t *testing.T) {
	cases := map[string]string{
		"email":   "mail person@" + strayDomain,
		"address": "host " + strayIP,
		"home":    "cd /" + "home/someone/work",
		"macos":   "cd /" + "Users/Someone/work/",
		"windows": `C:\` + `Users\Someone`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			root := fixture(t)
			testrepo.Write(t, root, "note.txt", content+"\n")
			out, err := newEnv(t, "").run(root)
			if err == nil || !strings.Contains(out, "file note.txt:1:") {
				t.Fatalf("want a match: %v\n%s", err, out)
			}
		})
	}
	root := fixture(t)
	testrepo.Write(t, root, "note.txt", "GET /users/42/ and /usr/local and 192.0.2.7\n")
	if out, err := newEnv(t, "").run(root); err != nil {
		t.Fatalf("a lower-case route and an example address are fine: %v\n%s", err, out)
	}
}

// TestReservedExamples removes one exemption at a time in the mutation check:
// each case must pass the scan, and the final test shows real values still fail.
func TestReservedExamples(t *testing.T) {
	cases := map[string]string{
		"example top-level domain": "mail person@mail.example",
		"example.com":              "mail person@example.com",
		"test top-level domain":    "mail person@host.test",
		"invalid top-level domain": "mail person@host.invalid",
		"loopback":                 "bind 127.0.0.1 and 127.1.2.3",
		"unspecified":              "bind 0.0.0.0",
		"private 10/8":             "hop 10.0.0.7",
		"private 172.16/12":        "hop 172.16.5.1 and 172.31.255.254",
		"private 192.168/16":       "hop 192.168.1.20",
		"link-local":               "hop 169.254.10.20",
		"documentation 1":          "hop 192.0.2.7",
		"documentation 2":          "hop 198.51.100.7",
		"documentation 3":          "hop 203.0.113.7",
		"cidr private":             "allow 10.0.0.0/8 and 172.16.0.0/12 and 192.168.0.0/16",
		"cidr loopback and link":   "allow 127.0.0.0/8 and 169.254.0.0/16",
		"cidr documentation":       "allow 192.0.2.0/24 and 198.51.100.0/24 and 203.0.113.0/24",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			root := fixture(t)
			testrepo.Write(t, root, "note.txt", content+"\n")
			if out, err := newEnv(t, "").run(root); err != nil {
				t.Fatalf("a reserved example should pass: %v\n%s", err, out)
			}
		})
	}
}

func TestReservedExemptionsStopShort(t *testing.T) {
	cases := map[string]string{
		"public address":                       "host " + strayIP,
		"public cidr":                          "allow 8.8." + "0.0/16",
		"just outside 172.16/12":               "hop 172.32." + "0.1",
		"just outside link-local":              "hop 169.253." + "0.1",
		"mailbox on a real domain":             "mail person@" + strayDomain,
		"domain merely ending in .example.org": "mail person@host.example." + "org",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			root := fixture(t)
			testrepo.Write(t, root, "note.txt", content+"\n")
			out, err := newEnv(t, "").run(root)
			if err == nil || !strings.Contains(out, "file note.txt:1:") {
				t.Fatalf("want a match: %v\n%s", err, out)
			}
		})
	}
}

func TestIdentityScan(t *testing.T) {
	// A fixture identity on an example domain passes.
	root := fixture(t)
	if out, err := newEnv(t, private).run(root); err != nil {
		t.Fatalf("test identity should pass: %v\n%s", err, out)
	}

	t.Run("private word in a name", func(t *testing.T) {
		root := fixture(t)
		testrepo.GitAs(t, root, "Pat "+fakeWord, "pat@example.com", "commit", "--allow-empty", "-qm", "Empty")
		newEnv(t, private).wantMatch(root, "identity sha256:", "private pattern line 2")
	})
	t.Run("a stray work address", func(t *testing.T) {
		root := fixture(t)
		testrepo.GitAs(t, root, "Pat Example", "pat@"+strayDomain, "commit", "--allow-empty", "-qm", "Empty")
		newEnv(t, "").wantMatch(root, "identity sha256:", "matched")
	})
	t.Run("a tagger", func(t *testing.T) {
		root := fixture(t)
		testrepo.GitAs(t, root, "Pat "+fakeWord, "pat@example.com", "tag", "-a", "v0.0.1", "-m", "Release")
		newEnv(t, private).wantMatch(root, "identity sha256:", "private pattern line 2")
	})
	t.Run("an accepted identity passes by digest", func(t *testing.T) {
		root := fixture(t)
		id := "Pat " + fakeWord + " <pat@" + strayDomain + ">"
		testrepo.GitAs(t, root, "Pat "+fakeWord, "pat@"+strayDomain, "commit", "--allow-empty", "-qm", "Empty")
		sum := sha256.Sum256([]byte(strings.ToLower(id)))
		allow := "allowlist.txt"
		testrepo.Write(t, root, allow, "identity\t"+hex.EncodeToString(sum[:])+"\t\"maintainer\"\n")
		e := newEnv(t, private)
		if out, err := e.run(root, "-allowlist", allow); err != nil {
			t.Fatalf("the allowlisted identity should pass: %v\n%s", err, out)
		}
		// Another identity is still caught.
		testrepo.GitAs(t, root, "Other "+fakeWord, "other@example.com", "commit", "--allow-empty", "-qm", "Again")
		e.wantMatch(root, "identity sha256:", "private pattern line 2")
	})
}
