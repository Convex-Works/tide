# API

Go is the source of truth. Every wire type (requests, responses, SSE payloads,
webhook-derived models) lives in `server/internal/api` as a plain struct with `json`
tags, and route paths are constants in the same package.

[tygo](https://github.com/gzuidhof/tygo) generates `web/src/lib/api/types.gen.ts`
from that package (`make gen`). The generated file is committed, and CI fails on
drift. Handlers accept and return only `internal/api` types, and nothing in `web/`
hand-writes a wire type. `lib/api/client.ts` is a small fetch wrapper over the
generated types, so it has no route string literals.

Room and participant metadata are the exception. Go writes them as JSON literals, and
`web/` reads them by hand, so CI doesn't check them.

## Contracts outside this repository

Three shapes are read by programs tide doesn't ship, and changing one breaks them.
Only the transcript params have a test that pins them:

- **`api.BotSession`**: the `session.json` a bot reads ([bots](../user/bots.md)).
  moil will deliver this same file.
- **`api.ChatPayload` on `api.ChatTopic`**: bots read and write chat.
- **A transcript job's params**, which the owner's hooks read
  ([transcripts](transcripts.md#job-params-are-a-contract)).
