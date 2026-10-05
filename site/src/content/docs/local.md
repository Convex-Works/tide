# Local development

Run tide on one machine.

## Just tide

Requires: Go 1.26+, Node 22+.

```sh
cd web && npm install && cd ..
make build
./bin/tide
```

Open `http://localhost:8080` and create a room. That is an anonymous meeting server with nothing else running; open the meeting link in a private window to join as a guest.

## The whole stack

Requires: Go 1.26+, Node 22+, Docker with Compose.

```sh
make dev
```

`make dev` runs tide with sign-in and recording: the sign-in server, the object store, Redis and the recorder in Docker, tide and the web app on the host. Open `http://localhost:5173` and sign in as `host@tide.dev` with password `tide-dev`.

`make dev` is made for macOS with Docker Desktop. On Linux, recordings need one line in `.env` at the repository root: `TIDE_MEDIA_NODE_IP=<your LAN address>` (`make dev` finds it with macOS's `ipconfig`). Meetings work without it.

| Command      | Does                                                  |
| ------------ | ----------------------------------------------------- |
| `make dev`   | run everything locally                                |
| `make check` | Go vet and tests, web checks, formatting, type drift  |
| `make build` | build `bin/tide` with the web app embedded            |
| `make media` | the full media test suite in Docker, about 16 minutes |

Stop with Ctrl-C. Containers keep running until `docker compose -f deploy/compose.yaml down`.

`make dev` uses development mode, which accepts the secrets committed to the repository, and turns transcripts on. Never use it in production.
