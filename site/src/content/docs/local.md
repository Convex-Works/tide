# Local development

Run tide and everything it needs on one machine, with a built-in sign-in server.

Requires: Go 1.24+, Node 22+, Docker with Compose.

```sh
cd web && npm install && cd ..
make dev
```

`make dev` starts the services in Docker, then runs tide and the web app. Open `http://localhost:5173` and sign in as `host@tide.dev` with password `tide-dev`.

| Command      | Does                                                  |
| ------------ | ----------------------------------------------------- |
| `make dev`   | run everything locally                                |
| `make check` | Go vet and tests, web checks, formatting, type drift  |
| `make build` | build `bin/tide` with the web app embedded            |
| `make media` | the full media test suite in Docker, about 16 minutes |

Stop with Ctrl-C. Containers keep running until `docker compose -f deploy/compose.yaml down`.

Development mode accepts the secrets committed to the repository and turns transcripts on. Never use it in production.
