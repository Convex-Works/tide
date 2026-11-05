# Notes for agents

## A note from the Convex Works development team

We like ambitious ideas, simple systems, and software that feels obvious. Do not preserve
complexity just because it already exists. Do not introduce machinery because it looks
architecturally impressive. Understand the real constraint, then fight for the smallest model
that makes the correct behavior unsurprising.

Channel both "measure twice, cut once" and "yagni". Fight scope creep. Try to honor the dev's
intent in both a minimal and realistic fashion.

The rest of this document helps you navigate the codebase and make changes effectively. Think
of these instructions less as hard rules, more as good defaults. The developer's preferences can
override anything here. Two groups are not defaults: "Secrets and production" and "Meetings and
data".

If a rule here fights the task in front of you, say so loudly and get a human sign-off before
you break it.

## What this is

tide: lean self-hosted video meetings. `server/` is one Go binary with the SvelteKit SPA from
`web/` embedded and LiveKit's media server inside it. Recording adds LiveKit Egress, Redis and
S3. `site/` is the public website and its docs. This repository stands alone: the code states
each rule first (types, names, tests, a short comment), and `docs/` holds only the deeper
explanation. If the code and a doc disagree, the code wins and the doc is updated.

Decisions a change must not undo by accident
([docs/internals/decisions.md](docs/internals/decisions.md) gives the reasons and the others):

1. The feature list is frozen, and this restraint is the product. Polls, whiteboards, server-side
   AI, RTMP and file transfer stay out. A new feature must displace an existing one. Two were
   admitted. Transcripts, only because they run on the room owner's own machine through moil.
   Bots, only because they run outside the server, under the room owner's control, in view of
   everyone: tide mints the token and shows the bot, and ships none. Nothing model-shaped runs on
   the server.
2. Authorization is the token. Every capability is a grant in a LiveKit token the server minted,
   and room ownership and roles are checked on the server. A client asserts nothing.
3. Transcript machines and bot sessions stay with the room's owner. Jobs run only on the owner's
   machines, and only the owner gets a bot session. Administrators can record, and download
   recordings and transcripts, but they get neither of these.
4. A bot can't hide. Its tag, its hairline and its chip stay for as long as it is in the room.
5. One executable and one instance: LiveKit runs inside tide as a library, SQLite (CGO-free, so the
   binary stays static) holds what lasts, and the lobby lives in memory. No Node runtime, and no
   Redis without a recorder. tide only configures LiveKit and speaks the server SDK to it.
6. Client media state is a convergent projection of LiveKit's own maps, never a log of events
   ([docs/internals/web.md](docs/internals/web.md)).

## Commits and PR titles

Use conventional commit-style messages and PR titles, in plain language: `type(scope): summary`.

Valid types are `feat`, `fix`, `perf`, `docs`, `chore`, `refactor` and `test`. Scopes are
optional. Use the affected area when it helps: `auth`, `rooms`, `lobby`, `recording`,
`transcripts`, `bots`, `media`, `store`, `api`, `moil`, `web`, `site`, `deploy`, `ci`.

Examples:

- `fix(recording): keep the indicator on while another recording runs`
- `fix(auth): refuse dev mode on a public host`
- `feat(web): remember pre-join choices between meetings`
- `refactor(media): send the signal relay through one forwarder`
- `test(media): skip the audio recording test on WebKit`
- `docs(deploy): pin 0.1.2 in the install docs`
- `chore(ci): annotate a failed media suite`

## Pull requests

- Never make a PR unless the developer explicitly asks you to.
- `origin` is GitHub (`Convex-Works/tide`), and the repository is public. Use `gh`.
- Body: the problem in a sentence or two, then how you fixed it. End with the model and harness
  that did the work.
- One request is one PR. Split it only when the developer asks.
- UI changes need before/after images. Motion or timing needs a short video. Attach them to the
  PR. Never commit PR-only screenshots or assets.
- When babysitting: call `watch_pull_request` and end your turn. You are woken on checks,
  comments or conflicts. Do not poll or sleep. Verify each bot finding against the source, fix real
  ones, and dismiss false positives with a written reason. Stop when the latest commit is green.

## Issues, plans and work artifacts

- Track work in the GitHub issue that owns it. A merged PR is the implementation record: close or
  update its issue when the work lands, and keep no second checklist in the repository.
- Text in an issue or a PR stays true after the next merge. Write no status such as "on a local
  branch, not pushed", "output only in `/tmp`" or "the removal PR". If evidence is worth citing,
  push it, then name its branch or commit. Pin a base by its commit.
- Do not commit implementation plans, research notes or agent scratch files. Keep temporary
  working material outside the worktree.

## Style guide

- Keep things in one function unless they are composable or reusable. Do not extract single-use
  helpers preemptively. Inline the logic at the call site unless the helper is reused, hides a
  genuinely complex boundary, or has a clear independent name that improves the caller.
- Before you add complexity for a speculative or vanishingly unlikely race or security edge case,
  explain the concrete failure mode, its likelihood and the complexity cost to the developer, and
  get their buy-in. Do not silently expand scope for theoretical robustness.
- Rely on type inference. Annotate only where a signature, an export or clarity needs it. In
  TypeScript, `any` is the enemy.
- Prefer array methods (`flatMap`, `filter`, `map`) to loops in TypeScript when they read better.
- Reduce the variable count: inline a value that is used once.
- Default to no comment. Write one only for a constraint, an external quirk or an invariant the
  code can't show, in one line where you can and four at most. Don't restate names or signatures,
  narrate steps, record history, or explain the fix you just made: the PR carries that. Name a
  page in `docs/` only when it holds a reason the comment can't.

## Taste

- Complexity belongs at the adapter boundary. A package's LiveKit use stays behind a narrow
  interface in its own client file, such as `moderation/livekit.go` and `recording/clients.go`.
  In the browser, `lib/rtc/room.svelte.ts` owns the meeting's `Room`, and
  the UI reads its state and calls its methods.
- Routes stay thin: a handler authorizes, calls `store`, and returns an `internal/api` type.
- All SQL is in `internal/store`, parameterised.
- `web/src/lib/ui` components own their look. Pick a prop; use `class` for layout, not to restyle.
  A look that belongs to one feature stays in that feature's component. Light shell, dark meeting
  stage, one accent blue, and other colour only for state. See
  [docs/internals/web.md](docs/internals/web.md).
- Svelte's `$effect` is for syncing with something outside Svelte. Use `$derived` in render and
  act in handlers instead of syncing state.
- People use tide live, on camera, in front of each other. They notice a frozen tile, a lying
  status, a stale label and a dropped frame. The recording hairline's pulse is the only ambient
  animation.
- Errors say what happened and what to do next.

## Before you commit

- Run `make check` (vet, Go tests, svelte-check, Prettier, type drift, the transcribe bundle's
  tests).
- After you change a type in `server/internal/api`, run `make gen`. Wire types are defined once,
  there: nothing in `web/` hand-writes one, room and participant metadata aside. `make check`
  compares against the index, so stage `web/src/lib/api/types.gen.ts` first.
- After a change to `site/`, or to a `TIDE_*` variable, run `npm run check` and `npm run lint` in
  `site/`. Every variable the server reads must be in `site/src/lib/config.ts`.
- A new browser spec that needs no server goes in the list in `.github/workflows/ci.yml`.
- After a change to `lib/rtc`, the media path, recording or `web/e2e/harness/`, run the media gate:
  `make media`, or `make media-dev ARGS="<spec> --project=chromium"` while you iterate.
- After a change to the transcripts pipeline, run `make moil-e2e` if a moil checkout is beside
  this one, and say so if it isn't.

## Secrets and production

- Never log a token, key, cookie, session secret, presigned URL or bot `session.json`. Each one
  grants access, and a bot session carries everything said in a room.
- Real secrets live only in `.env`, which git ignores. The development values in this repository
  are public, and tide refuses them outside dev mode.
- A pushed `v*` tag publishes a public image and a GitHub release. Tag only when the owner asks.
- `tide.convex.works` and `demo.tide.convex.works` are for their owner to run, not for an agent.

## Meetings and data

- Chat is ephemeral. Nothing a meeting says is stored, except a recording a host started.
- A recording never fails silently: every row ends `completed` or `failed`, even when webhooks are
  lost or tide restarts.
- A deleted recording never keeps its files. Every file it owns is in
  `store.Recording.ObjectKeys()`, and deleting queues them in `object_removals` in the transaction
  that deletes the row.
- Machines never write where tide serves from. Each attempt uploads to staging, and tide checks and
  copies what it keeps.
- Presigned download URLs last 5 minutes and are minted per request, after a manager check. A
  machine's URLs last its attempt, and are minted when it takes one.
- `server/third_party/moil` is a vendored copy. Fix moil upstream, then refresh with
  `scripts/vendor-moil.sh`.
- `server/internal/transcripts/bundle/` is the source of the transcribe bundle, and a test pins its
  hash. Changing any byte of `job.py`, `job.py.lock` or `manifest.json` makes every machine owner
  review and approve it again, so do it only on purpose.
- Schema changes are additive and run at open (`ensureColumn` in `internal/store`): released
  installs upgrade in place.
- `api.BotSession`, chat payloads and transcript job params are read by programs outside this
  repository ([docs/internals/api.md](docs/internals/api.md)).
- `livekit-client` is pinned exactly. Upgrade it and the server as a tested pair, behind the media
  gate.

## Documentation

The code is the record, and docs hold only what the code cannot say. Most code changes do not need
a documentation change. Agents can read the code.

Read the file for the area you change before you change it. Read
`docs/internals/media-gate.md` before you change a media spec, and `docs/internals/web.md` before
you change `lib/rtc`.

`docs/internals/` is for architectural decisions and their reasons, constraints that span
components, and implementation traps that are hard to discover from the source. Before you keep a
paragraph, ask what a maintainer would get wrong without it. If reading the relevant code answers
the question, cut it.

Do not document every feature, enumerate fields or methods, narrate control flow, maintain file
catalogs, or append PR summaries. Types, tests and code already record the implementation. The
glossary defines shared vocabulary; it is not a feature index.

A local reason the code can't show goes in a short nearby code comment. Use an internal doc when
the reasoning crosses boundaries or needs context the code cannot carry well. Link to the relevant
source instead of copying it.

When a documented decision or constraint changes, rewrite or remove the affected text. Do not append
another account of the new behavior.

A new internal page needs a distinct, durable reason to exist. A page with too little left after a
cut is deleted, and its few lines move to the page where a reader will look.

`docs/user/` helps people who hold meetings and write bots accomplish tasks. Give each major
feature a concise section: what it does, how to start, and anything unintuitive. Descriptions of
visible buttons, icons, layouts, animations or every UI state are not useful. Before you add text,
ask what task or decision it helps the user with. Keep it in the product's voice, with no
implementation details or contributor tooling.

`docs/operations/` holds procedures for running tide and for maintaining it. `site/src/content/docs`
is the published step-by-step path, and `site/src/lib/config.ts` is the list of variables, so link
to them rather than repeat them. `docs/releases/` holds each tag's notes.

- `docs/internals/`: [architecture](docs/internals/architecture.md) (boundaries),
  [decisions](docs/internals/decisions.md), [auth](docs/internals/auth.md) (with rooms, the lobby
  and limits), [recording](docs/internals/recording.md),
  [transcripts](docs/internals/transcripts.md), [web](docs/internals/web.md) (with the design
  language), [api](docs/internals/api.md), [media-gate](docs/internals/media-gate.md),
  [glossary](docs/internals/glossary.md).
- `docs/operations/`: [deployment](docs/operations/deployment.md),
  [development](docs/operations/development.md), [release](docs/operations/release.md).
- `docs/user/`: [guide](docs/user/guide.md), [bots](docs/user/bots.md).
