-- name: UserByEmail :one
SELECT id, name, email, role, password_hash, disabled, must_change_password
FROM users WHERE lower(email) = lower(sqlc.arg(email)::text);

-- name: UserByID :one
SELECT id, name, email, role, password_hash, disabled, must_change_password
FROM users WHERE id = $1;

-- name: UserCount :one
SELECT count(*) FROM users;

-- name: EnabledAdminCount :one
SELECT count(*) FROM users WHERE role = 'admin' AND NOT disabled;

-- name: ListUsers :many
SELECT id, name, email, role, password_hash, disabled, must_change_password
FROM users ORDER BY id;

-- name: InsertUser :one
INSERT INTO users (name, email, role, password_hash, disabled, must_change_password)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT DO NOTHING
RETURNING id, name, email, role, password_hash, disabled, must_change_password;

-- Takes a row lock on every enabled admin, in id order, before a change that
-- could remove the last one. Under READ COMMITTED the count in the guarded statement
-- below is only a snapshot, so two concurrent changes could each see two
-- admins and together remove both. Whoever holds these locks decides alone.
-- name: LockEnabledAdmins :many
SELECT id FROM users WHERE role = 'admin' AND NOT disabled ORDER BY id FOR UPDATE;

-- name: SetRole :execrows
UPDATE users AS u SET role = sqlc.arg(new_role)::text WHERE u.id = sqlc.arg(user_id)
  AND (u.role <> 'admin' OR u.disabled OR sqlc.arg(new_role)::text = 'admin'
    OR (SELECT count(*) FROM users AS a WHERE a.role = 'admin' AND NOT a.disabled) > 1);

-- name: SetDisabled :execrows
UPDATE users AS u SET disabled = sqlc.arg(disabled)::boolean WHERE u.id = sqlc.arg(user_id)
  AND (NOT sqlc.arg(disabled)::boolean OR u.role <> 'admin' OR u.disabled
    OR (SELECT count(*) FROM users AS a WHERE a.role = 'admin' AND NOT a.disabled) > 1);

-- name: UserExists :one
SELECT EXISTS (SELECT 1 FROM users WHERE id = $1);

-- name: RehashPassword :execrows
UPDATE users SET password_hash = sqlc.arg(new_hash)
WHERE id = sqlc.arg(user_id) AND password_hash = sqlc.arg(old_hash) AND NOT disabled;

-- name: ChangePassword :execrows
UPDATE users SET password_hash = sqlc.arg(new_hash), must_change_password = false
WHERE id = sqlc.arg(user_id) AND password_hash = sqlc.arg(old_hash) AND NOT disabled;

-- name: ResetPassword :execrows
UPDATE users SET password_hash = $1, must_change_password = true WHERE id = $2;
