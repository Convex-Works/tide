# Auth

Who may do what, and how tide knows. The grants themselves are in
`internal/livekit/token.go`, and the limits in `internal/httpapi/ratelimit.go`.

Every capability a client has is a grant inside a LiveKit token tide minted, and
tide mints one only after policy passes. There is no client-asserted role
anywhere. Moderation runs through the LiveKit server SDK, so it holds at the SFU
whatever a client does.

## Sessions

A host signs in with any OIDC provider. tide then sets a signed cookie holding
subject, email, name, administrator capability and expiry. The cookie is
stateless, apart from `revoked_sessions`, which lets sign-out end it everywhere.
OIDC group policy is evaluated once, at sign-in, so a provider that pushes no
group changes can't revoke access before the session expires.

Guests never authenticate. They exist as a display name typed on the pre-join
screen and admitted through the lobby. A name proves nothing, so nothing
authorizes by it.

An administrator can manage every room. An owner manages their own. A manager
is either, and is what `can_manage` reports. Only `is_owner` unlocks what stays
with the owner alone.

## Tokens

Token TTL is short because it authorizes _connecting_; once connected, the
session lives independently. Identities are per connection (`host:<sub>:<nonce>`,
`guest:<id>`, `bot:<id>`), so someone who rejoins gets a new identity and keeps
their name. Anything that follows a person across a meeting groups by display
name instead.

Kind `AGENT` makes a participant _dependent_: it doesn't hold a room open, so
bots and the observer can't keep a meeting, or its recording, alive once the
people have left. A client treats kind `AGENT` as a visible bot. That is safe
for the observer only because `hidden` keeps it from every client: LiveKit
leaves hidden participants out of join responses and participant updates alike.
`TestClientsSeeBotsAndNeverTheObserver` holds that against the real media server.

**Bots.** The room's owner alone gets a session, checked by `session.Sub` against
`rooms.owner_sub`. An administrator is refused, unlike for recording, because a
bot is a new live listener, and adding one is the owner's choice. A session
needs no machine, so it works in anonymous mode too. Its token subscribes to
everything in one room, so the room's only protection is seeing the bot: the
tag, hairline and chip must stay ([web](web.md#the-hairline)). `api.BotSession`
is a public contract ([api](api.md)).

**The observer** is tide's own participant during a recording, not a program
anyone connects ([recording](recording.md#who-spoke-when)). It doesn't carry the
Recorder grant, which would tell clients the room is recording.

## Anonymous mode

With no `TIDE_OIDC_ISSUER`, the browser that created a room owns it. Nothing else
about authorization changes: ownership is still a session's `sub` matched against
`rooms.owner_sub` on the server, and an anonymous owner joins without the lobby,
admits, mutes, removes and ends meetings as any owner does.

- **Sessions.** `/api/me` issues a session instead of answering 401, so the SPA
  never shows a sign-in screen. The `sub` is `anon:` plus 128 random bits. It lasts
  longer than a signed-in session because it is the only key to the rooms this
  browser created and grants nothing else.
- **Sessions belong to their mode.** Anonymous sessions are signed with a key
  derived from the session secret, so an operator who adds or drops sign-in
  without changing the secret doesn't turn anonymous cookies into hosts, or
  administrators into anonymous ones. tide also refuses the other mode's session
  outright.
- **Nothing may be counted per anonymous `sub`,** because anyone can mint one.
  Room creation counts per client address, room lookups count anonymous sessions
  as they do guests, and minting a session falls under the login limit. Addresses
  are cheap too, so only the room ceiling bounds memory.
- **The sweep can't catch a meeting that is starting.** It deletes a room only if
  it is still idle at that moment, and every token tide mints marks its room
  active first.

An anonymous deployment is an open SFU. That is the point of the mode.

## Rooms and slugs

A token names its room by slug and outlives the request that minted it. So:

- A slug freed by deleting or renaming its room is held in `freed_slugs` for the
  token lifetime before another room may take it. Otherwise a leftover token would
  join someone else's meeting, past their lobby.
- tide mints a token only if the room still exists as it marks it active, never
  for one deleted while the join was under way.
- Deleting a room ends any meeting live in it, so nobody stays on under a slug
  someone else can now create.

A meeting is an implicit session within a room: LiveKit creates it on first join
and destroys it on last leave.

## Lobby

The lobby lives entirely in the tide server, in memory. An unadmitted guest has no
token and can't touch the media plane. Join limits count per client and addresses
are cheap, so only the per-room ceiling bounds the queue.

A guest re-enters through the lobby only if their LiveKit session fully dropped.
LiveKit's resume covers blips without a new token.

## Moderation

A kicked participant's identity is denylisted, bots included, which stops a cached
token from reconnecting. Only the owner's own `host:` identities are never banned.
A new join gets a new identity, so the lobby is what keeps a removed guest out. The
participant list itself is client-side LiveKit
state, with no polling and no server involvement.

## Limits and trust boundaries

The per-client buckets count an IPv6 client by its /56, and read
`X-Forwarded-For` only through `TIDE_TRUSTED_PROXIES`. Two choices aren't obvious:

- Room lookups have a bucket of their own, keyed per client address for guests and
  per `sub` for signed-in hosts. A NAT shared with guests then can't refuse a
  host, and an account from a broad issuer can't test slugs unlimited. Slug
  entropy rests on these limits.
- `POST /moil/v1/pair` is the one moil endpoint without credentials that creates
  state. It must be JSON, so a web page can't post one without a CORS preflight,
  and anything else is refused before it counts.

The recorder's Redis carries recording jobs, and a job carries the S3 credentials.
tide refuses a Redis that answers without a password, because a Redis with no
`requirepass` accepts any password and authenticating proves nothing. The
reference deployments keep it off every network.

**Machines.** Pairing is confirmed only by a signed-in session with the CSRF
header, and the machine's owner is always that session. Nothing in the request
can name another owner. Machine tokens are stored as SHA-256 hashes. The rest of
what a machine may touch is in [transcripts](transcripts.md).

**The page.** Every response carries a CSP with scripts by hash. tide computes the
sha256 of SvelteKit's inline bootstrap from the embedded shell at startup, so
there is no `'unsafe-inline'`, and CI proves the built shell fits. Pages connect
only to tide's origin and the media server's public signaling origin. The
recorder's page alone may open a WebSocket to any host, because the recorder gives
it a media server address tide never sees. The SPA makes no third-party requests:
fonts are self-hosted.

**Secrets in the SPA.** The client knows its own token and public URLs, and a bot
session it requested, which goes to the owner's clipboard.

**`window.tideDiagnostics()`** contains identities and display names. It is
written to the user's own machine by an explicit action and never transmitted. It
is deliberately not a UI control.
