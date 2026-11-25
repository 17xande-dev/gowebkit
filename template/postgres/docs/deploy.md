# Deploy to one host with Kamal

This variant runs two containers on one Docker host: the app, deployed by
`kamal deploy`, and PostgreSQL, a Kamal **accessory** named `postgres` that is
reached from the app as `myapp-postgres:5432` on the Kamal Docker network. The
database port is not published on the host. Its data lives in the named volume
`myapp-postgres-data`, mounted at `/var/lib/postgresql` (the layout PostgreSQL 18
images expect). The app image has no data directory and no volume. Do not rename
an existing deployment or its volumes in place.

Because only the app and the database share the host, this is a single point of
failure, as any one-host setup is. If you need failover or provider-run backups,
replace the accessory with a managed PostgreSQL service: `DATABASE_URL` changes
and `accessories` can be deleted. Use a direct or session-mode connection, not
a transaction-mode pooler (PgBouncer-style endpoints, which many providers
offer alongside the direct one): the migration lock is a session-level advisory
lock and the driver caches prepared statements, and neither works through a
transaction-mode pooler, where the lock would silently protect nothing.

## Before you rely on it

Run `kamal config` locally with your installed Kamal version: it renders
`config/deploy.yml` without contacting a host, so it checks that the file parses
and that the accessory is well-formed, nothing more. Treat its output as
sensitive, because it can contain secret values. No check that ships with this
template contacts a host or deploys: `kamal setup`, `accessory boot`, `deploy`,
`rollback` and `app exec` are yours to try. Rehearse the first deploy, a
rollback and a restore on a disposable host before you rely on any of them.

## First deploy

1. Prepare one Docker host reachable by SSH. In `config/deploy.yml`, set the
   SSH host (in `servers.web.hosts` and `accessories.postgres.host`; keep them
   equal), the registry owner and image, and the public hostname. Set the
   builder architecture to match the host (`amd64` is the default). Run
   `make rename NAME=appname` first if you have not.
2. Point DNS at the ingress. The default lets Kamal Proxy terminate HTTPS on
   the host. For a private tunnel that terminates public HTTPS and reaches
   the proxy over the Docker network, set `proxy.ssl: false` and
   `proxy.run.publish: false`. Keep the public HTTPS `BASE_URL` and
   `COOKIE_SECURE: "true"` in either case.
3. `config/deploy.yml` ships `CLIENT_IP_SOURCE: forwarded`. Behind Kamal Proxy
   the socket peer of every request is the proxy container, so `remote` would
   put all clients in one rate-limit bucket for sign-in, setup and password
   change, and every failed-sign-in log line would show the proxy's address.
   `forwarded` takes the leftmost `X-Forwarded-For` value on the first line,
   and is only safe when the sole ingress **replaces** every incoming copy of
   that header. Kamal Proxy v0.10.2 with `proxy.ssl: true` does: it does not
   forward the client's own forwarding headers and builds `X-Forwarded-For`
   from the connecting client's address alone.

   If something in front of the proxy **appends** instead of replacing (a
   tunnel or CDN, or `proxy.ssl: false`, where Kamal Proxy forwards incoming
   headers), a client can supply `X-Forwarded-For` and `forwarded` is
   spoofable. Then use `cloudflare` only if Cloudflare is the sole ingress,
   sets `CF-Connecting-IP`, and no alternate path lets a client supply it; or
   keep `remote` and accept the shared bucket. After the first deploy, check
   the `client_ip` in a failed-sign-in log line. Revisit concurrent password
   work and the `512m` memory limit together if you change either.
4. Create `.env.production` with permissions `0600`; it is gitignored. Generate
   one URL-safe database password and use it twice:

   ```sh
   pw=$(openssl rand -hex 32)
   {
     printf 'KAMAL_REGISTRY_PASSWORD=%s\n' '<registry credential with package access>'
     printf 'POSTGRES_PASSWORD=%s\n' "$pw"
     printf 'DATABASE_URL=postgres://app:%s@myapp-postgres:5432/app?sslmode=disable\n' "$pw"
   } > .env.production
   unset pw
   ```

   `sslmode=disable` is acceptable only because the connection stays on the
   host's private Docker network. The password cannot be changed by editing
   `.env.production` afterwards: PostgreSQL stores it in the data volume on first
   start, so changing it later is an `ALTER USER` and a new `DATABASE_URL`.
   `.kamal/secrets` holds references to these names, never values. If your mail
   relay needs credentials, set the non-secret SMTP fields in `env.clear`, add
   `SMTP_PASSWORD` to `env.secret` and set it in `.env.production`.
5. Commit the application and deploy configuration, load the secrets in a
   trusted shell, and boot the database before the app:

   ```sh
   set -a
   . ./.env.production
   set +a
   kamal accessory boot postgres
   kamal accessory details postgres      # wait until the status is healthy
   make deploy
   ```

   `kamal setup` does the same in one command. The order matters: Kamal has no
   "wait for the database" setting, and the app exits if it cannot reach the
   database within ten seconds of starting, which fails the deploy's health check
   and leaves the previous version serving. Boot the accessory first, wait for the
   healthy status, then deploy; on a retry nothing is lost.

   `make deploy-check` runs before `kamal deploy`. It rejects placeholders and
   uncommitted tracked changes (Kamal builds from HEAD), then runs `make gate`
   (which starts the Compose development database for the tests). Run
   `kamal config` locally to check the rendered configuration; treat its output
   as sensitive.
6. Check that `https://app.example/healthz` on your configured host returns
   `200 ok` (it pings the database), and `kamal app logs` for a successful
   startup and the first-admin setup token.

Later `kamal deploy` runs replace only the app; the accessory keeps running. Use
`kamal accessory logs postgres` for the database's own log.

## Deploy a migration

Use a release note to record the exact Down SQL, what data it can discard, and
which image versions to use. Rehearse the deploy and rollback on a restored copy
of recent data (see [database](database.md)). **Take a fresh dump before the
deploy.**

Kamal starts the new container before stopping the old one. The new binary
migrates (under the advisory lock) before it accepts traffic, and each migration
is a transaction, so a failing migration leaves the old schema and the old
version still serving. But a migration that succeeds changes the schema under
the still-running old version until the swap completes: write migrations in two
steps (add, then remove in a later release) so both versions work, or, if the
old binary cannot safely read or write the new schema, use a maintenance window:
run `make deploy-check`, drain traffic, `kamal app stop`, take the final dump
and run `make deploy`. Expect downtime; the old image refuses to start against a
schema newer than it knows.

## Roll back

1. Identify the prior image in `kamal app containers` and the image that
   shipped the applied migrations. If the old version would keep writing
   while you change the schema, drain traffic and stop the app with
   `kamal app stop`. Take a fresh dump for forward recovery.
2. With the **newer** image, run one Down step per migration to undo, in a
   one-off container so the server stays stopped while the schema changes:

   ```sh
   kamal app exec --version "$new_version" "/myapp -migrate-status"
   kamal app exec --version "$new_version" "/myapp -migrate-down"
   kamal app exec --version "$new_version" "/myapp -migrate-status"
   ```

   `kamal app exec --reuse` targets a *running* container instead; it is not
   suitable for the stop-first procedure. If Down would destroy data you must
   keep, stop and use the rehearsed recovery plan instead. The command refuses
   to undo the baseline migration, whose Down SQL drops `setup`, `sessions` and
   `users`: go back past it with a verified dump.
3. Only after checking the schema, run `kamal rollback "$old_version"` and
   verify `/healthz` and application data before reopening traffic. If no
   schema migration was applied, skip the Down step. A failed container startup
   does not prove it failed before applying migrations.

## Back up and restore

See [database](database.md) for dumps, the restore check and restores against
the accessory. Recovering the whole host is a different job: the accessory's
data volume is only as safe as the disk it is on.
