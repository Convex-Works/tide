package recording

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	protocol "github.com/livekit/protocol/livekit"

	"klisi/internal/config"
	"klisi/internal/store"
)

// Storage out of reach holds a reconciler pass up for at most the removals'
// own deadline, however many files are due: the pass stops removing at the
// first file storage doesn't answer for and keeps them all queued. It heals
// the recording whose egress_ended webhook was lost first.
func TestStorageOutOfReachDoesntHoldTheReconcilerUp(t *testing.T) {
	for _, test := range []struct {
		name    string
		hangs   bool
		storage func(t *testing.T, requests *atomic.Int32) string // its endpoint
	}{
		{"storage takes requests and never answers", true, func(t *testing.T, requests *atomic.Int32) string {
			answer := make(chan struct{})
			s3 := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				select {
				case <-answer:
				case <-r.Context().Done():
				}
			}))
			t.Cleanup(s3.Close)
			t.Cleanup(func() { close(answer) })
			return s3.URL
		}},
		{"storage refuses connections", false, func(t *testing.T, _ *atomic.Int32) string {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			listener.Close()
			return "http://" + address
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "klisi.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			ctx := context.Background()
			room := store.Room{ID: "room-1", Slug: "calm-otter-412", Name: "Weekly", OwnerSub: "owner", CreatedAt: 1}
			if err := db.CreateRoom(ctx, room); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			objects := NewMinIOStore(config.Config{
				S3Endpoint: test.storage(t, &requests), S3PublicEndpoint: "https://s3.example", S3Bucket: "klisi",
				S3AccessKey: "key", S3SecretKey: "secret", S3Region: "us-east-1",
			})
			// A recording whose egress_ended webhook was lost.
			egress := &fakeEgressClient{egresses: []*protocol.EgressInfo{{
				EgressId: "egress-1", RoomName: room.Slug, Status: protocol.EgressStatus_EGRESS_COMPLETE,
				EndedAt: time.Unix(1_700_000_000, 0).UnixNano(),
				FileResults: []*protocol.FileInfo{{
					Filename: "recordings/calm-otter-412/rec-1/x.ogg", Duration: int64(time.Minute), Size: 1_000,
				}},
			}}}
			handler := NewHandler(db, egress, &fakeRoomService{}, objects, "", nil)
			handler.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
			handler.removeDueTimeout = time.Second
			insertRecording(t, handler, room, "rec-1", "egress-1", "recording", 1_699_999_000)
			// And a full batch of files due for removal.
			keys := make([]string, removalBatch)
			for i := range keys {
				keys[i] = fmt.Sprintf("recordings/calm-otter-412/old-%03d/x.ogg", i)
			}
			if err := db.QueueRemovals(ctx, keys, 1_600_000_000); err != nil {
				t.Fatal(err)
			}

			passed := make(chan struct{})
			start := time.Now()
			go func() {
				defer close(passed)
				handler.reconcile(ctx)
			}()
			if test.hangs {
				// While storage keeps the pass waiting, the recording is healed.
				for requests.Load() == 0 {
					time.Sleep(time.Millisecond)
				}
				if got := recordingStatus(t, handler, "rec-1"); got != "completed" {
					t.Fatalf("the recording whose webhook was lost is %q while the pass removes files", got)
				}
			}
			select {
			case <-passed:
			case <-time.After(10 * time.Second):
				t.Fatal("a reconciler pass is still removing files after 10 s")
			}
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("the pass took %v", took)
			}
			if got := recordingStatus(t, handler, "rec-1"); got != "completed" {
				t.Fatalf("the recording whose webhook was lost is %q", got)
			}
			if n := requests.Load(); n > 1 {
				t.Fatalf("storage got %d requests; the pass should stop at the first file", n)
			}
			if queued, err := db.DueRemovals(ctx, 1_700_000_000, 2*removalBatch); err != nil || len(queued) != removalBatch {
				t.Fatalf("still queued: %d, %v; want all %d", len(queued), err, removalBatch)
			}
		})
	}
}

// Removing queued files carries on past a file storage refuses, and stops
// at the first it can't reach storage for: the rest would fail the same
// way, each after the storage client's retries.
func TestRemovingQueuedFilesStopsWhenStorageIsOutOfReach(t *testing.T) {
	keys := []string{"a", "b", "c", "d"}
	for _, test := range []struct {
		name  string
		err   error
		tries int
	}{
		{"storage refuses a file", errors.New("AccessDenied: Access Denied."), len(keys)},
		{"storage refuses connections", &url.Error{Op: "Delete", URL: "http://minio:9000/klisi/a", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}, 1},
		{"storage doesn't answer in time", fmt.Errorf("remove: %w", context.DeadlineExceeded), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			objects := &refusingStore{err: test.err}
			err := RemoveQueued(context.Background(), objects, noQueue{}, keys, 1)
			if err == nil || !errors.Is(err, test.err) || objects.tries != test.tries {
				t.Fatalf("RemoveQueued() = %v after %d tries, want %d", err, objects.tries, test.tries)
			}
		})
	}
}

type refusingStore struct {
	err   error
	tries int
}

func (s *refusingStore) Remove(context.Context, string) error {
	s.tries++
	return s.err
}

type noQueue struct{}

func (noQueue) DueRemovals(context.Context, int64, int) ([]string, error) { return nil, nil }
func (noQueue) RemovalDone(context.Context, string, int64) error          { return nil }
