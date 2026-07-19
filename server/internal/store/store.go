package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
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
}

type Recording struct {
	ID         string
	RoomID     string
	RoomSlug   string
	EgressID   string
	Status     string
	StartedBy  string
	StartedAt  int64
	EndedAt    *int64
	DurationS  *int64
	S3Key      *string
	SizeBytes  *int64
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

	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		schema,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize sqlite: %w", err)
		}
	}
	return &Store{db: db}, nil
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

func (s *Store) RoomBySlug(ctx context.Context, slug string) (Room, error) {
	var room Room
	err := s.db.QueryRowContext(ctx, `
		SELECT id, slug, name, owner_sub, lobby_enabled, created_at
		FROM rooms WHERE slug = ?`, slug,
	).Scan(&room.ID, &room.Slug, &room.Name, &room.OwnerSub, &room.LobbyEnabled, &room.CreatedAt)
	return room, err
}

func (s *Store) RoomsByOwner(ctx context.Context, ownerSub string) ([]Room, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, slug, name, owner_sub, lobby_enabled, created_at
		FROM rooms WHERE owner_sub = ? ORDER BY created_at DESC, slug`, ownerSub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rooms := make([]Room, 0)
	for rows.Next() {
		var room Room
		if err := rows.Scan(&room.ID, &room.Slug, &room.Name, &room.OwnerSub, &room.LobbyEnabled, &room.CreatedAt); err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}
	return rooms, rows.Err()
}

func (s *Store) UpdateRoom(ctx context.Context, room Room) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE rooms SET name = ?, lobby_enabled = ? WHERE id = ?`,
		room.Name, room.LobbyEnabled, room.ID,
	)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

func (s *Store) DeleteRoom(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM rooms WHERE id = ?", id)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

func (s *Store) InsertRecording(ctx context.Context, recording Recording) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO recordings (
			id, room_id, room_slug, egress_id, status, started_by, started_at,
			ended_at, duration_s, s3_key, size_bytes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		recording.ID, recording.RoomID, recording.RoomSlug, recording.EgressID,
		recording.Status, recording.StartedBy, recording.StartedAt,
		recording.EndedAt, recording.DurationS, recording.S3Key, recording.SizeBytes,
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
		       ended_at, duration_s, s3_key, size_bytes
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
		       ended_at, duration_s, s3_key, size_bytes
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
		       ended_at, duration_s, s3_key, size_bytes
		FROM recordings WHERE id = ?`, id))
}

func (s *Store) RecordingByEgressID(ctx context.Context, egressID string) (Recording, error) {
	return scanRecording(s.db.QueryRowContext(ctx, `
		SELECT id, room_id, room_slug, egress_id, status, started_by, started_at,
		       ended_at, duration_s, s3_key, size_bytes
		FROM recordings WHERE egress_id = ?`, egressID))
}

func (s *Store) ActiveRecordingByRoomID(ctx context.Context, roomID string) (Recording, error) {
	return scanRecording(s.db.QueryRowContext(ctx, `
		SELECT id, room_id, room_slug, egress_id, status, started_by, started_at,
		       ended_at, duration_s, s3_key, size_bytes
		FROM recordings
		WHERE room_id = ? AND status IN ('starting', 'recording', 'finalizing')
		ORDER BY started_at DESC LIMIT 1`, roomID))
}

func (s *Store) DeleteRecording(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM recordings WHERE id = ?", id)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

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
		&endedAt, &durationS, &s3Key, &sizeBytes,
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
