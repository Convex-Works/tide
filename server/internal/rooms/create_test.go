package rooms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/store"
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
