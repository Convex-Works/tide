# klisi

Lean, self-hosted video meetings. Meeting URLs, host auth (OIDC), guest lobby,
mic/cam/screenshare, participant moderation, reconnection, ephemeral chat, and
server-orchestrated recording. Nothing else.

Built as a single Go binary (SvelteKit SPA embedded) on top of
[LiveKit](https://livekit.io) for the media plane.

- Architecture, design language, and build plan: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

## Development

Requires Go ≥ 1.24, Node ≥ 22, Docker + Compose.

```sh
make dev     # compose stack (livekit, redis, egress, minio, dex) + server + vite
make check   # vet, staticcheck, tests, svelte-check, lint
make gen     # regenerate TS types from Go (tygo)
make build   # production binary with embedded SPA
```
