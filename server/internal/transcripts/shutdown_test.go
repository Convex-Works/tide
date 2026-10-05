package transcripts_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"tide/internal/store"
)

// Stopping tide records the ends of the jobs machines finished for up to
// its stop grace in all, not for up to two minutes each: storage or the
// database hanging holds tide's exit up only so long. The ends it couldn't
// record leave their rows pending, and the next start submits them again.
func TestStoppingIsBoundedWhileStorageOrTheDatabaseHangs(t *testing.T) {
	const jobs = 4
	for _, test := range []struct {
		name string
		// hang makes storage or the database hang as tide stops, until
		// release. The jobs are finished by then.
		hang func(e *env) (release func())
		// within is how long stopping may take with a stop grace of 200 ms.
		within time.Duration
	}{
		{"storage hangs while the jobs' ends are recorded", func(e *env) func() {
			return e.s3.Hold(opStat).Release
		}, 5 * time.Second},
		// SQLite waits up to its busy timeout, 5 s, for a lock whatever the
		// context says: stopping takes that long once, not once per job.
		{"the database is locked while the jobs' ends are recorded", func(e *env) func() {
			// The jobs end while the database refuses to record them.
			e.sql(`CREATE TRIGGER refuse_ends BEFORE UPDATE ON transcripts
				BEGIN SELECT RAISE(ABORT, 'database or disk is full'); END`)
			return nil
		}, 10 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := newEnv(t)
			e.service.SetStopGrace(200 * time.Millisecond)
			room := e.room("alice", "Standup")
			machine := e.machine("alice")
			release := test.hang(e)
			locked := release == nil
			recordings := make([]store.Recording, jobs)
			for i := range recordings {
				recordings[i] = e.record(room, time.Now())
				finish(t, machine.NextAttempt(), 2)
			}
			if locked {
				// Every end has failed to be recorded once, then the
				// database locks up as tide stops.
				waitFor(t, "the ends to fail to be recorded", func() bool { return e.s3.Calls(opStat) >= 4*jobs })
				release = e.lockDatabase(`DROP TRIGGER refuse_ends`)
			}

			stopping := time.Now()
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				e.stopService()
			}()
			select {
			case <-stopped:
			case <-time.After(30 * time.Second):
				t.Fatal("tide didn't stop in 30 s")
			}
			if took := time.Since(stopping); took > test.within {
				t.Fatalf("tide took %v to stop, want at most %v", took, test.within)
			}
			release()
			for _, rec := range recordings {
				if row, ok := e.row(rec); !ok || row.Status != "pending" {
					t.Fatalf("row of %s after tide stopped = %+v, %t", rec.ID, row, ok)
				}
			}

			// The next start submits them again.
			_ = e.moil.Close()
			e.start()
			for _, rec := range recordings {
				e.submitted(rec)
			}
		})
	}
}

// lockDatabase takes SQLite's write lock from another connection, as
// another process would, runs statements in its transaction, and keeps it
// until release, or the end of the test, which commits them.
func (e *env) lockDatabase(statements ...string) (release func()) {
	e.t.Helper()
	ctx := context.Background()
	locker, err := sql.Open("sqlite", e.path)
	if err != nil {
		e.t.Fatal(err)
	}
	conn, err := locker.Conn(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, statement := range append([]string{"PRAGMA busy_timeout=5000", "BEGIN IMMEDIATE"}, statements...) {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			e.t.Fatalf("%s: %v", statement, err)
		}
	}
	release = sync.OnceFunc(func() {
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			e.t.Errorf("releasing the database: %v", err)
		}
		conn.Close()
		locker.Close()
	})
	e.t.Cleanup(release)
	return release
}
