# myapp

This Go + htmx web application was generated from gowebkit (github.com/17xande-dev/gowebkit); the first commit message records the template and version (the
PostgreSQL variant). It uses server-rendered HTML and keeps application data in
a PostgreSQL 18 database.

## Quickstart

Install Docker with Compose v2 or later, GNU Make and Bash, then run:

```sh
make up
curl -fsS http://localhost:8080/healthz
make logs
```

Read the one-time token in the logs and open <http://localhost:8080/setup> to
create the first admin. Do not share the token or log output containing it.
After setup, the home page requires sign-in and `/setup` is no longer available.
Admins manage accounts at `/admin/users`: create an account with a temporary
password (the person must change it at first sign-in), change a role, disable or
enable an account, reset a password and send a test message. The roles are
`admin` and `member`; the last enabled admin cannot be demoted or disabled.
Anyone signed in can change their own password at `/account/password`.

**Development only:** the Compose stack uses plain HTTP, a PostgreSQL server
with the published credentials `app` / `app`, and the unauthenticated Mailpit
settings in `compose.yaml`. Mailpit captures mail on <http://localhost:8025>; it
is not a production relay. All published ports bind to loopback (set
`POSTGRES_PORT` if 5432 is taken). `make down` stops the services without
removing the database volume; `docker compose down -v` deletes it.

## Development

Run `make db-up` to start only the database (`make db-down` removes it and its
volume; `make gate` keeps it running), then `make run` to serve natively
with Go 1.27 or later; without `DATABASE_URL` the app uses that development
database. Stop Compose's server first if it already has port 8080. Use
`make check-config` to validate the environment without opening the database,
and `make psql` for a shell on the development database. Run
`make gate` locally (it installs `govulncheck` and `sqlc` into your Go bin directory the first time) to check
that the generated query code is current, formatting, vet, uncached race tests
against the development database (a missing database fails the run, it is never
skipped) and a scan of reachable dependencies. `make help` lists the targets.

Each test package works in schemas of its own in that one database and keeps a
connection pool open until its test ends, so on a machine with many CPUs two
`make gate` runs at once against one server can exhaust PostgreSQL's default
100 connections: run one at a time, or raise `max_connections`. A test binary
that was killed cannot drop its schema; a later run removes schemas older than
an hour, or drop one by hand with `DROP SCHEMA t_... CASCADE`.

Queries live in `internal/db/queries` and are compiled to typed Go by sqlc: edit
the SQL, run `make sqlc`, and commit the generated `internal/db/gen`.
`make sqlc-check` (part of the gate) fails when it is stale.

Templates, assets and migrations are embedded. Change them in source and
rebuild; there are no runtime template or static directory overrides.

## Configuration

See [`.env.example`](.env.example) for copyable examples. The app reads
exported environment variables; it does not load dotenv files itself. To use
a trusted local copy, run `set -a; . ./.env; set +a` in your shell before
starting the native app. Compose supplies its own development settings.

| Variable | Default | Format and purpose |
| --- | --- | --- |
| `PORT` | `8080` | Integer 1–65535; HTTP listen port. |
| `BASE_URL` | Local HTTP origin using `PORT` (no `:80`) | Browser-facing canonical origin, e.g. `https://app.example`, with no path, trailing slash, query, fragment, userinfo or explicit default port. Use lowercase scheme and host. |
| `COOKIE_SECURE` | `false` | Only lowercase `false` (local HTTP allowed) or `true` (secure cookies, HTTPS origin and HSTS). Empty is invalid. |
| `SITE_NAME` | `My App` | Non-blank page display name. |
| `SETUP_TOKEN` | Unset | Optional first-admin token, at least 32 characters without whitespace or control characters. |
| `SETUP_TOKEN_FILE` | Unset | Read the setup token from a regular file instead. |
| `SESSION_IDLE` | `2h` | Positive Go duration until idle expiry; cannot be disabled and must not exceed the absolute limit. |
| `SESSION_ABSOLUTE` | `12h` | Positive Go duration for the hard session limit. |
| `CLIENT_IP_SOURCE` | `remote` | Socket peer; right for local runs. The Kamal deploy sets `forwarded` in `config/deploy.yml`. Use `forwarded` only when the sole ingress replaces `X-Forwarded-For`; `cloudflare` only when Cloudflare is the sole ingress and sets `CF-Connecting-IP`. |
| `PASSWORD_CONCURRENCY` | `2` | Integer 1–64; size for available memory (about 64 MiB per operation). |
| `LOG_LEVEL` | `info` | Minimum severity: `debug`, `info`, `warn` or `error`. |
| `LOG_FORMAT` | `json` | `json` or `text` output. |
| `SHUTDOWN_TIMEOUT` | `15s` | Positive Go duration for draining HTTP requests. |
| `SMTP_HOST` | Unset | Hostname or IP without a port. Empty disables mail. |
| `SMTP_PORT` | `587` | Integer 1–65535; relay port. |
| `SMTP_USERNAME` | Unset | Non-blank relay login, paired with a password; requires TLS. |
| `SMTP_PASSWORD` | Unset | Relay password, paired with a username. |
| `SMTP_PASSWORD_FILE` | Unset | Read the relay password from a regular file instead. |
| `SMTP_TLS` | `starttls` | `starttls` (required upgrade), `tls` (implicit TLS) or `none` (no credentials allowed). |
| `EMAIL_FROM` | Unset | Valid sender mailbox; required with a relay host. |
| `DATABASE_URL` | Development database from `compose.yaml` | PostgreSQL connection URL; the value carries a password. A `pool_max_conns` below 2 is refused. |
| `DATABASE_URL_FILE` | Unset | Read the connection URL from a regular file instead. |

For each secret, set the value or its file path, not both. Files must be
readable and nonempty after stripping one final LF or CRLF, and at most 64 KiB.

## Database choice

This variant is for an app that needs PostgreSQL: it must write from more than
one host, other services need independent access to its data, or managed
point-in-time recovery is a requirement. Otherwise the SQLite variant is
simpler to run and back up. Follow the [database guide](docs/database.md) for
migrations, locking, backups and restores.

## Deploy and roll back

Run `make rename NAME=appname` once, before starting the app or creating its
volumes. This renames the non-Go defaults; it does not change the Go module
path or the display name. Fill in `config/deploy.yml`, export the secrets
from an ignored `.env.production`, commit the configuration and run
`make deploy`. The database is a Kamal accessory on the same host; the first
deploy boots it before the app. If using GitHub Container Registry, add an
`org.opencontainers.image.source` label pointing to the actual source
repository to associate the image package with it.

See [deployment](docs/deploy.md) for the accessory, the first deploy and how
to roll back the schema with the newer image before rolling back the app.
See [operations](docs/operations.md) for logs and account recovery.

## Decisions still open

| Decision | Decide when |
| --- | --- |
| Mail relay | Choose a relay and sender identity when the app must deliver real mail. |
| Off-host backups | Choose an encrypted destination and retention policy before storing production data. |
| Managed PostgreSQL | Replace the accessory with a managed service if you need provider-run failover or point-in-time recovery; `DATABASE_URL` changes, and it must be a direct or session-mode connection, not a transaction-mode pooler (the migration lock is a session-level advisory lock). |

## Dependency rationale

Go's standard library handles HTTP and HTML; vendored htmx adds partial
updates without a frontend build. `pgx` is the PostgreSQL driver and pool;
sqlc turns checked SQL into typed Go at build time (a pinned tool, not a
dependency of the binary); goose runs versioned Up and Down SQL under a
PostgreSQL advisory lock. The shared `websec`, `password`, `ratelimit` and
`mailer` packages provide cookie, session and request-forgery mechanisms,
password hashing, bounded rate limiting and mail delivery without an
application framework.
