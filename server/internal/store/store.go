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

func IsSlugConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: rooms.slug")
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
