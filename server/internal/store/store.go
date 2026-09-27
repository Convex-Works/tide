package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"klisi/internal/api"
)

//go:embed schema.sql
var schema string

type Room struct {
	ID           string
	Slug         string
	Name         string
	OwnerSub     string
	LobbyEnabled bool
	CreatedAt    int64
	// LastActiveAt is Unix seconds of the most recent participant join, or nil
	// if the room has never been used.
	LastActiveAt *int64
}

type Recording struct {
	ID        string
	RoomID    string
	RoomSlug  string
	EgressID  string
	Status    string
	StartedBy string
	StartedAt int64
	AudioOnly bool
	EndedAt   *int64
	DurationS *int64
	S3Key     *string
	SizeBytes *int64
}

// HasFile reports whether egress stored a file for the recording.
func (r Recording) HasFile() bool {
	return r.S3Key != nil && *r.S3Key != ""
}

// A TranscriptFormat is one of the files a transcript is kept as, beside
// its recording (ARCHITECTURE.md §8.1).
type TranscriptFormat struct {
	// Extension is the file's extension, and the format's name in the
	// download route's format parameter.
	Extension string
	// ContentType is what the file is stored and downloaded as.
	ContentType string
}

// TranscriptFormats are the files every transcript is kept as.
var TranscriptFormats = []TranscriptFormat{
	{Extension: api.TranscriptFormatText, ContentType: "text/plain; charset=utf-8"},
	{Extension: api.TranscriptFormatVTT, ContentType: "text/vtt; charset=utf-8"},
}

// TranscriptFormatByExtension returns the transcript format with the given
// extension, if there is one.
func TranscriptFormatByExtension(extension string) (TranscriptFormat, bool) {
	for _, format := range TranscriptFormats {
		if format.Extension == extension {
			return format, true
		}
	}
	return TranscriptFormat{}, false
}

// TranscriptKey is where the recording's transcript in format is stored:
// beside the recording, under its basename, as players expect sidecar
// captions. It is "" when the recording has no file.
func (r Recording) TranscriptKey(format TranscriptFormat) string {
	if !r.HasFile() {
		return ""
	}
	key := *r.S3Key
	return strings.TrimSuffix(key, path.Ext(key)) + "." + format.Extension
}

// ObjectKeys names every file stored for the recording: the recording itself
// and its transcript sidecars, whether or not they exist yet. Whatever
// deletes a recording removes all of them.
func (r Recording) ObjectKeys() []string {
	if !r.HasFile() {
		return nil
	}
	keys := []string{*r.S3Key}
	for _, format := range TranscriptFormats {
		keys = append(keys, r.TranscriptKey(format))
	}
	return keys
}

type RecordingUpdate struct {
	Status    string
	EndedAt   *int64
	DurationS *int64
	S3Key     *string
	SizeBytes *int64
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if !strings.HasPrefix(path, "file:") && path != ":memory:" {
		dir := filepath.Dir(path)
		if dir != "." {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("create database directory: %w", err)
			}
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	// foreign_keys is off by default in SQLite; without it the ON DELETE
	// CASCADE in the schema is inert. MaxOpenConns(1) guarantees the pragma
	// applies to the only connection.
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		schema,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize sqlite: %w", err)
		}
	}
	// Additive column migrations for databases created before the column
	// existed. schema.sql's CREATE TABLE IF NOT EXISTS never alters an existing
	// table, so new nullable columns are added here idempotently.
	added, err := ensureColumn(db, "rooms", "last_active_at", "INTEGER")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate rooms: %w", err)
	}
	if added {
		// Rooms that predate activity tracking would otherwise all read "new".
		// We have no real history, so seed last_active_at from created_at as a
		// reasonable baseline; genuine joins advance it afterward. Runs only on
		// the one-time column add, so freshly created rooms still start NULL.
		if _, err := db.Exec(
			`UPDATE rooms SET last_active_at = created_at WHERE last_active_at IS NULL`); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("backfill rooms.last_active_at: %w", err)
		}
	}
	// Recordings that predate the audio-only mode were all video composites,
	// which the DEFAULT 0 already states — no backfill needed.
	if _, err := ensureColumn(db, "recordings", "audio_only", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate recordings: %w", err)
	}
	return &Store{db: db}, nil
}

// ensureColumn adds a column to an existing table if it is not already present,
// reporting whether it performed the ALTER. SQLite has no ADD COLUMN IF NOT
// EXISTS, so existence is checked first. The caller holds the only connection
// (MaxOpenConns(1)); the pragma rows are closed before the ALTER so the single
// connection is free.
func ensureColumn(db *sql.DB, table, column, decl string) (bool, error) {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false, err
	}
	exists := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return false, err
		}
		if name == column {
			exists = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	_ = rows.Close()
	if exists {
		return false, nil
	}
	if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl)); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) CreateRoom(ctx context.Context, room Room) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO rooms (id, slug, name, owner_sub, lobby_enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		room.ID, room.Slug, room.Name, room.OwnerSub, room.LobbyEnabled, room.CreatedAt,
	)
	return err
}

const roomColumns = `id, slug, name, owner_sub, lobby_enabled, created_at, last_active_at`

func scanRoom(scanner interface{ Scan(...any) error }) (Room, error) {
	var room Room
	var lastActiveAt sql.NullInt64
	err := scanner.Scan(
		&room.ID, &room.Slug, &room.Name, &room.OwnerSub,
		&room.LobbyEnabled, &room.CreatedAt, &lastActiveAt,
	)
	if err != nil {
		return Room{}, err
	}
	if lastActiveAt.Valid {
		room.LastActiveAt = &lastActiveAt.Int64
	}
	return room, nil
}

func (s *Store) RoomBySlug(ctx context.Context, slug string) (Room, error) {
	return scanRoom(s.db.QueryRowContext(ctx,
		`SELECT `+roomColumns+` FROM rooms WHERE slug = ?`, slug))
}

// RoomByID returns the room with the given ID, which unlike its slug never
// changes.
func (s *Store) RoomByID(ctx context.Context, id string) (Room, error) {
	return scanRoom(s.db.QueryRowContext(ctx,
		`SELECT `+roomColumns+` FROM rooms WHERE id = ?`, id))
}

func (s *Store) RoomsByOwner(ctx context.Context, ownerSub string) ([]Room, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+roomColumns+` FROM rooms WHERE owner_sub = ? ORDER BY created_at DESC, slug`, ownerSub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rooms := make([]Room, 0)
	for rows.Next() {
		room, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}
	return rooms, rows.Err()
}

func (s *Store) Rooms(ctx context.Context) ([]Room, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+roomColumns+` FROM rooms ORDER BY created_at DESC, slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rooms := make([]Room, 0)
	for rows.Next() {
		room, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}
	return rooms, rows.Err()
}

// TouchRoomActive records that a room saw activity at ts (Unix seconds),
// advancing last_active_at monotonically so out-of-order webhook delivery can
// never move the timestamp backwards.
func (s *Store) TouchRoomActive(ctx context.Context, slug string, ts int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE rooms SET last_active_at = ? WHERE slug = ? AND COALESCE(last_active_at, 0) < ?`,
		ts, slug, ts)
	return err
}

func (s *Store) UpdateRoom(ctx context.Context, room Room) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE rooms SET slug = ?, name = ?, lobby_enabled = ? WHERE id = ?`,
		room.Slug, room.Name, room.LobbyEnabled, room.ID,
	)
	if err != nil {
		return err
	}
	if err := requireChanged(result); err != nil {
		return err
	}
	// Recordings are addressed through their room's current slug. Keep the
	// denormalized value aligned so lists, authorization, and deletion continue
	// to work after the owner changes the meeting URL.
	if _, err := tx.ExecContext(ctx,
		`UPDATE recordings SET room_slug = ? WHERE room_id = ?`, room.Slug, room.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteRoom deletes a room, and with it its recordings and their
// transcripts, and queues every file of those recordings for removal from
// storage by now, all in one transaction. It returns the keys it queued.
func (s *Store) DeleteRoom(ctx context.Context, id string, now int64) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT `+recordingColumns+` FROM recordings WHERE room_id = ?`, id)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0)
	for rows.Next() {
		recording, err := scanRecording(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		keys = append(keys, recording.ObjectKeys()...)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := queueRemovals(ctx, tx, keys, now); err != nil {
		return nil, err
	}
	// Recording and transcript rows go with the room via ON DELETE CASCADE
	// (foreign_keys=ON).
	result, err := tx.ExecContext(ctx, "DELETE FROM rooms WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if err := requireChanged(result); err != nil {
		return nil, err
	}
	return keys, tx.Commit()
}

func (s *Store) InsertRecording(ctx context.Context, recording Recording) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO recordings (
			id, room_id, room_slug, egress_id, status, started_by, started_at,
			audio_only, ended_at, duration_s, s3_key, size_bytes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		recording.ID, recording.RoomID, recording.RoomSlug, recording.EgressID,
		recording.Status, recording.StartedBy, recording.StartedAt,
		recording.AudioOnly, recording.EndedAt, recording.DurationS, recording.S3Key, recording.SizeBytes,
	)
	return err
}

// recordingStatusRank orders statuses so transitions are monotonic:
// starting → recording → finalizing → completed/failed. An UPDATE carrying a
// lower-ranked status than the row already has is a stale or racing event
// (e.g. Stop writing "finalizing" after egress_ended completed the row) and
// is silently skipped; terminal states can never be overwritten.
const recordingStatusRank = `CASE %s
	WHEN 'starting' THEN 0
	WHEN 'recording' THEN 1
	WHEN 'finalizing' THEN 2
	WHEN 'completed' THEN 3
	WHEN 'failed' THEN 3
	ELSE -1 END`

func (s *Store) UpdateRecordingByEgress(ctx context.Context, egressID string, update RecordingUpdate) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE recordings SET
			status = ?,
			ended_at = COALESCE(?, ended_at),
			duration_s = COALESCE(?, duration_s),
			s3_key = COALESCE(?, s3_key),
			size_bytes = COALESCE(?, size_bytes)
		WHERE egress_id = ?
		  AND status NOT IN ('completed', 'failed')
		  AND `+fmt.Sprintf(recordingStatusRank, "status")+
		` <= `+fmt.Sprintf(recordingStatusRank, "?"),
		update.Status, update.EndedAt, update.DurationS, update.S3Key, update.SizeBytes,
		egressID, update.Status,
	)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed > 0 {
		return nil
	}
	// Distinguish "no such recording" (an error worth surfacing) from a
	// monotonic-guard skip (a benign stale event).
	var status string
	err = s.db.QueryRowContext(ctx, `SELECT status FROM recordings WHERE egress_id = ?`, egressID).Scan(&status)
	if err != nil {
		return err
	}
	return nil
}

// ListActiveRecordings returns every recording in a non-terminal status, for
// reconciliation against LiveKit's actual egress state.
func (s *Store) ListActiveRecordings(ctx context.Context) ([]Recording, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, room_id, room_slug, egress_id, status, started_by, started_at,
		       audio_only, ended_at, duration_s, s3_key, size_bytes
		FROM recordings WHERE status IN ('starting', 'recording', 'finalizing')
		ORDER BY started_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	recordings := make([]Recording, 0)
	for rows.Next() {
		recording, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		recordings = append(recordings, recording)
	}
	return recordings, rows.Err()
}

func (s *Store) RecordingsByRoomSlug(ctx context.Context, slug string) ([]Recording, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, room_id, room_slug, egress_id, status, started_by, started_at,
		       audio_only, ended_at, duration_s, s3_key, size_bytes
		FROM recordings WHERE room_slug = ?
		ORDER BY started_at DESC, id DESC`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	recordings := make([]Recording, 0)
	for rows.Next() {
		recording, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		recordings = append(recordings, recording)
	}
	return recordings, rows.Err()
}

func (s *Store) RecordingByID(ctx context.Context, id string) (Recording, error) {
	return scanRecording(s.db.QueryRowContext(ctx, `
		SELECT id, room_id, room_slug, egress_id, status, started_by, started_at,
		       audio_only, ended_at, duration_s, s3_key, size_bytes
		FROM recordings WHERE id = ?`, id))
}

func (s *Store) RecordingByEgressID(ctx context.Context, egressID string) (Recording, error) {
	return scanRecording(s.db.QueryRowContext(ctx, `
		SELECT id, room_id, room_slug, egress_id, status, started_by, started_at,
		       audio_only, ended_at, duration_s, s3_key, size_bytes
		FROM recordings WHERE egress_id = ?`, egressID))
}

func (s *Store) ActiveRecordingByRoomID(ctx context.Context, roomID string) (Recording, error) {
	return scanRecording(s.db.QueryRowContext(ctx, `
		SELECT id, room_id, room_slug, egress_id, status, started_by, started_at,
		       audio_only, ended_at, duration_s, s3_key, size_bytes
		FROM recordings
		WHERE room_id = ? AND status IN ('starting', 'recording', 'finalizing')
		ORDER BY started_at DESC LIMIT 1`, roomID))
}

// DeleteRecording deletes a recording and its transcript, and queues its
// files for removal from storage by now, in one transaction. It returns the
// keys it queued.
func (s *Store) DeleteRecording(ctx context.Context, id string, now int64) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	recording, err := scanRecording(tx.QueryRowContext(ctx,
		`SELECT `+recordingColumns+` FROM recordings WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	keys := recording.ObjectKeys()
	if err := queueRemovals(ctx, tx, keys, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM recordings WHERE id = ?", id); err != nil {
		return nil, err
	}
	return keys, tx.Commit()
}

const recordingColumns = `id, room_id, room_slug, egress_id, status, started_by, started_at,
	audio_only, ended_at, duration_s, s3_key, size_bytes`

type recordingScanner interface {
	Scan(...any) error
}

func scanRecording(scanner recordingScanner) (Recording, error) {
	var recording Recording
	var endedAt, durationS, sizeBytes sql.NullInt64
	var s3Key sql.NullString
	err := scanner.Scan(
		&recording.ID, &recording.RoomID, &recording.RoomSlug, &recording.EgressID,
		&recording.Status, &recording.StartedBy, &recording.StartedAt,
		&recording.AudioOnly, &endedAt, &durationS, &s3Key, &sizeBytes,
	)
	if err != nil {
		return Recording{}, err
	}
	if endedAt.Valid {
		recording.EndedAt = &endedAt.Int64
	}
	if durationS.Valid {
		recording.DurationS = &durationS.Int64
	}
	if s3Key.Valid {
		recording.S3Key = &s3Key.String
	}
	if sizeBytes.Valid {
		recording.SizeBytes = &sizeBytes.Int64
	}
	return recording, nil
}

func IsSlugConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: rooms.slug")
}

func IsActiveRecordingConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: recordings.room_id")
}

func requireChanged(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RevokeSession invalidates a session ID until the session itself would have
// expired; expired rows are pruned on the way in so the table stays bounded
// by the number of logouts within one session lifetime.
func (s *Store) RevokeSession(ctx context.Context, sid string, expiresAt int64) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM revoked_sessions WHERE expires_at <= unixepoch()`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO revoked_sessions (sid, expires_at) VALUES (?, ?)`, sid, expiresAt)
	return err
}

func (s *Store) IsSessionRevoked(ctx context.Context, sid string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM revoked_sessions WHERE sid = ? AND expires_at > unixepoch()`, sid).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
