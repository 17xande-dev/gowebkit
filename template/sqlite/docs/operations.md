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

To inspect database migrations without applying them, run
`/myapp -migrate-status`. Run `/myapp -migrate` to apply pending migrations
without starting the HTTP server.
`/myapp -migrate-down` reverses one applied migration, but will not undo the
baseline or create a missing database. If you need to go back past the
baseline, use a verified backup instead.

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
message to me" on the Users page; with no relay it says messages are discarded,
and the log line for a discarded message shows the subject and the number of
recipients, never an address. The test action is limited per client address. For a real relay choose mandatory STARTTLS or
implicit TLS; never send credentials over a plaintext connection. Microsoft
Graph is an alternative supported by the mailer library, not a configured
transport in this template.
