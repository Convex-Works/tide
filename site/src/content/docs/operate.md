# Operate

Goal: keep tide's data safe and upgrades boring.

## State

| Where                    | What                                                                         | Back up                         |
| ------------------------ | ---------------------------------------------------------------------------- | ------------------------------- |
| `/data/tide.db` (volume) | with sign-in: rooms, recording records, owners, paired machines, transcripts | yes                             |
| the bucket               | recording and transcript files                                               | yes, with the store's own tools |
| tide's memory            | meetings, lobby requests, rate limits; anonymous rooms                       | no                              |

An anonymous installation keeps nothing worth backing up.

## Back up the database

The database runs in WAL mode: never copy `tide.db` alone while tide runs. Use SQLite's online backup from a helper pod that mounts the `tide-data` volume (the tide image has no `sqlite3`):

```sh
sqlite3 /data/tide.db ".backup '/backup/tide.db'"
sqlite3 /backup/tide.db 'PRAGMA integrity_check;'   # must print: ok
```

Alternatives: stop tide, copy `tide.db`, start it; or a storage snapshot that captures the database and its `-wal` file atomically. Restore a backup on a schedule to prove it works. Backups hold names and email addresses: protect them like production data.

## Upgrade tide

Restarting tide restarts its media server, which ends every live meeting: browsers disconnect and offer **Rejoin**, and guests go through the lobby again. A recording running at the time fails. Upgrade when no meeting is running.

1. Back up the database.
2. Build and push the new image, then set its tag (or digest) in the overlay's `images` entry.
3. `kubectl apply -k deploy/overlays/production`. `Recreate` stops the old pod before the new one opens the database.
4. Wait for `kubectl -n tide rollout status deploy/tide` and check `/healthz`. Migrations run at startup, before tide listens.

On one server, replace the binary (or the image in `compose.yaml`) and restart the service.

tide stops within about 15 seconds of SIGTERM: it ends lobby streams, finishes requests for up to 10 seconds, stops its media server, then closes the database. The default 30-second grace period is enough.

If the installation uses [transcripts](/docs/transcripts) and was running a release from before the `TIDE_TRANSCRIPTS` switch, set `TIDE_TRANSCRIPTS=true` in the same upgrade.

## Upgrade from a release with a separate media server

Releases before this one ran the media server, redis and the recorder as their own Deployments. In this release `TIDE_OIDC_ISSUER` and `TIDE_S3_ENDPOINT` switch sign-in and recording on: an installation that lacks either loses that feature until it is set, and without the issuer tide doesn't open its database. An installation that sets `TIDE_MEDIA_URL` keeps using its own media server, unchanged.

To move onto tide's own media server, when no meeting is running:

1. Rebuild the overlay from the current base as in [Install](/docs/install): drop `TIDE_MEDIA_URL` and `TIDE_MEDIA_PUBLIC_URL`, set `TIDE_MEDIA_NODE_IP` only for `MEDIA_OPTION` `lb`, add `recorder-redis-password` to `media-secrets`, and move the media option to tide.
2. Delete the old media server first. It holds the host ports or load balancer address tide needs, and tide can't start beside it:

   ```sh
   kubectl -n tide delete deployment media redis recorder
   kubectl -n tide delete service media redis media-udp --ignore-not-found
   kubectl -n tide delete configmap media-config recorder-config
   ```

   For `lb`, reuse the old load balancer's static address for `tide-media`.

3. `kubectl apply -k deploy/overlays/production`, then remove the media server's hostname from the ingress and DNS.

## Roll back

Migrations only move forward. To roll back across a release that changed the database, restore the backup taken before the upgrade along with the old image.

## Upgrade the recorder

The recorder's image is pinned in `deploy/k8s/recording`, tested with the media server inside tide. Upgrade it only with a tide release, during a quiet period, and afterwards repeat checks 3 to 5 of [Verify](/docs/verify).

## When someone could not see or hear another participant

Ask them to open the browser console on the affected tab, **before reloading**, and run:

```js
tideDiagnostics();
```

It saves a JSON file on their machine describing what their browser received. Nothing is uploaded. It contains participants' names: treat it as personal data.
