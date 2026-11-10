# Recording

Room-composite recording through LiveKit Egress. A recording's lifetime is bound
to the egress job, not to any participant's tab.

## How a recording runs

`POST /api/rooms/:slug/recording/start` checks that the caller manages the room,
then asks for a room composite with S3 output and our layout URL. The S3
destination and its worker-visible endpoint travel in every egress request.

The layout is a route of our own SPA, `/egress-template`, implementing LiveKit's
egress template contract, so recordings use tide's own tile design. In
development Egress loads it through Vite at `host.docker.internal`, which
`server.allowedHosts` must admit.

Egress webhooks drive the `recordings` table. In the room, everyone sees recording
state through room metadata, rendered by the [hairline](web.md#the-hairline).

**Webhooks can be lost.** LiveKit doesn't guarantee delivery, and a row stuck
active would block every future recording for that room and its deletion.
`RunReconciler` compares active rows against LiveKit's actual egress state, after
a grace period that lets the normal webhook win. A recording ends as `completed`
or `failed`, and a failure shows in the management UI: it is never silent.

## Who spoke when

Each recording has one observer (`internal/speakers`), so that its transcript can
name its speakers. With `TIDE_MOIL` off there is no observer, since nothing would
read its timeline.

The observer is tide in the room, with the observer role ([auth](auth.md#tokens)).
It connects once egress has started, in the background. LiveKit reports speakers
only among participants you subscribe to, so it subscribes to every microphone
and throws the audio away. It keeps spans per display name, since identities are
per connection.

The observer stops when egress ends, and when the reconciler heals or fails the
recording. A room closing only ends its connection, and what it saw waits for the
recording to end. If the recording completed, tide writes `speakers.json` beside
it **before** the row becomes `completed`, on both the webhook and reconciler
paths, because that is what lets a transcript job start.

```json
{"v": 1, "participants": [{"name": "Ada", "spans": [[12.4, 18.0], [31.2, 33.9]]}]}
```

Times are seconds from the start of the file, by the egress file's own
`StartedAt`, not `recordings.started_at`, which is earlier.

All of this is best effort, and failing costs that recording's transcript its
names and nothing else. Start never waits for the observer, and the recording
completes without the file. Nothing reads the file but transcript jobs.

## Removing files

A recording's files are everything in `store.Recording.ObjectKeys()`, so a new
file beside a recording goes there.

The `object_removals` table lists objects to remove, each from a due time.
Deleting a recording, or its room, deletes the rows and queues every file in one
transaction, then removes them at once. The reconciler retries any removal that
failed, so a deleted recording never keeps its files, even when S3 is briefly
down. Each pass heals lost webhooks first, then spends a bounded time removing,
and stops at the first file it can't reach S3 for. S3 out of reach never holds the
reconciler up.
