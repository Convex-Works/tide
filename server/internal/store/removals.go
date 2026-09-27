package store

import (
	"context"
	"database/sql"
)

// QueueRemovals queues keys for removal from storage once dueAt (Unix
// seconds) has passed. A key that is queued already keeps the earlier of
// its due times: nothing postpones a removal.
func (s *Store) QueueRemovals(ctx context.Context, keys []string, dueAt int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := queueRemovals(ctx, tx, keys, dueAt); err != nil {
		return err
	}
	return tx.Commit()
}

func queueRemovals(ctx context.Context, tx *sql.Tx, keys []string, dueAt int64) error {
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO object_removals (key, due_at) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET due_at = MIN(due_at, excluded.due_at)`,
			key, dueAt); err != nil {
			return err
		}
	}
	return nil
}

// dueRemovalsQuery lists the keys due for removal by object_removals_due_idx,
// the longest due first.
const dueRemovalsQuery = `
	SELECT key FROM object_removals WHERE due_at <= ?
	ORDER BY due_at ASC, key ASC LIMIT ?`

// DueRemovals returns up to limit keys due for removal by now, the longest
// due first.
func (s *Store) DueRemovals(ctx context.Context, now int64, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, dueRemovalsQuery, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// RemovalDone takes a key storage removed off the queue, if it was due by
// now. A key that isn't due yet stays queued: it names an object that may
// still be written, such as a staging key whose URL hasn't expired.
func (s *Store) RemovalDone(ctx context.Context, key string, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM object_removals WHERE key = ? AND due_at <= ?`, key, now)
	return err
}
