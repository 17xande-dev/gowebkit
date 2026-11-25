-- +goose Up
CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT NOT NULL COLLATE NOCASE UNIQUE,
    role TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    password_hash TEXT NOT NULL,
    disabled INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
    must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0, 1))
) STRICT;

CREATE TABLE sessions (
    token_hash TEXT NOT NULL PRIMARY KEY CHECK (length(token_hash) = 64),
    user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    last_seen TEXT NOT NULL
) STRICT;

CREATE INDEX sessions_user_id ON sessions (user_id);

CREATE TABLE setup (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token_hash TEXT NOT NULL CHECK (length(token_hash) = 64),
    consumed INTEGER NOT NULL DEFAULT 0 CHECK (consumed IN (0, 1))
) STRICT;

-- +goose Down
DROP TABLE setup;
DROP TABLE sessions;
DROP TABLE users;
