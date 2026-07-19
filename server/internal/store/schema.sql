CREATE TABLE IF NOT EXISTS rooms (
    id TEXT PRIMARY KEY,
    slug TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL,
    owner_sub TEXT NOT NULL,
    lobby_enabled INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS rooms_owner_created_idx
    ON rooms (owner_sub, created_at DESC);

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
    FOREIGN KEY (room_id) REFERENCES rooms (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS recordings_room_started_idx
    ON recordings (room_slug, started_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS recordings_one_active_room_idx
    ON recordings (room_id)
    WHERE status IN ('starting', 'recording', 'finalizing');
