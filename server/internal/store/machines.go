package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"
)

// The Store keeps the machines hosts paired through moil (ARCHITECTURE.md
// §8.1): it is the moil.Server's Store.
var _ moil.Store = (*Store)(nil)

const machineColumns = `id, owner_sub, token_hash, paired_at, report`

// AddMachine implements moil.Store.
func (s *Store) AddMachine(ctx context.Context, machine moil.MachineRecord) error {
	report, err := json.Marshal(machine.Report)
	if err != nil {
		return fmt.Errorf("encode machine report: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO machines (`+machineColumns+`) VALUES (?, ?, ?, ?, ?)`,
		machine.ID, machine.Owner, machine.TokenHash, machine.PairedAt.Unix(), string(report))
	return err
}

// Machine implements moil.Store.
func (s *Store) Machine(ctx context.Context, id string) (moil.MachineRecord, error) {
	return scanMachine(s.db.QueryRowContext(ctx,
		`SELECT `+machineColumns+` FROM machines WHERE id = ?`, id))
}

// MachineByToken implements moil.Store.
func (s *Store) MachineByToken(ctx context.Context, tokenHash string) (moil.MachineRecord, error) {
	return scanMachine(s.db.QueryRowContext(ctx,
		`SELECT `+machineColumns+` FROM machines WHERE token_hash = ?`, tokenHash))
}

// Machines implements moil.Store: the owner's machines, oldest first.
func (s *Store) Machines(ctx context.Context, owner string) ([]moil.MachineRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+machineColumns+` FROM machines WHERE owner_sub = ?
		 ORDER BY paired_at ASC, rowid ASC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	machines := make([]moil.MachineRecord, 0)
	for rows.Next() {
		machine, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		machines = append(machines, machine)
	}
	return machines, rows.Err()
}

// SaveReport implements moil.Store. It never recreates a removed machine.
func (s *Store) SaveReport(ctx context.Context, id string, report moil.MachineReport) error {
	encoded, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode machine report: %w", err)
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE machines SET report = ? WHERE id = ?`, string(encoded), id)
	if err != nil {
		return err
	}
	return machineChanged(result)
}

// RemoveMachine implements moil.Store.
func (s *Store) RemoveMachine(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM machines WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return machineChanged(result)
}

func scanMachine(scanner recordingScanner) (moil.MachineRecord, error) {
	var machine moil.MachineRecord
	var pairedAt int64
	var report string
	err := scanner.Scan(&machine.ID, &machine.Owner, &machine.TokenHash, &pairedAt, &report)
	if errors.Is(err, sql.ErrNoRows) {
		return moil.MachineRecord{}, moil.ErrNotFound
	}
	if err != nil {
		return moil.MachineRecord{}, err
	}
	machine.PairedAt = time.Unix(pairedAt, 0).UTC()
	if err := json.Unmarshal([]byte(report), &machine.Report); err != nil {
		return moil.MachineRecord{}, fmt.Errorf("decode report of machine %s: %w", machine.ID, err)
	}
	return machine, nil
}

func machineChanged(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return moil.ErrNotFound
	}
	return nil
}
