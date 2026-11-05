# Development

You need Go and Node at the versions in `server/go.mod` and `web/package.json`, `uv`
for `make check`, and Docker with Compose for anything past the anonymous mode. The
`Makefile` lists every target.

The anonymous mode needs no stack: `make build && ./bin/tide`.

## The dev stack

`make dev` is the signed-in deployment with recording. `deploy/compose.yaml` runs what
tide doesn't contain (Dex, MinIO, Redis, the recorder), and tide, with its media
server, runs on the host beside Vite for fast iteration. Open `http://localhost:5173`
and sign in as `host@tide.dev` / `tide-dev`.

Stop tide and Vite with Ctrl-C, and the containers with
`docker compose -f deploy/compose.yaml down`.

What tends to surprise:

- **The media address.** The media server must advertise an address both host browsers
  and the recorder's headless browser in Docker can reach. `make dev` detects the LAN
  address with macOS's `ipconfig` and writes it to `deploy/.env`. On Linux, set
  `TIDE_MEDIA_NODE_IP` in `.env` at the repository root.
- **Three S3 views.** tide and browsers reach MinIO at `localhost:9000`, and the
  recorder at `minio:9000`, from the Compose network.
- **The layout page.** The recorder loads `/egress-template` through Vite at
  `host.docker.internal`.
- **`TIDE_DEV_MODE`** supplies the publicly known value for any secret left unset, and
  exposes `/api/dev/token`, which mints only non-host tokens. It turns nothing else on,
  and tide refuses it unless the base URL is local.
- **Vite proxies** `/api`, `/moil` and `/rtc`, WebSockets included, so the SPA signals
  through its own origin as it does in production.

## Checks

`make check` runs what a commit needs: `go vet`, `go test`, `svelte-check`, Prettier,
type drift and the transcribe bundle's tests. CI adds the rest:

- **The browser specs that need no server** run against a mock API and a fake SFU.
  `.github/workflows/ci.yml` lists them by name, so a new one must be added there.
  Run one with `npx playwright test <spec>` in `web/`.
- **`site/`** has its own `npm run check` and `npm run lint`. The check fails unless
  `site/src/lib/config.ts` documents exactly the `TIDE_*` variables the server reads.
- **The [media gate](../internals/media-gate.md)** is `make media`, and
  `make media-dev ARGS="…"` keeps its stack up between runs. Stop that with
  `make media-dev ARGS="--down"`.

Not in CI: `make moil-e2e` runs transcripts end to end with the real moil binary
([server/e2e](../../server/e2e/README.md)). It builds moil from a checkout beside this
one.

## moil

`server/third_party/moil` is a vendored copy of the SDK. Fix moil upstream, then
refresh with `scripts/vendor-moil.sh`, which records the commit in `COMMIT`.

`server/internal/transcripts/bundle/` belongs to tide, with its own
[README](../../server/internal/transcripts/bundle/README.md).
