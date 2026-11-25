# How the two templates are kept in step

For contributors. `template/sqlite` and `template/postgres` are two complete
apps. They differ only where the database forces it, and a script keeps
everything else identical.

## Ownership

`template/variants.txt` lists the paths each variant owns (see
[its format](../tools/variantsync/README.md)). Everything not listed is shared:
**it is written in `template/sqlite` and copied to `template/postgres`**, with
the module path in Go imports rewritten, which is the same rewrite `gonew`
makes. Never edit a shared file under `template/postgres`: the next sync
overwrites it and the local comparison fails until it does.

Typical variant-owned paths are `go.mod`, `internal/db/`, the store
implementation, the database tests' helpers, `compose.yaml`, `Dockerfile`, the
Kamal configuration, `.env.example`, the README, and the database and
deployment guides.

## Making a change

1. Shared file: edit it in `template/sqlite`, then sync.
2. Variant-owned file: edit it in the variant that needs it, and look at the
   other: if it has the equivalent, change it too.
3. A new variant-owned path: add it to `template/variants.txt` first, or the
   sync will treat it as shared (and delete it from `template/postgres`). A
   listed path must exist in both variants; if only one variant has it, write
   `postgres-only: path` (or `sqlite-only: path`). The check fails for a listed
   path that is missing from a variant that should have it.

```sh
(cd tools && GOWORK=off go run ./variantsync -root ..)         # sync
(cd tools && GOWORK=off go run ./variantsync -root .. -check)  # compare without writing
```

Then run `make check-all-fast` from the repository root. It compares the
variants again and runs both source and generated gates (generation uses
`HEAD`). To run only a source gate:

```sh
(cd template/sqlite && make gate)
(cd template/postgres && TEST_REQUIRE_DB=1 make gate)   # starts the Compose database
```

## What keeps them honest

- **The compiler.** Shared code calls the variant packages by the same names. A
  shared change that needs something one variant lacks fails that variant's
  build.
- **Shared behaviour tests.** The store contract tests and the session
  conformance tests are shared files that run against each real database.
- **Generation test.** `tools/gonewtest` builds a module zip of each template
  from a commit, serves it from a temporary file proxy, runs the real `gonew`
  and then the generated app's own gate. That proves what a user receives,
  including that executable bits do not survive `gonew`: the templates run
  everything through make targets.
- **Browser check.** `tools/e2e/run.sh` runs the first-run flow in headless
  Chromium against each generated app (see [the tools notes](../tools/README.md)).

## Dependencies

Each variant's `go.mod` pins released tags of the shared modules, never a
pseudo-version and never a `replace`. Third-party modules are pinned to a
release at least two weeks old. For local work against unreleased module
checkouts use an uncommitted `go.work` at the repository root.
