# Changelog

Each entry says what changed in the templates and how to port it into an
existing app. Both templates are released together under one version.

## Unreleased

- CI removed; run `make check-all`.
- Documented a local Kamal 2.12.0 rehearsal of both apps generated from the
  published v0.1.0 modules. It covered `make deploy-check`, setup, `/healthz`,
  the one-time setup token, redeploy with no failed health polls, rollback,
  data persistence (SQLite volume and database file inode; PostgreSQL marker
  row), backups (SQLite file at boot; `pg_dump` over SSH), baseline
  `-migrate-down` refusal, and a failed deploy that kept the old version
  serving. Stopping the PostgreSQL accessory returned `503` from `/healthz`
  and caused the deploy to fail after 30 seconds. The rehearsal did not cover
  real DNS, TLS or Let's Encrypt, Kamal Proxy with `ssl: true`, a real remote
  host and registry, tunnel or CDN ingress, slow-disk PostgreSQL startup, or
  SMTP. For existing apps, use the updated [SQLite](template/sqlite/docs/deploy.md)
  or [PostgreSQL](template/postgres/docs/deploy.md) guide for first-deploy
  ordering, proxy trust, sign-in smoke tests and volume retention. No code
  changes are needed.

## v0.1.0 (2026-11-25)

The first release: two complete templates, `template/sqlite` (the default) and
`template/postgres`.

Each generated app contains:

- **Accounts.** A first-run setup token creates the first admin; sign-in and
  sign-out; roles `admin` and `member`; an admin page to create accounts with a
  temporary password, change a role, disable or enable, and reset a password;
  password change, which a temporary password forces; a `-reset-password`
  command.
- **Sessions and request safety.** Cookie-based sessions with an idle and an
  absolute limit, request-forgery protection, per-address and per-account
  sign-in limits, request size caps, and security headers with an exact
  content-security-policy test.
- **Operations.** One binary with `-check-config`, `-migrate`,
  `-migrate-status` and `-migrate-down`; JSON logs with request IDs; `/healthz`
  that checks the database; graceful shutdown; optional mail with an admin test
  message (local Mailpit in Compose).
- **Database.** SQLite: goose migrations under a lock, scheduled `VACUUM INTO`
  backups. PostgreSQL: goose under a session lock, `sqlc`-generated queries.
- **Deployment and checks.** Dockerfile, Compose development stack, Kamal
  configuration, a `make deploy-check` that refuses placeholders, and
  `make gate` (with a database service for PostgreSQL).

Repository tooling: variant sync, a generation test that runs the real `gonew`
against a commit, a hygiene scan, and a headless-browser first-run check.

Porting: there is nothing to port from a previous release. To bring an existing
app up to what these templates do, follow
[docs/upgrading-an-app.md](docs/upgrading-an-app.md).

Both variants were subsequently generated from the public module proxy and
passed their gates. See Unreleased for the later local Kamal rehearsal.
