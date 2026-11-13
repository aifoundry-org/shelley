# Local subscription credentials

Claude, ChatGPT (Codex), and Kimi Code subscription tokens live in the main
Shelley database. The official Connect ChatGPT accounts remain in their
existing tables. Local refresh tokens are never replicated: the fleet database
contains published access tokens, ownership metadata, and the fleet PSK.

## Upgrading from credentials.json

There is **no automatic import**, including on startup, `shelley models`, or
opening a fresh/test database. In **Manage Models**, use **Import existing
subscription credentials** once on the intended production database.

1. Stop any older Shelley processes that still read `credentials.json`.
   Import cannot lock out old versions that do not implement this protocol.
2. Request the import in Manage Models on the desired database.
3. Keep the unchanged `credentials.json` backup together with its adjacent
   `.sqlite-owner` and `.sqlite-import.lock` files. Do not delete those sidecars
   or copy the old token file to another installation for reuse; sign in again
   there instead.

Import durably reserves the canonical legacy file for the main database's
unique store ID and canonical path, then commits missing credentials and the
completion marker in one SQLite transaction. Existing database credentials and
logout tombstones win. After completion, even logout/restart cannot reimport.
A fresh/replaced database with a different store ID cannot reuse the
reservation. If a crash happens after reservation but before commit, retry from
the same database; a changed legacy file is rejected. Import does not claim
ownership over a live fleet owner, and does not contact a token endpoint.

An old pre-import database backup restored with the **same store ID at the same
path** is indistinguishable from crash recovery: it can reimport the old
credentials. Do not run such a restore while any copy of that token family is
active; prefer signing in afresh. Back up the database and import ownership
receipt together, and do not restore the pre-import state as a parallel server.

If the reserved DB was lost or moved before import completed, or its legacy
file changed, sign in again instead of removing the safety record. Never run
an old JSON-backed server alongside the new owner of that token family.

Both main and fleet databases, including WAL, SHM and rollback-journal files,
are private (0600), with permissions tightened before SQLite opens. SQLite
derives newly-created sidecar modes from the main file. **Main database backups
contain refresh tokens; fleet backups contain bearer access tokens and the fleet
PSK**, including historical credentials in the replication log. Protect backups,
snapshots, exports and copies like credentials. Changing permissions cannot
revoke access to copies made previously, or file descriptors another process
already opened.

## API

`GET /api/subscriptions` retains `providers` and adds
`legacy_import_available: boolean`; `credentials_path` is removed. Availability
checks metadata only. `POST /api/subscriptions/import-legacy` explicitly imports,
reconciles fleet state without a takeover claim, refreshes models, and returns
`{"status":"ok"}`. Repeating an already completed import is harmless. A file bound
to another database returns 409; read/parse/reservation failures return 500
with a plain-text explanation. Subscription handlers without an injected
credential store (including predictable-only) return 503. No response includes
access or refresh tokens.

`shelley models` opens no credential store without `-db`; with `-db` it reads
only that database's local tokens and never imports, joins a fleet or starts a
refresh loop. Predictable-only mode does not build subscription sources or
start the credential manager's refresh loop.
