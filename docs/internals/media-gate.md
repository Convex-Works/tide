# The media gate

Real meetings against the real media server and recorder, in CI on every pull
request. Nothing is deferred to a nightly job. Read this before you change a media
spec or an assertion in `web/e2e/harness/`.

It runs, in order: the lifecycle scenarios, recording, the reliability suite
(chromium and webkit), the seeded fuzzer, and a rerun under `netem` loss and latency.
The lifecycle scenarios and the audio recording scenario run on chromium only:
Playwright's Linux WebKit deadlocks in its GStreamer media backend under them
(issue #55), a code path Safari doesn't have.

`make media` runs it as CI does, and `make media-dev ARGS="…"` keeps the stack up
between runs.

## The rules the suites encode

**Prove presence before flow.** `expectMediaInvariant` only checks that whatever a
client already knows about is attached and receiving RTP, so it passes vacuously when
a client never learned a participant published at all, which is the failure mode every
media incident here has had. Scenarios therefore assert that an _existing_ participant
converges on a change: a later joiner, a reload, a mute, a screen share, a departure,
a reconnect. A test whose receiver joins after the publications already exist is
testing what a page reload does.

**A guard that swallows is the same bug.** `failNextParticipantEntered` and
`failNextReconcile` inject a throw into a handler
([why](web.md#media-state-is-a-convergent-projection)), and both scenarios also assert
the fault was _recorded_.

**Rendered tiles are their own witness.** A client must catch up from LiveKit's own
maps without being told. `deafen()` removes every listener tide registered while
leaving the SFU connection intact, and `full-reconnect` does the same by a route that
happens in production. In both, the probe (which reads the SDK) passes while the
rendered tiles (which read tide's projection) are the only witness that the state
reached the user, so both are asserted, separately, in that order.

**The heartbeat costs nothing when idle.** A scenario watches an idle meeting for 60 s
and requires the reconcile count to climb while the republish count stays flat.

## The sealed stack

`deploy/media-test/compose.yaml` is used only by this suite: a private network with
**no published host ports**, and the browser under test inside the `runner` container,
so services are reachable only by compose DNS name. tide runs its media server
embedded, as production does, and the recorder shares tide's network namespace.

- Configuration is baked into images rather than bind-mounted, because CI drives
  compose from inside a container where host bind mounts don't resolve on the daemon's
  filesystem.
- Dex is present so the suite can hold a real session. `/api/dev/token` mints only
  non-host tokens, so without it the lobby, host grants, moderation and recording are
  unreachable from a test.
- The recorder signals at `ws://127.0.0.1:7880`, not `localhost`: the media server
  listens on IPv4 loopback only, and `localhost` may resolve to `::1` first.
- The rate limits are raised, because every browser shares one container address.

**One deliberate limitation.** The stack serves plain http on a non-localhost origin,
so pages are not secure contexts and `getUserMedia` doesn't exist. Actors join with
capture off and publish synthetic canvas and oscillator tracks, which take the identical
`publishTrack` path to the SFU. Covering local capture as well needs the stack served
over TLS.
