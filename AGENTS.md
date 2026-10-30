# tide

Lean self-hosted video meetings: single Go binary (embedded SvelteKit SPA) +
LiveKit + Redis + Egress + S3. **docs/ARCHITECTURE.md is the contract** — read
it before building anything; it defines the system shape, API surface, design
language (§10), type-sync rules (§11), and the phased build plan (§16).

Rules that bite:

- Wire types are defined ONCE in `server/internal/api` (Go). Never hand-write a
  request/response type in `web/`. Run `make gen` after changing them; CI fails
  on drift.
- All capability checks live server-side (room ownership, host role). Never
  trust a client-asserted role; capabilities travel only in LiveKit token
  grants minted by the server.
- Feature list is frozen (see ARCHITECTURE.md §1). Do not add features
  (polls, whiteboard, server-side AI, RTMP, file transfer…) — this restraint is
  the product. Transcripts are the one exception, and only because they run on
  the room owner's own machine through moil (§8.1); nothing model-shaped runs
  on the server.
- `server/third_party/moil` is a vendored copy of the moil Go SDK. Never edit
  it in place: fix moil upstream, then refresh with `scripts/vendor-moil.sh`.
- The transcribe bundle's hash is pinned by a test. Changing any byte of
  `job.py`, `job.py.lock` or `manifest.json` in
  `server/internal/transcripts/bundle/` makes every machine owner review and
  approve it again, so do it only on purpose.
- Design tokens and density rules are in ARCHITECTURE.md §10 — small paddings,
  4-6px radii, phosphor-svelte 16px regular icons, Inter 13px base, Geist Mono
  for slugs/timers. Light shell, dark meeting stage, one accent blue.
- `make check` must pass before any commit.
- Merging a `release/v*` PR and pushing its tag are one step. The release PR
  pins the new image in the install docs, and that image doesn't exist until
  `v<version>` is pushed, so whoever merges the PR pushes the tag right after
  CI passes on main (`scripts/check-release-pins.sh <version>` first). Until
  then the README's quickstart fails, and CI's `release-pin` job warns.
