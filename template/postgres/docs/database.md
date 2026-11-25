# PostgreSQL database

The app needs a PostgreSQL 18 database it owns. Locally that is the `postgres`
service in `compose.yaml` (user, password and database `app`, published on
loopback only: development values, never reuse them). In a deployment it is the
Kamal accessory described in [deployment](deploy.md), reached through
`DATABASE_URL` or `DATABASE_URL_FILE`. A URL with a password must be URL-safe:
pgx splits the userinfo at the first `@`, so generate the password with
`openssl rand -hex 32` rather than choosing one with punctuation.

`make db-up` starts only that service and `make test` and `make gate` start it
if it is not running, then keep it running so the next run is fast and your
development data is kept. `make db-down` removes the container and its volume
(`docker compose down -v`) when you want a clean slate or to free the port.

## Choosing PostgreSQL or SQLite

The rule is the number of application instances that must write. One instance
on one host is what SQLite does well, with the simplest backups and nothing
extra to run. Choose PostgreSQL when more than one instance or host must write
to the same data, when other services need their own access to it, or when you
need managed point-in-time recovery or failover. Moving between the two later
means moving the data by hand: the schemas and the store are separate
implementations, so decide early.

## Schema, queries and migrations

Migrations are numbered SQL files in `internal/db/migrations/`, each with
`-- +goose Up` and `-- +goose Down` sections. Ids are `bigint GENERATED ALWAYS
AS IDENTITY`, timestamps are `timestamptz`, and email uniqueness is
`UNIQUE (lower(email))`. Every timestamp is written and compared using the Go
clock, never `now()`, so a predicate and the value it protects use one instant.

Queries live in `internal/db/queries/` and are compiled to typed Go in
`internal/db/gen/` by sqlc (version pinned in `db.mk`; installed on first use, `make sqlc-install` reinstalls it).
The workflow for a change is: add a migration, edit the queries, run
`make sqlc`, commit the generated code. `make sqlc-check` runs first in
`make gate` and fails when the checked-in code is stale. Test Up, Down, then Up
again on an isolated database.

Server startup applies pending migrations, as do `make migrate` and
the binary's `-migrate` flag. `make migrate-status` reads the migrations shipped in this
build without creating goose's version table. A binary refuses a database
whose schema is newer than it knows, and refuses a database that has tables
but no migration history. The rollback command undoes one step, never the
baseline, and never creates history on an empty database.

### Migration lock

Migrations run under a session-level advisory lock, taken before the first
query and held through the check and the migration, with a wait of at most one
minute. Two processes that boot together (a deploy overlap, a scaled-out
start) queue instead of racing to create the same tables. If the holder
crashes, PostgreSQL ends its session and releases the lock; there is no lock
file to delete. Each migration also runs in a transaction, so a failed one
leaves the previous schema in place (a statement that cannot run in a
transaction, such as `CREATE INDEX CONCURRENTLY`, needs `-- +goose NO TRANSACTION`
and a hand-written recovery plan).

### Row locks that guard invariants

The default `READ COMMITTED` isolation does not make "count, then write" safe,
so the two places where concurrent requests could break a rule take row locks:

- **The last enabled admin.** Changing a role or disabling a user first locks
  every enabled admin row (`SELECT ... FOR UPDATE`, in id order), then runs one
  guarded statement that counts the remaining admins in its `WHERE`. Without the
  lock, two simultaneous demotions would each see two admins and together remove
  both.
- **Password reset versus sign-in.** Creating a session inserts it with
  `SELECT ... FROM users ... FOR SHARE`, conditioned on the user being enabled
  and the password hash still being the one that was verified. A reset, a
  password change or a disable updates that user row, so it waits for an
  in-flight sign-in, and a sign-in that starts after it fails the condition.
  A session can never outlive the revocation that was just committed.

Both are covered by tests that hold a lock in one transaction and prove the
other call blocks.

## Recovering a lost setup token

[Operations](operations.md) describes when this applies: no account exists and
the token is lost or spent. Stop the app, then run `DELETE FROM setup;` with
`make psql` (development) or, on a host,
`docker exec -i myapp-postgres psql -U app -d app`. Do not drop the volume: the
statement is enough. The next boot issues a new token.

## Back up

The application makes no backups of its own on PostgreSQL. Use `pg_dump` in
custom format, which is compressed and restorable selectively.

Development database, through Compose (`make backup` also checks that
`pg_restore` can read the result):

```sh
make backup                       # writes data/backups/app-<UTC time>.dump
make backup BACKUP_FILE=/safe/place/app.dump
```

Deployed accessory, from a trusted machine with SSH access to the host
(the accessory container is named after `service:` in `config/deploy.yml`, followed by `-postgres`; set it once, and use your own host):

```sh
umask 077
db_container=SERVICE-postgres
ssh server.example docker exec "$db_container" pg_dump -U app -d app --format=custom > app.dump.partial
pg_restore --list app.dump.partial > /dev/null && mv app.dump.partial app.dump
```

The dump holds every account and password hash: keep it private, encrypt it
before copying off the host, and never commit it. Take a fresh dump before every
migration deploy, not just the latest scheduled one. A dump on the same disk as
the database does not protect against losing the disk. Schedule dumps with your
host's tools (a cron job running the command above), and keep off-host copies.

## Verify a backup by restoring it

A dump you have never restored is a hope. Restore into a scratch database and
look at it (for the development database, from the repository root):

```sh
docker compose exec -T postgres createdb -U app app_verify
docker compose exec -T postgres pg_restore -U app -d app_verify --no-owner --exit-on-error < app.dump
docker compose exec -T postgres psql -U app -d app_verify -tc 'SELECT count(*) FROM users'
docker compose exec -T postgres psql -U app -d app_verify -tc 'SELECT max(version_id) FROM goose_db_version'
docker compose exec -T postgres dropdb -U app app_verify
```

The counts must match what you expect, and the migration version must be one
your image knows. On the host, run the same against the accessory with
`docker exec -i "$db_container" ...`.

## Restore

1. Stop every process that writes to the database (`kamal app stop`, then check
   `docker ps` on the host). Verify the dump as above and confirm its migration
   version is not newer than the image you will start.
2. Take a dump of the current database first, even if it is damaged; it may be
   needed for forward recovery.
3. Restore over the database in one transaction:
   `pg_restore -U app -d app --clean --if-exists --no-owner --single-transaction < app.dump`
   (for the development database, `make restore BACKUP_FILE=app.dump CONFIRM=1`).
4. Start the app, confirm `/healthz` returns `200 ok` and check application data
   before reopening traffic.

## Major version upgrades

A new PostgreSQL major version cannot start on the old data directory. Dump with
the old server, start a new accessory and restore into it, then point
`DATABASE_URL` at it. The new accessory needs a new image tag, a new volume name
and a new accessory **name** (a container is named `<service>-<accessory>`, so
the same name would collide with the old one), unless you stop the old one
first; its host name in `DATABASE_URL` changes with it. Rehearse on a copy
first, and keep the old volume until the new one is verified. Minor versions (the
`18.x` in the pinned tag) can be raised in place: change the tag in
`config/deploy.yml`, take a dump, run `kamal accessory reboot postgres`.

After a host reboot both containers come back by themselves (`unless-stopped`).
The app may restart a few times until PostgreSQL accepts connections; that is
harmless, because it refuses to start without the database and is started
again.
