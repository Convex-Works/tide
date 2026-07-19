# klisi — Architecture

klisi is a lean, self-hosted video meeting product. It does few things well:
meeting URLs, host auth (OIDC), a guest lobby, mic/cam/screen controls, device
selection, a participant list with moderation, reconnection, ephemeral chat, and
Zoom-like server-orchestrated recording with recording management.

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
                          │  (Chrome + GST)   │   MP4       │       │  S3 /  │
                          └──────────────────┘─────────────►└──────►│ MinIO  │
                                                                    └────────┘
```

| Component | What it is | What it owns |
|---|---|---|
| **klisi server** | Single Go binary, embeds the built SPA via `embed.FS` | Auth, sessions, rooms, tokens, lobby, moderation API, recording lifecycle + management, webhooks |
| **LiveKit server** | Stateless Go binary (upstream, Apache 2.0) | All media: SFU, simulcast, adaptive streaming, ICE, reconnection/resume |
| **Egress worker** | Upstream worker service (headless Chrome + GStreamer) | Renders our composite layout page, encodes MP4, writes directly to S3 |
| **Redis** | Required by LiveKit once Egress runs | Egress job queue, LiveKit node state |
| **MinIO (dev) / S3 (prod)** | Object storage | Recording files |
| **Dex (dev only)** | OIDC identity provider | Host login in the dev stack; any OIDC provider in prod |

Single-instance by design for v1: SQLite for durable state, in-memory for
ephemeral state (lobby). Nothing in the design blocks moving to Postgres +
multi-replica later; nothing pays that cost now.

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
      store/               SQLite (modernc.org/sqlite, CGO-free)
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
HMAC-signed payload (subject, email, name, expiry). No session table.

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
- Slug format: `word-word-NNN` (e.g. `calm-otter-412`) — readable, typeable,
  unguessable enough combined with the lobby. Meeting URL: `https://…/m/calm-otter-412`.
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

## 8. Recording

Room-composite recording via LiveKit Egress. The recording's lifetime is bound
to the egress job, not to any participant's tab.

1. Host clicks Record → `POST /api/rooms/:slug/recording/start` → server checks
   ownership → `StartRoomCompositeEgress` with S3 output
   (`recordings/<room>/<ts>.mp4`) and our **layout URL**.
2. The layout is a route of our own SPA (`/egress-template`) implementing
   LiveKit's egress template contract (it receives `url`, `token`, `layout`
   query params and joins as a hidden subscriber). Recordings therefore use
   klisi's own tile design — same components as the live room.
3. Egress lifecycle webhooks (`egress_started/updated/ended`) hit
   `POST /api/webhooks/livekit` (signature-verified) and drive the
   `recordings` table: `id, room_id, egress_id, status, started_by, started_at,
   ended_at, duration_s, s3_key, size_bytes`.
4. Management: `GET /api/recordings?room=`, `DELETE /api/recordings/:id`,
   `GET /api/recordings/:id/download` → presigned S3 URL. Surfaced on the
   dashboard per room.
5. In-room, everyone sees recording state (webhook → LiveKit room metadata
   update → client event), rendered by the hairline (§10).

Stop on: explicit stop, room emptying (LiveKit auto-ends the egress), or
egress failure (status `failed`, surfaced in management UI — never silent).

## 9. Frontend

SvelteKit (Svelte 5 runes) + `adapter-static`, embedded in the Go binary. Vite
dev server proxies `/api` to the Go server during development.

Routes:

| Route | Page |
|---|---|
| `/` | Dashboard: your rooms, create room, recent recordings (auth) |
| `/login` | OIDC entry (redirects to provider) |
| `/m/[slug]` | Pre-join → lobby wait → room (one route, three states) |
| `/rooms/[slug]` | Room settings + its recordings (auth, owner) |
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
--paper:    #F5F5F0;   /* app background */
--surface:  #ECECE6;   /* cards, inputs */
--surface-2:#E3E3DB;   /* hover, wells */
--border:   #D8D8CE;
--ink:      #171717;
--ink-2:    #6E6E64;   /* secondary text */
--accent:   #2320E6;   /* the blue — links, primary actions, focus */
--accent-hover: #1B18C4;

/* stage (dark, in-meeting) */
--stage:    #0F0F0E;
--panel:    #161615;   /* side panels, control bar */
--panel-2:  #1E1E1C;
--border-d: #2A2A27;
--text:     #EDEDE6;   /* warm off-white, echoes paper */
--text-2:   #8F8F85;
--accent-d: #5B58FF;   /* accent lightened for dark ground */

/* semantic (both surfaces) */
--rec:      #E5484D;   /* recording */
--ok:       #4FA36F;
--warn:     #D9A03F;
```

Typography: **Inter** (variable) for all UI — base 13px/20px, weights 450/550,
scale 11 / 12.5 / 13 / 15 / 18, with 24px reserved for the pre-join room name.
**Geist Mono** for anything machine-flavored: room slugs, timers, participant
counts, keyboard shortcuts. The slug is always a mono chip
(`calm-otter-412` in a bordered pill) — it is the product's recurring artifact.

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
KLISI_LIVEKIT_URL=ws://…:7880    KLISI_LIVEKIT_API_KEY / _API_SECRET
KLISI_OIDC_ISSUER=…              KLISI_OIDC_CLIENT_ID / _CLIENT_SECRET
KLISI_S3_ENDPOINT=…              KLISI_S3_BUCKET / _ACCESS_KEY / _SECRET_KEY
```

## 13. Development environment

`deploy/compose.yaml` runs the platform; the app runs on the host for fast
iteration:

| Service | Port(s) |
|---|---|
| livekit | 7880 (ws/http), 7881 (tcp), 50000-50100/udp |
| redis | 6379 |
| egress | — (worker; needs livekit + redis + minio) |
| minio | 9000 (S3), 9001 (console) |
| dex | 5556 (issuer), static test users (`host@klisi.dev`) |

`Makefile` targets: `dev` (compose up + Go server + Vite, concurrently),
`gen` (tygo), `check` (vet + staticcheck + go test + svelte-check + lint),
`build` (SPA build → embed → single binary), `clean`.

## 14. CI

Forgejo Actions (`.forgejo/workflows/ci.yml`), on every push/PR:

1. **server** — `go vet`, `staticcheck`, `go test ./...`
2. **web** — `svelte-check`, prettier check, `vite build`
3. **typesync** — `make gen && git diff --exit-code` (§11)
4. **build** — full binary build (SPA embed included) as the merge gate

## 15. Security notes

- All capability checks are server-side: room ownership on every moderation and
  recording endpoint; tokens minted only after policy passes.
- LiveKit webhook requests are verified against the API key/secret signature.
- Session cookies: HttpOnly, Secure, SameSite=Lax, HMAC-signed, short expiry.
- Guests are rate-limited on join requests per IP; lobby requests expire.
- Presigned download URLs are short-lived (5 min) and minted per request after
  an ownership check.
- No secrets in the SPA: the client knows only its own token and public URLs.
- CSP on the shell; the SPA makes no third-party requests (fonts self-hosted).

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
