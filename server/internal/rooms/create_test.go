package rooms

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/store"
)

// POST /api/rooms takes an optional name and an optional slug
// (ARCHITECTURE.md §5).

func create(t *testing.T, handler *Handler, body string) (int, api.RoomInfo, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, api.RoomsPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: "owner"}))
	response := httptest.NewRecorder()
	handler.Create(response, request)
	var room api.RoomInfo
	if response.Code == http.StatusCreated {
		if err := json.Unmarshal(response.Body.Bytes(), &room); err != nil {
			t.Fatal(err)
		}
	}
	return response.Code, room, response.Body.String()
}

func TestCreateRoom(t *testing.T) {
	handler, _, db, existing := deleteTestHandler(t)
	long := strings.Repeat("n", 101)
	// Names are counted in characters: 60 two-byte characters are 120 bytes.
	greek60 := strings.Repeat("λ", 60)
	greek101 := strings.Repeat("λ", 101)

	for _, test := range []struct {
		name, body string
		// wantSlug is the slug the room gets; "default" is a generated one.
		wantStatus        int
		wantSlug          string
		wantName          string
		wantErrorContains string
	}{
		{name: "name only", body: `{"name":"Weekly sync"}`, wantStatus: 201, wantSlug: "default", wantName: "Weekly sync"},
		{name: "empty body", body: `{}`, wantStatus: 201, wantSlug: "default", wantName: "default"},
		{name: "blank name", body: `{"name":"   "}`, wantStatus: 201, wantSlug: "default", wantName: "default"},
		{name: "blank slug is generated", body: `{"name":"Retro","slug":"  "}`, wantStatus: 201, wantSlug: "default", wantName: "Retro"},
		{name: "given slug", body: `{"name":"Design review","slug":"design-review"}`, wantStatus: 201, wantSlug: "design-review", wantName: "Design review"},
		{name: "given slug is normalized", body: `{"slug":"  Team-Weekly "}`, wantStatus: 201, wantSlug: "team-weekly", wantName: "team-weekly"},
		{name: "invalid slug", body: `{"name":"Bad","slug":"no spaces"}`, wantStatus: 400, wantErrorContains: "lowercase letters, numbers, and single hyphens"},
		{name: "short slug", body: `{"slug":"ab"}`, wantStatus: 400, wantErrorContains: "between 3 and 64"},
		{name: "taken slug", body: `{"name":"Again","slug":"` + existing.Slug + `"}`, wantStatus: 409, wantErrorContains: "That room link is already in use."},
		{name: "taken slug, any case", body: `{"slug":"` + strings.ToUpper(existing.Slug) + `"}`, wantStatus: 409, wantErrorContains: "That room link is already in use."},
		{name: "long name", body: `{"name":"` + long + `"}`, wantStatus: 400, wantErrorContains: "at most 100 characters"},
		{name: "multi-byte name of 60 characters", body: `{"name":"` + greek60 + `"}`, wantStatus: 201, wantSlug: "default", wantName: greek60},
		{name: "multi-byte name of 101 characters", body: `{"name":"` + greek101 + `"}`, wantStatus: 400, wantErrorContains: "at most 100 characters"},
		{name: "long name with a slug", body: `{"name":"` + long + `","slug":"long-name"}`, wantStatus: 400, wantErrorContains: "at most 100 characters"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, err := db.RoomsByOwner(context.Background(), "owner")
			if err != nil {
				t.Fatal(err)
			}
			status, room, body := create(t, handler, test.body)
			if status != test.wantStatus {
				t.Fatalf("status = %d %s, want %d", status, body, test.wantStatus)
			}
			after, err := db.RoomsByOwner(context.Background(), "owner")
			if err != nil {
				t.Fatal(err)
			}
			if status != http.StatusCreated {
				if !strings.Contains(body, test.wantErrorContains) {
					t.Fatalf("body = %s, want %q", body, test.wantErrorContains)
				}
				if len(after) != len(before) {
					t.Fatalf("a refused request made a room: %d rooms, then %d", len(before), len(after))
				}
				return
			}
			wantSlug, wantName := test.wantSlug, test.wantName
			if wantSlug == "default" {
				if !defaultSlugPattern.MatchString(room.Slug) {
					t.Fatalf("slug = %q, want a default slug", room.Slug)
				}
				wantSlug = room.Slug
			}
			if wantName == "default" {
				wantName = room.Slug
			}
			if room.Slug != wantSlug || room.Name != wantName || !room.LobbyEnabled {
				t.Fatalf("created %+v, want slug %q and name %q", room, wantSlug, wantName)
			}
			stored, err := db.RoomBySlug(context.Background(), room.Slug)
			if err != nil || stored.Name != wantName || stored.OwnerSub != "owner" {
				t.Fatalf("stored %+v, %v", stored, err)
			}
		})
	}
}

// A slug taken by another owner's room is taken all the same.
func TestCreateRoomRefusesAnotherOwnersSlug(t *testing.T) {
	handler, _, db, _ := deleteTestHandler(t)
	if err := db.CreateRoom(context.Background(), store.Room{
		ID: "theirs", Slug: "all-hands", Name: "All hands", OwnerSub: "someone-else", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if status, _, body := create(t, handler, `{"slug":"all-hands"}`); status != http.StatusConflict {
		t.Fatalf("status = %d %s, want 409", status, body)
	}
	if room, err := db.RoomBySlug(context.Background(), "all-hands"); err != nil || room.OwnerSub != "someone-else" {
		t.Fatalf("all-hands = %+v, %v", room, err)
	}
}

// An anonymous deployment caps how many rooms it keeps (ARCHITECTURE.md
// §4.1): at the ceiling creating one answers 503 until a room goes.
func TestCreateRoomStopsAtTheCap(t *testing.T) {
	handler, _, db, existing := deleteTestHandler(t)
	handler.SetRoomCap(3)
	for _, name := range []string{"Second", "Third"} {
		if status, _, body := create(t, handler, `{"name":"`+name+`"}`); status != http.StatusCreated {
			t.Fatalf("create %s = %d %s", name, status, body)
		}
	}
	status, _, body := create(t, handler, `{"name":"Fourth"}`)
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "This server has too many rooms. Try again later.") {
		t.Fatalf("create past the cap = %d %s, want 503", status, body)
	}
	if count, err := db.CountRooms(context.Background()); err != nil || count != 3 {
		t.Fatalf("rooms = %d, %v; want 3", count, err)
	}
	// A room going makes room for one.
	response := httptest.NewRecorder()
	handler.Delete(response, deleteRequest(existing.Slug, "owner"))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", response.Code)
	}
	if status, _, body := create(t, handler, `{"name":"Fourth"}`); status != http.StatusCreated {
		t.Fatalf("create after a delete = %d %s", status, body)
	}
	// Without a cap (a deployment with sign-in) there is none.
	handler.SetRoomCap(0)
	if status, _, body := create(t, handler, `{"name":"Fifth"}`); status != http.StatusCreated {
		t.Fatalf("create without a cap = %d %s", status, body)
	}
}

// Deleting a room ends the meeting in it (ARCHITECTURE.md §5), so that
// nobody stays on under a slug someone else can now create; a media server
// that can't be reached doesn't stop the delete.
func TestDeleteEndsTheMeetingInTheRoom(t *testing.T) {
	handler, _, db, room := deleteTestHandler(t)
	ender := &fakeEnder{err: errors.New("media server unavailable")}
	handler.SetMeetingEnder(ender)
	response := httptest.NewRecorder()
	handler.Delete(response, deleteRequest(room.Slug, "owner"))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", response.Code, response.Body)
	}
	if want := []string{room.Slug}; !slices.Equal(ender.ended, want) {
		t.Fatalf("ended meetings in %q, want %q", ender.ended, want)
	}
	if _, err := db.RoomBySlug(context.Background(), room.Slug); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("room lookup after delete = %v", err)
	}
	// A refused delete ends nothing.
	other := store.Room{ID: "room-2", Slug: "other-room", Name: "Other", OwnerSub: "owner", CreatedAt: 1}
	if err := db.CreateRoom(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.Delete(response, deleteRequest(other.Slug, "stranger"))
	if response.Code != http.StatusForbidden || len(ender.ended) != 1 {
		t.Fatalf("a stranger's delete = %d, ended %q", response.Code, ender.ended)
	}
}
