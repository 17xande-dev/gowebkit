# Deploy to one host with Kamal

The SQLite app runs on one host. Its containers mount `myapp-data` at `/data`.
The image runs as UID/GID `65532`; Docker seeds an empty named volume with the
image directory's permissions. Keep `/data/app.db`, its WAL sidecars, locks,
and `/data/backups` on local disk. Never mount a network filesystem for SQLite.
Do not rename an existing deployment or its volume in place.

## First deploy

1. Run `make rename NAME=appname` first, before filling in deployment values
   or creating any volumes. Then prepare one Docker host reachable by SSH.
   In `config/deploy.yml`, set the SSH host, registry owner and image, and
   the public hostname. Set the builder architecture to match the host
   (`amd64` is the default).
2. Point DNS at the ingress. The default lets Kamal Proxy terminate HTTPS on
   the host. For a private tunnel that terminates public HTTPS and reaches
   the proxy over the Docker network, set `proxy.ssl: false` and
   `proxy.run.publish: false`. Keep the public HTTPS `BASE_URL` and
   `COOKIE_SECURE: "true"` in either case. The app port itself is not published.
3. `config/deploy.yml` ships `CLIENT_IP_SOURCE: forwarded`. Behind Kamal Proxy
   the socket peer of every request is the proxy container, so `remote` would
   put all clients in one rate-limit bucket for sign-in, setup and password
   change, and every failed-sign-in log line would show the proxy's address.
   `forwarded` takes the leftmost `X-Forwarded-For` value on the first line,
   and is only safe when the sole ingress **replaces** every incoming copy of
   that header. Review of Kamal Proxy v0.10.2's source shows that with
   `proxy.ssl: true` it does not forward the client's own forwarding headers
   and builds `X-Forwarded-For` from the connecting client's address alone.

   In one local Kamal 2.12.0 rehearsal of both variants, with `proxy.ssl: false`,
   Kamal Proxy appended to `X-Forwarded-For` and the app with `forwarded`
   logged the forged leftmost address. With `proxy.ssl: false` or any ingress
   that appends rather than rebuilds the header, use `cloudflare` only if
   Cloudflare is the sole ingress, sets `CF-Connecting-IP`, and no alternate
   path lets a client supply it; or keep `remote` and accept the shared
   bucket. An append chain from a controlled proxy network needs a trusted
   proxy range or header setting in the `ratelimit/clientip` package, which
   this template does not expose through the environment. Local runs
   (`make run`, `compose.yaml`) keep `remote`, which is correct there. After
   the first deploy, check the `client_ip` in a failed-sign-in log line.
   Revisit concurrent password work and the `512m` memory limit together if
   you change either.
4. Create `.env.production` with permissions `0600`. It is gitignored.
   Set `KAMAL_REGISTRY_PASSWORD` to a registry credential with package
   access. `.kamal/secrets` references exported values; never write real
   secrets there. If your relay requires credentials, configure the non-secret
   SMTP fields in `env.clear`, add `SMTP_PASSWORD` to `env.secret`, and set it
   in `.env.production`.
5. Once the app is renamed and the remaining values are filled in, commit
   the application and deploy configuration. Then run the check before
   contacting the host:

   ```sh
   set -a
   . ./.env.production
   set +a
   make deploy-check
   ```

   `make deploy-check` rejects placeholders (including those in comments) and
   uncommitted tracked changes (Kamal builds from HEAD), then runs `make gate`.
   Run `kamal config` locally with your installed Kamal version: it renders
   the configuration without contacting a host, so it checks that the file
   parses and nothing more. Treat its output as sensitive. After the checks
   pass, run `kamal setup` for the first deploy. For subsequent deploys,
   `make deploy` runs `make deploy-check` before `kamal deploy`.
6. Check that `https://app.example/healthz` on your configured host returns
   `200 ok`. Check `kamal app logs` for a successful startup, the one-time
   setup token on first boot and, when backups are enabled, that a daily
   backup exists.

### Smoke-test sign-in

Use the configured public scheme and host (`BASE_URL`). For a `curl` POST,
retain the CSRF cookie and form token from the preceding GET and send an
`Origin` matching `BASE_URL` and a `Referer` on that origin. For a disposable
test over plain HTTP directly to the proxy, the origin headers still need to
match the configured HTTPS origin; do not send real credentials over HTTP.
With the deployed `COOKIE_SECURE: "true"` setting, CSRF treats requests as
HTTPS even if the connection to the app is HTTP. The origin check falls back
to `Referer` if `Origin` is missing; `curl` does not send these by default.

## What was verified

Kamal 2.12.0 was exercised once on a disposable local Docker-in-Docker host
over SSH using the published v0.1.0 templates. For SQLite, the run covered
`make deploy-check`, `kamal setup`, `/healthz`, a setup token logged once,
redeploy with no failed health polls, rollback, persistence of the named
volume and database file inode, a backup file at boot, and `-migrate-down`
refusing to undo the baseline. A failed deploy kept the old version serving.

The rehearsal did not cover real DNS, TLS or Let's Encrypt, Kamal Proxy
with `ssl: true`, a real remote host and registry, tunnel or CDN ingress,
slow-disk PostgreSQL startup, or SMTP. Rehearse the first deploy, rollback
and restore on a disposable host that matches your intended setup.

## Deploy a migration

Use a release note to record the exact Down SQL, what data it can discard,
and which image versions to use. Rehearse the deploy and rollback on a
recent copy. Take a fresh backup before changing the schema, not just the
latest scheduled copy. To work with the production volume, inspect it on the
Docker host with `docker volume inspect myapp-data`. Run SQLite maintenance
with a trusted tool on that host or in a maintenance container mounting that
volume under the app's UID/GID. The service image itself has no shell or
SQLite CLI. See [database backups and restores](database.md).

Kamal usually starts the new container before stopping the old one. On the
same host, WAL lets readers and a writer share the file; the busy timeout
helps with write contention, and the migration lock serializes schema
operations. The lock does not stop traffic from an already-running binary.

If the old binary cannot safely read or write during or after a migration,
use a maintenance window. Run `make deploy-check` first, drain traffic, run
`kamal app stop`, and verify there are no other users of the volume before
taking the final backup and running `make deploy`. The new binary migrates
before it accepts traffic. Expect downtime; the old image cannot be
restarted against a schema version newer than it knows.

## Roll back

1. Identify the prior image in `kamal app containers` and the image that
   shipped the applied migrations. Drain traffic, stop the app with
   `kamal app stop`, and verify no other app containers or clients are still
   using the database. Save a fresh backup for forward recovery.
2. With the **newer** image, run one Down step per migration to undo. Use a
   one-off container so the server stays stopped while the schema changes:

   ```sh
   kamal app exec --version "$new_version" "/myapp -migrate-status"
   kamal app exec --version "$new_version" "/myapp -migrate-down"
   kamal app exec --version "$new_version" "/myapp -migrate-status"
   ```

   `kamal app exec --reuse "/myapp -migrate-down"` targets a *running* container
   instead. It is not suitable for this stop-first procedure: it would run
   alongside the server and its background database work. If Down would
   destroy data you must keep, pause and use the rehearsed recovery plan
   instead of proceeding automatically. The command refuses to undo the
   baseline migration, whose Down SQL drops `setup`, `sessions` and `users`.
3. Only after checking the schema, run `kamal rollback "$old_version"` and
   verify `/healthz` and application data before reopening traffic. If no
   schema migration was applied, skip the Down step. A failed container
   startup does not prove it failed before applying migrations.

## Remove a deployment

`kamal remove` removes the deployment and deletes the Kamal Proxy image.
It intentionally keeps named data volumes, including `myapp-data`. Deleting
a data volume is a separate, explicit step; verify your backups first.
