# SQLite database

The native server uses `data/app.db`. Compose and Kamal mount a named volume at
`/data`, with the database at `/data/app.db` and scheduled copies under
`/data/backups`. Keep the database, its `-wal` and `-shm` sidecars and lock files
in the same writable local directory. Never put it on a network filesystem.
The app creates new databases with mode `0600`. For an existing database,
verify that the file is private to the app user before serving traffic.

## Schema and migrations

Use `STRICT` tables, `INTEGER` IDs, and a `CHECK` constraint for booleans
stored as `0` or `1`. Store timestamps as fixed-width UTC text (Go layout
`2006-01-02T15:04:05.000000Z`) so that lexical comparison preserves time
order. Use the same Go clock for writing and comparing timestamps.

Add a numbered SQL file to `internal/db/migrations/` with both `-- +goose Up`
and `-- +goose Down` sections. Test Up, Down, then Up again on an isolated
database. Server startup applies pending migrations, as does `make migrate`.
`make migrate-status` reports the migrations in the current build without
creating a database or a migration version table. A binary refuses an
on-disk schema newer than it knows about.
The operator rollback command refuses to undo the baseline.

Startup checks foreign keys, WAL mode and the busy timeout. The migration
lock serializes schema checks and changes across processes on the same host,
with a wait of at most one minute. Do not delete a lock file as a remedy for
a slow migration: a process can still hold the lock on the old inode.

For disposable native data only, stop all local writers and readers, then
run `make recreate-db CONFIRM=1`. It deletes the default database and its
sidecars, then reapplies the migrations. It does not touch Compose volumes
or cached backups.

## Recovering a lost setup token

[Operations](operations.md) describes when this applies: no account exists and
the token is lost or spent. Stop the app, then either run `DELETE FROM setup;`
with `sqlite3 data/app.db`, or, as there are no accounts to lose, remove
`data/app.db` and its `-wal` and `-shm` files. The next boot issues a new token.

## Back up

The server checks at startup and hourly. It writes one `app-YYYY-MM-DD.db`
copy per UTC day with `VACUUM INTO` and keeps the newest 14 by default. Set
`BACKUP_KEEP=0` to turn off scheduled backups. These files are private to
the app user and can be copied once complete.
Backup runs take a separate lock file next to the database
(`app.db.backup-lock`; it is created once and never deleted) so an online
copy does not hold the schema migration lock. A failed run is retried within
the hour.

For a separate pre-deploy snapshot when `sqlite3` is available, run
`make backup-now` with `DB_PATH` pointing to the database. The target
uses `VACUUM INTO`; it stores a `manual-*.db` file in `BACKUP_DIR` (default:
`backups` beside the database). Run as the database owner and manage the
retention of these copies separately. See [deployment](deploy.md) for
working with the production volume.

Never `cp`, `docker cp` or archive the live database file as a backup:
recent commits can be in WAL rather than the main file. Keep encrypted
copies of completed backups off-host. A backup in the same volume protects
against some data mistakes, not against losing the disk. Rehearse restores
and check that off-host copies are usable.

## Restore

1. Stop **all** containers and other processes using the database. Verify the
   selected copy with `sqlite3 -readonly "$backup" 'PRAGMA quick_check;'`
   (it must print `ok`) and confirm its schema is compatible with the image
   you plan to start. Rehearse on a separate copy before replacing live data.
2. Move the current database and any `-wal` and `-shm` sidecars to a private
   quarantine directory. Keep them together in case recovery is needed. Do
   not move the lock files or pair old sidecars with a restored database.
3. Install the verified backup at `DB_PATH` with mode `0600` and ownership
   matching the app (UID/GID `65532` in the container). Keep the database
   directory writable by that account.
4. Start the chosen image, confirm `/healthz` returns `200 ok`, and verify
   application data before reopening traffic.
