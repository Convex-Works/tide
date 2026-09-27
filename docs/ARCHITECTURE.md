# klisi — Architecture

klisi is a lean, self-hosted video meeting product. It does few things well:
meeting URLs, host auth (OIDC), a guest lobby, mic/cam/screen controls, device
selection, a participant list with moderation, reconnection, ephemeral chat, and
Zoom-like server-orchestrated recording with recording management, including
speaker-labelled transcripts made on the room owner's own machine (§8.1).

Everything else is out of scope by design. Feature restraint is the product.

## 1. Principles

- **Lean to run.** Production is four processes: the klisi binary, LiveKit,
  Redis, and the Egress worker. No Node runtime in production.
- **The media plane is not ours.** LiveKit owns WebRTC, simulcast, reconnection,
  and recording capture. klisi owns *policy*: who gets a token, with which
  grants, and what happens around the meeting.
- **Authorization is the token.** Every capability a client has (join, publish,
  screenshare, admin) is a grant inside a LiveKit JWT minted by the klisi
  server. There is no client-asserted role anywhere.
- **One source of truth per type.** API types are defined once in Go and
  generated into TypeScript. CI fails on drift.
- **Frozen feature list.** New features must displace an existing one.
  Transcripts were admitted as part of recording management, on one
  condition that stays load-bearing: no model runs on the server. Heavy
  compute happens on machines the recording's owner paired (§8.1).

## 2. System architecture

```
┌──────────────────────┐   HTTPS (SPA + /api + SSE)   ┌───────────────────────────┐
│  Browser             │◄────────────────────────────►│  klisi server (Go binary)  │
│  SvelteKit SPA       │                              │  · serves embedded SPA     │
│  livekit-client      │                              │  · OIDC + sessions        │
└──────────┬───────────┘                              │  · token minting          │
           │ WebRTC + WS                              │  · lobby (SSE)            │
           ▼                                          │  · moderation API         │
┌──────────────────────┐    Redis (job bus)           │  · egress control         │
│  LiveKit server      │◄────────────┐                │  · webhook receiver       │
│  (SFU)               │             │                │  · SQLite (rooms, recs)   │
└──────────┬───────────┘             │                └─────┬───────────┬─────────┘
           │ joins room as           │                      │ server    │ presigned
           │ hidden participant      ▼                      │ SDK       │ URLs
           │              ┌──────────────────┐              │           ▼
           └─────────────►│  Egress worker    │─────────────┼──────►┌────────┐
                          │  (Chrome + GST)   │ OGG / MP4   │       │  S3 /  │
                          └──────────────────┘─────────────►└──────►│ MinIO  │
                                                                    └───▲────┘
┌──────────────────────┐   WebSocket /moil/v1/connect  (opened by     │
│  Owner's machine     │──────────────────────────────► the machine)  │
│  moil app + uv       │◄───── recording GET, transcript PUT ─────────┘
└──────────────────────┘        (presigned)
```

| Component | What it is | What it owns |
|---|---|---|
| **klisi server** | Single Go binary, embeds the built SPA via `embed.FS` | Auth, sessions, rooms, tokens, lobby, moderation API, recording lifecycle + management, webhooks, the moil service side (pairing, transcript jobs) |
| **LiveKit server** | Stateless Go binary (upstream, Apache 2.0) | All media: SFU, simulcast, adaptive streaming, ICE, reconnection/resume |
| **Egress worker** | Upstream worker service (headless Chrome + GStreamer) | Renders our composite layout page, encodes OGG audio or MP4 video, writes directly to S3 |
| **Redis** | Required by LiveKit once Egress runs | Egress job queue, LiveKit node state |
| **MinIO (dev) / S3 (prod)** | Object storage | Recording files |
| **Dex (dev only)** | OIDC identity provider | Host login in the dev stack; any OIDC provider in prod |
| **Paired machines (optional)** | Hosts' own computers running the [moil](https://git.convex.works/ConvexWorks/moil) app | Transcribing their owner's recordings; not part of the deployment |

Single-instance by design for v1: SQLite for durable state, in-memory for
ephemeral state (lobby). Nothing in the design blocks moving to Postgres +
multi-replica later; nothing pays that cost now.

The server stops in order on SIGTERM or SIGINT (`httpapi.Serve`): it stops
taking connections and gives requests in flight 10 seconds, ending lobby
streams at once; then it disconnects the machines, ending their jobs without
touching the jobs' rows; then it waits for the reconcilers and job followers;
and only then closes the database. A second signal stops it at once.

## 3. Repository layout

```
klisi/
  server/                  Go module (module klisi)
    cmd/klisi/main.go      entrypoint: config, embed, serve
    internal/
      api/                 request/response types  ← tygo source of truth
      httpapi/             route handlers, middleware, SSE
      auth/                OIDC flow, session cookies
      rooms/               room CRUD, slugs
      lobby/               in-memory lobby registry
      livekit/             token minting, server SDK calls, webhook verify
      recording/           egress control, recording state machine
      machines/            pairing and managing hosts' moil machines
      transcripts/         transcript jobs, reconciler; bundle/ is the
                           embedded moil bundle
      store/               SQLite (modernc.org/sqlite, CGO-free)
    third_party/moil/      vendored moil Go SDK (scripts/vendor-moil.sh)
    web_embed.go           //go:embed of web/build
  web/                     SvelteKit SPA (adapter-static)
    src/
      lib/api/             typed fetch client + types.gen.ts (generated)
      lib/rtc/             livekit-client wrapper (room store, tracks, devices)
      lib/ui/              design-system components
      routes/              pages (see §9)
  deploy/
    compose.yaml           dev stack: livekit, redis, egress, minio, dex
    livekit.yaml           LiveKit config
    egress.yaml            Egress config
    dex.yaml               Dev IdP config (static test users)
  docs/ARCHITECTURE.md     this document
  Makefile                 dev, gen, check, build targets
  .forgejo/workflows/      CI
```

## 4. Identity, auth, and the token model

**Hosts** authenticate with any OIDC provider (authorization code flow,
config-driven: issuer, client ID/secret, redirect URL). On callback, the klisi
server creates a session: an HttpOnly, Secure, SameSite=Lax cookie containing an
HMAC-signed payload (subject, email, name, global-admin capability, expiry). No
session table. Optional comma-separated user and administrator group policies
are evaluated from the verified OIDC `groups` claim at sign-in. Administrators
can manage every room; ordinary hosts manage rooms they own.

**Guests** never authenticate. They exist only as a display name typed on the
pre-join screen and admitted through the lobby.

**LiveKit access tokens** are minted exclusively by the klisi server:

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

## 5. Rooms and meeting URLs

- A room row: `id, slug, name, owner_sub, lobby_enabled (default true), created_at`.
- New rooms receive an opaque UUID v4 slug by default. Owners can replace it
  with a unique 3–64 character lowercase slug containing letters, numbers, and
  single hyphens while the room is idle. Meeting URL: `https://…/m/<slug>`.
- Any authenticated host can create rooms; the creator is the room's owner.
  Rooms are persistent (reusable URLs), meetings are implicit sessions within
  them (LiveKit room created on first join, destroyed on last leave).

## 6. Guest lobby

The lobby lives entirely in the klisi server, in memory. LiveKit is never
involved — an unadmitted guest has no token and cannot touch the media plane.

```
guest                          klisi server                       host
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

1. Host clicks Record → `POST /api/rooms/:slug/recording/start` → server checks
   ownership → `StartRoomCompositeEgress` with S3 output
   (`recordings/<room>/<recording-id>/<yyyy-mm-dd hh-mm> - <meeting>.<ext>`) and
   our **layout URL**. Audio-only recordings use OGG; video composites use MP4.
   The S3 destination and identifying recording metadata travel in every egress
   request; its worker-visible endpoint is configured by
   `KLISI_S3_EGRESS_ENDPOINT`.
2. The layout is a route of our own SPA (`/egress-template`) implementing
   LiveKit's egress template contract (it receives `url`, `token`, `layout`
   query params and joins as a hidden subscriber). Recordings therefore use
   klisi's own tile design — same components as the live room. In development,
   Egress loads the route through Vite at `host.docker.internal`; Vite admits
   that hostname through `server.allowedHosts`.
3. Egress lifecycle webhooks (`egress_started/updated/ended`) hit
   `POST /api/webhooks/livekit` (signature-verified) and drive the
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
owner** paired with klisi — never on the server, and never on anyone else's
machine. The owner can already download the recording, so transcription adds
no new reader of the audio.

**Machines.** A signed-in host pairs a computer running the moil app with
moil's device flow (RFC 8628): the app shows a code and opens
`/machines?code=XXXX-XXXX`, where the host checks the machine's name and the
moil address the app must be pairing with, and confirms. The confirming
session's `sub` becomes the machine's owner; nothing in the request body can
name another. The moil SDK
(`server/third_party/moil`) serves the machine side of the protocol under
`/moil/` — the moil base URL is `<KLISI_BASE_URL>/moil` — and each machine keeps
one WebSocket open to `/moil/v1/connect`. Paired machines live in the
`machines` table, which stores only the SHA-256 of a machine's token. `/machines`
lists the host's own machines and unpairs them. A machine is `idle`, `busy`,
`paused` or `offline`; a state klisi doesn't know, which only a newer moil SDK
could report, shows as `busy`, since moil offers jobs only to idle machines.

**The bundle.** klisi publishes one moil bundle, `transcribe` (Nemotron 3
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

A job is eligible only for the room owner's machines
(`moil.OwnedBy(rooms.owner_sub)`, the room found by ID); administrators can
request a transcript for any room they manage, but it still runs on that room
owner's machines, and `Prepare` checks again that the machine taking an
attempt belongs to the room's current owner. Each attempt may run for three
hours or for one hour plus twice the recording's duration, whichever is
longer: a machine's first attempt also downloads 2.9 GB of models. When a job
succeeds its row becomes `completed` with the speaker count; when it fails,
`failed` with an error the UI can show. Cancellation and server shutdown leave
the row as it is. A `pending` row no machine has finished within 14 days of
its request fails ("No machine transcribed it within 14 days"); requesting it
again retries. The end of a job is recorded even if klisi is stopping; if the
write fails, the row stays `pending` and is retried rather than re-run.

**Files.** Presigned URLs are minted when a machine takes an attempt
(`moil.Job.Prepare`), not at submission, because a job can wait days for a
laptop to wake. They last for the attempt's time limit plus 15 minutes, at
most S3's 7 days: a GET for the recording (input `recording.ogg` or
`recording.mp4`) and a PUT for each output. Machines never write where klisi
serves from: each attempt uploads to a staging directory of its own,

```
transcripts-staging/<recording-id>/<attempt>-<16 hex digits>/transcript.txt
transcripts-staging/<recording-id>/<attempt>-<16 hex digits>/transcript.vtt
```

named for the attempt klisi made it for, and unique by its random part: moil
numbers the attempts of a job submitted again from 1 once it has forgotten the
last run, as it has after klisi restarts. A machine that takes the job again
within ten minutes of klisi making its directory, having let it go before
starting, is handed the same directory, with URLs that expire when the first
ones do; another machine, or the same one later, gets a new one. A machine that
keeps taking a job and letting it go so costs klisi at most one directory every
ten minutes.

and when the job succeeds klisi checks each file (at most 16 MiB), copies it
beside the recording under the recording's basename, as a video player
expects sidecar captions, and removes the staged copy:

```
recordings/<room>/<recording-id>/2026-09-27 14-00 - Standup.ogg
recordings/<room>/<recording-id>/2026-09-27 14-00 - Standup.txt   transcript
recordings/<room>/<recording-id>/2026-09-27 14-00 - Standup.vtt   WebVTT captions
```

**Removing files.** The `object_removals` table lists objects to remove, each
from a due time. Deleting a recording, or its room, deletes the rows and
queues every file of every recording (`store.Recording.ObjectKeys()`) in one
transaction, then removes them at once; the recording reconciler retries any
removal that failed, so a deleted recording never keeps its files, even when
S3 is briefly down. `Prepare` queues each staging key for when its URL
expires, so an upload that arrives late, from a machine that lost klisi but
not S3, is removed too. A transcript copied beside a recording that was
deleted meanwhile is removed again.

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
with its current status. When `KLISI_S3_PUBLIC_ENDPOINT` is plain `http` and
klisi isn't on loopback, machines would refuse its URLs, so jobs fail at once
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
  `setSubscribed` only where reality differs. klisi runs no retry loop against
  the SFU; livekit-client re-establishes subscriptions itself via
  `sendSyncState()`. Unlocking playback must never touch subscriptions.
- **livekit-client is pinned exactly.** It owns the media path; a caret range
  is its own supply of regressions. Upgrade client and server as a tested pair,
  behind the media gate (§13).

`window.klisiDiagnostics()` writes a JSON file with the bounded event ledger,
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
**Geist Mono** for anything machine-flavored: room slugs, timers, participant
counts, keyboard shortcuts. The human room name is shown during join and lobby
transitions; internal room slugs are kept out of the meeting UI.

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

All server config via `KLISI_*` env vars (12-factor, `.env` in dev):

```
KLISI_ADDR=:8080                 KLISI_BASE_URL=http://localhost:8080
KLISI_SESSION_SECRET=…           KLISI_DB_PATH=./data/klisi.db
KLISI_LIVEKIT_URL=ws://…:7880    KLISI_LIVEKIT_PUBLIC_URL=wss://…
KLISI_LIVEKIT_API_KEY=…          KLISI_LIVEKIT_API_SECRET=…
KLISI_OIDC_ISSUER=…              KLISI_OIDC_CLIENT_ID / _CLIENT_SECRET
KLISI_USER_GROUPS=…              KLISI_ADMIN_GROUPS=…
KLISI_S3_ENDPOINT=…              KLISI_S3_PUBLIC_ENDPOINT=…
KLISI_S3_EGRESS_ENDPOINT=…       KLISI_S3_BUCKET / _ACCESS_KEY / _SECRET_KEY
KLISI_S3_REGION=…                KLISI_EGRESS_TEMPLATE_URL=…
KLISI_TRUSTED_PROXIES=…          KLISI_DEV_MODE=false
KLISI_JOIN_RATE_LIMIT=10         KLISI_WAIT_RATE_LIMIT=20
KLISI_LOGIN_RATE_LIMIT=10        KLISI_PAIR_RATE_LIMIT=10
```

The four rate limits are per-client ceilings over a one-minute window, an IPv6
client counting by its /64. They are configuration because the right value
depends on the deployment: a public install wants the defaults, while the media
gate — where every browser shares one container IP — would throttle itself
without raising them. A value that is missing, zero, negative or unparseable
falls back to the default; it never becomes zero, which would deny every
request.

Per-variable reference, defaults, and production rules: `docs/DEPLOYMENT.md`.

## 13. Development environment

`deploy/compose.yaml` runs the platform; the app runs on the host for fast
iteration:

| Service | Port(s) |
|---|---|
| livekit | 7880 (ws/http), 7881 (tcp), 7882/udp (single-port UDP mux) |
| redis | 6379 |
| egress | — (worker; needs livekit + redis + minio) |
| minio | 9000 (S3), 9001 (console) |
| dex | 5556 (issuer), static test users (`host@klisi.dev`) |

`Makefile` targets: `dev` (compose up + Go server + Vite, concurrently),
`gen` (tygo), `check` (vet + go test + svelte-check + Prettier + type drift),
`build` (SPA build → embed → single binary), `clean`.

`make dev` detects the host LAN address and writes it as `KLISI_NODE_IP` in
`deploy/.env`. LiveKit advertises that address to host browsers. Egress shares
LiveKit's network namespace so the same WebRTC candidates work inside its
headless browser, and it stages recordings under `/recordings` on a tmpfs
before upload.

S3 has three deliberate views in development:

| Caller | Endpoint | Why |
|---|---|---|
| klisi server | `http://localhost:9000` | Object management from the host |
| Browser | `http://localhost:9000` | Host-reachable presigned download URLs |
| Egress | `http://minio:9000` | Uploads from the Compose network |

### Media gate stack

`deploy/media-test/compose.yaml` is a second, sealed stack used only by the
media suite: redis, livekit, dex, minio, egress, klisi and a Playwright
`runner`, on a private `10.253.0.0/24` with **no published host ports**. The
browser under test runs inside `runner`, so services are reachable only by
compose DNS name. Configuration is baked into images (`*.Dockerfile`) rather
than bind-mounted, because CI drives compose from inside a container where host
bind mounts do not resolve on the daemon's filesystem.

Dex is present so the suite can hold a real session: `/api/dev/token` mints
only non-host tokens, so without it `/m/[slug]`, the lobby, host grants,
moderation and recording are unreachable from a test.

One deliberate limitation: the stack serves plain http on a non-localhost
origin, so pages are **not secure contexts** and `getUserMedia` does not exist.
Actors therefore join with capture off and publish synthetic canvas/oscillator
tracks, which take the identical `publishTrack` → SFU path. Covering local
capture as well needs the stack served over TLS (klisi *and* LiveKit signalling,
or the page hits mixed content).

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
klisi registered while leaving the SFU connection intact, and `full-reconnect`
does the same thing by a route that happens in production. In both, the probe
(which reads the SDK) passes while the rendered tiles (which read klisi's
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
- Per-IP token buckets limit guest joins (10/min), lobby wait streams (20/min),
  login redirects (10/min) and machines starting a pairing (10/min,
  `POST /moil/v1/pair`, the one moil endpoint without credentials that creates
  state, which must be JSON so a web page can't post one without a CORS
  preflight; anything else is refused before it counts); stale buckets are
  cleaned in memory. IPv6 clients are counted by their /64.
- JSON request bodies are limited to 1 MB before decoding; lobby requests
  expire after 10 minutes.
- Presigned download URLs are short-lived (5 min) and minted per request after
  an ownership check.
- Machines (§8.1): pairing is confirmed only by a signed-in session with the
  CSRF header, and the machine's owner is always that session. Looking up,
  confirming and denying codes is limited to 20 a minute per host and 60 per
  client address (RFC 8628 §5.1), and a host pairs at most 10 machines. The
  confirm page shows klisi's moil address, which the moil app must be pairing
  with. Machine tokens are stored as SHA-256 hashes.
  Transcript jobs go only to the room owner's machines. A machine gets
  presigned URLs for one attempt, valid for its time limit plus 15 minutes: a
  GET for the recording, and a PUT for each output to a staging key klisi
  never serves from; klisi checks the size and copies the file beside the
  recording, and removes staging keys when their URLs expire. The moil app
  accepts plain http only to loopback storage and only when klisi itself is on
  loopback, so production needs an https `KLISI_S3_PUBLIC_ENDPOINT`.
- The transcript bundle runs with the machine owner's permissions and no
  sandbox (moil's alpha). Owners read and approve it in the moil app; klisi
  pins its hash so it cannot change silently.
- No secrets in the SPA: the client knows only its own token and public URLs.
- `window.klisiDiagnostics()` (§9.1) contains participant identities and display
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
MinIO in klisi's own layout; appears on the dashboard; downloads and plays;
host's tab crash does not stop the recording.*

**Phase 5 — Polish and hardening.**
Design QA against §10 across every state (empty, error, denied, expired),
rate limits, CSP, README, CI green end-to-end, `make build` binary verified on
a clean machine.
*AC: full CI matrix green; the binary + compose stack runs the entire Phase
1-4 acceptance list from scratch.*

## 17. Decision log

| Decision | Choice | Why |
|---|---|---|
| Media stack | LiveKit (self-hosted) | Ships reconnection, simulcast, moderation, and Egress recording; Apache 2.0 |
| Fork vs fresh | Fresh repo, fresh history | mirotalksfu is AGPL; klisi derives nothing from it |
| Backend | Single Go binary, SPA embedded | Leanest production artifact; first-party LiveKit Go SDK |
| Frontend | SvelteKit SPA, adapter-static | No Node in prod; SSR worthless behind auth |
| Host auth | Generic OIDC (Dex in dev) | Provider-agnostic by config |
| Chat | Ephemeral only | No storage, no privacy surface; history is a non-goal |
| DB | SQLite (modernc, CGO-free) | Single instance; keeps the static binary |
| Type sync | tygo + CI drift gate | Go structs as single source of truth |
| Recording | Egress room composite + custom template | Server-owned lifetime; recordings wear klisi's design |
| Transcripts | moil jobs on the room owner's own machine | No model or GPU on the server, no fifth process, no new reader of the audio |
| Transcript trigger | Automatic once the owner has paired a machine; otherwise on request | Pairing is the opt-in; no per-recording button for hosts who use it |
| moil SDK | Vendored in `server/third_party/moil` | The forge sits behind Cloudflare Access, so Go can't fetch it in CI or Docker; same precedent as `web/vendor` |
