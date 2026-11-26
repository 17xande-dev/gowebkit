# Operations

## Check configuration and readiness

`make check-config` validates the same environment that server startup uses
without opening the database. In the container, run `/myapp -check-config`: the image's default command starts
the server, so name the binary and its flag together.
Unset and empty settings use their defaults. The command reports all
configuration problems together without printing input values or secret file
paths. For an HTTPS `BASE_URL`, set `COOKIE_SECURE=true` explicitly; for an
HTTP `BASE_URL`, use `false` or the default.
If `COOKIE_SECURE` contains an invalid value, validation uses `false` for
the remaining checks, so an HTTPS origin can produce a second problem under
`BASE_URL`. Correct `COOKIE_SECURE` and run the check again.

`/myapp -migrate-status` reads both migration histories without creating them.
Lines prefixed with `outbox` come from `outbox_goose_version`.
`/myapp -migrate` applies pending application migrations first, then outbox
migrations, without starting the server or worker.
`/myapp -migrate-down` reverses one application migration and refuses to undo
the application baseline. It does not create a database or touch queue tables,
job data or the outbox migration history. To reverse a queue schema change,
follow the queue release's rollback instructions and rehearse on a copy.

With PostgreSQL, startup opens up to `pool_max_conns` + 1 database sessions:
the migration lock uses one connection outside the pool. A database role whose
connection limit equals `pool_max_conns` therefore fails at startup; raise the
limit by one.

Use `/healthz` for readiness. It pings the database with a two-second deadline
and responds with `200 ok` or `503`; responses are not cached. Do not use a
health response alone as evidence that a backup can be restored. The server
logs the reason for a failed check without sending it to the client. The image
has no Dockerfile `HEALTHCHECK` (it is distroless, with no shell to run one);
under Kamal the proxy polls `/healthz` before it routes traffic to a new
container.

## Read logs

Use `make logs` for Compose, `kamal app logs` for a deploy, or stdout from a
native run. Logs are JSON by default; `LOG_FORMAT=text` is convenient at a
terminal. Look for startup errors first, then correlate requests using the
request ID. Access and request-forgery refusals log the route pattern instead
of the raw URL. Credential refusals log the client address and either the
account ID or `unknown account`, never the email.
Restrict access to operational logs, especially during first-admin setup.

## First administrator

Before the first account exists, open `/setup` with the one-time token. The
server logs a generated token at warning level only on the boot that issued it.
If you configured `SETUP_TOKEN` or `SETUP_TOKEN_FILE` before that boot, use the
configured value instead; it is not logged. Setting a different value later
does not replace an issued token. Once claimed, the token stays spent even if
all accounts are removed. Treat logs containing a generated token as secrets.

If the generated token is lost (the logs of that boot are gone), or the token is
spent while no accounts exist, nothing in the settings recovers it. With no
accounts to lose, stop the app, run `DELETE FROM setup;` against the database
with the database's own client (`sqlite3` or `psql`) and start it again: the
next boot issues a new token. Do this only while the database holds no accounts. A wrong token at
`/setup` is logged as `setup refused` with the client address, never the token.

Sessions stop working at the idle or absolute limit. The server removes
expired session rows hourly; a row awaiting cleanup does not grant access.

## Account recovery

`-reset-password EMAIL` sets a new password for one account. The server must
have created the database already; the command never creates one. It reads a
single line from stdin, or the first line of the file named by `-password-file`,
and applies the same 12-character policy as every other password write. Do not
pass the password as a command argument (which can be recorded in process
listings or shell history):

```bash
unset new_password
if read -rsp 'New password: ' new_password; then
    printf '\n' >&2
    printf '%s\n' "$new_password" | ./myapp -reset-password admin@example.com
fi
unset new_password
```

A password set this way is temporary: the account must choose a new one at its
next sign-in, and all of its sessions end at once. The command logs nothing
about the password. If the account is disabled, the password is still set and
its sessions still end, and the command says so: the person cannot sign in until
an admin enables the account from the Users page.

Use the target deployment's validated environment and database. In a
container, the binary is `/myapp`; use `docker exec -i` with an existing
container on the host to pipe stdin to it. Do not edit a password hash directly in the database.

## Sign-in throttling

Sign-in is limited twice: per client address (shared with setup and password
change) and per account. An account allows five attempts, then one a minute.
The trade-off is that anyone who knows an account's email address can keep that
account throttled by failing against it, and a restart clears the limits (they
are held in memory). Changing a password is limited per account the same way,
so a stolen session cannot guess the current password from many addresses.

## Mail

With no relay configured, the app does not deliver mail. Compose supplies a
local Mailpit sink. Admins verify a relay from the running app with "Send a test
message to me" on the Users page. The confirmation means the job was queued;
check the inbox for delivery. With no relay the page says messages are discarded,
and the log line for a discarded message shows the subject and the number of
recipients, never an address. The test action is limited per client address. For a real relay choose mandatory STARTTLS or
implicit TLS; never send credentials over a plaintext connection. Microsoft
Graph is an alternative supported by the mailer library, not a configured
transport in this template.

## Job queue

From **Users → View job queue** (`/admin/jobs`), administrators can see counts
by state and the 100 most recent dead jobs. Fix the underlying problem and
click **Retry** beside the job. This puts it back in the queue and records
the admin's account ID. If someone already retried it, the page says it is
no longer available to retry. The worker logs job ID, kind, attempt, outcome
and error class; it never logs job payloads or the text of mail errors.

The `mail-test` kind in `internal/jobs` stores the address from the time of
the request. SMTP 5xx refusals go dead on the first attempt. Other failures
retry with backoff and become dead after six attempts. The queue can run a
job more than once, for example after a process crash just after sending;
design new job handlers so repeated execution is safe. When work depends
on a database change, use `Enqueue` in that change's transaction and call
`Wake` after it commits. The test button has no related write, so it uses
`EnqueueNow`.

An hourly cleanup removes done and discarded jobs older than `JOB_KEEP_DONE`
(336 hours by default). Dead jobs are kept until handled. Jobs that succeeded
have their payload cleared; restrict access to the database and its backups
because unfinished jobs can still hold addresses.

To retire a kind, stop enqueueing it and deploy a replacement handler that
returns `outbox.Permanent` with a safe description. Wait until there are no
pending or running jobs of that kind, including delayed jobs. Discard the dead
jobs with the queue's `Discard` API, using an operator identifier, before
removing the registration. Without a registered handler, pending jobs of
that kind cannot run or be discarded.

For upgrades that change queue tables, stop all old instances and back up
the database before starting the new build or running `-migrate`. Give the
process manager enough time to drain HTTP and join the job worker before
forcing a stop. If killed during a job, the queue can recover the claim after
its lease expires and may repeat the effect.
