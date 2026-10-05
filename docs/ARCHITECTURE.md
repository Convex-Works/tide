# tide — Architecture

tide is a lean, self-hosted video meeting product. It does few things well:
meeting URLs, host auth (OIDC, or none at all — §4.1), a guest lobby,
mic/cam/screen controls, device selection, a participant list with moderation,
reconnection, ephemeral chat, and Zoom-like server-orchestrated recording with
recording management, including speaker-labelled transcripts made on the room
owner's own machine (§8.1).

Everything else is out of scope by design. Feature restraint is the product.

## 1. Principles

- **Lean to run.** tide is one executable. It runs the media server inside
  itself (§2.1), so a deployment without recording is that one process and
  nothing else. Recording adds exactly one more, the recorder (headless Chrome
  and GStreamer, which can't live in a Go binary), plus the object storage it
  writes to, and the Redis the two coordinate over. No Node runtime, and no
  Redis unless you record.
- **The media plane is not ours.** LiveKit owns WebRTC, simulcast, reconnection,
  and recording capture. It runs inside the tide binary as a library, but tide
  only configures it and talks to it through the server SDK, as it would to an
  external one. tide owns *policy*: who gets a token, with which grants, and
  what happens around the meeting.
- **Authorization is the token.** Every capability a client has (join, publish,
  screenshare, admin) is a grant inside a LiveKit JWT minted by the tide
  server. There is no client-asserted role anywhere.
- **One source of truth per type.** API types are defined once in Go and
  generated into TypeScript. CI fails on drift.
- **Frozen feature list.** New features must displace an existing one.
  Transcripts were admitted as part of recording management, on one
  condition that stays load-bearing: no model runs on the server. Heavy
  compute happens on machines the recording's owner paired (§8.1).

## 2. System architecture

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

| Component | What it is | What it owns |
|---|---|---|
| **tide** | Single Go binary: the SPA via `embed.FS` and LiveKit's SFU as a library | Auth, sessions, rooms, tokens, lobby, moderation API, recording lifecycle + management, webhooks, the moil service side (pairing, transcript jobs), and every media session |
| **Media server** | LiveKit's server (Apache 2.0) running inside tide (§2.1) | All media: SFU, simulcast, adaptive streaming, ICE, reconnection/resume |
| **Recorder** *(recording only)* | LiveKit's Egress worker, a separate process (headless Chrome + GStreamer) | Renders our composite layout page, encodes OGG audio or MP4 video, writes directly to S3 |
| **Redis** *(recording only)* | Redis 7, beside the recorder (§2.1), nothing durable in it | The job bus between the media server and the recorder, which also carries the media server's signal relay once configured |
| **MinIO (dev) / S3 (prod)** *(recording only)* | Object storage | Recording files |
| **Dex (dev only)** | OIDC identity provider | Host login in the dev stack; any OIDC provider in prod; none in anonymous mode |
| **Paired machines (optional)** | Hosts' own computers running the [moil](https://git.convex.works/ConvexWorks/moil) app | Transcribing their owner's recordings; not part of the deployment |

### 2.1 One binary: modes and the embedded media server

Configuration decides what a deployment is. Nothing is a separate flag that
could contradict it:

| Configuration | What tide is | Processes |
|---|---|---|
| no `TIDE_OIDC_ISSUER` | **Anonymous**: anyone creates rooms, rooms live in memory, no recording (§4.1) | tide |
| `TIDE_OIDC_ISSUER` | **Signed-in**: hosts sign in, rooms persist in SQLite, no recording | tide |
| `TIDE_OIDC_ISSUER` + `TIDE_S3_ENDPOINT` | **Signed-in with recording** (§8) | tide + recorder + Redis |
| … + `TIDE_TRANSCRIPTS=true` | **… with transcripts** on owners' machines (§8.1) | tide + recorder + Redis |

tide refuses to start rather than guess: S3 without sign-in, transcripts
without recording, or a recorder that would lack fixed keys or a Redis
password are configuration errors (§12).

**The media server runs inside tide** unless `TIDE_MEDIA_URL` names an
external one, which keeps the pre-1.0 four-process topology working
unchanged. Embedded, `internal/media` starts LiveKit's server from options,
not a YAML file:

- Its HTTP API and signaling listen on **127.0.0.1:7880** only
  (`TIDE_MEDIA_API_PORT`, for two tides on one host). tide's server SDK calls
  go there, exactly as they would to an external server.
- **Browsers reach signaling through tide**: tide forwards `/rtc` and
  everything under it (WebSocket upgrades included) to that loopback port, so
  the token's `ws_url` is tide's own origin (`wss://<host>`) and a deployment
  has one origin and one certificate. The forwarding clears tide's
  read/write deadlines, since a signaling socket lives as long as a meeting.
- **Media** is UDP 7882 (single-port mux) with ICE/TCP 7881 as the fallback,
  on every interface. The advertised address is `TIDE_MEDIA_NODE_IP`, or
  127.0.0.1 when the base URL is loopback, or the public address found over
  STUN. A discovered address is advertised *alongside* the machine's own
  interface addresses; a configured one *replaces* them (LiveKit has no way to
  add internal candidates to a fixed address). So a recorder in tide's network
  namespace reaches media through an internal address only when the address
  is discovered; with `TIDE_MEDIA_NODE_IP` set it must reach that address,
  which needs the network to route it back (hairpin) where it isn't local.
- **Webhooks** go to tide's own `/api/webhooks/media` over loopback, signed
  as before.
- **Keys**: with no recorder, nothing outside the process needs the API key
  and secret, so tide generates them per start and never prints them. With
  recording on they must be configured, because the recorder joins rooms with
  them.
- **State**: with no recorder the media server keeps its state in memory and
  needs no Redis. The recorder only speaks Redis, so with recording on the
  media server and the recorder share a **Redis** at
  `TIDE_RECORDER_REDIS_ADDR` (default `127.0.0.1:6379`: a Redis in tide's
  network namespace, beside the recorder), with `requirepass` set to
  `TIDE_RECORDER_REDIS_PASSWORD`. Once the media server uses Redis its signal
  relay goes over it too, so that Redis is in the path of every meeting's
  signaling, though it holds nothing durable: tide waits up to 30 seconds for
  it at startup, then refuses to start naming the address, and a Redis
  restart interrupts joins briefly while the clients reconnect to it.

The cost of one process: **restarting tide restarts the media server.**
Stopping tide stops it, which tells every participant the server is shutting
down; their clients disconnect rather than retry, and the meeting page offers
**Rejoin**, which asks tide for a new token (guests through the lobby again).
Without a recorder the media keys are new each start anyway, so no earlier
token would be accepted. A recording running across a restart fails and says
so (§8). An operator who can't accept that keeps an external media server
with `TIDE_MEDIA_URL`.

Single-instance by design for v1: SQLite for durable state (in memory in
anonymous mode), in-memory for ephemeral state (lobby). Nothing in the design
blocks moving to Postgres + multi-replica later; nothing pays that cost now.

The server starts in order: it binds tide's listener, waits for Redis
(recording only), then starts the media server (its webhooks need the
listener's port), and serves only once it is up; either failing stops tide
with that error.

The server stops in order on SIGTERM or SIGINT (`httpapi.Serve`, then
`main`): it stops taking connections and gives requests in flight 10 seconds,
ending lobby streams at once; then it disconnects the machines, ending their
jobs without touching the jobs' rows; then it waits for the reconcilers and
job followers; then it stops the media server, which ends every room; and
only then closes the database. A second signal stops it at
once.

## 3. Repository layout

```
tide/
  server/                  Go module (module tide)
    cmd/tide/main.go      entrypoint: config, embed, serve
    internal/
      api/                 request/response types  ← tygo source of truth
      httpapi/             route handlers, middleware, SSE
      auth/                OIDC flow, anonymous sessions, session cookies
      rooms/               room CRUD, slugs
      lobby/               in-memory lobby registry
      livekit/             token minting, server SDK calls, webhook verify
      media/               the embedded media server and its /rtc
                           forwarding (§2.1)
      recording/           egress control, recording state machine
      machines/            pairing and managing hosts' moil machines
      transcripts/         transcript jobs, reconciler; bundle/ is the
                           embedded moil bundle
      store/               SQLite (modernc.org/sqlite, CGO-free); a file,
                           or :memory: in anonymous mode
    third_party/moil/      vendored moil Go SDK (scripts/vendor-moil.sh)
    web_embed.go           //go:embed of web/build
  web/                     SvelteKit SPA (adapter-static)
    src/
      lib/api/             typed fetch client + types.gen.ts (generated)
      lib/rtc/             livekit-client wrapper (room store, tracks, devices)
      lib/ui/              design-system components
      routes/              pages (see §9)
  deploy/
    compose.yaml           dev stack: recorder, minio, dex (tide, with its
                           media server, runs on the host)
    egress.yaml            Recorder config
    dex.yaml               Dev IdP config (static test users)
  docs/ARCHITECTURE.md     this document
  Makefile                 dev, gen, check, build targets
  .forgejo/workflows/      CI
```

## 4. Identity, auth, and the token model

**Hosts** authenticate with any OIDC provider (authorization code flow,
config-driven: issuer, client ID/secret, redirect URL). On callback, the tide
server creates a session: an HttpOnly, Secure, SameSite=Lax cookie containing an
HMAC-signed payload (subject, email, name, global-admin capability, expiry). No
session table. Optional comma-separated user and administrator group policies
are evaluated from the verified OIDC `groups` claim at sign-in. Administrators
can manage every room; ordinary hosts manage rooms they own.

**Guests** never authenticate. They exist only as a display name typed on the
pre-join screen and admitted through the lobby.

**LiveKit access tokens** are minted exclusively by the tide server:

| Grant | Host | Guest |
|---|---|---|
| `roomJoin`, `canSubscribe` | ✓ | ✓ |
| `canPublish` (mic/cam) | ✓ | ✓ |
| `canPublishSources` incl. screenshare | ✓ | ✓ |
| `roomAdmin` | ✓ | ✗ |
| metadata `{"role":"host"}` | ✓ | ✗ |

Token TTL is short (10 min) — it authorizes *connecting*; once connected the
session lives independently. Identity is `host:<sub>` or `guest:<nanoid>`;
display name travels in the token's `name` field.

### 4.1 Anonymous mode

With no `TIDE_OIDC_ISSUER`, tide has no sign-in. Anyone may create a room, and
the browser that created it owns it. Nothing else about authorization changes:
ownership is still a session's `sub` matched against `rooms.owner_sub`, checked
on the server, and capabilities still travel only in tokens tide mints.

- **Anonymous sessions.** A session whose `sub` is `anon:` followed by 26
  lowercase base32 characters (128 bits from `crypto/rand`), with no email, no
  name and no administrator capability, in the same signed cookie. `GET
  /api/me` without a valid session issues one and answers with it, rather
  than 401, so the SPA never shows a sign-in screen; `GET
  /api/auth/login?next=…` issues one and redirects to `next` instead of to an
  identity provider. Issuing one counts against the login rate limit (a
  refused one answers 429), and the callback route doesn't exist. An
  anonymous session lasts 30 days rather than one, because it is the only key
  to the rooms this browser created and grants nothing else.
- **Sessions belong to their mode.** Anonymous sessions are signed with a key
  derived from the session secret for that purpose (HMAC of the secret over
  `tide-session:anonymous`); signed-in sessions keep the secret itself. A
  cookie from one mode therefore fails verification in the other, so an
  operator who adds sign-in to an anonymous deployment (or drops it) without
  changing the secret doesn't turn anonymous cookies into signed-in hosts, or
  administrators into anonymous ones. tide also refuses an `anon:` session
  while signed in, and any other session while anonymous.
- **Owners are owners.** Everything §6 and §7 say about a room's owner holds
  for the anonymous one: they join without the lobby, admit and deny guests,
  remove, mute, and end the meeting. Every other session, anonymous or not, is
  a guest in that room. An anonymous owner has no name, so they type one on
  the pre-join screen as a guest does.
- **Rooms live in memory.** The store is SQLite on `:memory:`: the same
  schema and code, nothing on disk. A room is deleted 24 hours after it was
  created or last joined, whichever is later, unless a meeting is live in it;
  the sweep runs every 5 minutes. Restarting tide deletes every room, and the
  session secret, unless `TIDE_SESSION_SECRET` sets one, is generated per
  start, so every anonymous session ends with them.
- **Creating rooms is limited** per client address, in a bucket of its own
  sized by `TIDE_JOIN_RATE_LIMIT` (§15), since it no longer takes an account,
  and in total: with 10,000 rooms in memory, creating another answers 503
  ("This server has too many rooms. Try again later.") until the sweep frees
  some. Many addresses are cheap (an IPv6 /48 is 65,536 of the /64s the
  limits count), so only a ceiling bounds memory.
- **No recording, transcripts or machines.** Those routes answer 404 and
  `/api/me` reports `anonymous: true, recording: false, transcripts: false`.
  The SPA shows the same dashboard (this browser's rooms and **New**) with no
  account and no sign-out, since signing out would only orphan this browser's
  rooms.

An anonymous deployment is an open SFU: anyone who can reach it can hold
meetings on it. That is the point of the mode; an operator who wants to know
who hosts configures sign-in.

## 5. Rooms and meeting URLs

- A room row: `id, slug, name, owner_sub, lobby_enabled (default true), created_at`.
- A slug is 3–64 lowercase letters, numbers and single hyphens, unique across
  rooms. Meeting URL: `https://…/m/<slug>`. Owners can change it while the
  room is idle. Rooms created before readable slugs keep their UUID slugs.
- **Default slugs are readable and random**: ten letters `a–z` drawn uniformly
  from `crypto/rand`, grouped 3-4-3 (`abc-defg-hij`), ~47 bits. That is
  enough to be unguessable behind the per-client limits on room
  lookups and joins (§15) and short enough to read aloud.
- **Creating a room** (`POST /api/rooms`) takes an optional name and an
  optional slug. A missing or blank name becomes the room's slug. A missing
  slug is generated by the server, retrying on a collision; a given slug is
  validated as above and a taken one answers 409 ("That room link is already
  in use."), never silently replaced.
- The dashboard's **New** button opens a dialog with three optional parts:
  the name; the slug, prefilled with a suggestion the browser draws in the
  same format (if that untouched suggestion collides, the browser draws
  another and retries); and a start and end time. The dialog has two actions,
  **Create** and **Create & join** (Meet's "for later" and "instant").
- **Calendar invites are client-side**: with a start and end set, creating
  the room downloads `<slug>.ics`, a single-`VEVENT` iCalendar file built in
  the browser (times in UTC, the meeting URL as `URL` and `LOCATION`, the
  room name as `SUMMARY`). Each room card also has **Add to calendar…**,
  since rooms are reusable. tide stores no times, sends no reminders and
  syncs no calendar; scheduling on the server is a later decision (§17).
- Deleting a room ends any meeting live in it (as **End meeting** does,
  best effort), so its participants can't stay on under a slug someone else
  can now create and own. Tokens name their room by slug and live 10 minutes,
  so a slug freed by deleting or renaming its room is held that long
  (`freed_slugs`) before another room may take it (409, as for a slug in
  use), and tide mints a token only if the room still exists as it marks it
  active, never for one deleted while the join was under way. The anonymous sweep (§4.1) deletes a room only if
  it is still idle at that moment, and every token tide mints for a room
  marks it active first, so a meeting that is starting is never swept.
- Any signed-in host can create rooms (in anonymous mode, anyone: §4.1); the
  creator is the room's owner. Rooms are persistent (reusable URLs; in
  anonymous mode, until 24 hours unused), meetings are implicit sessions
  within them (LiveKit room created on first join, destroyed on last leave).

## 6. Guest lobby

The lobby lives entirely in the tide server, in memory. LiveKit is never
involved — an unadmitted guest has no token and cannot touch the media plane.

```
guest                          tide server                       host
  │ POST /api/rooms/:slug/join     │                                │
  │  {name}                        │                                │
  │◄── {request_id} ───────────────│                                │
  │ GET /api/lobby/:id/wait (SSE)  │      SSE /api/rooms/:slug/lobby│
  │ ...waiting...                  │───────────{pending list}──────►│
  │                                │◄── POST /api/lobby/:id/approve │
  │◄── event: admitted {token} ────│                                │
  │  connect to LiveKit            │                                │
```

- Owner joining their own room: token immediately, no lobby.
- `lobby_enabled=false`: guests also get a token immediately.
- Deny sends `event: denied`; guest request rows expire after 10 minutes.
- Reconnecting guests re-enter through the lobby only if their LiveKit session
  fully dropped (LiveKit resume covers blips without a new token).

## 7. Moderation

Host-only REST endpoints, enforced by session + room ownership, executed via
the LiveKit server SDK — so they hold at the SFU regardless of client behavior:

- `POST /api/rooms/:slug/participants/:identity/kick` → `RemoveParticipant`
- `POST /api/rooms/:slug/participants/:identity/mute` → `MutePublishedTrack` (audio)

The participant list itself is client-side LiveKit state (join/leave/track
events) — no polling, no server involvement.

Every remote participant tile has a viewer-local action menu. Hiding video
unsubscribes that participant's camera publications for the current viewer,
while leaving audio and screen sharing untouched; room managers also receive
the server-enforced mute and remove actions in the same menu.

## 8. Recording

Room-composite recording via LiveKit Egress. The recording's lifetime is bound
to the egress job, not to any participant's tab.

**On only when configured.** Recording needs sign-in and object storage: it
is on when `TIDE_OIDC_ISSUER` and `TIDE_S3_ENDPOINT` are both set (§2.1), and
then the recorder must run too. When it is off, tide makes no storage client
and runs no recording reconciler, registers none of the recording routes
(start, stop, list, delete, download: they answer 404 like any unknown path),
and reports `recording: false` on `/api/me`, so the SPA hides the record
control and every recording list. The media webhook route stays: it also
enforces bans and marks rooms active (§7). Turning recording off in a
signed-in deployment deletes nothing: recording rows stay, and deleting a
room queues its recordings' files in `object_removals` as always, to be
removed once storage is configured again. With the media server embedded, a
recording running when tide restarts is lost with the media server's memory;
the reconciler fails its row after the usual grace (below), so it is never
silent.

1. Host clicks Record → `POST /api/rooms/:slug/recording/start` → server checks
   ownership → `StartRoomCompositeEgress` with S3 output
   (`recordings/<room>/<recording-id>/<yyyy-mm-dd hh-mm> - <meeting>.<ext>`) and
   our **layout URL**. Audio-only recordings use OGG; video composites use MP4.
   The S3 destination and identifying recording metadata travel in every egress
   request; its worker-visible endpoint is configured by
   `TIDE_S3_RECORDER_ENDPOINT`.
2. The layout is a route of our own SPA (`/egress-template`) implementing
   LiveKit's egress template contract (it receives `url`, `token`, `layout`
   query params and joins as a hidden subscriber). Recordings therefore use
   tide's own tile design — same components as the live room. In development,
   Egress loads the route through Vite at `host.docker.internal`; Vite admits
   that hostname through `server.allowedHosts`.
3. Egress lifecycle webhooks (`egress_started/updated/ended`) hit
   `POST /api/webhooks/media` (signature-verified) and drive the
   `recordings` table: `id, room_id, egress_id, status, started_by, started_at,
ended_at, duration_s, s3_key, size_bytes`.
4. Management: `GET /api/rooms/:slug/recordings`, `DELETE /api/recordings/:id`,
   `GET /api/recordings/:id/download` → presigned S3 URL. Surfaced on the
   dashboard per room.
5. In-room, everyone sees recording state (webhook → LiveKit room metadata
   update → client event), rendered by the hairline (§10).

Stop on: explicit stop, room emptying (LiveKit auto-ends the egress), or
egress failure (status `failed`, surfaced in management UI — never silent).

### 8.1 Transcripts

A completed recording can carry a transcript with speaker labels. It is made by
[moil](https://git.convex.works/ConvexWorks/moil) on a computer the **room's
owner** paired with tide — never on the server, and never on anyone else's
machine. The owner can already download the recording, so transcription adds
no new reader of the audio.

**Off unless the operator turns it on.** moil is alpha (macOS only, unsigned,
no sandbox), so transcripts are behind `TIDE_TRANSCRIPTS` (§12), default
`false`. When it is off, tide starts no moil server and no transcript
reconciler, registers none of the machine, pairing, transcript or `/moil/`
routes (they answer 404 like any unknown path), never fills
`RecordingInfo.transcript`, and reports `transcripts: false` on `/api/me` so
the SPA hides `/machines` and every transcript control. Nothing is deleted:
the `machines` and `transcripts` rows stay as they are, and paired machines
stay paired. Turning it back on does not pick up exactly where it stopped,
because time kept passing and recordings kept ending:

- A `pending` row's 14 days (below) count from its request, not from the
  time transcripts were on, so a row older than that fails on the first pass
  as having waited too long. A host can request it again.
- The first pass creates `pending` rows for the recordings that completed
  while it was off, when their room's owner had a machine paired by the time
  each ended (step 1 below), so those owners' machines get that backlog.

Deleting a recording or room while it is off still removes that recording's
transcript files (`object_removals`, below), so no object outlives its
recording. Everything below describes tide with it on.

**Machines.** A signed-in host pairs a computer running the moil app with
moil's device flow (RFC 8628): the app shows a code and opens
`/machines?code=XXXX-XXXX`, where the host checks the machine's name and the
moil address the app must be pairing with, and confirms. The confirming
session's `sub` becomes the machine's owner; nothing in the request body can
name another. The moil SDK
(`server/third_party/moil`) serves the machine side of the protocol under
`/moil/` — the moil base URL is `<TIDE_BASE_URL>/moil` — and each machine keeps
one WebSocket open to `/moil/v1/connect`. Paired machines live in the
`machines` table, which stores only the SHA-256 of a machine's token. `/machines`
lists the host's own machines and unpairs them. A host with no machine sees
three steps there instead: get the moil app (its latest release, which the moil
SDK names as `moil.AppURL`), pair it, and approve the transcribe bundle, by
name, version and the hash prefix the app shows. A machine is `idle`, `busy`,
`paused` or `offline`; a state tide doesn't know, which only a newer moil SDK
could report, shows as `busy`, since moil offers jobs only to idle machines.

**The bundle.** tide publishes one moil bundle, `transcribe` (Nemotron 3
Diarization and Parakeet TDT 0.6B v3), vendored in
`server/internal/transcripts/bundle/` and embedded in the binary. Machine
owners read and approve it by hash in the moil app. A test pins that hash: a
new hash asks every owner to review and approve again, so it only ever changes
on purpose.

**Intent and projection.** The `transcripts` table is the source of truth: one
row per recording that should have a transcript, in status `pending`,
`completed` or `failed`. A moil job is a disposable projection of a `pending`
row, the way recording rows are reconciled against Egress (§8). The reconciler
runs at startup, every minute, and whenever it is nudged (a recording ends or
is deleted, a transcript is requested):

1. It creates a `pending` row for every completed recording that has a file
   and no row, when the room's owner had a machine paired by the time the
   recording ended (`machines.paired_at <= recordings.ended_at`). Pairing a
   machine is the opt-in; hosts without one never see transcripts.
2. It submits a job for every `pending` row it isn't following yet, with job
   ID `recording-<id>`. moil treats resubmitting a live ID as a no-op, so a
   restart, which loses moil's in-memory jobs, just submits them again.
3. It cancels every job whose row is gone or no longer `pending`.

A job's title is the room's name, and its params are the recording's times,
from its row (§8): `started_at`, when tide started it; `ended_at`, when
egress ended it; and `duration_s`, its file's length in whole seconds, which
can be a little shorter than the time between. Times are written as moil
writes them, RFC 3339 in UTC to the second. A time the recording doesn't have
is left out: egress may not say when a recording ended.

```json
{"recording": {"started_at": "2026-09-27T22:10:00Z", "ended_at": "2026-09-27T22:52:14Z", "duration_s": 2529}}
```

The bundle doesn't use them: it logs a warning that it ignores the
`recording` param, and tide doesn't act on a script's log. They are for the
owner's hooks, which a machine runs after a job succeeds, with the job's title
and params (moil's `spec/machine.md` §2): a hook filing meeting notes can tell
when the meeting was, not only when its transcript was made. The params say
nothing more about the room than the title does. Hooks may read them; changing
their shape breaks the hooks that do.

A job is eligible only for the room owner's machines
(`moil.OwnedBy(rooms.owner_sub)`, the room found by ID); administrators can
request a transcript for any room they manage, but it still runs on that room
owner's machines, and `Prepare` checks again that the machine taking an
attempt belongs to the room's current owner. Each attempt may run for three
hours or for one hour plus twice the recording's duration, whichever is
longer: a machine's first attempt also downloads 2.9 GB of models. When a job
succeeds its row becomes `completed` with the speaker count; when it fails,
`failed` with an error the UI can show. A machine that ends a job as cancelled
when tide didn't cancel it fails it too, rather than have tide submit it again
as fast as the machine answers. If tide can't save the transcript a machine
made, its storage or database failing, it tries again every pass for an hour,
or until the staged files' URLs expire if that's sooner, then fails the row,
blaming its storage. tide cancelling a job and server shutdown leave the row
as it is. A `pending` row no machine has finished within 14 days of
its request fails ("No machine transcribed it within 14 days"); requesting it
again retries. The end of a job is recorded even if tide is stopping, for up
to 10 seconds in all once it starts to. If the write fails while tide runs, the
row stays `pending` and each pass tries to record the end again, rather than run
the job again; an end tide couldn't record by the time it stopped leaves the row
`pending`, and the next start submits the job again.

**Files.** Presigned URLs are minted when a machine takes an attempt
(`moil.Job.Prepare`), not at submission, because a job can wait days for a
laptop to wake. They last for the attempt's time limit plus 15 minutes, at
most S3's 7 days: a GET for the recording (input `recording.ogg` or
`recording.mp4`) and a PUT for each output. Machines never write where tide
serves from: each attempt uploads to a staging directory of its own,

```
transcripts-staging/<recording-id>/<attempt>-<16 hex digits>/transcript.txt
transcripts-staging/<recording-id>/<attempt>-<16 hex digits>/transcript.vtt
```

and when the job succeeds tide checks each file against what moil says the
machine uploaded (both formats, the size the machine reported, at most 16 MiB),
before asking storage anything; checks storage has it at that size; copies it
beside the recording under the recording's basename, as a video player expects
sidecar captions; checks the copy's size too, since storage that reports no
entity tag copies whatever is staged by then; and removes the staged copy. A
file that fails a check fails the row, and tide removes what it copied:

```
recordings/<room>/<recording-id>/2026-09-27 14-00 - Standup.ogg
recordings/<room>/<recording-id>/2026-09-27 14-00 - Standup.txt   transcript
recordings/<room>/<recording-id>/2026-09-27 14-00 - Standup.vtt   WebVTT captions
```

A staging directory is named for the attempt tide made it for, and unique by
its random part: moil numbers the attempts of a job submitted again from 1 once
it has forgotten the last run, as it has after tide restarts. A machine that
takes the job again within ten minutes of tide making its directory, having let
it go before starting, is handed the same directory, with URLs that expire when
the first ones do; another machine, or the same one later, gets a new one. A
machine that keeps taking a job and letting it go so costs tide at most one
directory every ten minutes.

**Removing files.** The `object_removals` table lists objects to remove, each
from a due time. Deleting a recording, or its room, deletes the rows and
queues every file of every recording (`store.Recording.ObjectKeys()`) in one
transaction, then removes them at once; the recording reconciler retries any
removal that failed, so a deleted recording never keeps its files, even when
S3 is briefly down. Each pass heals lost egress webhooks (§8) first, then
spends at most 30 seconds removing, and stops at the first file it can't reach
S3 for, leaving the rest queued: S3 out of reach never holds the reconciler up. `Prepare` queues each staging key for when its URL
expires, so an upload that arrives late, from a machine that lost tide but
not S3, is removed too. A transcript copied beside a recording that was
deleted meanwhile is removed again, however tide's work on it ends: a job
knows where its files go beside the recording from its submission, and the try
that finds the recording gone queues both for removal, whether or not they
landed.

**Status.** `RecordingInfo.transcript` combines the row with the live job:

| `status` | Meaning |
|---|---|
| *(null)* | No transcript, and none possible: the recording isn't completed, or the owner has no machine |
| `available` | Completed recording, the owner has a machine, nothing requested yet, and the reconciler won't request one on its own (the recording ended before the owner paired a machine) |
| `waiting` | Pending, or about to be. `message` says why no machine is on it: no machine is paired, none approved the current bundle, the paired machines are paused, offline or busy, or one is about to start |
| `running` | A machine is working on it; `progress` (0–1) and `message` when it reports them |
| `completed` | `GET /api/recordings/:id/transcript/download?format=txt\|vtt` redirects to a 5-minute presigned URL that downloads as a file named after the recording, as text |
| `failed` | `error` says what happened and what to do |

`POST /api/recordings/:id/transcript` requests a transcript for an `available`
recording or retries a `failed` one, and answers a request for a `pending` one
with its current status. When `TIDE_S3_PUBLIC_ENDPOINT` is plain `http` and
tide isn't on loopback, machines would refuse its URLs, so jobs fail at once
with an error naming the setting. Transcripts appear only in recording
management; there is nothing in the meeting itself: no live captions,
summaries or editing.

**Limits.** A host can pair at most 10 machines: confirming an eleventh is
refused, and its code keeps waiting while they unpair one. Looking up,
confirming and denying pairing codes is limited to 20 a minute per host, and 60
per client address. Text machines report (names, OS, versions, progress, errors)
is shown without control or format characters. A job keeps at most 4 MiB of data
events, and a finished job leaves moil's memory after 5 minutes; the transcript
itself travels as files.

## 9. Frontend

SvelteKit (Svelte 5 runes) + `adapter-static`, embedded in the Go binary. Vite
dev server proxies `/api` and `/moil` (including the machines' WebSocket) to the
Go server during development.

Routes:

| Route | Page |
|---|---|
| `/` | Dashboard: your rooms, create room, recent recordings (auth) |
| `/login` | OIDC entry (redirects to provider) |
| `/m/[slug]` | Pre-join → lobby wait → room (one route, three states) |
| `/rooms/[slug]` | Room settings + its recordings and their transcripts (auth, owner) |
| `/machines` | Your paired machines; with `?code=`, confirming a machine's pairing (auth) |
| `/egress-template` | Recording composite layout (headless Chrome only) |

State architecture: one `lib/rtc/room.svelte.ts` class wraps `livekit-client`'s
`Room` and exposes reactive state (participants, tracks, connection state,
active speakers, devices) as runes. UI components read this store and call its
methods (`toggleMic`, `setCamera`, `shareScreen`, …); nothing else touches
livekit-client. Chat is a thin layer on LiveKit data messages (topic `chat`),
ephemeral by design: no history, no persistence, late joiners see only what
arrives after them.

Meeting chrome:

- **Top left** of the stage: the wall clock (`HH:MM` in the viewer's locale,
  Inter with tabular numerals, updating on the minute), a 1px divider, the
  room's human name (ellipsized), and an (i) button. The (i) opens a small
  popover with the meeting link, shown whole and wrapped rather than cut
  off, and a **Copy link** button; it doesn't repeat the name beside it. It
  is the only place in the meeting the link appears. The REC chip and the autoplay
  unlock join this cluster; the connection state stays on the right.
- **Control bar**, left to right: microphone and camera side by side (each
  with its device caret), screen share, record (hosts only), then view,
  people and chat, then leave. Hosts and guests see mic and camera in the
  same place; record never sits between them.

The typed API client (`lib/api/client.ts`) is a small hand-written fetch
wrapper importing only generated types (§11).

### 9.1 Media state is a convergent projection

**Client state is a convergent projection of LiveKit's own maps, never a log of
events.** Everything the meeting UI shows is re-derived by `reconcileMedia()`
from `room.localParticipant` + `room.remoteParticipants`. Events are only ever
a *hint that it is time to re-derive*; they never carry the state itself.

The rules that follow from it, all of them load-bearing:

- **No handler may throw into livekit-client.** Every `room.on(...)` goes
  through `RoomState.listen()`, which catches, records the fault and logs it.
  LiveKit's emitter dispatches through a bare `ReflectApply` loop with no
  `try`/`catch`, and `getOrCreateParticipant` emits `ParticipantConnected`
  *before* it installs that participant's track-event forwarding — so one
  escaped exception wires that participant to nothing for the rest of the
  session. A bare `catch {}` is not acceptable either: the fault is recorded so
  the next incident is attributable.
- **Nothing reconciles inline.** Handlers call `markProjectionDirty()`;
  reconciliation is coalesced onto a microtask. That is not only for
  efficiency — it means the projection observes the SDK's state *after* the
  synchronous transition that produced the events, so `handleRestarting()`
  (which disconnects every participant before it flips the room to
  `Reconnecting`) cannot empty the stage.
- **A deferred reconcile is never discarded.** While the room is not
  `Connected` the projection is frozen and the dirty flag *stays set*; the
  transition back to `Connected` flushes it.
- **A heartbeat is the floor.** While connected, the projection is marked dirty
  every 2 s. This is what makes any dropped event — ours, LiveKit's, or one
  nobody has thought of — a ≤2 s self-heal instead of a page reload. It is free
  on an idle meeting because a reconcile whose signature is unchanged does not
  republish.
- **Playback recovery is attempted once, not chased.** Unblocking autoplay
  needs a user gesture, so retrying without one cannot succeed.
  livekit-client reports playback status from two probes that `startAudio()`
  drives together — the media elements and the AudioContext
  (`acquireAudioContext` emits `AudioPlaybackStatusChanged` whenever the
  context's running state disagrees with `canPlaybackAudio`). While an attempt
  is in flight those two disagree *by construction*: the context resumes and
  reports healthy, the elements stay blocked and report blocked. Retrying on
  either report is an unbounded microtask loop that pins the CPU. So: one
  automatic attempt, re-armed only by a healthy report raised outside an
  attempt, then the unlock control and the armed listeners wait for the
  gesture. The gate asserts blocked playback stays quiet.
- **A Lost participant is not shown.** The server keeps an abruptly departed
  participant (closed tab, dead laptop) in its resume grace window and
  re-announces them on every reconnect sync, so `remoteParticipants` alone
  renders ghost tiles that can outlive several reconnects. The projection
  filters remote participants whose connection quality is `Lost` — the
  server's own signal that no connection stands behind the map entry. A resume
  changes their quality and the tile returns. `Unknown` is never filtered: a
  fresh joiner reports `Unknown` until the first quality update.
- **Subscriptions are declarative.** `applySubscriptions()` states what should
  be subscribed (everything except a camera the local user hid) and calls
  `setSubscribed` only where reality differs. tide runs no retry loop against
  the SFU; livekit-client re-establishes subscriptions itself via
  `sendSyncState()`. Unlocking playback must never touch subscriptions.
- **livekit-client is pinned exactly.** It owns the media path; a caret range
  is its own supply of regressions. Upgrade client and server as a tested pair,
  behind the media gate (§13).

`window.tideDiagnostics()` writes a JSON file with the bounded event ledger,
recorded handler faults and the current subscription state. It is local-only
and exists for incident attribution; it is deliberately not a UI control, since
the feature list is frozen (§1).

## 10. Design language

Cursor-grade density with Convex Works' materials: ivory paper, near-black ink,
one electric blue. Calm, small, precise. Text is minimal — controls are
icon-first (phosphor-svelte) with tooltips; words appear where they carry
information.

**Two surfaces.** The shell (dashboard, pre-join, settings) is light — paper
and ink. The meeting stage is near-black so video tiles carry the color.
Both share the same accent and geometry, so the room reads as the same product
with the lights dimmed.

Tokens:

```css
/* shell (light) */
--paper: #f5f5f0; /* app background */
--surface: #ecece6; /* cards, inputs */
--surface-2: #e3e3db; /* hover, wells */
--border: #d8d8ce;
--ink: #171717;
--ink-2: #6e6e64; /* secondary text */
--accent: #2320e6; /* the blue — links, primary actions, focus */
--accent-hover: #1b18c4;

/* stage (dark, in-meeting) */
--stage: #0f0f0e;
--panel: #161615; /* side panels, control bar */
--panel-2: #1e1e1c;
--border-d: #2a2a27;
--text: #edede6; /* warm off-white, echoes paper */
--text-2: #8f8f85;
--accent-d: #5b58ff; /* accent lightened for dark ground */

/* semantic (both surfaces) */
--rec: #e5484d; /* recording */
--ok: #4fa36f;
--warn: #d9a03f;
```

Typography: **Inter** (variable) for all UI — base 13px/20px, weights 450/550,
scale 11 / 12.5 / 13 / 15 / 18, with 24px reserved for the pre-join room name.
**Geist Mono** for anything machine-flavored: room slugs being edited,
participant counts, keyboard shortcuts. Words people read in the meeting —
the clock, names, "(You)" — stay in Inter.

**Roles are words, not badges.** No pills or outlined chips for roles: the
local participant is "Name (You)" and the host carries a secondary line,
"Meeting host", in `--text-2` (Meet's pattern). The pre-join account line
says "· Host" in secondary ink. The human room name is shown during join and lobby
transitions and in the stage's top-left cluster (§9); room slugs are kept out
of the meeting UI except inside the meeting URL in its details popover.

Geometry: 4px spacing base; controls 28px tall; panel padding 12px; gaps 8px;
radius 4px (controls), 6px (cards/panels), 8px (video tiles). Borders are 1px,
always; no shadows except a single elevation for menus/dialogs.

Icons: phosphor-svelte, 16px, regular weight everywhere (20px only on pre-join
device controls). No filled/duotone variants.

**Signature — the hairline.** A 2px line across the very top of the app chrome
(inherited from the Convex Works site) that doubles as the status channel:
accent blue when idle/connected, amber while reconnecting, pulsing `--rec` red
while the room is being recorded. State lives in the chrome, not in a toast.

Motion: 120ms ease-out on hovers and panel slides; the only ambient animation
permitted is the recording hairline pulse (2s); `prefers-reduced-motion`
disables it in favor of a static red line.

Voice: sentence case, plain verbs, no filler. "Start recording" → toast
"Recording started". Errors say what happened and what to do next.

## 11. Go ↔ TypeScript type sync

Go is the source of truth. All wire types (requests, responses, SSE payloads,
webhook-derived models) live in `server/internal/api` as plain structs with
`json` tags.

- **[tygo](https://github.com/gzuidhof/tygo)** generates
  `web/src/lib/api/types.gen.ts` from that package (`tygo.yaml` at repo root;
  run via `make gen`, pinned with `go run github.com/gzuidhof/tygo@vX`).
- The generated file is committed. **CI runs `make gen` and fails on
  `git diff --exit-code`** — drift cannot merge.
- Route paths are exported as constants in the same package and mirrored by the
  generator, so the fetch wrapper has no string literals.
- Discipline: handlers may only accept/return `internal/api` types; nothing in
  `web/` may hand-write a wire type. Enforced in review, kept honest by CI.

## 12. Configuration

All server config via `TIDE_*` env vars (12-factor, `.env` in dev):

```
TIDE_ADDR=:8080                 TIDE_BASE_URL=http://localhost:8080
TIDE_SESSION_SECRET=…           TIDE_DB_PATH=./data/tide.db
TIDE_OIDC_ISSUER=…              TIDE_OIDC_CLIENT_ID / _CLIENT_SECRET
TIDE_USER_GROUPS=…              TIDE_ADMIN_GROUPS=…
TIDE_MEDIA_NODE_IP=…            TIDE_MEDIA_TCP_PORT=7881   TIDE_MEDIA_UDP_PORT=7882
TIDE_MEDIA_API_PORT=7880
TIDE_MEDIA_API_KEY=…            TIDE_MEDIA_API_SECRET=…
TIDE_MEDIA_URL=…                TIDE_MEDIA_PUBLIC_URL=…    (external media server only)
TIDE_S3_ENDPOINT=…              TIDE_S3_PUBLIC_ENDPOINT=…
TIDE_S3_RECORDER_ENDPOINT=…     TIDE_S3_BUCKET / _ACCESS_KEY / _SECRET_KEY
TIDE_S3_REGION=…                TIDE_RECORDER_TEMPLATE_URL=…
TIDE_RECORDER_REDIS_ADDR=127.0.0.1:6379  TIDE_RECORDER_REDIS_PASSWORD=…
TIDE_TRUSTED_PROXIES=…          TIDE_DEV_MODE=false
TIDE_JOIN_RATE_LIMIT=10         TIDE_WAIT_RATE_LIMIT=20
TIDE_LOGIN_RATE_LIMIT=10        TIDE_PAIR_RATE_LIMIT=10
TIDE_TRANSCRIPTS=false
```

**Every variable is optional.** `tide` with an empty environment is an
anonymous deployment on `http://localhost:8080` with its media server
embedded. Each group below turns something on, and is then checked as a
whole:

- **Sign-in**: a non-empty `TIDE_OIDC_ISSUER` turns it on, and then
  `TIDE_OIDC_CLIENT_SECRET` (at least 16 characters) and
  `TIDE_SESSION_SECRET` (at least 32) are required and rooms persist at
  `TIDE_DB_PATH`. Without it, tide is anonymous (§4.1): `TIDE_DB_PATH` is
  ignored for `:memory:`, and `TIDE_SESSION_SECRET`, if unset, is generated
  per start.
- **Recording**: a non-empty `TIDE_S3_ENDPOINT` turns it on, and needs
  sign-in. `TIDE_S3_PUBLIC_ENDPOINT` and `TIDE_S3_RECORDER_ENDPOINT` default
  to `TIDE_S3_ENDPOINT`; the access key and a secret key of at least 16
  characters are required. With the media server embedded, recording also
  requires `TIDE_MEDIA_API_KEY`, `TIDE_MEDIA_API_SECRET` (at least 32
  characters) and `TIDE_RECORDER_REDIS_PASSWORD` (at least 32), which the
  recorder is configured with too, and `TIDE_RECORDER_REDIS_ADDR` names the
  Redis they share (`host:port`, a name or an address).
- **Transcripts**: `TIDE_TRANSCRIPTS=true` needs recording.
- **Media server**: embedded unless `TIDE_MEDIA_URL` is non-empty. Embedded,
  the token's `ws_url` is the base URL's origin with `ws`/`wss` (so
  `TIDE_MEDIA_PUBLIC_URL` is normally unset, though it wins when set), the
  API key and secret are generated per start unless set or required above,
  and `TIDE_MEDIA_NODE_IP` must be an IP address and the ports 1–65535. With
  `TIDE_MEDIA_URL`, tide behaves as before 1.0: `TIDE_MEDIA_PUBLIC_URL`, the
  API key and the secret are required, the `TIDE_MEDIA_NODE_IP`, port and
  `TIDE_RECORDER_REDIS_*` settings are unused, and Redis between the media
  server and the recorder is the operator's.

A variable set to the empty string is unset, for every variable: it takes its
default. tide refuses to start, naming every problem at once, on anything
else: S3 without sign-in, transcripts without recording, user or
administrator groups without sign-in (a missing issuer must not quietly open
a deployment that meant to restrict who hosts), a required secret missing,
short, or equal to a shipped dev value, an unparseable IP or port, the media
API and TCP ports equal. It logs
its mode at startup in one line, e.g. `tide: anonymous, media server
embedded (node IP discovered), recording off`.

`TIDE_DEV_MODE=true` supplies the publicly-known dev value for any secret
left unset, and exposes `/api/dev/token`. It turns nothing else on: the dev
stack (§13) sets `TIDE_OIDC_ISSUER` and `TIDE_S3_ENDPOINT` itself.

`TIDE_TRANSCRIPTS` turns on transcripts and machine pairing (§8.1). Like
`TIDE_DEV_MODE` it is read with `strconv.ParseBool`, and a value it can't
parse means the default, off; tide logs at startup whether transcripts are
on.

The four rate limits are per-client ceilings over a one-minute window, an IPv6
client counting by its /64. `TIDE_JOIN_RATE_LIMIT` also sizes the separate
bucket for room lookups (§15), which a meeting page uses once per load. They are configuration because the right value
depends on the deployment: a public install wants the defaults, while the media
gate — where every browser shares one container IP — would throttle itself
without raising them. A value that is missing, zero, negative or unparseable
falls back to the default; it never becomes zero, which would deny every
request.

Per-variable reference, defaults, and production rules: `docs/DEPLOYMENT.md`.

## 13. Development environment

`deploy/compose.yaml` runs what tide doesn't contain; tide itself, with its
media server, runs on the host for fast iteration:

| Service | Port(s) |
|---|---|
| redis | 127.0.0.1:6379 (for tide on the host; the recorder uses `redis:6379`) |
| egress (the recorder) | — (worker; reaches tide at `host.docker.internal`, needs redis + minio) |
| minio | 9000 (S3), 9001 (console) |
| dex | 5556 (issuer), static test users (`host@tide.dev`) |

tide on the host listens on 8080 (HTTP and `/rtc` signaling), 7881/tcp and
7882/udp (media) and 127.0.0.1:7880 (its media server's API). `make dev`'s
LAN detection uses macOS's `ipconfig`; on Linux `TIDE_MEDIA_NODE_IP` is set
by hand.

`Makefile` targets: `dev` (compose up + Go server + Vite, concurrently),
`gen` (tygo), `check` (vet + go test + svelte-check + Prettier + type drift),
`build` (SPA build → embed → single binary), `clean`. `make dev` is the
signed-in deployment with recording: it sets `TIDE_OIDC_ISSUER` (Dex) and
`TIDE_S3_ENDPOINT` (MinIO) along with `TIDE_DEV_MODE`. The anonymous mode
needs none of the stack: `make build && ./bin/tide`.

`make dev` detects the host LAN address and writes it as `TIDE_MEDIA_NODE_IP`
in `deploy/.env`, which it also loads into tide's environment. The media server
advertises that address, which both host browsers and the recorder's headless
browser in Docker can reach; the recorder reaches signaling at
`ws://host.docker.internal:8080` and Redis at `redis:6379`, and stages
recordings under `/recordings` on a tmpfs before upload. Vite proxies `/api`, `/moil` and `/rtc` (WebSockets
included) to the Go server, so the SPA on 5173 signals through its own
origin as it does in production.

S3 has three deliberate views in development:

| Caller | Endpoint | Why |
|---|---|---|
| tide server | `http://localhost:9000` | Object management from the host |
| Browser | `http://localhost:9000` | Host-reachable presigned download URLs |
| Egress | `http://minio:9000` | Uploads from the Compose network |

### Media gate stack

`deploy/media-test/compose.yaml` is a second, sealed stack used only by the
media suite: dex, minio, redis, egress, tide and a Playwright `runner`, on a
private `10.253.0.0/24` with **no published host ports**. tide runs its media
server embedded, as production does, advertising its own address on that
network, and reaches Redis at `redis:6379`; the recorder shares tide's
network namespace, so it signals at `ws://127.0.0.1:7880` (the media server
itself; it listens on IPv4 loopback only, and `localhost` may resolve to
`::1` first), uses the same `redis:6379`, loads the layout from
`http://localhost:8080`, and gets the same media candidates as the browsers.
It waits for tide's `/healthz`, which answers only once the media server is
up. The browser under test runs inside
`runner`, so services are reachable only by compose DNS name. Configuration is
baked into images (`*.Dockerfile`) rather than bind-mounted, because CI drives
compose from inside a container where host bind mounts do not resolve on the
daemon's filesystem.

Dex is present so the suite can hold a real session: `/api/dev/token` mints
only non-host tokens, so without it `/m/[slug]`, the lobby, host grants,
moderation and recording are unreachable from a test.

One deliberate limitation: the stack serves plain http on a non-localhost
origin, so pages are **not secure contexts** and `getUserMedia` does not exist.
Actors therefore join with capture off and publish synthetic canvas/oscillator
tracks, which take the identical `publishTrack` → SFU path. Covering local
capture as well needs the stack served over TLS (tide, which now carries the
signalling too).

## 14. CI

Forgejo Actions (`.forgejo/workflows/ci.yml`), on every push/PR:

1. **server** — `go vet`, `go test ./...`
2. **web** — `svelte-check`, prettier check, `vite build`
3. **typesync** — `make gen && git diff --exit-code` (§11)
4. **media-e2e** — the full real-SFU gate (below), against the sealed stack
5. **build** — full binary build (SPA embed included) as the merge gate

The media gate runs in full on every pull request; nothing is deferred to a
nightly job, because a regression only a nightly catches has already shipped.
It covers, in order: the lifecycle scenarios, recording, the reliability suite
(chromium + webkit), the seeded fuzzer, and a rerun under `netem` loss/latency.

**The rule the lifecycle suite encodes.** Assertions must prove *presence*
before they prove flow. `expectMediaInvariant` only checks that whatever a
client already knows about is attached and receiving RTP, so it passes
vacuously when a client never learned a participant published at all — which is
the failure mode every media incident here has had. Scenarios therefore assert
that an *existing* participant converges on a change: a later joiner, a reload,
a mute, a screen share, a departure, a reconnect. A test whose receiver joins
after the publications already exist is testing what a page reload does.

Handlers passed to `livekit-client` must not throw. LiveKit emits
`ParticipantConnected` *before* it installs that participant's track-event
forwarding and its emitter does not catch, so one escaped exception wires that
participant to nothing for the rest of the session. `failNextParticipantEntered`
and `failNextReconcile` inject exactly that fault; both are gate scenarios, and
both also assert the fault was *recorded* — a guard that swallows is the same
bug one step further from view.

**The rule the convergence scenarios encode** (§9.1). A client must catch up
from LiveKit's own maps without being told. `deafen()` removes every listener
tide registered while leaving the SFU connection intact, and `full-reconnect`
does the same thing by a route that happens in production. In both, the probe
(which reads the SDK) passes while the rendered tiles (which read tide's
projection) are the only witness that the state actually reached the user — so
both are asserted, separately, in that order. The heartbeat that makes this
work must also cost nothing when idle: a gate scenario watches an idle meeting
for 60 s and requires the reconcile count to climb while the republish count
stays flat.

`make media` runs the gate as CI does. `make media-dev ARGS="…"` keeps the stack
up between runs for iteration.

## 15. Security notes

- All capability checks are server-side: room ownership on every moderation and
  recording endpoint; tokens minted only after policy passes.
- LiveKit webhook requests are verified against the API key/secret signature.
- Session cookies: HttpOnly, Secure, SameSite=Lax, HMAC-signed, short expiry.
- Per-IP token buckets limit joins (10/min, guests and hosts alike), room
  lookups (`GET /api/rooms/:slug`, found or not, 10/min in a bucket of their
  own sized by the join limit: per client address for guests, per `sub` for
  signed-in hosts, so a NAT shared with guests doesn't refuse a host and an
  account from a broad issuer can't test slugs unlimited either), lobby wait
  streams (20/min),
  login redirects (10/min) and machines starting a pairing (10/min,
  `POST /moil/v1/pair`, the one moil endpoint without credentials that creates
  state, which must be JSON so a web page can't post one without a CORS
  preflight; anything else is refused before it counts); stale buckets are
  cleaned in memory. IPv6 clients are counted by their /64.
- Anonymous mode (§4.1): anyone can mint a session, so nothing may be counted
  per anonymous `sub`. Room creation is limited per client address (10/min,
  sized by the join limit), room lookups count anonymous sessions per client
  address as they do guests, and minting a session is a login redirect under
  the login limit. An anonymous session owns only the rooms it created.
- Signaling (§2.1): tide forwards only `/rtc` and the paths under it to the
  embedded media server, which checks the token on every connection, and only
  GET requests without a body (405 and 400 otherwise): every signaling
  request is a GET, and a body nobody sends would hold a connection open
  forever. tide's read and write deadlines stand until the media server
  upgrades the connection to a WebSocket. The media server's API (room
  service, egress) listens on loopback only and is never reachable through
  tide.
- Anonymous rooms are capped at 10,000 in memory (§4.1), and anonymous and
  signed-in sessions are signed with different keys (§4.1).
- The recorder's Redis carries recording jobs, and a recording job carries
  the S3 credentials the recorder uploads with, as well as the media server's
  signal relay. It requires a password of at least 32 characters
  (`requirepass`), and tide checks: it refuses to start against a Redis that
  answers without one (a Redis with no `requirepass` accepts any password, so
  authenticating proves nothing). The reference deployments keep it off every
  network:
  in Kubernetes it runs in tide's pod bound to 127.0.0.1, in Compose on the
  project's private network with no published port. It is a separate,
  ordinary Redis, so nothing a client sends it can take tide down.
- With no recorder, the media server's API key and secret are generated per
  start and never leave the process.
- A default room slug carries ~47 bits from `crypto/rand` (§5). With
  lookups and joins limited per client and per host, guessing a
  live room's link is not a practical
  attack, and the lobby (on by default) still stands between a guesser and
  the meeting. Owners who want more can set a longer slug.
- JSON request bodies are limited to 1 MB before decoding; lobby requests
  expire after 10 minutes.
- Presigned download URLs are short-lived (5 min) and minted per request after
  an ownership check.
- Machines (§8.1): pairing is confirmed only by a signed-in session with the
  CSRF header, and the machine's owner is always that session. Looking up,
  confirming and denying codes is limited to 20 a minute per host and 60 per
  client address (RFC 8628 §5.1), and a host pairs at most 10 machines. The
  confirm page shows tide's moil address, which the moil app must be pairing
  with. Machine tokens are stored as SHA-256 hashes.
  Transcript jobs go only to the room owner's machines. A machine gets
  presigned URLs when it takes an attempt, valid for its time limit plus 15
  minutes (the same ones if it takes the job again within ten minutes): a
  GET for the recording, and a PUT for each output to a staging key tide
  never serves from; tide checks each file against the size the machine
  reported, copies it beside the recording, checks the copy, and removes
  staging keys when their URLs expire. The moil app
  accepts plain http only to loopback storage and only when tide itself is on
  loopback, so production needs an https `TIDE_S3_PUBLIC_ENDPOINT`.
- The transcript bundle runs with the machine owner's permissions and no
  sandbox (moil's alpha). Owners read and approve it in the moil app; tide
  pins its hash so it cannot change silently.
- No secrets in the SPA: the client knows only its own token and public URLs.
- `window.tideDiagnostics()` (§9.1) contains participant identities and display
  names. It is written to the user's own machine by an explicit action and is
  never transmitted anywhere; treat an exported file as personal data.
- CSP, clickjacking, MIME-sniffing, and referrer-policy headers wrap every
  response; the SPA makes no third-party requests (fonts are self-hosted).

## 16. Phased build

Each phase ends demoable and merged; acceptance criteria are the definition of
done.

**Phase 0 — Steel thread.**
Repo scaffold, compose stack, Go server serving a minimal SPA, token minting
for a hardcoded dev room.
*AC: two browser tabs join the same room locally and see/hear each other.*

**Phase 1 — Meeting core.**
Pre-join (name, device pickers, camera preview, mic level), room stage (tile
grid, speaking indicators), control bar (mic/cam/screenshare/leave), mid-call
device switching, reconnect states on the hairline.
*AC: 3+ participants; screenshare promotes to focus layout; unplugging a
headset mid-call recovers; killing the network for 10s shows amber hairline
then resumes without reload.*

**Phase 2 — Identity and access.**
OIDC (Dex) login, sessions, dashboard, room CRUD, meeting URLs, guest lobby
end-to-end (SSE both sides), owner bypass, lobby toggle per room.
*AC: host logs in via Dex, creates a room, shares `/m/slug`; guest waits in
lobby, host admits from inside the room; denied guest sees a clear end state.*

**Phase 3 — Moderation and chat.**
Participant panel (roles, media state), kick and mute-audio enforced at the
SFU, ephemeral chat panel with unread badge.
*AC: kicked guest is disconnected and cannot rejoin without re-admission;
muted guest's audio stops for everyone; chat works across 3 participants and
vanishes on reload.*

**Phase 4 — Recording.**
Egress template route, start/stop from the control bar (host only), webhook
receiver, recordings table, management UI (list, download via presigned URL,
delete), red hairline while recording.
*AC: record a 3-participant meeting including a screenshare; MP4 lands in
MinIO in tide's own layout; appears on the dashboard; downloads and plays;
host's tab crash does not stop the recording.*

**Phase 5 — Polish and hardening.**
Design QA against §10 across every state (empty, error, denied, expired),
rate limits, CSP, README, CI green end-to-end, `make build` binary verified on
a clean machine.
*AC: full CI matrix green; the binary + compose stack runs the entire Phase
1-4 acceptance list from scratch.*

**Phase 6 — One binary.**
The media server inside tide (§2.1), anonymous mode (§4.1), recording on
only when configured (§8), `/rtc` signaling on tide's origin, Redis only
beside the recorder, the dev, gate and Kubernetes stacks without a LiveKit
process.
*AC: `./bin/tide` with an empty environment serves a meeting two browsers on
the machine can hold, creator as host and the other through the lobby, with
no other process running; the media gate, recording included, passes with
the recorder and its Redis as the only other processes; a signed-in deployment without
S3 hides every recording control; `TIDE_MEDIA_URL` still drives an external
media server.*

## 17. Decision log

| Decision | Choice | Why |
|---|---|---|
| Media stack | LiveKit (self-hosted) | Ships reconnection, simulcast, moderation, and Egress recording; Apache 2.0 |
| Fork vs fresh | Fresh repo, fresh history | mirotalksfu is AGPL; tide derives nothing from it |
| Backend | Single Go binary, SPA embedded | Leanest production artifact; first-party LiveKit Go SDK |
| Frontend | SvelteKit SPA, adapter-static | No Node in prod; SSR worthless behind auth |
| Host auth | Generic OIDC (Dex in dev) | Provider-agnostic by config |
| Chat | Ephemeral only | No storage, no privacy surface; history is a non-goal |
| DB | SQLite (modernc, CGO-free) | Single instance; keeps the static binary |
| Type sync | tygo + CI drift gate | Go structs as single source of truth |
| Recording | Egress room composite + custom template | Server-owned lifetime; recordings wear tide's design |
| Transcripts | moil jobs on the room owner's own machine | No model or GPU on the server, no fifth process, no new reader of the audio |
| Transcript trigger | Automatic once the owner has paired a machine; otherwise on request | Pairing is the opt-in; no per-recording button for hosts who use it |
| Transcripts switch | `TIDE_TRANSCRIPTS`, default off | moil is alpha; tide ships publicly without asking operators to run it |
| Default slugs | Readable random (`abc-defg-hij`), ~47 bits | Short enough to read aloud; unguessable behind the lookup and join limits; UUIDs made ugly links |
| Calendar invites | `.ics` built in the browser, nothing stored | Covers "send people a time" without a scheduling model; server-side scheduling stays a later decision |
| moil SDK | Vendored in `server/third_party/moil` | The forge sits behind Cloudflare Access, so Go can't fetch it in CI or Docker; same precedent as `web/vendor` |
| Media server process | LiveKit's server as a library inside tide; external one still possible via `TIDE_MEDIA_URL` | One executable to deploy; the price is that a tide restart ends live meetings (§2.1) |
| Signaling origin | tide forwards `/rtc` to the embedded server | One origin, one certificate, one ingress route |
| Redis | None without a recorder; an ordinary Redis 7 beside the recorder with one | LiveKit needs Redis only to reach the recorder, which only speaks Redis. miniredis inside tide was tried and measured: fast enough (≈1,600 commands/s, sub-millisecond publishes), but one subscriber that stopped reading froze its single lock, and every meeting's signal relay with it, and it trusted request sizes before AUTH; a bug in it would take every meeting down. Valkey works too (same protocol); Redis matches what production already runs |
| Sign-in | Optional: no issuer means anonymous mode | Running a meeting server shouldn't require an identity provider; ownership stays a server-checked `sub` either way |
| Anonymous rooms | In memory, gone 24 hours after last use or at restart | No persistence to operate; links still work for later the same day |
| Recording switch | On when sign-in and `TIDE_S3_ENDPOINT` are set | Recording costs a second process and storage; a deployment that doesn't want it shouldn't run either |
| TLS | Left to a proxy or ingress | Built-in ACME means a certificate cache, port 443 and renewal failures in tide; Caddy in front is two lines and production already has an ingress |
