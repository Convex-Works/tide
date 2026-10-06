# Media convergence refactor — 2026-08-03

Status record, not the contract. The rules this work established live in
`docs/ARCHITECTURE.md` §9.1 and §14; read those. This file exists only to say
what changed, what it was measured against, and what is still open. Delete it
when the two open items below are closed.

## What the incident was

`RoomState` was a reducer over `livekit-client`'s event stream.
`reconcileMedia()` re-derived everything from `room.remoteParticipants` and was
already idempotent and correct — but it only ever ran when an event fired.
There was no periodic re-derivation, so a single lost event meant permanently
stale UI until reload.

Events were lost two ways, both verified against livekit-client 2.20.1:

- **LiveKit dropped them.** `ParticipantConnected`, `TrackPublished`,
  `TrackSubscribed`, `TrackMuted`/`Unmuted`, `TrackSubscriptionStatusChanged`
  and `ParticipantActive` all route through `emitWhenConnected`, which buffers
  only while `Reconnecting`/`isResuming`/`pendingReconnect` and otherwise
  returns `false` without emitting unless the state is exactly `Connected`.
  `TrackUnpublished`/`TrackUnsubscribed` use plain `emit`, so teardown was
  delivered when setup was not — ghost tiles followed from the asymmetry.
- **tide dropped them again.** Three drop windows, all now deleted:
  `reconcileMedia()`'s early return during `Reconnecting`/`SignalReconnecting`;
  a `ConnectionStateChanged` handler that never reconciled at the moment
  reconciliation became legal again; and `scheduleRemovalReconcile()`, a 100 ms
  timer that discarded its reconcile with no retry when the room was not
  `Connected` — with four call sites, one of which routed a _subscription_
  event through the removal path.

On top of that, LiveKit's `getOrCreateParticipant` does
`remoteParticipants.set(...)` → `emitWhenConnected(ParticipantConnected)` →
**then** installs that participant's track-event forwarding. Its emitter
dispatches through a bare `ReflectApply` loop with no `try`/`catch`, so one
escaped exception aborted construction after the map insert and the forwarding
was never installed for the rest of the session.

## What landed

| Phase | Change                                                                                                                                                                                            | Turns green                                 |
| ----- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------- |
| 1     | `RoomState.listen()` guards every registration; faults are recorded and logged, never swallowed. Sounds run off the emit stack via `queueMicrotask`.                                              | the two fault scenarios                     |
| 2     | Events mark the projection dirty; a coalescing microtask flush plus a 2 s heartbeat reconcile it. A deferred flush is never discarded. A signature check keeps an idle meeting from republishing. | `deafen`, `full-reconnect`                  |
| 3     | `applySubscriptions()` derives subscriptions from `hiddenCameraIdentities` in both directions. The retry state machine is gone; playback unlock no longer touches subscriptions.                  | — (removes a live hazard)                   |
| 4     | Blocked playback surfaces a "Tap to hear audio" control, and recovery is attempted once rather than chased.                                                                                       | the three vacuous `Media paused` assertions |
| 5     | The event ledger and fault log ship in production behind `window.tideDiagnostics()`.                                                                                                             | — (attribution)                             |

### A latent spin the Phase 4 test exposed

Writing a test that keeps `play()` failing _across_ attempts — nothing had done
that before; the old hook rejected once — surfaced a CPU-pinning loop that was
already in the code. `Room.startAudio()` awaits the media elements and
`acquireAudioContext()` together, and the latter emits
`AudioPlaybackStatusChanged(true)` when the context resumes. So one recovery
attempt emits _both_ signals: the elements report blocked, the context reports
healthy. tide retried on the blocked report and re-armed on the healthy one,
which is a closed cycle — measured at ~12 000 `play()` calls per second, enough
to starve the page completely.

It needed a browser that blocks element autoplay while the AudioContext runs,
which is why no real session had hit it yet. The rule is now in ARCHITECTURE
§9.1, and the gate asserts blocked playback stays quiet.

Two design points worth keeping in view, because they are easy to undo:

- **The freeze during reconnect stays.** A full restart tears down every remote
  participant _before_ it flips the state, so projecting inline would empty the
  keyed `{#each}` and churn every tile. The fix was never to remove the freeze
  but to guarantee the flush on the way out of it — and the microtask coalescing
  is what makes the flush observe the state _after_ the SDK's synchronous
  transition rather than in the middle of it.
- **The heartbeat must stay free.** `reconcileMedia` assigns a fresh
  `ParticipantView[]`, which invalidates every tile. Without the signature
  check an idle meeting pays that twice a second forever. A gate scenario
  watches 60 s of idle and requires the reconcile count to climb while the
  republish count stays at zero.

## Phase 6 — split `RoomState`: declined

The only item with no reliability payoff, and a contract change: ARCHITECTURE
§9 specifies one `lib/rtc/room.svelte.ts` class wrapping livekit-client's
`Room`. Revisit only with a reason that is not "the file is long".

## Still open

- **Triage `node-failure` and `disconnect-signal-on-resume`.** Both leave the
  host with zero remote participants, i.e. livekit-client did not recover at
  the SDK level rather than tide mis-projecting. The media stack is
  single-node, so this may be a harness artifact. They are `test.skip` with the
  reason recorded; decide, then either gate them or delete them.
- **TLS for the media stack.** The gate serves plain http on a non-localhost
  origin, so pages are not secure contexts and `getUserMedia` does not exist;
  actors publish synthetic tracks and local capture is uncovered. Closing it
  needs TLS for tide _and_ LiveKit signalling, or the page hits mixed content.

`livekit-client` is now pinned exactly (2.20.1); upgrade it and LiveKit server
as a tested pair behind the gate.
