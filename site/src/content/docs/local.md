# Local development

You need Go 1.26 or later and Node 22 or later.

## Only tide

```sh
cd web && npm install && cd ..
make build
./bin/tide
```

Open `http://localhost:8080` and make a room. To join as a guest, open the room link in a private window.

## With sign-in and recording

You also need Docker with Compose.

```sh
make dev
```

Open `http://localhost:5173`. Sign in as `host@tide.dev` with the password `tide-dev`.

`make dev` is for macOS. On Linux, add `TIDE_MEDIA_NODE_IP=<your LAN address>` to `.env` in the repository root, or recordings fail.

| Command      | Does                                        |
| ------------ | ------------------------------------------- |
| `make dev`   | runs everything                             |
| `make check` | runs the tests and checks                   |
| `make build` | builds `bin/tide` with the web app in it    |
| `make media` | runs the media tests in Docker (16 minutes) |

To stop, press Ctrl-C. Then run `docker compose -f deploy/compose.yaml down`.
