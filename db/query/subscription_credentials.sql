-- name: GetSubscriptionCredential :one
SELECT token FROM subscription_credentials WHERE provider = ?;

-- name: SaveSubscriptionCredential :exec
INSERT INTO subscription_credentials (provider, token) VALUES (?, ?)
ON CONFLICT(provider) DO UPDATE SET token = excluded.token;

-- name: DeleteSubscriptionCredential :exec
INSERT INTO subscription_credentials (provider, token) VALUES (?, NULL)
ON CONFLICT(provider) DO UPDATE SET token = NULL;

-- name: RefreshSubscriptionCredential :execrows
UPDATE subscription_credentials SET token = sqlc.arg(token)
WHERE provider = sqlc.arg(provider) AND token = sqlc.arg(previous);

-- name: ImportSubscriptionCredential :exec
INSERT INTO subscription_credentials (provider, token) VALUES (?, ?)
ON CONFLICT(provider) DO NOTHING;

-- name: InitSubscriptionCredentialStore :exec
INSERT INTO subscription_credential_store (singleton, store_id) VALUES (1, ?)
ON CONFLICT(singleton) DO NOTHING;

-- name: GetSubscriptionCredentialStore :one
SELECT store_id, legacy_imported FROM subscription_credential_store WHERE singleton = 1;

-- name: CompleteSubscriptionCredentialImport :exec
UPDATE subscription_credential_store SET legacy_imported = 1 WHERE singleton = 1;
