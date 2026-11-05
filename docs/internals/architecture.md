# Architecture

The shape of the system and the constraints that cross its components. For a
single component, read the code first. If this page and the code disagree, the
code wins and this page is updated.

```
┌──────────────────────┐  HTTPS: SPA, /api, SSE, /rtc signaling  ┌──────────────────────────────┐
│  Browser             │◄───────────────────────────────────────►│  tide (one Go binary)        │
│  SvelteKit SPA       │                                         │  · serves embedded SPA       │
│  livekit-client      │  WebRTC media (UDP 7882, TCP 7881)      │  · OIDC or anonymous sessions│
│                      │◄───────────────────────────────────────►│  · token minting, lobby (SSE)│
└──────────────────────┘                                         │  · moderation, webhooks      │
                                                                 │  · SQLite or in-memory store │
                                                                 │  ┌────────────────────────┐  │
                                                                 │  │ media server (LiveKit, │  │
                                                                 │  │ in-process, loopback   │  │
                                                                 │  │ API on 127.0.0.1:7880) │  │
                                                                 │  └───────────┬────────────┘  │
                                                                 └───────▲──────┼─────┬─────────┘
                                                 signaling via /rtc,     │      │     │ presigned URLs,
                                                 media via 7881/7882     │      │     │ object management
                 only with recording ┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄│┄┄┄┄┄┄│┄┄┄┄┄│┄┄┄┄┄┄┄┄┄
                                                                 ┌───────┴────┐ │     ▼
                                                                 │ Recorder   │ │  ┌────────┐
                                                                 │ (Egress:   │─┼─►│ S3 /   │
                                                                 │ Chrome+GST)│ │  │ MinIO  │
                                                                 └───────┬────┘ │  └───▲────┘
                                                                         ▼      ▼      │
                                                                 ┌──────────────────┐  │
                                                                 │ Redis: jobs and  │  │
                                                                 │ the signal relay │  │
                                                                 └──────────────────┘  │
┌──────────────────────┐   WebSocket /moil/v1/connect  (opened by the machine)         │
│  Owner's machine     │──────────────────────────────► tide                           │
│  moil app + uv       │◄───── recording GET, transcript PUT (presigned) ──────────────┘
└──────────────────────┘
```

## Boundaries

- **The media plane is not ours.** LiveKit owns WebRTC, simulcast, reconnection
  and recording capture. It runs inside tide as a library, but tide only
  configures it and talks to it through the server SDK, as it would to an
  external server. tide owns _policy_: who gets a token, with which grants, and
  what happens around the meeting.
- **Lean to run.** A deployment without recording is one process. Recording
  adds the recorder (headless Chrome and GStreamer, which can't live in a Go
  binary), the object storage it writes to and the Redis the two coordinate
  over.
- **One instance.** SQLite holds durable state, on `:memory:` in anonymous
  mode. The lobby, rate limits and every meeting live in the process. Nothing
  in the design blocks Postgres and replicas later; nothing pays that cost now.
- **Heavy compute happens elsewhere.** Bots and transcript jobs run on
  computers tide doesn't own ([transcripts](transcripts.md),
  [bots](../user/bots.md)).

Reasons are in [decisions](decisions.md).

## Modes

Configuration decides what a deployment is, and no separate flag could
contradict it ([the table](../operations/deployment.md#choose-a-mode)). tide
refuses to start rather than guess, naming every problem at once.

**A feature that is off is absent, not hidden.** With recording or moil off,
tide starts none of that feature's background work and registers none of its
routes, which answer 404 like any unknown path. `/api/me` reports `recording`,
`moil` and `anonymous`, and the SPA follows them. The media webhook route stays
with recording off, because it also enforces bans and marks rooms active.

Turning a feature off deletes nothing. Rows stay, and deleting a room still queues
its recordings' files, to be removed once storage is back.

## The embedded media server

`internal/media` starts LiveKit's server from options, unless `TIDE_MEDIA_URL`
names an external one. What the embedding changes:

- **Signaling shares tide's origin.** LiveKit's API and signaling listen on
  127.0.0.1 only. tide forwards `/rtc` and the paths under it, so a deployment
  has one origin and one certificate, and the rest of that API (room service,
  egress) is never reachable through tide. The forwarding clears tide's
  deadlines once the connection upgrades, since a signaling socket lives as long
  as a meeting. It takes only GET without a body: a body nobody sends would hold
  a connection open forever.
- **A configured media address replaces the machine's own.** A discovered
  address is advertised _alongside_ the interface addresses. `TIDE_MEDIA_NODE_IP`
  _replaces_ them, because LiveKit can't add internal candidates to a fixed
  address. So the recorder and the speaker observer, which reach media like any
  client, need that address routed back to tide when it isn't local.
- **Keys exist only if something outside needs them.** With no recorder, tide
  generates the API key and secret per start and they never leave the process.
  The recorder joins rooms with them, so recording requires configured ones.
- **Redis exists only to reach the recorder.** The recorder speaks only Redis.
  Once the media server uses Redis its signal relay goes over it too, so that
  Redis is in the path of every meeting's signaling although it holds nothing
  durable. Two media servers on one Redis take each other for one cluster.
- **Webhooks** reach tide's own `/api/webhooks/media` over loopback, signed as an
  external server's would be.

**A restart ends every meeting.** Stopping tide stops the media server, which
tells every participant it is shutting down. Their clients disconnect rather
than retry, and the meeting page offers **Rejoin**. Without a recorder the keys
are new each start, so no earlier token would be accepted anyway. A recording
running across a restart is lost with the media server's memory, and the
reconciler fails its row, so it is never silent.

## Start and stop

`run` in `cmd/tide/main.go` and `httpapi.Serve` hold the order. New background
work has to fit into it:

- The listener binds before the media server starts, because the media server's
  webhooks need its port. tide serves only once the media server is up.
- A signal first stops new connections and gives requests in flight 10 seconds,
  ending lobby streams at once. Machines disconnect next, which ends their jobs
  without touching the jobs' rows. Speaker observers leave their rooms. tide
  waits for the reconcilers and job followers, then stops the media server,
  which ends every room. The database closes last, because all of the above may
  write to it. A second signal stops tide at once.
