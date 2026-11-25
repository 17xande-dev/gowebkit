# Deploy to one host with Kamal

The SQLite app runs on one host. Its containers mount `myapp-data` at `/data`.
The image runs as UID/GID `65532`; Docker seeds an empty named volume with the
image directory's permissions. Keep `/data/app.db`, its WAL sidecars, locks,
and `/data/backups` on local disk. Never mount a network filesystem for SQLite.
Do not rename an existing deployment or its volume in place.

## First deploy

1. Prepare one Docker host reachable by SSH. In `config/deploy.yml`, set the
   SSH host, registry owner and image, and the public hostname. Set the builder
   architecture to match the host (`amd64` is the default).
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
   that header. Kamal Proxy v0.10.2 with `proxy.ssl: true` does: its source
   (`internal/cmd/deploy.go`, `internal/server/target.go`) does not forward the
   client's own forwarding headers and builds `X-Forwarded-For` from the
   connecting client's address alone.

   If something in front of the proxy **appends** instead of replacing (a
   tunnel or CDN, or `proxy.ssl: false`, where Kamal Proxy forwards incoming
   headers), a client can supply `X-Forwarded-For` and `forwarded` is
   spoofable. Then use the value that matches the path: `cloudflare` only if
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
5. Commit the application and deploy configuration, then run the following
   in a trusted shell:

   ```sh
   set -a
   . ./.env.production
   set +a
   make deploy
   ```

   `make deploy-check` runs before `kamal deploy`. It rejects placeholders and
   uncommitted tracked changes (Kamal builds from HEAD), then runs `make gate`.
   Run `kamal config` locally with your installed Kamal version: it renders
   the configuration without contacting a host, so it checks that the file
   parses and nothing more. Treat its output as sensitive. No check that ships
   with this template contacts a host or deploys. Rehearse the first deploy, a
   rollback and a restore on a disposable host before using it in production.
6. Check that `https://app.example/healthz` on your configured host returns
   `200 ok`. Check `kamal app logs` for a successful startup and, when
   backups are enabled, that a daily backup exists.

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
