# klisi

klisi is a small, self-hosted video meeting service. The production artifact is
one Go binary with an embedded SvelteKit app; LiveKit handles media, Redis backs
LiveKit jobs, Egress records meetings, and S3-compatible storage keeps the files.

The feature list is frozen:

- Reusable meeting links and OIDC sign-in for hosts
- Guest lobby with admit and deny controls
- Microphone, camera, screen sharing, device selection, and reconnection
- Per-participant controls for local camera hiding and host mute/remove
- Ephemeral in-room chat
- Server-owned room recording with download and delete management, and
  speaker-labelled transcripts made on the host's own computer

The system shape and design rules are in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Quickstart

Requires Go 1.24 or newer, Node 22 or newer, and Docker with Compose. Install
the web dependencies once, then start the app and its development services:

```sh
cd web
npm install
cd ..
make dev
```

Open `http://localhost:5173`. The Dex test login is `host@klisi.dev` with
password `klisi-dev`. `make dev` writes the detected LAN address to
`deploy/.env`, starts the Compose stack, then runs the Go server and Vite.

Useful targets:

```sh
make check   # server tests and vet, web checks, formatting, generated-type drift
make gen     # regenerate TypeScript API types from Go
make build   # build bin/klisi with the SPA embedded
```

## End-to-end tests

Start `make dev` first so LiveKit, Redis, Egress, MinIO, Dex, the server, and
Vite are available. The Playwright setup uses Chromium fake media devices.

```sh
cd web
npx playwright test
```

### Media gate

The media suite runs against a real SFU in its own sealed Compose stack
(`deploy/media-test/`) — LiveKit, Redis, Dex, MinIO, Egress, klisi, and the
browser itself, on a private network with no published ports. It is the merge
gate, and it runs in full on every pull request.

```sh
make media                                              # exactly what CI runs
make media-dev ARGS="media-lifecycle.spec.ts --project=chromium"   # iterate
```

`make media-dev` keeps the stack up between runs and rebuilds only what
changed. Note that changes under `web/src` need the klisi image rebuilt,
because the SPA is embedded in the Go binary; the script handles that.

`media-lifecycle.spec.ts` holds the scenarios where a participant is *already*
in the room when something changes — a later joiner, a reload, a mute, a screen
share, a departure, a reconnect. See
[Architecture §14](docs/ARCHITECTURE.md#14-ci) for the rule those encode and
why an assertion has to prove presence before it proves flow.

### Transcripts with the real moil

`make moil-e2e` builds the moil command line from a checkout at `../moil`
(or `MOIL_REPO`) and runs `server/e2e`: klisi in process, with machines
that run the real `moil pair`, `moil approve` and `moil agent`, real uv,
and MinIO in Docker. It isn't part of `make check`; see
[server/e2e/README.md](server/e2e/README.md).

## Recording

The server starts a LiveKit room-composite Egress job and includes the S3
destination in that request. Egress renders klisi's own `/egress-template`,
writes OGG audio or MP4 video to S3-compatible storage, and reports state
through signed LiveKit webhooks. Object names include the UTC start time and
meeting name, with identifying recording metadata stored alongside the file.

## Transcripts

Hosts can pair their own computer with klisi through the
[moil](https://git.convex.works/ConvexWorks/moil) app. From then on, each of
their recordings is transcribed on that computer, never on the server: the
machine downloads the recording, runs the transcription bundle klisi publishes
(Nemotron 3 Diarization and Parakeet), and uploads a plain-text transcript and
WebVTT captions beside the recording. Jobs only go to the room owner's own
machines, and the owner approves the bundle's exact code in the app first.

To try it in development, open `/machines` and choose **Add a machine**, or
pair from a terminal with the moil CLI:

```sh
moil pair http://localhost:5173/moil   # confirm the code on the page it opens
moil review klisi                      # the transcription bundle and its hash
moil approve klisi <hash>              # read it in full, then approve it
moil agent                             # take jobs until Ctrl-C
```

[Architecture §8.1](docs/ARCHITECTURE.md#81-transcripts) has the design.

## Production notes

Configuration is described in [Architecture §12](docs/ARCHITECTURE.md#12-configuration),
and the service topology and address boundaries are described in
[Architecture §13](docs/ARCHITECTURE.md#13-development-environment). Build the
deployable binary with `make build`; production still requires LiveKit, Redis,
Egress, and S3-compatible object storage.

klisi is an AGPL-free, fresh-history implementation and derives no code from
the AGPL-licensed mirotalksfu project. That separation is recorded in the
[decision log](docs/ARCHITECTURE.md#17-decision-log).
