# gowebkit

**Status: v0.3.3 is the latest release.** `gonew` is experimental. See
[Deployment verification](#deployment-verification) for what has been
rehearsed with Kamal.

A project template for small Go + htmx web applications that have accounts and
an admin area. Each template is a complete, runnable app (sign-in, sessions,
request-forgery protection, a first-run setup token, an admin page for users,
optional mail, a durable job queue, a Docker and Kamal deployment, and its own tests),
built on these shared modules: `websec`, `ratelimit`, `password`, `sqlitekit`
`mailer` and `outbox` under `github.com/17xande-dev`.

Generated apps are forks from day one. They do not track the template; see
[Upgrading an app](docs/upgrading-an-app.md) and the [changelog](CHANGELOG.md)
for what changed between releases.

## Generate an app

Install [`gonew`](https://pkg.go.dev/golang.org/x/tools/cmd/gonew) (it is still
marked experimental) and have Go 1.27 or later, Docker with Compose v2, Bash
and make on your `PATH` (the Makefiles use Bash):

```sh
go install golang.org/x/tools/cmd/gonew@latest

# SQLite (the default)
gonew github.com/17xande-dev/gowebkit/template/sqlite example.com/myapp

# PostgreSQL
gonew github.com/17xande-dev/gowebkit/template/postgres example.com/myapp
```

Then, inside the new directory (the formatting check lists files with
`git ls-files`, so commit once first):

```sh
git init -q && git add -A && git commit -qm "Generate from gowebkit"   # add the template and the tag you used
make gate   # format check, vet, race tests, vulnerability scan (installs its own tools on first use)
make up     # the app and a local mail sink
make logs   # prints the one-time setup token; open /setup
```

`gonew` rewrites only the Go module path. The non-Go files keep the placeholder
name `myapp`. Run `make rename NAME=appname` first, then fill in the remaining
deployment values and commit the configuration before running `make deploy-check`.
The generated README covers the rest.

## Deployment verification

Both published v0.1.0 variants were generated from the public module proxy and
exercised once with Kamal 2.12.0 on a disposable local Docker-in-Docker host over
SSH. The rehearsal covered `make deploy-check`, setup, `/healthz`, the one-time
setup token, redeploy with no failed health polls, rollback, data persistence
(SQLite volume and database file inode; PostgreSQL marker row), backups (SQLite
file at boot; `pg_dump` over SSH), baseline `-migrate-down` refusal, and a failed
deploy that kept the old version serving. A stopped PostgreSQL accessory caused
`/healthz` to return `503` and the deploy to fail after 30 seconds.

Not verified: real DNS, TLS or Let's Encrypt, Kamal Proxy with `ssl: true`,
a real remote host and registry, tunnel or CDN ingress, slow-disk PostgreSQL
startup, and SMTP. See the [SQLite](template/sqlite/docs/deploy.md) and
[PostgreSQL](template/postgres/docs/deploy.md) deployment guides.

## SQLite or PostgreSQL

Use **SQLite** unless something in the list below is true. Use **PostgreSQL**
when more than one app instance must write to the data, when another service or
tool needs its own access to it, or when you need managed point-in-time
recovery. The number of instances decides, not the amount of data. Moving
between the two later means moving the data by hand, so decide early. The
[database guide](docs/choosing-a-database.md) has the reasoning.

## Layout of this repository

```
template/sqlite/     the default template (its own Go module)
template/postgres/   the PostgreSQL template (its own Go module)
template/variants.txt  which files differ between the two
tools/               repository tooling (an untagged module): variant sync,
                     generation test, hygiene scan, browser first-run check
docs/                guides, listed below
```

The repository root is not a Go module. Everything under `template/<name>/`
becomes the generated app; everything else stays here.

## Documentation

- [Choosing a database](docs/choosing-a-database.md)
- [Upgrading an app](docs/upgrading-an-app.md): bring an older app up to what
  the template does
- [How the two variants are kept in step](docs/variants.md)
- [Releasing](docs/releasing.md)
- [Repository tools](tools/README.md)

## Working on the repository

```sh
make tools-test   # Go tooling checks and offline browser startup tests
make hygiene      # scans files, history, messages and identities for private material
```

`make tools-test` needs Node 22 or newer for the offline tests (no browser or npm
packages needed). It also installs the pinned `govulncheck` into your Go bin
directory, as the templates' `make gate` does on first use.

The two templates have their own local gates (`make gate` in each directory;
the PostgreSQL one starts its Compose database first).

## Contributing

Issues and pull requests are not monitored. The templates are MIT-0: fork them
freely and change what you need.

From the repository root:

```sh
make check-all-fast  # everyday check; no image builds or browser checks
make check-all       # full local check before release
```

Both commands check the tools, run the hygiene scan and variant comparison,
then run `make gate` in the source templates and in apps generated from
`HEAD`. Commit template changes before checking what gets generated. Both
commands need Docker with Compose v2 for the temporary PostgreSQL instances;
they use free ports and remove the test instances. The full check also
builds both app images and runs both first-run flows in headless Chromium.
It needs Node 22 or later and Chromium or Chrome. See
[Releasing](docs/releasing.md) for the private-list and screenshot options
and the manual latest-module check.

## Licence

MIT-0: see [LICENSE](LICENSE). The licence is copied into every generated app
and asks nothing of it.
