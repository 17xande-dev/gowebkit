# Changelog

Each entry says what changed in the templates and how to port it into an
existing app. Both templates are released together under one version.

## Unreleased

Planned for v0.2.0:

- **Defaults and diagnostics.** Unset and empty settings both select the
  default; nonempty booleans still accept exactly `true` or `false`. Startup
  and `-check-config` now report all configuration problems together without
  echoing setting values or secret file paths.
- **PostgreSQL development fallback.** An empty or unset `DATABASE_URL`
  uses the local development URL unless `DATABASE_URL_FILE` supplies a URL.
  `-check-config` accepts that fallback; with `COOKIE_SECURE=true` the server
  logs a warning before trying to connect.
- **HTTPS requires Secure cookies.** Set `COOKIE_SECURE=true` for an HTTPS
  `BASE_URL`, even if TLS terminates at a proxy. An omitted or blank setting
  defaults to `false` and is refused for HTTPS. HTTP still requires `false`.
- **New dependency: `github.com/17xande-dev/env` v0.1.0.** Setup tokens, SMTP
  passwords and PostgreSQL connection strings are redacted when formatted.
  Secret files now strip all trailing CR and LF bytes, not just a final line
  ending, and are supported on Unix only.

Apps generated from v0.1.x keep working unchanged. To adopt these changes:

1. Run `go get github.com/17xande-dev/env@v0.1.0`. Port `internal/config` and
   its tests from the template for your database, retaining app-specific
   settings and checks. Read all keys, record validation failures with
   `Reader.Fail`, and check `Reader.Err` after validation and before any
   database work. Keep credentials as `env.Secret` until validation or use
   requires `Reveal`.
2. In `main.go`, pass `cfg.SMTP.MailerConfig()` to `mail.New` and
   `cfg.SetupToken.Reveal()` to `ensureSetupToken`. `Config.SMTP` is now
   `config.SMTP`; convert it with `MailerConfig()` wherever app code expects
   a `mailer.SMTPConfig`. Use the same conversion for the loader's
   `mailer.NewSMTPSender` validation, which needs no network connection.
   Port `db.LogStartupWarnings` from the matching `internal/db/db.go` and
   call it as `db.LogStartupWarnings(log, cfg)` before `open(ctx, cfg.DB)` in
   `runServer`.
3. For PostgreSQL, `config.DB.URL` is now an `env.Secret`, and `DB.String`
   and `DB.GoString` are removed. In `internal/db/db.go` and database tests,
   pass `cfg.ConnectionString()` to the driver instead of `cfg.URL`.
   Port `dbtest.ConfigWithURL`, use it for test fixtures that formerly used
   `config.DB{URL: ...}`, and have `dbtest.Config` pass its schema URL to it.
   In `dbtest.SetEnv`, put the string from `NewSchema` directly in
   `DATABASE_URL` and clear `DATABASE_URL_FILE`. Do not log the string returned
   by `ConnectionString()`.
4. Port the configuration parity and database startup tests in `main_test.go`
   and `database_test.go`. Update any callers or tests that match old
   configuration error text: the new format is one `KEY: problem` per line,
   and secret-setting conflicts now say `cannot be set together with`.
5. Update `.env.example`, the README's configuration reference and the
   operations guide; on PostgreSQL, also update the deployment guide. Run
   `go mod tidy`, `make check-config` with the intended deployment settings,
   and `make gate`.

## v0.1.1 (2026-11-25)

A patch release: no change to the generated apps' behaviour, the documentation
of the first deploy is fuller, and the generated apps no longer ship a hosted
CI workflow.

- **No hosted CI.** The generated apps no longer contain `.github/workflows/ci.yaml`;
  the repository's own checks run locally with `make check-all` (everything,
  including image builds and the headless browser run), `make check-all-fast`
  (no Docker build or browser) and `make check-latest` (newest kit module tags
  in a throwaway copy). To port this into an app generated from v0.1.0, delete
  `.github/workflows/ci.yaml` if you prefer local checks; keeping it is fine.
- **Goose aligned.** The PostgreSQL template now requires goose v3.30.1, the
  version the SQLite template already used. Port: `go get
  github.com/pressly/goose/v3@v3.30.1 && go mod tidy` in an app generated from
  the PostgreSQL template.
- **Deploy documentation.** Both templates document a local Kamal 2.12.0
  rehearsal of both apps generated from the published v0.1.0 modules. It
  covered `make deploy-check`, setup, `/healthz`, the one-time setup token,
  redeploy with no failed health polls, rollback, data persistence (SQLite
  volume and database file inode; PostgreSQL marker row), backups (SQLite file
  at boot; `pg_dump` over SSH), baseline `-migrate-down` refusal, and a failed
  deploy that kept the old version serving. Stopping the PostgreSQL accessory
  returned `503` from `/healthz` and caused the deploy to fail after 30
  seconds. With `proxy.ssl: false` the app logged a forged `X-Forwarded-For`
  address when `CLIENT_IP_SOURCE` was `forwarded`: use `forwarded` only behind a
  proxy that rebuilds the header. The rehearsal did not cover real DNS, TLS or
  Let's Encrypt, Kamal Proxy with `ssl: true`, a real remote host and
  registry, tunnel or CDN ingress, slow-disk PostgreSQL startup, or SMTP. For
  existing apps, use the updated [SQLite](template/sqlite/docs/deploy.md) or
  [PostgreSQL](template/postgres/docs/deploy.md) guide for first-deploy
  ordering (`make rename`, then fill in values, then `make deploy-check`),
  PostgreSQL readiness, proxy trust, sign-in smoke tests and volume retention.
- **Dependencies.** modernc.org/sqlite is now v1.63.0 in the SQLite template and
  pgx v5.13.0 in the PostgreSQL template. Both were under the project's
  two-week minimum release age at this release; the maintainer fast-tracked
  them for v0.1.1 (a one-time exception, the rule stands for later releases).
  goose v3.30.1 was 13 days old; it was already part of v0.1.0. Port: `go get
  modernc.org/sqlite@v1.63.0` (SQLite template) or `go get
  github.com/jackc/pgx/v5@v5.13.0` (PostgreSQL template), then `go mod tidy`.

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
