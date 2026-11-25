# Upgrading an app

Nothing forces an existing app to change, and generated apps do not follow the
template. This page lists what the template does that an older app may not, in
an order that is worth doing it, each with the reason. Take the items one at a
time, each in its own commit with its tests; check [the changelog](../CHANGELOG.md)
for what changed in a release.

For any item, read the template's code at the version you are porting from:

```sh
GOWORK=off go mod download -json github.com/17xande-dev/gowebkit/template/sqlite@vX.Y.Z
```

and open the directory named by the `Dir` field (use `template/postgres` for
that variant). The command works outside any module.

## The checklist

1. **Shared modules at current tags, and no local copies of what they do.**
   A limiter, client-address parsing, request-forgery protection, cookie
   building, redirect checking, spreadsheet-cell escaping, the SQLite connection
   string and backups all live in modules. One fix then reaches every app.
2. **One configuration loader that validates everything before anything else
   happens.** It must refuse what the server would refuse (including the public
   base URL against the request-forgery origin rules) before any migration runs,
   and `-check-config` must refuse the same inputs. A test builds the whole
   server from every accepted configuration. Retired environment variables are
   ignored, never fatal.
3. **Request-forgery policy built before the handlers; a size cap in front of
   every parser.** No code may read a body ahead of the wrapper. Renew the
   token at sign-in, sign-out and password change, and test that the old token
   is then refused. Log the matched route pattern, not the raw path.
4. **Session cookie through the cookie module.** Switching to the prefixed name
   signs everyone out once: say so in the deploy note, and test that a bare
   legacy cookie is ignored.
5. **Sessions with an idle and an absolute limit.** Throttle the last-seen
   write, rewrite a last-seen value in the future, use only the application's
   clock, sweep rows that are idle-expired and absolute-expired, end a user's
   other sessions on password change, and run the session conformance tests
   against the real store.
6. **Sign-in that cannot be used to learn anything.** The per-address limit
   first, then the per-account one; a dummy password check for unknown
   accounts; the disabled flag checked after the password; one message for
   every failure; no email address in logs; rehash on login with a
   compare-and-swap; password hashing concurrency sized to the container's
   memory; the password-change form behind the same limit.
7. **Access and refusal logs carry the route pattern**, never a path that could
   contain a token.
8. **The database setup.** SQLite: the connection helper and its startup check, a
   migration lock with a bounded wait, goose on strict tables, scheduled
   backups. PostgreSQL: goose with a session-level lock, and the generated-code
   check in the gate.
9. **Every migration has a Down, and the deploy note carries the undo.**
   Rehearse on a copy; stop the old version first when it cannot live with
   the new schema. The template's `-migrate-down` flag runs one step with the
   new image before a rollback.
10. **Security headers and markup guards in tests.** A policy struct with a test
    that pins the exact header text; tests that no page contains inline
    `style=`, `on*=` handlers or (data-)`hx-on` attributes; a sweep that
    requests every protected route anonymously with its own method; one parsed
    template set per page.
11. **Server timeouts and a header size limit**, and a graceful-shutdown test
    that proves in-flight requests finish and new connections are refused.
12. **A gate that cannot pass by accident.** Format check that fails on
    unparseable files, vet, tests with the race detector and `-count=1`, the
    skipped-test count printed, database tests required (not skipped) in CI,
    the vulnerability scan last, and a test that `.env.example` and the
    variables the code reads agree.
13. **Random values where a test asserts text.** Fixed short strings hide
    escaping and collision bugs (a leading apostrophe in a spreadsheet cell, a
    two-character name that happens to match).

## Checking your work

After each item run your own gate. When you are done, generate the template
into a scratch directory, run the same gate there and compare behaviours you
changed. `make gate` in a generated app is the reference for what "green" means.
