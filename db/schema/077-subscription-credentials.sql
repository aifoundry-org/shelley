-- Local subscription credentials. Never replicate this table to the fleet.
-- A NULL token is a logout tombstone, including before a legacy import.
CREATE TABLE subscription_credentials (
    provider TEXT PRIMARY KEY NOT NULL,
    token TEXT
);

CREATE TABLE subscription_credential_store (
    singleton INTEGER PRIMARY KEY NOT NULL,
    store_id TEXT NOT NULL,
    legacy_imported INTEGER NOT NULL DEFAULT 0
);
