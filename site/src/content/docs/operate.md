# Operate

Goal: keep tide's data safe and upgrades boring.

## State

| Where                    | What                                                           | Back up                         |
| ------------------------ | -------------------------------------------------------------- | ------------------------------- |
| `/data/tide.db` (volume) | rooms, recording records, owners, paired machines, transcripts | yes                             |
| the bucket               | recording and transcript files                                 | yes, with the store's own tools |
| tide's memory            | lobby requests, rate limits                                    | no                              |
| redis                    | coordination between media server and recorder                 | no                              |

## Back up the database

The database runs in WAL mode: never copy `tide.db` alone while tide runs. Use SQLite's online backup from a helper pod that mounts the `tide-data` volume (the tide image has no `sqlite3`):

```sh
sqlite3 /data/tide.db ".backup '/backup/tide.db'"
sqlite3 /backup/tide.db 'PRAGMA integrity_check;'   # must print: ok
```

Alternatives: stop tide, copy `tide.db`, start it; or a storage snapshot that captures the database and its `-wal` file atomically. Restore a backup on a schedule to prove it works. Backups hold names and email addresses: protect them like production data.

## Upgrade tide

1. Back up the database.
2. Build and push the new image, then set its tag (or digest) in the overlay's `images` entry.
3. `kubectl apply -k deploy/overlays/production`. `Recreate` stops the old pod before the new one opens the database.
4. Wait for `kubectl -n tide rollout status deploy/tide` and check `/healthz`. Migrations run at startup, before tide listens.

tide stops within about 15 seconds of SIGTERM: it ends lobby streams, finishes requests for up to 10 seconds, then closes the database. The default 30-second grace period is enough.

If the installation uses [transcripts](/docs/transcripts) and was running a release from before the `TIDE_TRANSCRIPTS` switch, set `TIDE_TRANSCRIPTS=true` in the same upgrade.

## Roll back

Migrations only move forward. To roll back across a release that changed the database, restore the backup taken before the upgrade along with the old image.

## Upgrade the media server and recorder

Upgrade them together, to versions tested as a pair, during a quiet period: restarts end active meetings and recordings. Afterwards repeat checks 3 to 5 of [Verify](/docs/verify).

## When someone could not see or hear another participant

Ask them to open the browser console on the affected tab, **before reloading**, and run:

```js
tideDiagnostics();
```

It saves a JSON file on their machine describing what their browser received. Nothing is uploaded. It contains participants' names: treat it as personal data.
