package store

import (
	"strings"
	"testing"
)

// plan is SQLite's query plan for query, one step per line.
func plan(t *testing.T, db *Store, query string, args ...any) string {
	t.Helper()
	rows, err := db.db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var steps []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(steps, "\n")
}

// The transcript queries klisi runs on every recordings list and every
// reconciler pass find their rows by index, however many recordings and
// transcripts there are: no full scan, no sort.
func TestTranscriptQueriesUseIndexes(t *testing.T) {
	db := transcriptsTestStore(t)
	for _, test := range []struct {
		name, query string
		args        []any
		want        []string
	}{
		{"a room's transcripts", transcriptsByRoomQuery, []any{"room-a"}, []string{
			"SEARCH r USING INDEX recordings_room_idx (room_id=?)",
			"SEARCH t USING INDEX sqlite_autoindex_transcripts_1 (recording_id=?)",
		}},
		{"the pending transcripts, oldest request first", pendingTranscriptsQuery, nil, []string{
			"SCAN t USING COVERING INDEX transcripts_pending_requested_idx",
			"SEARCH r USING INDEX sqlite_autoindex_recordings_1 (id=?)",
		}},
	} {
		if got := plan(t, db, test.query, test.args...); got != strings.Join(test.want, "\n") {
			t.Errorf("%s: plan\n%s\nwant\n%s", test.name, got, strings.Join(test.want, "\n"))
		}
	}
}
