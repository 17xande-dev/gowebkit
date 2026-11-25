# gowebkit

**Status: v0.1.0. Experimental: `gonew`
is experimental, and neither deployment has been exercised on a real Kamal host
yet.**

A project template for small Go + htmx web applications that have accounts and
an admin area. Each template is a complete, runnable app (sign-in, sessions,
request-forgery protection, a first-run setup token, an admin page for users,
optional mail, a Docker and Kamal deployment, a CI workflow and its own tests),
built on these shared modules: `websec`, `ratelimit`, `password`, `sqlitekit`
and `mailer` under `github.com/17xande-dev`.

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
name `myapp`; `make rename NAME=<app>` replaces it, and `make deploy` refuses to
run until the deployment placeholders are gone. The generated README covers the
rest.

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

The two templates have their own gates (`make gate` in each directory; the
PostgreSQL one starts its Compose database first). CI runs all of it, plus a
browser run of the first-run flow for both generated apps.

## Contributing

Issues and pull requests are not monitored. The templates are MIT-0: fork them
freely and change what you need.

## Licence

MIT-0: see [LICENSE](LICENSE). The licence is copied into every generated app
and asks nothing of it.
