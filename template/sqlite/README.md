# myapp

This Go + htmx web application was generated from gowebkit (github.com/17xande-dev/gowebkit); the first commit message records the template and version. It uses
server-rendered HTML and keeps application data in SQLite on one host.

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
enable an account, reset a password, check the newest backup date and send a
test message. The roles are `admin` and `member`; the last enabled admin cannot
be demoted or disabled. Anyone signed in can change their own password at
`/account/password`.

**Development only:** the Compose service uses plain HTTP and the published,
unauthenticated Mailpit settings in `compose.yaml`. Mailpit captures mail on
<http://localhost:8025>; it is not a production relay. Both ports bind to
loopback. `make down` stops the services without removing the data volume.

## Development

Run `make run` to serve natively with Go 1.27 or later. This uses a separate
local database from Compose. Stop Compose first if it already has port 8080.
Use `make check-config` to validate the environment without opening the
database. Run `make gate` (it installs `govulncheck` into your Go bin directory the first time) to check
formatting, run vet and uncached race tests, and scan reachable dependencies
for vulnerabilities. `make help` lists the available targets.

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
| `DB_PATH` | `data/app.db`; image: `/data/app.db` | Local file path, not a `file:` URI or `:memory:`. |
| `BACKUP_DIR` | `backups` beside `DB_PATH` | Private directory for scheduled backups. |
| `BACKUP_KEEP` | `14` | Non-negative integer retention count; `0` disables scheduled backups. |

For each secret, set the value or its file path, not both. Files must be
readable and nonempty after stripping one final LF or CRLF, and at most 64 KiB.

## Database choice

SQLite is a good default for one application host. Use Postgres when the app
must write from multiple hosts, other services need independent database access,
or managed point-in-time recovery becomes a requirement. Follow the
[database guide](docs/database.md) for migrations, backups and restores.

## Deploy and roll back

Run `make rename NAME=appname` once, before starting the app or creating its
volumes. This renames the non-Go defaults; it does not change the Go module
path or the display name. Fill in `config/deploy.yml`, export the secrets
from an ignored `.env.production`, commit the configuration and run
`make deploy`. If using GitHub Container Registry, add an
`org.opencontainers.image.source` label pointing to the actual source
repository to associate the image package with it.

See [deployment](docs/deploy.md) for Kamal, a stop-first deployment, and how
to roll back the schema with the newer image before rolling back the app.
See [operations](docs/operations.md) for logs and account recovery.

## Decisions still open

| Decision | Decide when |
| --- | --- |
| Mail relay | Choose a relay and sender identity when the app must deliver real mail. |
| Off-host backups | Choose an encrypted destination and retention policy before storing production data. |
| Postgres | Revisit the database choice if a second host or managed recovery is required. |

## Dependency rationale

Go's standard library handles HTTP and HTML; vendored htmx adds partial
updates without a frontend build. The pure-Go `modernc.org/sqlite` driver
works without CGO. `sqlitekit` sets and checks SQLite's connection policy,
serializes migrations and creates online backups; goose runs versioned Up
and Down SQL. The shared `websec`, `password`, `ratelimit` and `mailer`
packages provide cookie, session and request-forgery mechanisms, password
hashing, bounded rate limiting and mail delivery without an application
framework.
