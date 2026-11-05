# Decisions

Why tide is the way it is. Change one on purpose, and rewrite its row when you
do.

## The feature list is frozen

tide does few things well: meeting URLs, host sign-in or none, a guest lobby,
mic, camera and screen controls, device selection, a participant list with
moderation, reconnection, ephemeral chat, and server-orchestrated recording with
management. Everything else is out of scope by design. A new feature must
displace an existing one.

Two were admitted on conditions that stay load-bearing:

- **Transcripts**, as part of recording management: no model runs on the server,
  and heavy compute happens on machines the recording's owner paired.
- **Bots**: a program runs outside the server, under the room owner's control,
  in view of everyone. tide mints its token and shows it to the room, and ships
  no bot.

## The table

| Decision | Choice | Why |
|---|---|---|
| Media stack | LiveKit, self-hosted | Ships reconnection, simulcast, moderation and Egress recording; Apache 2.0 |
| Fork vs fresh | Fresh repo, fresh history | mirotalksfu is AGPL; tide derives nothing from it |
| Backend | Single Go binary, SPA embedded | Leanest production artifact; first-party LiveKit Go SDK |
| Frontend | SvelteKit SPA, adapter-static | No Node in production; SSR is worthless behind auth |
| Host auth | Generic OIDC (Dex in development) | Provider-agnostic by configuration |
| Sign-in | Optional: no issuer means anonymous mode | Running a meeting server shouldn't require an identity provider; ownership stays a server-checked `sub` either way |
| Anonymous rooms | In memory, gone 24 hours after last use or at restart | No persistence to operate; links still work for later the same day |
| Chat | Ephemeral only | No storage, no privacy surface; history is a non-goal |
| DB | SQLite (modernc, CGO-free) | Single instance; keeps the static binary |
| Type sync | tygo and a CI drift gate | Go structs are the single source of truth |
| Recording | Egress room composite with our own template | Server-owned lifetime; recordings wear tide's design |
| Recording switch | On when sign-in and `TIDE_S3_ENDPOINT` are set | Recording costs a second process and storage; a deployment that doesn't want it shouldn't run either |
| Transcripts | moil jobs on the room owner's own machine | No model or GPU on the server, no fifth process, no new reader of the audio |
| Transcript trigger | Automatic once the owner has paired a machine; otherwise on request | Pairing is the opt-in; no per-recording button for hosts who use it |
| moil switch | `TIDE_MOIL`, default off; needs recording | moil is alpha, and `/moil/` takes unauthenticated pairing requests, so an operator opts in |
| moil SDK | Vendored in `server/third_party/moil` | The forge sits behind Cloudflare Access, so Go can't fetch it in CI or Docker; same precedent as `web/vendor` |
| Speaker names | A hidden observer writes who was active beside the recording; the bundle matches that against its diarization | Diarization stays the truth: active-speaker data is too coarse to say who said a word but enough to vote over a meeting. Grouped by display name because identities are per connection. Best effort: failing costs names, never a recording |
| Bots | Admitted to the frozen list | People want programs in a meeting, AI agents among them, and it costs tide one route and three signs in the UI |
| Bot sessions | Owner only, from the meeting's details; no API keys, no pre-join notice | A bot is a new live listener, and adding one is the owner's choice, so administrators don't get one. A key would be a credential that outlives a meeting. Bots show in the room once you're in it, and nothing more |
| Bot on the hairline | Its own green line, not a hairline state, plus a BOT chip | A state would lose to recording or a reconnect; this is the only thing telling guests a program is listening, so nothing may hide it |
| Default slugs | Readable random (`abc-defg-hij`), ~47 bits | Short enough to read aloud; unguessable behind the lookup and join limits; UUIDs made ugly links |
| Calendar invites | `.ics` built in the browser, nothing stored | Covers "send people a time" without a scheduling model; server-side scheduling stays a later decision |
| Media server process | LiveKit's server as a library inside tide; an external one still possible via `TIDE_MEDIA_URL` | One executable to deploy; the price is that a tide restart ends live meetings |
| Signaling origin | tide forwards `/rtc` to the embedded server | One origin, one certificate, one ingress route |
| Redis | None without a recorder; an ordinary Redis 7 beside the recorder with one | LiveKit needs Redis only to reach the recorder. miniredis inside tide was tried and measured: fast enough (≈1,600 commands/s, sub-millisecond publishes), but one subscriber that stopped reading froze its single lock, and every meeting's signal relay with it, and it trusted request sizes before AUTH; a bug in it would take every meeting down. Valkey works too (same protocol); Redis matches what production already runs |
| TLS | Left to a proxy or ingress | Built-in ACME means a certificate cache, port 443 and renewal failures in tide; Caddy in front is two lines and production already has an ingress |
| Media gate | Runs in full on every pull request, no nightly | A regression only a nightly catches has already shipped |
