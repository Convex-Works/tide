package store

import (
	"context"
	"database/sql"
)

// A Transcript is the transcripts row of a recording (ARCHITECTURE.md §8.1):
// the truth about whether the recording should have a transcript, and how
// making it went. A moil job is only ever a projection of a pending one.
type Transcript struct {
	RecordingID string
	// Status is "pending", "completed" or "failed".
	Status      string
	RequestedAt int64
	FinishedAt  *int64
	// Speakers is how many speakers a completed transcript found, if the
	// machine said.
	Speakers *int
	// Error says why a failed transcript failed.
	Error string
}

const transcriptColumns = `t.recording_id, t.status, t.requested_at, t.finished_at, t.speakers, t.error`

// CreateTranscripts adds a pending transcript for every completed recording
// with a file and no transcript, when its room's owner had a machine paired
// by the time it ended: pairing a machine is the opt-in. A recording that
// ended before its owner paired one waits for a request. It returns how many
// transcripts it added.
func (s *Store) CreateTranscripts(ctx context.Context, now int64) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO transcripts (recording_id, status, requested_at)
		SELECT r.id, 'pending', ?
		FROM recordings r JOIN rooms ON rooms.id = r.room_id
		WHERE r.status = 'completed' AND COALESCE(r.s3_key, '') != ''
		  AND NOT EXISTS (SELECT 1 FROM transcripts t WHERE t.recording_id = r.id)
		  AND EXISTS (
			SELECT 1 FROM machines m
			WHERE m.owner_sub = rooms.owner_sub AND m.paired_at <= r.ended_at)
		ON CONFLICT (recording_id) DO NOTHING`, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// A PendingTranscript is a pending transcript's recording, with what the
// reconciler needs of its room, found by the room's ID.
type PendingTranscript struct {
	Recording
	RequestedAt int64
	RoomName    string
	RoomOwner   string
}

// pendingTranscriptsQuery lists the pending transcripts in the order
// transcripts_pending_requested_idx keeps them, each with its recording
// and its recording's room.
const pendingTranscriptsQuery = `
	SELECT r.id, r.room_id, r.room_slug, r.egress_id, r.status, r.started_by, r.started_at,
	       r.audio_only, r.ended_at, r.duration_s, r.s3_key, r.size_bytes,
	       t.requested_at, rooms.name, rooms.owner_sub
	FROM transcripts t
	JOIN recordings r ON r.id = t.recording_id
	JOIN rooms ON rooms.id = r.room_id
	WHERE t.status = 'pending'
	ORDER BY t.requested_at ASC, t.recording_id ASC`

// PendingTranscripts returns the pending transcripts, the ones requested
// first first.
func (s *Store) PendingTranscripts(ctx context.Context) ([]PendingTranscript, error) {
	rows, err := s.db.QueryContext(ctx, pendingTranscriptsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pending := make([]PendingTranscript, 0)
	for rows.Next() {
		var transcript PendingTranscript
		transcript.Recording, err = scanRecording(rows,
			&transcript.RequestedAt, &transcript.RoomName, &transcript.RoomOwner)
		if err != nil {
			return nil, err
		}
		pending = append(pending, transcript)
	}
	return pending, rows.Err()
}

// Transcript returns a recording's transcript, or sql.ErrNoRows if it has
// none.
func (s *Store) Transcript(ctx context.Context, recordingID string) (Transcript, error) {
	return scanTranscript(s.db.QueryRowContext(ctx,
		`SELECT `+transcriptColumns+` FROM transcripts t WHERE t.recording_id = ?`, recordingID))
}

// transcriptsByRoomQuery finds a room's recordings by recordings_room_idx,
// and each one's transcript by its primary key.
const transcriptsByRoomQuery = `
	SELECT ` + transcriptColumns + `
	FROM transcripts t JOIN recordings r ON r.id = t.recording_id
	WHERE r.room_id = ?`

// TranscriptsByRoom returns the transcripts of a room's recordings, by
// recording ID.
func (s *Store) TranscriptsByRoom(ctx context.Context, roomID string) (map[string]Transcript, error) {
	rows, err := s.db.QueryContext(ctx, transcriptsByRoomQuery, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	transcripts := make(map[string]Transcript)
	for rows.Next() {
		transcript, err := scanTranscript(rows)
		if err != nil {
			return nil, err
		}
		transcripts[transcript.RecordingID] = transcript
	}
	return transcripts, rows.Err()
}

// RequestTranscript adds a pending transcript for a completed recording with
// a file and no transcript. It reports whether it did.
func (s *Store) RequestTranscript(ctx context.Context, recordingID string, now int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO transcripts (recording_id, status, requested_at)
		SELECT id, 'pending', ? FROM recordings
		WHERE id = ? AND status = 'completed' AND COALESCE(s3_key, '') != ''
		ON CONFLICT (recording_id) DO NOTHING`, now, recordingID)
	return changedOne(result, err)
}

// RetryTranscript makes a failed transcript pending again. It reports
// whether it did.
func (s *Store) RetryTranscript(ctx context.Context, recordingID string, now int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE transcripts
		SET status = 'pending', requested_at = ?, finished_at = NULL, speakers = NULL, error = NULL
		WHERE recording_id = ? AND status = 'failed'`, now, recordingID)
	return changedOne(result, err)
}

// CompleteTranscript records that a pending transcript was made. A
// transcript that is gone or no longer pending is left alone.
func (s *Store) CompleteTranscript(ctx context.Context, recordingID string, speakers *int, now int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE transcripts SET status = 'completed', finished_at = ?, speakers = ?, error = NULL
		WHERE recording_id = ? AND status = 'pending'`, now, speakers, recordingID)
	return err
}

// FailTranscript records why a pending transcript couldn't be made. A
// transcript that is gone or no longer pending is left alone.
func (s *Store) FailTranscript(ctx context.Context, recordingID, message string, now int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE transcripts SET status = 'failed', finished_at = ?, speakers = NULL, error = ?
		WHERE recording_id = ? AND status = 'pending'`, now, message, recordingID)
	return err
}

func scanTranscript(scanner recordingScanner) (Transcript, error) {
	var transcript Transcript
	var finishedAt, speakers sql.NullInt64
	var message sql.NullString
	err := scanner.Scan(
		&transcript.RecordingID, &transcript.Status, &transcript.RequestedAt,
		&finishedAt, &speakers, &message,
	)
	if err != nil {
		return Transcript{}, err
	}
	if finishedAt.Valid {
		transcript.FinishedAt = &finishedAt.Int64
	}
	if speakers.Valid {
		count := int(speakers.Int64)
		transcript.Speakers = &count
	}
	transcript.Error = message.String
	return transcript, nil
}

func changedOne(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}
