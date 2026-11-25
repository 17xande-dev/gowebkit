-- +goose Up
CREATE TABLE users (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL,
    email text NOT NULL,
    role text NOT NULL CHECK (role IN ('admin', 'member')),
    password_hash text NOT NULL,
    disabled boolean NOT NULL DEFAULT false,
    must_change_password boolean NOT NULL DEFAULT false
);

-- Emails are compared case-insensitively; the application also stores them lower-cased.
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

CREATE TABLE sessions (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    user_id bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    last_seen timestamptz NOT NULL
);

CREATE INDEX sessions_user_id ON sessions (user_id);

-- One row at most: the first-run token, kept as a digest.
CREATE TABLE setup (
    id smallint PRIMARY KEY CHECK (id = 1),
    token_hash bytea NOT NULL CHECK (octet_length(token_hash) = 32),
    consumed boolean NOT NULL DEFAULT false
);

-- +goose Down
DROP TABLE setup;
DROP TABLE sessions;
DROP TABLE users;
