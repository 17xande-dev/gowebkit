# Repository hygiene

`make hygiene` scans tracked file contents and names, reachable Git patches,
branch and tag names, commit and annotated-tag messages, and the author,
committer and tagger identities. Scan a full clone to cover all published
history. Diagnostics show the location and a rule label, never the source line
or the text of a private rule.

## Two kinds of rule

- **Generic rules** are in the source: mailbox addresses, IPv4 addresses and
  home-directory shapes for several systems. Example
  mailbox domains (`example.com`, `.test`, `.invalid`) and the IPv4 addresses
  `127.0.0.1`, `0.0.0.0` and the three RFC 5737 documentation blocks are
  built-in exceptions. This repository is public, so no rule in the source may
  name an organisation, person, host, application, place or provider.
- **Private rules** come from a file kept outside the repository: one
  case-insensitive Go regular expression per line, `#` for comments. The tool
  reads, in order, `-patterns FILE`, `$GOWEBKIT_HYGIENE_PATTERNS`, then
  `$XDG_CONFIG_HOME/gowebkit/hygiene-private.txt` (default
  `~/.config/gowebkit/hygiene-private.txt`) if it exists. A file that was named
  but is missing is an error. Without any list the tool prints
  `private pattern list not loaded: generic rules only` and runs the generic
  rules. `-require-private` (or `HYGIENE_REQUIRE_PRIVATE=1`) turns a missing or
  empty list into a failure; the maintainer sets it before a push. A private
  match is labelled `private pattern line N`.

Text is compared in a normal form: Unicode NFKC (full-width and mathematical
letters become plain ones), then decomposed with combining marks dropped (a
letter with an accent or a diaeresis is the plain letter), with format
characters removed (zero-width spaces, joiners, soft hyphens, bidirectional
controls), and with the common look-alike Cyrillic and Greek letters replaced by
Latin ones (a small table in the source, not the full Unicode set). A line that
holds a percent escape (`%XX`), an HTML numeric entity (`&#NN;`, `&#xNN;`) or a
backslash-x escape for a printable ASCII character is also tried with those
escapes decoded. Private rules are also tried on the text split into identifier
tokens at `_`, `-` and case changes, so a word-bounded rule still sees
`my_word` and `myWord`.

Diagnostics for a file name or a reference name print it with the matched span
replaced by asterisks, so a public log does not repeat the word.

Not covered: encodings (base64, hex, ROT13), reversed or concatenated text,
words split by ordinary characters or a line continuation, digits for letters,
look-alikes outside the table, images, archives and other binary content. The
manual read in [the releasing guide](../../docs/releasing.md) covers what a
scan cannot.

## Identities

Every distinct author, committer and tagger `Name <email>` is checked with all
rules. One that fails is shown as `identity sha256:<digest>`, the SHA-256 of
the lower-cased identity. To accept an identity, add `identity`, that digest
and a quoted label to the allowlist, so the allowlist need not repeat the name.

## Allowlist

Use `allowlist.txt` only for intentional matches that cannot be reworded. Each
record has three **tab-separated** columns: `scope`, `location`, and a
double-quoted Go string containing the exact matched text (case-insensitive,
after normalisation). The scope is `file`, `commit`, `tag`, `ref` or
`identity`. The location is a repository-relative path for `file`, a full
object ID for `commit` (patch and message), the tag name for `tag`, the full
reference name for `ref`, and the digest for `identity` (which accepts the whole
identity; the third column is then only a label). There are no wildcards. Blank
lines and lines starting with `#` are ignored; use a comment to explain an
exception. The allowlist itself is scanned, so use a Go character escape such
as `\x61` for one letter of an excepted word.
