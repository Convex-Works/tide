# Web

The SvelteKit SPA in `web/`, embedded in the Go binary. Read this before you change
`lib/rtc` or the look of anything.

`RoomState` in `lib/rtc/room.svelte.ts` is the one owner of a meeting's LiveKit
`Room`: UI components read its state and call its methods. The other files that
import `livekit-client` take its types, enums and helpers. The recorder's
`/egress-template` is the exception, with a `Room` of its own. Chat is a thin layer
on LiveKit data messages, ephemeral by design.

## Media state is a convergent projection

**Client state is a convergent projection of LiveKit's own maps, never a log of
events.** Everything the meeting UI shows is re-derived by `reconcileMedia()` from
`room.localParticipant` and `room.remoteParticipants`. Events are only ever a _hint
that it is time to re-derive_; they never carry the state itself.

The rules that follow from it, all of them load-bearing:

- **No handler may throw into livekit-client.** Every `room.on(...)` goes through
  `RoomState.listen()`, which catches, records the fault and logs it. LiveKit's
  emitter dispatches with no `try`/`catch`, and `getOrCreateParticipant` emits
  `ParticipantConnected` _before_ it installs that participant's track-event
  forwarding, so one escaped exception wires that participant to nothing for the
  rest of the session. A bare `catch {}` is not acceptable either: the fault is
  recorded so the next incident is attributable.
- **Nothing reconciles inline.** Handlers call `markProjectionDirty()`, and
  reconciliation is coalesced onto a microtask. That is not only for efficiency: the
  projection observes the SDK's state _after_ the synchronous transition that
  produced the events, so `handleRestarting()` (which disconnects every participant
  before it flips the room to `Reconnecting`) can't empty the stage.
- **A deferred reconcile is never discarded.** While the room is not `Connected`
  the projection is frozen and the dirty flag _stays set_; the transition back to
  `Connected` flushes it.
- **A heartbeat is the floor.** While connected, the projection is marked dirty every
  2 s. Any dropped event, ours or LiveKit's, is then a short self-heal instead of a
  page reload. It is free on an idle meeting because a reconcile whose signature is
  unchanged doesn't republish.
- **Playback recovery is attempted once, not chased.** Unblocking autoplay needs a
  user gesture, so retrying without one can't succeed. livekit-client reports
  playback status from two probes that `startAudio()` drives together, the media
  elements and the AudioContext. While an attempt is in flight those two disagree
  _by construction_, and retrying on either report is an unbounded microtask loop
  that pins the CPU. So: one automatic attempt, re-armed only by a healthy report
  raised outside an attempt, then the unlock control waits for the gesture.
- **A Lost participant is not shown.** The server keeps an abruptly departed
  participant in its resume grace window and re-announces them on every reconnect
  sync, so `remoteParticipants` alone renders ghost tiles. The projection filters
  remote participants whose connection quality is `Lost`. A resume changes their
  quality and the tile returns. `Unknown` is never filtered: a fresh joiner reports
  `Unknown` until the first quality update.
- **Subscriptions are declarative.** `applySubscriptions()` states what should be
  subscribed and calls `setSubscribed` only where reality differs. tide runs no
  retry loop against the SFU; livekit-client re-establishes subscriptions itself.
  Unlocking playback must never touch subscriptions.
- **livekit-client is pinned exactly.** It owns the media path, and a caret range is
  its own supply of regressions. Upgrade client and server as a tested pair, behind
  the [media gate](media-gate.md).

`window.tideDiagnostics()` writes a JSON file with the bounded event ledger, recorded
handler faults and the current subscription state, for
[incident attribution](../operations/deployment.md#diagnose-a-media-incident).

## Meeting chrome

`web/e2e/meeting-chrome.spec.ts` holds the layout. Three rules it serves:

- Hosts and guests see mic and camera in the same place. Record never sits between
  them.
- The meeting link appears only in the details popover. Room slugs stay out of the
  rest of the meeting UI.
- Connection state stays on the right. Signs of who else is listening (REC, BOT) sit
  beside the room's name.

## Design language

Cursor-grade density with Convex Works' materials: ivory paper, near-black ink, one
electric blue. Calm, small, precise. Controls are icon-first with tooltips, and words
appear where they carry information. The tokens are in `web/src/app.css`.

**Two surfaces.** The shell (dashboard, pre-join, settings) is light. The meeting
stage is near-black so video tiles carry the colour. Both share one accent and one
geometry, so the room reads as the same product with the lights dimmed.

**Type.** Inter for all UI, 13px base. Geist Mono for machine-flavoured text: room
slugs being edited, participant counts, keyboard shortcuts. Words people read in the
meeting (the clock, names, "(You)") stay in Inter.

**Geometry.** 4px spacing base, controls 28px tall, panel padding 12px, gaps 8px.
Radius 4px on controls, 6px on cards and panels, 8px on video tiles. Borders are 1px,
always. No shadows except one elevation for menus and dialogs.

**Icons.** phosphor-svelte, 16px, regular weight (20px only on pre-join device
controls). No filled or duotone variants.

**Roles are words, not badges.** No pills or outlined chips for roles. The local
participant is "Name (You)", and the host carries a secondary line, "Meeting host". A
bot carries the word "Bot" with a robot icon, in the same ink.

**Motion.** 120ms ease-out on hovers and panel slides. The only ambient animation
permitted is the recording hairline's pulse, and `prefers-reduced-motion` turns it
into a static red line.

**Voice.** Sentence case, plain verbs, no filler. "Start recording" → toast "Recording
started". Errors say what happened and what to do next.

### The hairline

A 2px line across the very top of the app chrome that doubles as the status channel:
accent blue when connected, amber while reconnecting, pulsing `--rec` red while the
room is being recorded. State lives in the chrome, not in a toast.

A bot in the room adds a static `--ok` green line of its own rather than another
state, because it is the only thing that tells guests a program is listening, and no
other state may hide it. Alone it takes the whole width; beside recording, reconnecting
or offline it takes the right half. The BOT chip says in words what it means, as REC
does for recording.
