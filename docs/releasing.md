# Releasing

Both templates are released together: the same commit gets the same version on
each, as two tags. While the major version is 0, a breaking change to the
generated layout is a minor bump.

For v0.1.0, both published templates were generated from the public module
proxy and passed `make gate` in the generated apps.

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

Commit the candidate first: the source gates read the worktree, but generation
and browser checks read the committed templates at `HEAD`.

```sh
make check-all
```

It stops at the first failure. It checks the tooling, scans for private
material, verifies variant sync, runs both source gates and both generated
gates, builds both generated images, and runs both headless first-run checks.
Docker with Compose v2 is required. PostgreSQL binds a free loopback port,
runs with database tests required, and is torn down afterwards. The browser
checks need Node 22 or later and Chromium or Chrome. For everyday work,
`make check-all-fast` skips image builds and browser checks only.

`make hygiene` scans the files and their names, every patch in history, every
commit and tag message, branch and tag names, and the author, committer and
tagger identities. Fix anything it lists before going on, including in history.

The public tool holds only generic rules. The project-specific list lives in a
private file outside the repository (`~/.config/gowebkit/hygiene-private.txt`,
or the path in `GOWEBKIT_HYGIENE_PATTERNS`; see
[the tool's README](../tools/hygiene/README.md)). The local checks require a
nonempty list when the file exists or a path was configured; otherwise
they say that only generic rules were scanned. The maintainer runs
`HYGIENE_REQUIRE_PRIVATE=1 make hygiene` before every push so an absent
list fails the check.

## 3. Review the browser screenshots

By default the full check removes its screenshots. To keep them, set
`E2E_OUT` to a directory outside the worktree when running step 2:

```sh
E2E_OUT=/tmp/gowebkit-screenshots make check-all
```

In each of the `sqlite` and `postgres` subdirectories, open the phone (390 px)
and desktop images of the users page and of at least two other pages.
The run checks that nothing overflows and that table cells and short texts are
not squeezed into columns of single letters, but only a person sees whether a
page looks right.

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

## Check the latest kit modules manually

Run `make check-latest` to generate both apps from `HEAD` into throwaway
directories, update each app's kit modules to their latest tags, run
`go mod tidy`, and run both gates with real database tests. It needs Docker
with Compose v2 but does not build app images or run the browser checks. It
never changes the repository or its pinned versions. A failure may indicate
that a newer module release needs a template change before updating the pins.
