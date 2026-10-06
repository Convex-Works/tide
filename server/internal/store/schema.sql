CREATE TABLE IF NOT EXISTS rooms (
    id TEXT PRIMARY KEY,
    slug TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL,
    owner_sub TEXT NOT NULL,
    lobby_enabled INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    -- Unix seconds of the most recent participant join; NULL until first use.
    -- Powers the dashboard's "idle · 4d ago" state. Fresh DBs get it here;
    -- existing DBs get it via the ensureColumn migration in Open().
    last_active_at INTEGER
);

CREATE INDEX IF NOT EXISTS rooms_owner_created_idx
    ON rooms (owner_sub, created_at DESC);

-- Room links freed by deleting a room or changing its slug, held until every
-- token minted for them has expired (ARCHITECTURE.md §5): a token names its
-- room by slug, so a new room under a freed slug would let the old room's
-- tokens into its meeting, past its lobby.
CREATE TABLE IF NOT EXISTS freed_slugs (
    slug TEXT PRIMARY KEY,
    until INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS recordings (
    id TEXT PRIMARY KEY,
    room_id TEXT NOT NULL,
    room_slug TEXT NOT NULL,
    egress_id TEXT UNIQUE NOT NULL,
    status TEXT NOT NULL CHECK (status IN (
        'starting', 'recording', 'finalizing', 'completed', 'failed'
    )),
    started_by TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    ended_at INTEGER NULL,
    duration_s INTEGER NULL,
    s3_key TEXT NULL,
    size_bytes INTEGER NULL,
    -- 1 for audio-only recordings (the default mode). Audio recordings use
    -- .ogg and video composites use .mp4; the mode remains explicit so API
    -- consumers never need to infer it from an object key.
    -- Existing DBs get it via the ensureColumn migration in Open().
    audio_only INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (room_id) REFERENCES rooms (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS recordings_room_started_idx
    ON recordings (room_slug, started_at DESC);

-- A room's recordings by its ID, which never changes: its transcripts, its
-- deletion and the cascade that deletes its recordings.
CREATE INDEX IF NOT EXISTS recordings_room_idx
    ON recordings (room_id);

CREATE UNIQUE INDEX IF NOT EXISTS recordings_one_active_room_idx
    ON recordings (room_id)
    WHERE status IN ('starting', 'recording', 'finalizing');

-- Revoked session IDs let logout invalidate the stateless session cookie.
-- Rows expire with the session itself and are pruned opportunistically.
CREATE TABLE IF NOT EXISTS revoked_sessions (
    sid TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);

-- Machines hosts paired with tide through moil (ARCHITECTURE.md §8.1). The
-- Store implements moil.Store over this table. A machine's token is never
-- stored, only its SHA-256; report is the machine's last moil.MachineReport as
-- JSON (name, hardware, approved bundle hashes, last seen).
CREATE TABLE IF NOT EXISTS machines (
    id TEXT PRIMARY KEY,
    owner_sub TEXT NOT NULL,
    token_hash TEXT UNIQUE NOT NULL,
    paired_at INTEGER NOT NULL, -- Unix seconds
    report TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS machines_owner_paired_idx
    ON machines (owner_sub, paired_at);

-- One row per recording that should have a transcript (ARCHITECTURE.md §8.1).
-- The row is the truth; a moil job is only a projection of a pending row, so a
-- restart resubmits every pending one.
CREATE TABLE IF NOT EXISTS transcripts (
    recording_id TEXT PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('pending', 'completed', 'failed')),
    requested_at INTEGER NOT NULL,
    finished_at INTEGER NULL,
    speakers INTEGER NULL,
    error TEXT NULL,
    FOREIGN KEY (recording_id) REFERENCES recordings (id) ON DELETE CASCADE
);

-- The pending transcripts, in the order they were requested. It replaces
-- transcripts_pending_idx, which couldn't serve that order.
DROP INDEX IF EXISTS transcripts_pending_idx;
CREATE INDEX IF NOT EXISTS transcripts_pending_requested_idx
    ON transcripts (requested_at, recording_id) WHERE status = 'pending';

-- Objects to remove from storage (ARCHITECTURE.md §8.1), each once due_at
-- (Unix seconds) has passed. Deleting a recording or its room queues its
-- files, due at once, in the transaction that deletes the rows; handing a
-- machine an attempt queues the attempt's staging keys, due when their URLs
-- expire. The recording reconciler removes due objects every minute and
-- keeps those storage didn't remove.
CREATE TABLE IF NOT EXISTS object_removals (
    key TEXT PRIMARY KEY,
    due_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS object_removals_due_idx
    ON object_removals (due_at, key);
