# Glossary

Words that mean something exact here.

| Word | Means |
|---|---|
| **Room** | A row with a slug and an owner. Persistent and reusable. |
| **Meeting** | An implicit session in a room, from first join to last leave. |
| **Slug** | A room's name in its URL, `/m/<slug>`. |
| **Host** | A signed-in person, or an anonymous session that owns a room. In a room, whoever holds the host role in their token. |
| **Owner** | The session whose `sub` is `rooms.owner_sub`. Transcript machines and bot sessions stay with the owner alone. |
| **Administrator** | A host in an admin group, who can manage every room but isn't its owner. |
| **Manager** | An owner or an administrator (`can_manage`). |
| **Guest** | A display name admitted through the lobby. Never authenticated. |
| **Bot** | A program in a meeting, joined with a session the owner asked for. Always visible. "AI agent" is one application; a LiveKit participant of kind `AGENT` is the mechanism. |
| **Observer** | tide's own hidden participant during a recording. Not a bot. |
| **Operator** | Whoever runs a tide deployment. |
| **Recorder** | LiveKit's Egress worker: headless Chrome and GStreamer, a separate process. |
| **Machine** | A host's own computer, paired through moil. Not part of the deployment. |
| **Bundle** | The `transcribe` moil job tide publishes, approved by hash. |
| **Reconciler** | A loop that heals rows against the outside world: recordings against Egress, transcripts against moil jobs. |
| **Projection** | The client's meeting state, re-derived from LiveKit's maps. |
| **Shell, stage** | The light surface around a meeting, and the dark one in it. |
| **Hairline** | The 2px status line across the top of the app chrome. |
| **Media gate** | The real-SFU test suite. |
