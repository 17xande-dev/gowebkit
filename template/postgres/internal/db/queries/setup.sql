-- name: SetupState :one
SELECT token_hash, consumed FROM setup WHERE id = 1;

-- name: IssueSetupToken :execrows
INSERT INTO setup (id, token_hash)
SELECT 1, sqlc.arg(token_hash)::bytea WHERE NOT EXISTS (SELECT 1 FROM users)
ON CONFLICT (id) DO NOTHING;

-- name: ConsumeSetupToken :execrows
UPDATE setup SET consumed = true
WHERE id = 1 AND token_hash = $1 AND NOT consumed AND NOT EXISTS (SELECT 1 FROM users);
