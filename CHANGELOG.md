# Changelog

Each entry says what changed in the templates and how to port it into an
existing app. Both templates are released together under one version.

## v0.3.3 (2026-11-28)

Both templates give Kamal an explicit 30-second container stop window so
HTTP draining, background work and database close can finish before Docker
forces the container to exit.

The SQLite template now uses the backup loop that sqlitekit v0.4.0 provides
instead of its own timer loop. The loop makes today's backup at startup,
re-checks hourly, retries a failure within the hour, bounds each run to five
minutes and is joined by `Stop` before the database closes.

To port this into an app generated from v0.3.2:

1. Add top-level `stop_timeout: 30` to `config/deploy.yml`. If you increased
   `SHUTDOWN_TIMEOUT`, allow at least another 15 seconds for the worker,
   maintenance, database close and a margin of at least 5 seconds.
2. SQLite only: run `go get github.com/17xande-dev/sqlitekit@v0.4.0` and
   `go mod tidy`.
3. SQLite only: in `internal/db/backup.go` delete `BackupSet.Run` and add
   `BackupSet.Start(ctx, log)`, which calls the settings' `Start` with
   `LockPath: cfg.DBPath + ".backup-lock"` and a `Report` that logs through
   the parent `ctx` (Report receives none). Delete `runBackups` from
   `cleanup.go`.
4. SQLite only: in `main.go` replace the backup goroutine, ticker and join
   with `loop, err := backups.Start(ctx, log)` and `defer loop.Stop()`
   (a nil set returns a nil loop; Stop on it does nothing). Keep the defer
   before the database closes.
5. SQLite only: deploy when today's backup already exists (the admin page
   shows its date), so not in the first hour after UTC midnight. During that
   one rolling deploy the old container still uses the old lock file and the
   new one uses `app.db.backup-lock`, so they do not take turns. Once the
   deploy has finished, delete the old `app.db.backups.migrate-lock` from the
   data volume (not before: the old container recreates it if it runs a
   backup first). The library refuses that name as a `LockPath`.
6. SQLite only: update the tests that called `Run` or `runBackups`: the loop
   has no tick seam, so test the startup backup and that Stop is joined
   before the database closes. Remove the `path/filepath` import from
   `cleanup.go` if nothing else there uses it.

PostgreSQL apps need only step 1: the PostgreSQL template makes no
application backups, and its only change is a no-op loop type that keeps the
shared startup code compiling.

With backups turned on, the SQLite template now refuses to start on a
platform without `flock`, because the library rejects a lock path there.
Linux and macOS are unaffected. Set `BACKUP_KEEP=0` to run without backups.

## v0.3.2 (2026-11-28)

Both templates depend on outbox v0.2.0. `-migrate-status` prints the queue's
migrations and applied timestamps using the library's `MigrationStatus` API.

To port this into an app generated from v0.3.1:

1. Run `go get github.com/17xande-dev/outbox@v0.2.0` and `go mod tidy`.
2. Delete the copied history reader in `internal/db/outbox_status.go`,
   including its hard-coded latest queue migration version. Keep the
   `MigrationStatus` result type used by the CLI.
3. In `internal/db/status.go`, call `sqlitestore.MigrationStatus(ctx, pool)`
   (SQLite) or `pgxstore.MigrationStatus(ctx, pool)` (PostgreSQL). Append the
   returned migration rows to the application's status, keeping their
   applied timestamps and the `outbox` prefix. On SQLite, use an empty
   in-memory database to list pending queue migrations if the file is absent.
   On PostgreSQL, read with UTC status sessions to preserve the CLI's
   display of stored timestamps.

## v0.3.1 (2026-11-27)

What changed:

- The sign-in page focuses the email field when it loads. After a failed
  attempt the page keeps the submitted email and focuses the password field
  instead. The password is never written back, and a wrong password and an
  unknown account render identical markup. The page uses the `autofocus`
  attribute only; there is no script.

To port this into an app generated from v0.3.0:

1. In `internal/handler/handler.go` add `Email string` to `pageData`, after
   `Next`.
2. In `internal/handler/auth.go`, in the `failure` closure of `login`, pass the
   submitted address back: `pageData{Message: signInFailed, Next: dest, Email: input}`.
   `input` is the raw form value, so the page shows exactly what was typed, and
   the template escapes it.
3. In `internal/handler/templates/pages/plain/login.html` replace the two
   inputs with:

   ```html
   <input type="email" name="email" autocomplete="username" value="{{.Email}}" required{{if not .Email}} autofocus{{end}}>
   <input type="password" name="password" autocomplete="current-password" required{{if .Email}} autofocus{{end}}>
   ```

4. Remove any other `autofocus` on that page. If a test asserted that the
   failed-attempt page does not contain the submitted email, change it to expect
   the escaped value in the email field. Compare the wrong-password and
   unknown-account pages after masking that value.

## v0.3.0 (2026-11-26)

What changed:

- The admin test-message button queues a `mail-test` job. The worker delivers
  it in the background, retries transient failures and parks permanent
  refusals as dead. Admins can see counts and retry dead jobs at `/admin/jobs`.
- Application migrations run before the queue's migrations.
  `-migrate-status` lists both histories, and `-migrate-down` continues to
  roll back application migrations only. `JOB_KEEP_DONE` controls how long
  done and discarded jobs are retained.

To port these changes into an app generated from v0.2.x:

1. In the app module run `go get github.com/17xande-dev/outbox@v0.1.0`.
   SQLite also uses `sqlitekit` v0.3.0. Port `internal/jobs` and the migration
   wiring from `internal/db` in the matching template; keep your application
   migrations and use the separate `outbox_goose_version` history.
   PostgreSQL apps must port `migrationLock` in `internal/db` with the rest:
   it now holds the application's lock on a connection of its own, outside the
   pool, because the queue's migration takes two pool connections (its lock
   and its schema changes). An app that keeps the old lock on a pool
   connection needs `pool_max_conns` of at least 3 to boot; with
   `pool_max_conns=2` its boot waits for a pool connection until the process
   is stopped. With the new lock the minimum stays 2, which `internal/config`
   refuses to go below. Startup now opens up to `pool_max_conns` + 1 database
   sessions, because that lock connection is outside the pool: a database role
   whose connection limit equals `pool_max_conns` fails at startup, so raise
   the limit by one.
2. Port the queue construction, worker start and joined shutdown from
   `main.go`, and the purge call in `cleanup.go`. Port `JOB_KEEP_DONE` with
   the env loader, `.env.example` and README entry, then run `go mod tidy`.
3. Replace the synchronous test-message handler with the enqueue handler.
   Port the queue page, Users link, protected retry route, fixed notices and
   route tests. Keep any application-specific jobs safe to repeat.
4. Run `make check-config` and `make gate`. Stop the old app and back up the
   database before applying the queue schema. Run `-migrate-status` after
   migrating to check both histories, and send a test message to check that
   the worker delivers it. Consult the operations guide for retries and
   queue schema rollbacks.

## v0.2.0 (2026-11-26)

What changed:

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
