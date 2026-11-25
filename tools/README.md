# Repository tools

This untagged Go module is separate from the downloadable templates. Run
`make check-all` or `make check-all-fast` from the repository root for the
local checks, or `make tools-test` and `make hygiene` for just the tooling.

- [Variant sync](variantsync/README.md) compares shared files or syncs them
  from SQLite to Postgres.
- `gonewtest` packages a Git revision (not the worktree) into a temporary
  file proxy, runs the pinned `gonew`, checks the generated module, imports
  and `LICENSE`, then runs `make gate` by default. Pass a different
  command after `--` and add `-docker` to build the image too (it is tagged
  `gonewtest-*` and removed again).
  The temporary app is removed on exit; when it has a Compose file, `docker compose
  down -v --remove-orphans` runs in it afterwards (also after a failed command),
  so services the command started do not outlive it.
- `e2e/run.sh` generates an app from a commit (through `gonewtest`), starts its
  Compose stack on free ports and runs `e2e/firstrun.mjs`: setup, sign-in,
  the forced password change, the last-admin refusal and the mail test in
  headless Chromium, with zero console errors and CSP violations and no
  horizontal overflow at 390x844. It needs Node 22, Docker and Chromium or
  Chrome (`E2E_CHROME` names a binary), writes screenshots to `E2E_OUT`, and
  always removes what it started. `--app DIR` uses an existing generated app;
  `--url` with `--mailpit` checks a stack that is already running.

  ```sh
  bash tools/e2e/run.sh --variant sqlite    # or postgres; --rev REV, default HEAD
  ```

- [Hygiene](hygiene/README.md) checks tracked files and Git history for
  private or project-specific material.

```sh
(cd tools && GOWORK=off go run ./gonewtest -root .. -rev HEAD \
  -template template/sqlite -module example.com/app -- make gate)
```

The sole tools dependency, `golang.org/x/mod`, supplies module path validation,
`go.mod` parsing, and module zip rules. The zip excludes nested modules and
inherits the repository's `LICENSE` if the template has no `LICENSE` of its own.
The generation tests use a local template fixture; acquiring the pinned
`gonew` itself still requires a Go module proxy.
