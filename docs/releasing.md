# Releasing

Both templates are released together: the same commit gets the same version on
each, as two tags. While the major version is 0, a breaking change to the
generated layout is a minor bump.

Status: every step has been run once, for v0.1.0: the checks and generation in
steps 2 and 3, then the tags, and `gonew` against the public module proxy for
both templates, followed by `make gate` in each generated app.

## 1. Bump the shared modules

For each kit module that has a new tag, in `template/sqlite`:

```sh
cd template/sqlite
GOWORK=off go get github.com/17xande-dev/websec@vX.Y.Z   # and the others
GOWORK=off go mod tidy
cd ../..
(cd tools && GOWORK=off go run ./variantsync -root ..)
```

`go.mod` and `go.sum` are variant-owned, so repeat the `go get` and `go mod tidy`
in `template/postgres`. Pin the newest tag of a third-party module that is at
least two weeks old.

The vendored htmx script follows the same rule, and it runs in every user's
browser. Check `npm view htmx.org time --json` and pin the newest version that
is at least 14 days old. Download the npm tarball, verify it against its
`dist.integrity`, replace `htmx.min.js` in `template/sqlite`, update the
SHA-256 and integrity lines in `internal/handler/static/README.md` (a test
compares the SHA-256), and sync. Re-check the pin's age again just before
tagging: a pin that was old enough when chosen is still old enough later, but a
newer pin chosen in a hurry is not.

## 2. Check

```sh
make tools-test
HYGIENE_REQUIRE_PRIVATE=1 make hygiene
(cd template/sqlite && make gate)
(cd template/postgres && TEST_REQUIRE_DB=1 make gate)
(cd tools && GOWORK=off go run ./variantsync -root .. -check)
```

`make hygiene` scans the files and their names, every patch in history, every
commit and tag message, branch and tag names, and the author, committer and
tagger identities. Fix anything it lists before going on, including in history.

The public tool holds only generic rules. The project-specific list lives in a
private file outside the repository (`~/.config/gowebkit/hygiene-private.txt`,
or the path in `GOWEBKIT_HYGIENE_PATTERNS`; see
[the tool's README](../tools/hygiene/README.md)). The maintainer runs
`HYGIENE_REQUIRE_PRIVATE=1 make hygiene` before every push: with that variable
a missing list is a failure, not a notice. CI reads the same list from the
repository secret `HYGIENE_PRIVATE_PATTERNS` and warns in the log when the
secret is absent (as it is for a fork).

## 3. Generate from the commit

Commit everything first: both tools read a commit, never the working tree.

```sh
for v in sqlite postgres; do
  (cd tools && GOWORK=off go run ./gonewtest -root .. -rev HEAD \
    -template template/$v -module example.com/app -docker -- make gate)
  bash tools/e2e/run.sh --variant $v --rev HEAD
done
```

`gonewtest` needs Docker for `-docker`; the PostgreSQL gate starts its Compose
database, so stop any other service using port 5432 first. The e2e runner needs
Node 22 and Chromium or Chrome.

Then look at the screenshots, which the runner writes to `E2E_OUT` (a new
temporary directory unless you set it): open the phone (390 px) and desktop
images of the users page and of at least two other pages, for both variants.
The run checks that nothing overflows and that table cells and short texts are
not squeezed into columns of single letters, but only a person sees whether a
page looks right. In CI the same images are the `e2e-screenshots-*` artefacts.

## 4. Changelog

Add an entry to [CHANGELOG.md](../CHANGELOG.md): what changed, and how to port
it into an existing app (point at the item in
[upgrading-an-app.md](upgrading-an-app.md) where one applies). The heading of an
unreleased version reads "(unreleased until the tags are pushed)", which stays
true on the tagged commit; once the tags are pushed, the next commit replaces
the parenthesis with the date.

## 5. Review

Read the diff since the last release as someone who has never seen the apps:
no names of products, organisations or people, no real hostnames, addresses or
paths. The hygiene scan catches the common patterns, not all of them.

## 6. Tag

Tags need the maintainer's explicit go-ahead. Never ask the module proxy for a
version before its tag is pushed: it caches a negative answer.

```sh
git tag -a template/sqlite/vX.Y.Z -m "Template vX.Y.Z"
git tag -a template/postgres/vX.Y.Z -m "Template vX.Y.Z"
git push origin template/sqlite/vX.Y.Z template/postgres/vX.Y.Z
```

## 7. Check the published module

Only after the tags are pushed:

```sh
scratch=$(mktemp -d) && cd "$scratch"
for v in sqlite postgres; do
  GOWORK=off gonew github.com/17xande-dev/gowebkit/template/$v@vX.Y.Z example.com/check-$v
done
```

Then run `make gate` in each result. If a command fails for a module that the
tag should have made available, wait a few minutes before retrying.

The weekly workflow `latest.yaml` runs both templates against the latest tags of
the shared modules; a red run means the next module release breaks the template
(bump or fix it), and it never changes the repository.
