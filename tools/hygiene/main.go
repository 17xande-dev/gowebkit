// Command hygiene checks publishable files and reachable history.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// The rules in this file are generic on purpose: nothing here may name an
// organisation, person, host, application or place, because this file is
// published. Rules that do are read from a private file outside the
// repository (see loadPrivate). Character classes keep the rules themselves
// from being flagged as examples.
const (
	emailPattern = `[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[a-z]{2,}`
	ipv4Pattern  = `\b(?:\d{1,3}\.){3}\d{1,3}\b`
)

// rule is one compiled expression and the label shown when it matches. A
// private rule is labelled by its line in the private file, never by its
// text, so the diagnostics do not repeat the list.
type rule struct {
	re      *regexp.Regexp
	pattern string
	label   string
	private bool
}

var genericRules = compileGeneric(
	emailPattern, ipv4Pattern,
	`/[h]ome/`,
	// Home-directory shapes of other systems. They are case-sensitive so a
	// lower-case URL route is not mistaken for a macOS path.
	`(?-i:/[U]sers/[A-Za-z][^/\s]*/)`, `[A-Za-z]:\\+[U]sers\\+`,
)

func compileGeneric(patterns ...string) []rule {
	var rules []rule
	for _, p := range patterns {
		rules = append(rules, rule{re: regexp.MustCompile("(?i)" + p), pattern: p, label: p})
	}
	return rules
}

// loadPrivate reads one case-insensitive regular expression per line; blank
// lines and lines starting with # are ignored. Errors name the line, never
// its content.
func loadPrivate(path string) ([]rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rules []rule
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		re, err := regexp.Compile("(?i)" + line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: not a valid regular expression", path, i+1)
		}
		rules = append(rules, rule{re: re, pattern: line, label: fmt.Sprintf("private pattern line %d", i+1), private: true})
	}
	return rules, nil
}

// resolvePrivate finds the private list: the -patterns flag, else
// $GOWEBKIT_HYGIENE_PATTERNS, else the per-user config file if it exists. A
// path that was asked for and is missing is an error; a missing default is
// only "not loaded".
func resolvePrivate(flagPath string) (path string, explicit bool, err error) {
	if flagPath != "" {
		return flagPath, true, nil
	}
	if env := os.Getenv("GOWEBKIT_HYGIENE_PATTERNS"); env != "" {
		return env, true, nil
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false, nil
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "gowebkit", "hygiene-private.txt"), false, nil
}

type exception struct{ scope, location, match string }

type audit struct {
	allow map[exception]bool
	rules []rule
	hits  int
}

func main() {
	dir := flag.String("root", ".", "Git repository root")
	allow := flag.String("allowlist", "tools/hygiene/allowlist.txt", "exception file relative to the repository root")
	patterns := flag.String("patterns", "", "private pattern file (default $GOWEBKIT_HYGIENE_PATTERNS, else $XDG_CONFIG_HOME/gowebkit/hygiene-private.txt if present)")
	require := flag.Bool("require-private", false, "fail when the private pattern list is not loaded (also enabled by HYGIENE_REQUIRE_PRIVATE=1)")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	switch env := os.Getenv("HYGIENE_REQUIRE_PRIVATE"); env {
	case "", "0":
	case "1":
		*require = true
	default:
		fmt.Fprintf(os.Stderr, "hygiene: HYGIENE_REQUIRE_PRIVATE must be 0, 1 or empty\n")
		os.Exit(2)
	}
	rules := append([]rule(nil), genericRules...)
	private, err := privateRules(*patterns, *require)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hygiene:", err)
		os.Exit(1)
	}
	rules = append(rules, private...)
	a, err := scan(*dir, *allow, rules)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hygiene:", err)
		os.Exit(1)
	}
	if a.hits > 0 {
		fmt.Fprintf(os.Stderr, "hygiene: %d matches\n", a.hits)
		os.Exit(1)
	}
	scope := "generic rules only"
	if len(private) > 0 {
		scope = fmt.Sprintf("%d private rules loaded", len(private))
	}
	fmt.Printf("hygiene: no matches in tracked files, file names, patches, commit and tag messages or identities (%s)\n", scope)
}

// privateRules loads the private list, or says clearly that it is absent.
func privateRules(flagPath string, require bool) ([]rule, error) {
	path, explicit, err := resolvePrivate(flagPath)
	if err != nil {
		return nil, err
	}
	var rules []rule
	if path != "" {
		rules, err = loadPrivate(path)
		if errors.Is(err, fs.ErrNotExist) && !explicit {
			err = nil
		} else if err != nil {
			return nil, fmt.Errorf("private pattern file: %w", err)
		}
	}
	if len(rules) == 0 {
		if require {
			return nil, errors.New("the private pattern list is required but not loaded or empty (-patterns, $GOWEBKIT_HYGIENE_PATTERNS, or ~/.config/gowebkit/hygiene-private.txt)")
		}
		fmt.Fprintln(os.Stderr, "hygiene: private pattern list not loaded: generic rules only")
	}
	return rules, nil
}

func scan(dir, allowPath string, rules []rule) (*audit, error) {
	if !fs.ValidPath(allowPath) || allowPath == "." {
		return nil, fmt.Errorf("invalid allowlist path %q", allowPath)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	allow, err := readAllowlist(root, allowPath)
	if err != nil {
		return nil, err
	}
	a := &audit{allow: allow, rules: rules}
	top, err := git(dir, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return nil, fmt.Errorf("find repository prefix: %w", err)
	}
	if string(top) != "\n" && len(top) != 0 {
		return nil, fmt.Errorf("-root must point to the Git repository root")
	}
	if err := a.files(root, dir); err != nil {
		return nil, err
	}
	if err := a.patches(dir); err != nil {
		return nil, err
	}
	if err := a.messages(dir); err != nil {
		return nil, err
	}
	if err := a.refs(dir); err != nil {
		return nil, err
	}
	if err := a.identities(dir); err != nil {
		return nil, err
	}
	return a, nil
}

func readAllowlist(root *os.Root, name string) (map[exception]bool, error) {
	allow := make(map[exception]bool)
	data, err := root.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return allow, nil
	}
	if err != nil {
		return nil, err
	}
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[1] == "" || (fields[0] != "file" && fields[0] != "commit" && fields[0] != "tag" && fields[0] != "ref" && fields[0] != "identity") {
			return nil, fmt.Errorf("%s:%d: expected scope, location and quoted match, separated by tabs", name, i+1)
		}
		match, err := strconv.Unquote(fields[2])
		if err != nil || match == "" || !strings.HasPrefix(fields[2], `"`) {
			return nil, fmt.Errorf("%s:%d: invalid quoted match", name, i+1)
		}
		if fields[0] == "identity" {
			match = "" // An identity is accepted whole; the third column is a label.
		}
		allow[exception{fields[0], fields[1], strings.ToLower(match)}] = true
	}
	return allow, nil
}

func (a *audit) files(root *os.Root, dir string) error {
	out, err := git(dir, "ls-files", "--cached", "--deduplicate", "-z").Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %w", err)
	}
	for name := range strings.SplitSeq(string(out), "\x00") {
		if name == "" {
			continue
		}
		a.line("file", name, " name", 1, name)
		info, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue // A deletion may not be staged yet; history is still checked.
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := root.Readlink(name)
			if err != nil {
				return err
			}
			a.line("file", name, "", 1, target)
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: tracked path is not a regular file or link", name)
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		readErr := readLines(f, func(n int, line string) {
			a.line("file", name, "", n, line)
		})
		if err := errors.Join(readErr, f.Close()); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (a *audit) patches(dir string) error {
	cmd := git(dir, "log", "-p", "--all", "--full-history", "--diff-merges=separate", "--root",
		"--format=%x1e%H", "--no-color", "--no-ext-diff", "--no-textconv", "--text", "--no-renames", "--")
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var commit string
	var lineNum int
	readErr := readLines(out, func(_ int, line string) {
		if hash, marker := strings.CutPrefix(line, "\x1e"); marker {
			commit = strings.TrimSpace(hash)
			lineNum = 0
		} else if commit != "" {
			lineNum++
			a.line("commit", commit, " patch", lineNum, line)
		}
	})
	if err := errors.Join(readErr, out.Close(), cmd.Wait()); err != nil {
		return fmt.Errorf("git log patches: %w", err)
	}
	return nil
}

func (a *audit) messages(dir string) error {
	out, err := git(dir, "log", "--all", "-z", "--format=%H%x00%B").Output()
	if err != nil {
		return fmt.Errorf("git log messages: %w", err)
	}
	records := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if len(out) != 0 && len(records)%2 != 0 {
		return fmt.Errorf("incomplete Git commit message record")
	}
	for i := 0; i+1 < len(records); i += 2 {
		a.message("commit", records[i], records[i+1])
	}
	out, err = git(dir, "for-each-ref", "--format=%(objecttype)%00%(refname:lstrip=2)%00%(contents)%00", "refs/tags/").Output()
	if err != nil {
		return fmt.Errorf("git tag messages: %w", err)
	}
	records = strings.Split(strings.TrimSuffix(string(out), "\x00\n"), "\x00")
	if len(out) != 0 && len(records)%3 != 0 {
		return fmt.Errorf("incomplete Git tag message record")
	}
	for i := 0; i+2 < len(records); i += 3 {
		if strings.TrimPrefix(records[i], "\n") == "tag" {
			a.message("tag", records[i+1], records[i+2])
		}
	}
	return nil
}

func (a *audit) message(scope, name, body string) {
	for i, line := range strings.Split(body, "\n") {
		a.line(scope, name, " message", i+1, line)
	}
}

func (a *audit) line(scope, name, part string, number int, raw string) {
	for _, m := range a.matches(raw) {
		if a.allow[exception{scope, name, m.text}] {
			continue
		}
		shown := name
		if part == " name" || scope == "ref" {
			shown = mask(name, m.text)
		}
		fmt.Printf("%s %s%s:%d: matched %s\n", scope, shown, part, number, m.label)
		a.hits++
	}
}

type match struct{ text, label string }

// mask hides the matched text in a file or ref name before it is printed, so a
// diagnostic does not repeat the word the rule exists to keep out. When the
// match is only visible after normalisation, the whole name is replaced by its
// SHA-256 prefix.
func mask(name, matched string) string {
	if i := strings.Index(strings.ToLower(name), matched); i >= 0 && matched != "" {
		return name[:i] + strings.Repeat("*", len(matched)) + name[i+len(matched):]
	}
	sum := sha256.Sum256([]byte(name))
	return "sha256:" + hex.EncodeToString(sum[:4])
}

// matches returns every non-exempt rule hit in one line of text. The text is
// compared in a normal form (see normalise) and, when it holds percent, HTML
// or backslash-x escapes, also in decoded form. Private rules are also tried on the text split into identifier tokens
// at '_', '-' and case changes, so a word-bounded rule still sees `my_word`
// and `myWord`.
func (a *audit) matches(raw string) []match {
	text := normalise(raw)
	// The decoded form is a second text, not a replacement: decoding could
	// hide a match as easily as it reveals one.
	texts := []string{text}
	if decoded := normalise(decode(raw)); decoded != text {
		texts = append(texts, decoded)
	}
	var found []match
	for _, r := range a.rules {
		var views []string
		for _, t := range texts {
			views = append(views, t)
			if r.private {
				views = append(views, tokenise(t))
			}
		}
		seen := make(map[string]bool)
		for _, view := range views {
			for _, m := range r.re.FindAllString(view, -1) {
				lower := strings.ToLower(m)
				if seen[lower] || (!r.private && allowedExample(r.pattern, m)) {
					continue
				}
				seen[lower] = true
				found = append(found, match{lower, r.label})
			}
		}
	}
	return found
}

// confusables maps look-alike Cyrillic and Greek letters to the Latin letter
// they imitate. It is a small table of the common ones, not the full Unicode
// skeleton: enough that swapping a letter by hand does not hide a word. Case is
// kept because some rules are case-sensitive.
var confusables = strings.NewReplacer(
	// Cyrillic, lower case.
	"\u0430", "a", "\u0435", "e", "\u043e", "o", "\u0440", "p", "\u0441", "c", "\u0443", "y",
	"\u0445", "x", "\u043a", "k", "\u0456", "i", "\u0458", "j", "\u0455", "s", "\u04bb", "h",
	"\u0501", "d", "\u051b", "q", "\u051d", "w", "\u0491", "r", "\u04cf", "l",
	// Cyrillic, upper case.
	"\u0410", "A", "\u0412", "B", "\u0415", "E", "\u041a", "K", "\u041c", "M", "\u041d", "H",
	"\u041e", "O", "\u0420", "P", "\u0421", "C", "\u0422", "T", "\u0423", "Y", "\u0425", "X",
	"\u0406", "I", "\u0408", "J", "\u0405", "S", "\u0500", "D", "\u051a", "Q", "\u051c", "W",
	// Greek, lower case.
	"\u03b1", "a", "\u03b5", "e", "\u03b9", "i", "\u03ba", "k", "\u03bf", "o", "\u03c1", "p",
	"\u03bd", "v", "\u03c5", "u", "\u03c7", "x", "\u03c4", "t", "\u03b7", "n",
	// Greek, upper case.
	"\u0391", "A", "\u0392", "B", "\u0395", "E", "\u0396", "Z", "\u0397", "H", "\u0399", "I",
	"\u039a", "K", "\u039c", "M", "\u039d", "N", "\u039f", "O", "\u03a1", "P", "\u03a4", "T",
	"\u03a5", "Y", "\u03a7", "X",
)

// normalise puts text in the form the rules see: compatibility-folded (NFKC,
// so full-width and mathematical letters become plain ones), decomposed with
// combining marks dropped (so "o" with a diaeresis is "o"), stripped of
// invisible format characters (zero-width spaces and joiners, bidirectional
// controls, soft hyphens) and with look-alike Cyrillic and Greek letters
// replaced by Latin ones.
func normalise(s string) string {
	return confusables.Replace(strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, norm.NFD.String(norm.NFKC.String(s))))
}

// escapes are the ways a letter is written without being typed: a percent
// escape, an HTML numeric entity and a backslash-x escape.
var escapes = regexp.MustCompile(`%[0-9A-Fa-f]{2}|&#[xX][0-9A-Fa-f]{1,6};|&#[0-9]{1,7};|\\x[0-9A-Fa-f]{2}`)

// decode replaces those escapes that stand for a printable ASCII character
// with the character, so a word whose first letter is written as an escape is
// still found. Anything else is left alone.
func decode(s string) string {
	return escapes.ReplaceAllStringFunc(s, func(e string) string {
		var digits string
		base := 10
		switch {
		case e[0] == '%':
			digits, base = e[1:], 16
		case strings.HasPrefix(e, `\x`):
			digits, base = e[2:], 16
		case e[2] == 'x' || e[2] == 'X':
			digits, base = e[3:len(e)-1], 16
		default:
			digits = e[2 : len(e)-1]
		}
		n, err := strconv.ParseUint(digits, base, 32)
		if err != nil || n < 0x20 || n > 0x7e {
			return e
		}
		return string(rune(n))
	})
}

// tokenise replaces '_' and '-' by spaces and puts a space at each case
// change: "myWord_app" becomes "my Word app", "HTTPServer" "HTTP Server".
func tokenise(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		if r == '_' || r == '-' {
			b.WriteByte(' ')
			continue
		}
		if i > 0 && unicode.IsUpper(r) {
			prev := rs[i-1]
			acronymEnd := unicode.IsUpper(prev) && i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || acronymEnd {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// refs checks branch and tag names.
func (a *audit) refs(dir string) error {
	out, err := git(dir, "for-each-ref", "--format=%(refname)").Output()
	if err != nil {
		return fmt.Errorf("git for-each-ref: %w", err)
	}
	for ref := range strings.SplitSeq(string(out), "\n") {
		if ref != "" {
			a.line("ref", ref, "", 1, ref)
		}
	}
	return nil
}

// identities checks the author, committer and annotated-tag tagger of every
// reachable object. An identity is named by the SHA-256 of its lower-cased
// "Name <email>" so the allowlist need not repeat the name or address.
func (a *audit) identities(dir string) error {
	seen := make(map[string]bool)
	out, err := git(dir, "log", "--all", "--format=%an%x1f%ae%x1f%cn%x1f%ce").Output()
	if err != nil {
		return fmt.Errorf("git log identities: %w", err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) == 4 {
			seen[f[0]+" <"+f[1]+">"] = true
			seen[f[2]+" <"+f[3]+">"] = true
		}
	}
	out, err = git(dir, "for-each-ref", "--format=%(taggername)%1f%(taggeremail)", "refs/tags/").Output()
	if err != nil {
		return fmt.Errorf("git tag identities: %w", err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		name, email, ok := strings.Cut(line, "\x1f")
		if ok && (name != "" || email != "") {
			seen[name+" <"+strings.Trim(email, "<>")+">"] = true
		}
	}
	for id := range seen {
		sum := sha256.Sum256([]byte(strings.ToLower(id)))
		digest := hex.EncodeToString(sum[:])
		if a.allow[exception{"identity", digest, ""}] {
			continue
		}
		for _, m := range a.matches(id) {
			fmt.Printf("identity sha256:%s: matched %s\n", digest, m.label)
			a.hits++
		}
	}
	return nil
}

func allowedExample(pattern, match string) bool {
	switch pattern {
	case emailPattern:
		_, domain, _ := strings.Cut(strings.ToLower(match), "@")
		return domain == "example.com" || strings.HasSuffix(domain, ".example") ||
			strings.HasSuffix(domain, ".test") || strings.HasSuffix(domain, ".invalid")
	case ipv4Pattern:
		ip, err := netip.ParseAddr(match)
		if err != nil {
			return false
		}
		return reservedAddr(ip)
	default:
		return false
	}
}

// reservedAddr reports whether an IPv4 address cannot name a public host:
// the unspecified address, loopback (127/8), private ranges (RFC 1918),
// link-local (169.254/16) and the documentation blocks (RFC 5737). The address
// part of a CIDR prefix such as 10.0.0.0/8 is judged the same way.
func reservedAddr(ip netip.Addr) bool {
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	a := ip.As4()
	switch [3]byte(a[:3]) {
	case [3]byte{192, 0, 2}, [3]byte{198, 51, 100}, [3]byte{203, 0, 113}:
		return true
	}
	return false
}

func readLines(r io.Reader, visit func(int, string)) error {
	b := bufio.NewReader(r)
	for n := 1; ; n++ {
		line, err := b.ReadString('\n')
		if line != "" {
			visit(n, strings.TrimSuffix(line, "\n"))
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func git(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-c", "log.showSignature=false"}, args...)...)
	cmd.Dir = dir
	return cmd
}
