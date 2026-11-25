# Choosing a database

Start with SQLite. Change only when one of the triggers below is true.

## The rule

The deciding number is how many application instances must write to the data,
not how much data there is. SQLite runs inside the app process, so reads cost
a function call, and with a write-ahead log it handles far more writes per
second than a small server-rendered app produces. A database of a few hundred
thousand rows is not a reason to switch.

What SQLite cannot do is be shared: the data is a file on one machine's local
disk, and a network filesystem makes that worse, not better.

## Triggers for PostgreSQL

| Trigger | Why SQLite cannot meet it |
| --- | --- |
| Two or more app instances write to the same data | The database is a local file; two containers on two hosts cannot share it. |
| Deploys that run old and new versions side by side on different hosts | Same reason: two processes, one file, on one machine only. |
| Another service, report or dashboard needs its own access | There is no server to connect to; they would have to open the file. |
| Managed point-in-time recovery or failover is a requirement | You get file-level backups; the app writes a consistent copy daily. |
| More than one region | No. |

If none applies, stay on SQLite and say so in the app's README together with
the trigger that would change it. The generated README has a "Decisions still
open" table for this.

## What differs in the two templates

Both templates have the same pages, routes, settings (apart from the database
ones) and tests of behaviour. The differences are the ones the database forces:

| | SQLite | PostgreSQL |
| --- | --- | --- |
| Database settings | `DB_PATH`, `BACKUP_DIR`, `BACKUP_KEEP` | `DATABASE_URL` or `DATABASE_URL_FILE` |
| Queries | hand-written methods on `database/sql` | `sqlc`-generated code, checked by the local `make gate` |
| Migrations | goose, run at startup under a lock file | goose, run under a PostgreSQL session lock |
| Backups | the app writes a daily copy with `VACUUM INTO` | none in the app: `pg_dump` through Compose or Kamal |
| Deployment | a named volume at `/data` | a Kamal accessory for the database |
| Tests need | nothing (temporary files) | a running PostgreSQL (`make test` starts the Compose one) |

A managed PostgreSQL service works with the PostgreSQL template when you give
it a direct or session-mode connection; a transaction-mode pooler does not
(the migration lock is a session-level advisory lock). Each generated app's
`docs/database.md` covers its engine in detail.

## Moving between them later

There is no switch. The schemas and the store are separate implementations, so
moving an existing app means generating the other template, porting your
changes into it, and copying the data across with your own script. Decide
early; if the answer is unclear, SQLite is the cheaper mistake.
