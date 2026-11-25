-- FOR SHARE makes the insert wait for a concurrent change to the user row
-- (password reset, change or disable) and then re-check the conditions. Without
-- it the insert can read the row from before that change commits, and the new
-- session would outlive the revocation the change just made.
-- name: CreateSession :execrows
INSERT INTO sessions (token_hash, user_id, created_at, last_seen)
SELECT sqlc.arg(token_hash)::bytea, users.id, sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz
FROM users
WHERE users.id = sqlc.arg(user_id) AND users.password_hash = sqlc.arg(password_hash) AND NOT users.disabled
FOR SHARE OF users;

-- name: LookupSession :one
SELECT users.id, users.name, users.email, users.role, users.password_hash, users.disabled,
  users.must_change_password, sessions.created_at, sessions.last_seen
FROM sessions JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = $1 AND NOT users.disabled;

-- The same now is bound as the new value and, minus the touch interval, as the
-- threshold. A last_seen in the future is rewritten, so a clock that was ahead
-- cannot keep a session alive.
-- name: TouchSession :exec
UPDATE sessions SET last_seen = sqlc.arg(now)::timestamptz
WHERE token_hash = sqlc.arg(token_hash)
  AND (last_seen <= sqlc.arg(touched_before)::timestamptz OR last_seen > sqlc.arg(now)::timestamptz);

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = $1;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteOtherUserSessions :exec
DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2;

-- name: SweepAbsolute :execrows
DELETE FROM sessions WHERE created_at <= sqlc.arg(absolute_before)::timestamptz;

-- GREATEST keeps idle time from starting before creation after a clock step back.
-- name: SweepAbsoluteOrIdle :execrows
DELETE FROM sessions
WHERE created_at <= sqlc.arg(absolute_before)::timestamptz
   OR GREATEST(created_at, last_seen) <= sqlc.arg(idle_before)::timestamptz;
