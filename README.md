# tide

tide is a small, self-hosted video meeting service, and one executable: a Go
binary with the SvelteKit app and the media server inside it. Recording adds
one more process, the recorder, and an S3-compatible bucket for the files.

The feature list is frozen:

- Reusable meeting links, with OIDC sign-in for hosts or none at all
- Guest lobby with admit and deny controls
- Microphone, camera, screen sharing, device selection, and reconnection
- Per-participant controls for local camera hiding and host mute/remove
- Ephemeral in-room chat
- Server-owned room recording with download and delete management, and
  speaker-labelled transcripts made on the host's own computer

The system shape and design rules are in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Quickstart

Requires Go 1.26 or newer and Node 22 or newer.

```sh
cd web && npm install && cd ..
make build
./bin/tide
```

Open `http://localhost:8080` and create a room: that is a working, anonymous
meeting server, with nothing else running. Open its meeting link in a private
window to join as a guest. To run it for real, see
[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md): one binary behind a TLS proxy, Docker
Compose with the recorder, or Kubernetes.

## Development

`make dev` runs the signed-in deployment with recording. It needs Docker with
Compose for what tide doesn't contain: Dex for sign-in, MinIO for storage and
the recorder.

```sh
make dev
```

Open `http://localhost:5173`. The Dex test login is `host@tide.dev` with
password `tide-dev`. `make dev` writes the detected LAN address to
`deploy/.env` as the media address, starts the Compose stack, then runs the Go
server and Vite.

Useful targets:

```sh
make check   # server tests and vet, web checks, formatting, generated-type drift
make gen     # regenerate TypeScript API types from Go
make build   # build bin/tide with the SPA embedded
```

## End-to-end tests

Start `make dev` first so tide, the recorder, MinIO, Dex and Vite are
available. The Playwright setup uses Chromium fake media devices.

```sh
cd web
npx playwright test
```

### Media gate

The media suite runs against a real SFU in its own sealed Compose stack
(`deploy/media-test/`) — tide with its media server, the recorder, Dex, MinIO,
and the browser itself, on a private network with no published ports. It is
the merge gate, and it runs in full on every pull request.

```sh
make media                                              # exactly what CI runs
make media-dev ARGS="media-lifecycle.spec.ts --project=chromium"   # iterate
```

`make media-dev` keeps the stack up between runs and rebuilds only what
changed. Note that changes under `web/src` need the tide image rebuilt,
because the SPA is embedded in the Go binary; the script handles that.

`media-lifecycle.spec.ts` holds the scenarios where a participant is *already*
in the room when something changes — a later joiner, a reload, a mute, a screen
share, a departure, a reconnect. See
[Architecture §14](docs/ARCHITECTURE.md#14-ci) for the rule those encode and
why an assertion has to prove presence before it proves flow.

### Transcripts with the real moil

`make moil-e2e` builds the moil command line from a checkout at `../moil`
(or `MOIL_REPO`) and runs `server/e2e`: tide in process, with machines
that run the real `moil pair`, `moil approve` and `moil agent`, real uv,
and MinIO in Docker. It isn't part of `make check`; see
[server/e2e/README.md](server/e2e/README.md).

## Recording

Recording is on when hosts sign in and `TIDE_S3_ENDPOINT` is set. tide starts
a room-composite job on the recorder and includes the S3 destination in that
request. The recorder renders tide's own `/egress-template`, writes OGG audio
or MP4 video to S3-compatible storage, and reports state through signed
webhooks. Object names include the UTC start time and meeting name, with
identifying recording metadata stored alongside the file.

## Transcripts

Hosts can pair their own computer with tide through the
[moil](https://git.convex.works/ConvexWorks/moil) app. From then on, each
recording of a room they own is transcribed on that computer, never on the
server: the machine downloads the recording, runs the transcription bundle
tide publishes (Nemotron 3 Diarization and Parakeet), and returns a
plain-text transcript and WebVTT captions, which tide keeps beside the
recording. Older recordings can be transcribed on request. Jobs only go to the
room owner's own machines, and the owner approves the bundle's exact code in
the app first. A machine's first transcript downloads 2.9 GB of models, and
transcribing uses up to 10 GB of memory. On an M3 Max an hour of meeting takes
about a minute and a half on the GPU, or four minutes on the CPU.

Transcripts need recording, and are off unless the server runs with
`TIDE_TRANSCRIPTS=true` ([docs/DEPLOYMENT.md](docs/DEPLOYMENT.md#serve-transcripts)):
moil is alpha.
`make dev` turns them on. To try them in development, open `/machines` and
choose **Add a machine**, or pair from a terminal with the moil CLI:

```sh
moil pair http://localhost:5173/moil   # confirm the code on the page it opens
moil review tide                      # the transcription bundle and its hash
moil approve tide <hash>              # read it in full, then approve it
moil agent                             # take jobs until Ctrl-C
```

[Architecture §8.1](docs/ARCHITECTURE.md#81-transcripts) has the design.

## Production notes

[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) covers the modes, ports, every
variable and the three ways to run tide. Configuration is specified in
[Architecture §12](docs/ARCHITECTURE.md#12-configuration). Restarting tide
restarts its media server and ends live meetings; an operator who can't
accept that keeps an external media server with `TIDE_MEDIA_URL`.

tide is an AGPL-free, fresh-history implementation and derives no code from
the AGPL-licensed mirotalksfu project. That separation is recorded in the
[decision log](docs/ARCHITECTURE.md#17-decision-log).
