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
- Server-owned room recording with download and delete management

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

## Recording

The server starts a LiveKit room-composite Egress job and includes the S3
destination in that request. Egress renders klisi's own `/egress-template`,
writes OGG audio or MP4 video to S3-compatible storage, and reports state
through signed LiveKit webhooks. Object names include the UTC start time and
meeting name, with identifying recording metadata stored alongside the file.

## Production notes

Configuration is described in [Architecture §12](docs/ARCHITECTURE.md#12-configuration),
and the service topology and address boundaries are described in
[Architecture §13](docs/ARCHITECTURE.md#13-development-environment). Build the
deployable binary with `make build`; production still requires LiveKit, Redis,
Egress, and S3-compatible object storage.

klisi is an AGPL-free, fresh-history implementation and derives no code from
the AGPL-licensed mirotalksfu project. That separation is recorded in the
[decision log](docs/ARCHITECTURE.md#17-decision-log).
