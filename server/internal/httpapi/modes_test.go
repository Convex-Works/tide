package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	protocolauth "github.com/livekit/protocol/auth"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/config"
	"tide/internal/store"
)

// The deployment modes (ARCHITECTURE.md §2.1), through tide's real handler
// over HTTP: anonymous (§4.1), signed in without recording (§8), and the
// embedded media server's signaling (§2.1).

// anonymousMode configures startTide as a deployment without sign-in, with
// every per-client limit set to limit, behind a reverse proxy that says
// which client each request comes from.
func anonymousMode(limit int) func(*config.Config, *http.Server) {
	return func(cfg *config.Config, _ *http.Server) {
		cfg.Anonymous, cfg.Recording, cfg.Transcripts = true, false, false
		cfg.JoinRateLimit, cfg.LoginRateLimit = limit, limit
		behindProxy(cfg)
	}
}

// A browser keeps the one cookie that matters, tide's session, and comes
// from its own address.
type browser struct {
	k      *tideServer
	from   string
	cookie *http.Cookie
}

func (k *tideServer) browser(from string) *browser {
	return &browser{k: k, from: from}
}

// send makes a request as the SPA does, keeps the session tide sets, and
// returns tide's answer.
func (b *browser) send(method, path, body string) (int, http.Header, string) {
	b.k.t.Helper()
	status, header, answer := b.k.requestFrom(b.from, method, path, body, b.cookie, true)
	for _, cookie := range (&http.Response{Header: header}).Cookies() {
		if cookie.Name == auth.SessionCookieName {
			b.cookie = cookie
		}
	}
	return status, header, answer
}

// want fails the test unless tide answers want, and decodes the answer into
// out if set.
func (b *browser) want(method, path, body string, want int, out any) http.Header {
	b.k.t.Helper()
	status, header, answer := b.send(method, path, body)
	if status != want {
		b.k.t.Fatalf("%s %s from %s = %d %s, want %d", method, path, b.from, status, answer, want)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(answer), out); err != nil {
			b.k.t.Fatalf("%s %s = %s: %v", method, path, answer, err)
		}
	}
	return header
}

func (b *browser) me() api.Me {
	b.k.t.Helper()
	var me api.Me
	header := b.want(http.MethodGet, api.MePath, "", http.StatusOK, &me)
	if header.Get("Cache-Control") != "no-store" {
		b.k.t.Fatalf("/api/me Cache-Control = %q, want no-store", header.Get("Cache-Control"))
	}
	return me
}

func (b *browser) join(slug, name string) api.JoinResponse {
	b.k.t.Helper()
	var joined api.JoinResponse
	b.want(http.MethodPost, fill(api.RoomJoinPath, slug), fmt.Sprintf(`{"name":%q}`, name), http.StatusOK, &joined)
	return joined
}

// grants verifies a meeting token as the media server would, and returns
// its grants.
func grants(t *testing.T, cfg config.Config, token string) *protocolauth.ClaimGrants {
	t.Helper()
	verifier, err := protocolauth.ParseAPIToken(token)
	if err != nil {
		t.Fatal(err)
	}
	_, claims, err := verifier.Verify(cfg.LiveKitAPISecret)
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

// recordingRoutes are a request to every route recording adds, and to the
// transcripts and machines, which need it.
func recordingRoutes(slug string) []struct{ method, path string } {
	return []struct{ method, path string }{
		{http.MethodPost, fill(api.RecordingStartPath, slug)},
		{http.MethodPost, fill(api.RecordingStopPath, slug)},
		{http.MethodGet, fill(api.RoomRecordingsPath, slug)},
		{http.MethodDelete, fill(api.RecordingPath, "rec-1")},
		{http.MethodGet, fill(api.RecordingDownloadPath, "rec-1")},
		// A method a route doesn't take: with the route there, 405.
		{http.MethodGet, fill(api.RecordingStartPath, slug)},
		{http.MethodPost, fill(api.RecordingTranscriptPath, "rec-1")},
		{http.MethodGet, api.MachinesPath},
		{http.MethodGet, api.MoilBasePath + "/v1/info"},
	}
}

func TestAnonymousTideHoldsMeetingsWithoutSignIn(t *testing.T) {
	k := startTide(t, anonymousMode(100))

	// /api/me gives a browser without a session an anonymous one.
	owner := k.browser("192.0.2.10")
	me := owner.me()
	if !me.Anonymous || me.Recording || me.Transcripts || !strings.HasPrefix(me.Sub, "anon:") || me.Email != "" || me.Name != "" {
		t.Fatalf("/api/me = %+v, want an anonymous session", me)
	}
	if owner.cookie == nil || owner.cookie.MaxAge != int(auth.AnonymousSessionLifetime/time.Second) {
		t.Fatalf("session cookie = %+v, want one lasting 30 days", owner.cookie)
	}
	// It keeps it.
	kept := owner.cookie.Value
	if again := owner.me(); again.Sub != me.Sub || owner.cookie.Value != kept {
		t.Fatalf("/api/me again = %+v, want the same session", again)
	}

	// The browser that creates a room owns it...
	var room api.RoomInfo
	owner.want(http.MethodPost, api.RoomsPath, `{"name":"Standup"}`, http.StatusCreated, &room)
	var listed []api.RoomInfo
	owner.want(http.MethodGet, api.RoomsPath, "", http.StatusOK, &listed)
	if len(listed) != 1 || listed[0].Slug != room.Slug {
		t.Fatalf("rooms = %+v, want the one it created", listed)
	}
	// ...joins it as its host, without the lobby...
	joined := owner.join(room.Slug, "Ada")
	if joined.Status != "admitted" || joined.WSURL != k.cfg.LiveKitPublicURL {
		t.Fatalf("owner's join = %+v", joined)
	}
	claims := grants(t, k.cfg, joined.Token)
	if !claims.Video.RoomAdmin || claims.Video.Room != room.Slug || claims.Metadata != `{"role":"host"}` ||
		!strings.HasPrefix(claims.Identity, "host:"+me.Sub+":") || claims.Name != "Ada" {
		t.Fatalf("owner's token grants %+v, identity %q, name %q", claims.Video, claims.Identity, claims.Name)
	}

	// ...while another anonymous browser is a guest there, through the
	// lobby, and can manage nothing.
	guest := k.browser("192.0.2.20")
	if guestMe := guest.me(); guestMe.Sub == me.Sub || !guestMe.Anonymous {
		t.Fatalf("guest's /api/me = %+v", guestMe)
	}
	if waiting := guest.join(room.Slug, "Grace"); waiting.Status != "waiting" || waiting.RequestID == "" || waiting.Token != "" {
		t.Fatalf("guest's join = %+v, want the lobby", waiting)
	}
	var public api.PublicRoomInfo
	guest.want(http.MethodGet, fill(api.RoomPath, room.Slug), "", http.StatusOK, &public)
	if public.CanManage {
		t.Fatal("a guest can manage the room")
	}
	guest.want(http.MethodPatch, fill(api.RoomPath, room.Slug), `{"name":"Mine"}`, http.StatusForbidden, nil)
	guest.want(http.MethodDelete, fill(api.RoomPath, room.Slug), "", http.StatusForbidden, nil)
	guest.want(http.MethodPost, "/api/rooms/"+room.Slug+"/participants/guest%3Aabc/kick", "", http.StatusForbidden, nil)

	// Nothing that records exists, nor the identity provider's callback.
	for _, route := range recordingRoutes(room.Slug) {
		if status, _, body := owner.send(route.method, route.path, ""); status != http.StatusNotFound {
			t.Errorf("%s %s = %d %s, want 404", route.method, route.path, status, body)
		}
	}
	owner.want(http.MethodGet, api.AuthCallbackPath+"?state=x&code=y", "", http.StatusNotFound, nil)

	// The owner deletes the room, with no storage to clean.
	owner.want(http.MethodDelete, fill(api.RoomPath, room.Slug), "", http.StatusNoContent, nil)
	owner.want(http.MethodGet, fill(api.RoomPath, room.Slug), "", http.StatusNotFound, nil)

	// Signing out ends the session for good: the next /api/me is another.
	owner.want(http.MethodPost, api.AuthLogoutPath, "", http.StatusNoContent, nil)
	revoked := &browser{k: k, from: owner.from, cookie: &http.Cookie{Name: auth.SessionCookieName, Value: kept}}
	if after := revoked.me(); after.Sub == me.Sub {
		t.Fatal("a signed-out anonymous session still works")
	}
}

// Signing in without sign-in issues the same anonymous session, and keeps
// one the browser has.
func TestAnonymousLoginIssuesASessionAndRedirects(t *testing.T) {
	k := startTide(t, anonymousMode(100))
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login := func(cookie *http.Cookie) *http.Response {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, k.url+api.AuthLoginPath+"?next=/rooms/abc-defg-hij", nil)
		if err != nil {
			t.Fatal(err)
		}
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		return response
	}
	response := login(nil)
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/rooms/abc-defg-hij" {
		t.Fatalf("login = %d to %q", response.StatusCode, response.Header.Get("Location"))
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.SessionCookieName {
		t.Fatalf("login set %+v, want the session", cookies)
	}
	b := &browser{k: k, cookie: cookies[0]}
	if me := b.me(); !strings.HasPrefix(me.Sub, "anon:") {
		t.Fatalf("/api/me after login = %+v", me)
	}
	if again := login(cookies[0]); again.StatusCode != http.StatusFound || len(again.Cookies()) != 0 {
		t.Fatalf("login with a session = %d setting %+v, want it kept", again.StatusCode, again.Cookies())
	}
}

// Anyone can mint an anonymous session, so the limits count client
// addresses, never sessions (ARCHITECTURE.md §15): issuing sessions shares
// the login limit, and creating rooms has a bucket of its own.
func TestAnonymousLimitsCountAddressesNotSessions(t *testing.T) {
	const limit = 3
	k := startTide(t, anonymousMode(limit))

	for i := range limit {
		if me := k.browser("192.0.2.1").me(); !me.Anonymous {
			t.Fatalf("session %d = %+v", i+1, me)
		}
	}
	status, header, body := k.browser("192.0.2.1").send(http.MethodGet, api.MePath, "")
	if status != http.StatusTooManyRequests || !strings.Contains(body, "Too many requests") || header.Get("Set-Cookie") != "" {
		t.Fatalf("a session over the limit = %d %s, cookie %q", status, body, header.Get("Set-Cookie"))
	}
	if status, _, body := k.browser("192.0.2.1").send(http.MethodGet, api.AuthLoginPath, ""); status != http.StatusTooManyRequests {
		t.Fatalf("login from the same address = %d %s, want the shared limit", status, body)
	}
	k.browser("192.0.2.2").me() // another address isn't limited

	// Creating rooms: a second session from the same address shares the
	// first one's bucket.
	first, second := k.browser("198.51.100.7"), k.browser("198.51.100.7")
	first.me()
	second.me()
	for i := range limit {
		first.want(http.MethodPost, api.RoomsPath, fmt.Sprintf(`{"name":"Room %d"}`, i), http.StatusCreated, nil)
	}
	var refused api.ErrorResponse
	second.want(http.MethodPost, api.RoomsPath, `{"name":"One more"}`, http.StatusTooManyRequests, &refused)
	if refused.Error == "" {
		t.Fatal("429 without the JSON error")
	}
	elsewhere := k.browser("198.51.100.8")
	elsewhere.me()
	elsewhere.want(http.MethodPost, api.RoomsPath, `{"name":"Elsewhere"}`, http.StatusCreated, nil)

	// Looking rooms up: a session counts against its address, like a
	// guest, rather than getting a bucket of its own.
	var room api.RoomInfo
	elsewhere.want(http.MethodPost, api.RoomsPath, `{"name":"Lookup"}`, http.StatusCreated, &room)
	guest := k.browser("203.0.113.9")
	for range limit - 1 {
		guest.want(http.MethodGet, fill(api.RoomPath, room.Slug), "", http.StatusOK, nil)
	}
	withSession := &browser{k: k, from: guest.from, cookie: first.cookie}
	withSession.want(http.MethodGet, fill(api.RoomPath, room.Slug), "", http.StatusOK, nil)
	withSession.want(http.MethodGet, fill(api.RoomPath, room.Slug), "", http.StatusTooManyRequests, nil)
}

// Signed in without storage: everything but recording.
func TestSignedInWithoutRecording(t *testing.T) {
	k := startTide(t, func(cfg *config.Config, _ *http.Server) {
		cfg.Recording, cfg.Transcripts = false, false
	})
	alice := k.signIn(auth.Session{Sub: "alice", Name: "Alice", Email: "alice@example.com"})
	if me := alice.me(); me.Recording || me.Anonymous || me.Transcripts || me.Sub != "alice" {
		t.Fatalf("/api/me = %+v, want alice without recording", me)
	}
	room := alice.createRoom("Standup")
	for _, route := range recordingRoutes(room.Slug) {
		if status, _, body := k.request(route.method, route.path, "", alice.cookie, true); status != http.StatusNotFound {
			t.Errorf("%s %s = %d %s, want 404", route.method, route.path, status, body)
		}
	}
	// The identity provider's callback is there.
	if status, _, body := k.request(http.MethodGet, api.AuthCallbackPath, "", nil, false); status == http.StatusNotFound {
		t.Fatalf("callback = %d %s, want the route", status, body)
	}
	// Hosts still meet.
	b := &browser{k: k, cookie: alice.cookie}
	if joined := b.join(room.Slug, "Alice"); joined.Status != "admitted" || !grants(t, k.cfg, joined.Token).Video.RoomAdmin {
		t.Fatalf("host's join = %+v", joined)
	}
	// A recording made while storage was configured keeps its files queued
	// when its room is deleted now, to be removed once storage is back.
	recording := addRecording(t, k.db, room, "rec-1")
	alice.send(http.MethodDelete, fill(api.RoomPath, room.Slug), http.StatusNoContent, nil)
	queued, err := k.db.DueRemovals(context.Background(), time.Now().Add(time.Hour).Unix(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if want := recording.ObjectKeys(); len(queued) != len(want) {
		t.Fatalf("queued for removal %q, want the recording's files %q", queued, want)
	}
}

func TestSignedInWithRecordingServesItsRoutes(t *testing.T) {
	k := startTide(t, func(cfg *config.Config, _ *http.Server) { cfg.Transcripts = false })
	alice := k.signIn(auth.Session{Sub: "alice", Name: "Alice"})
	if me := alice.me(); !me.Recording || me.Anonymous {
		t.Fatalf("/api/me = %+v, want recording", me)
	}
	room := alice.createRoom("Standup")
	var recordings []api.RecordingInfo
	alice.send(http.MethodGet, fill(api.RoomRecordingsPath, room.Slug), http.StatusOK, &recordings)
	if status, _, body := k.request(http.MethodGet, fill(api.RecordingStartPath, room.Slug), "", alice.cookie, true); status != http.StatusMethodNotAllowed {
		t.Fatalf("GET start = %d %s, want 405 from the route", status, body)
	}
}

// signalRecorder stands in for the media server's signaling.
type signalRecorder struct{ paths []string }

func (s *signalRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.paths = append(s.paths, r.Method+" "+r.URL.RequestURI())
	w.WriteHeader(http.StatusSwitchingProtocols)
}

func TestSignalingIsForwardedUnderRTCOnly(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := config.Config{
		BaseURL: "http://localhost:8080", SessionSecret: "test-session-secret",
		LiveKitURL: "http://127.0.0.1:7880", LiveKitAPIKey: "devkey",
		LiveKitAPISecret: "test-livekit-secret-with-enough-bytes", Anonymous: true, MediaEmbedded: true,
	}
	signal := &signalRecorder{}
	handler, background, err := New(cfg, nil, db, nil, signal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = background.Close() })
	serve := func(method, target string) int {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, target, nil))
		return response.Code
	}
	// Every signaling path and method reaches it, without session or CSRF.
	for _, target := range []string{"/rtc", "/rtc/v1?access_token=t", "/rtc/validate", "/rtc/v1/validate"} {
		if code := serve(http.MethodGet, target); code != http.StatusSwitchingProtocols {
			t.Errorf("GET %s = %d, want the media server", target, code)
		}
	}
	if code := serve(http.MethodPost, "/rtc/v1"); code != http.StatusSwitchingProtocols {
		t.Errorf("POST /rtc/v1 = %d, want the media server", code)
	}
	// Nothing else does: not its API, not a path that only starts alike.
	for _, target := range []string{"/rtcx", "/twirp/livekit.RoomService/ListRooms", "/api/rtc"} {
		if code := serve(http.MethodGet, target); code == http.StatusSwitchingProtocols {
			t.Errorf("GET %s reached the media server", target)
		}
	}
	if len(signal.paths) != 5 {
		t.Fatalf("the media server saw %q", signal.paths)
	}

	// With an external media server, tide forwards nothing.
	cfg.MediaEmbedded = false
	handler, background, err = New(cfg, nil, db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = background.Close() })
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/rtc", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("GET /rtc without the embedded server = %d", response.Code)
	}
}

// New refuses the combinations config.Load refuses.
func TestNewRefusesWhatTheModesForbid(t *testing.T) {
	for name, cfg := range map[string]config.Config{
		"transcripts without recording": {Transcripts: true},
		"recording without sign-in":     {Anonymous: true, Recording: true},
	} {
		cfg.BaseURL, cfg.SessionSecret = "http://localhost:8080", "test-session-secret"
		if _, _, err := New(cfg, nil, nil, transcribeBundle(t), nil); err == nil {
			t.Errorf("New with %s succeeded", name)
		}
	}
}

// A session belongs to the mode that issued it (ARCHITECTURE.md §4.1), even
// when an operator switches modes keeping the session secret: an anonymous
// cookie is no host in a deployment with sign-in, and a host's (or an
// administrator's) cookie is nobody in an anonymous one.
func TestSessionsDontCrossModes(t *testing.T) {
	anonymous := startTide(t, anonymousMode(100))
	owner := anonymous.browser("192.0.2.10")
	owner.me()
	var room api.RoomInfo
	owner.want(http.MethodPost, api.RoomsPath, `{"name":"Mine"}`, http.StatusCreated, &room)

	// An administrator's cookie, signed with the same secret by a deployment
	// with sign-in, gets a new anonymous session here, and no room.
	admin := &browser{k: anonymous, from: "192.0.2.11",
		cookie: makeSessionCookie(t, anonymous.cfg, auth.Session{Sub: "root", Name: "Root", IsAdmin: true})}
	if me := admin.me(); !me.Anonymous || me.Sub == "root" || !strings.HasPrefix(me.Sub, auth.AnonymousSubPrefix) {
		t.Fatalf("an administrator's cookie in an anonymous deployment = %+v", me)
	}
	var lookup api.PublicRoomInfo
	admin.want(http.MethodGet, fill(api.RoomPath, room.Slug), "", http.StatusOK, &lookup)
	if lookup.CanManage {
		t.Fatal("an administrator's cookie manages an anonymous owner's room")
	}
	admin.want(http.MethodDelete, fill(api.RoomPath, room.Slug), "", http.StatusForbidden, nil)

	// The anonymous owner's cookie, in a deployment with sign-in and the same
	// secret, is no session at all.
	signedIn := startTide(t, func(cfg *config.Config, _ *http.Server) {
		cfg.Recording, cfg.Transcripts = false, false
		cfg.SessionSecret = anonymous.cfg.SessionSecret
	})
	stranger := &browser{k: signedIn, cookie: owner.cookie}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, api.MePath}, {http.MethodGet, api.RoomsPath}, {http.MethodPost, api.RoomsPath},
	} {
		if status, _, body := stranger.send(route.method, route.path, `{"name":"Smuggled"}`); status != http.StatusUnauthorized {
			t.Errorf("%s %s with an anonymous cookie = %d %s, want 401", route.method, route.path, status, body)
		}
	}
}

// An anonymous deployment stops creating rooms at its ceiling (§4.1); a
// deployment with sign-in has none.
func TestOnlyAnonymousRoomsHaveACeiling(t *testing.T) {
	saved := anonymousRoomCap
	anonymousRoomCap = 2
	t.Cleanup(func() { anonymousRoomCap = saved })

	k := startTide(t, anonymousMode(100))
	creator := k.browser("192.0.2.20")
	creator.me()
	creator.want(http.MethodPost, api.RoomsPath, `{"name":"One"}`, http.StatusCreated, nil)
	other := k.browser("192.0.2.21")
	other.me()
	other.want(http.MethodPost, api.RoomsPath, `{"name":"Two"}`, http.StatusCreated, nil)
	var refused api.ErrorResponse
	other.want(http.MethodPost, api.RoomsPath, `{"name":"Three"}`, http.StatusServiceUnavailable, &refused)
	if refused.Error != "This server has too many rooms. Try again later." {
		t.Fatalf("503 says %q", refused.Error)
	}

	signedIn := startTide(t, func(cfg *config.Config, _ *http.Server) { cfg.Recording, cfg.Transcripts = false, false })
	alice := signedIn.signIn(auth.Session{Sub: "alice", Name: "Alice"})
	for _, name := range []string{"One", "Two", "Three"} {
		alice.createRoom(name)
	}
}

// Every token tide mints for a room marks the room active before it leaves
// (§5), so the anonymous sweep, which deletes only rooms still idle, never
// deletes one a meeting is starting in, though the media server's webhook
// hasn't arrived (here it never does).
func TestTokensMarkTheirRoomActive(t *testing.T) {
	k := startTide(t, anonymousMode(100))
	owner := k.browser("192.0.2.30")
	owner.me()
	var room api.RoomInfo
	owner.want(http.MethodPost, api.RoomsPath, `{"name":"Fresh"}`, http.StatusCreated, &room)
	lastActive := func() *int64 {
		t.Helper()
		stored, err := k.db.RoomBySlug(context.Background(), room.Slug)
		if err != nil {
			t.Fatal(err)
		}
		return stored.LastActiveAt
	}
	if lastActive() != nil {
		t.Fatal("a room nobody joined is active")
	}
	if joined := owner.join(room.Slug, "Owner"); joined.Token == "" {
		t.Fatalf("owner's join = %+v", joined)
	}
	if lastActive() == nil {
		t.Fatal("the owner's token left without marking the room active")
	}
	// The lobby's admission does too: clear the mark, and admit a guest.
	if _, err := k.db.DeleteRoom(context.Background(), room.ID, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	owner.want(http.MethodPost, api.RoomsPath, `{"name":"Again","slug":"`+room.Slug+`"}`, http.StatusCreated, &room)
	guest := k.browser("192.0.2.31")
	waiting := guest.join(room.Slug, "Guest")
	if waiting.RequestID == "" {
		t.Fatalf("guest's join = %+v, want the lobby", waiting)
	}
	if lastActive() != nil {
		t.Fatal("a lobby request marked the room active before anyone was admitted")
	}
	owner.want(http.MethodPost, fill(api.LobbyApprovePath, waiting.RequestID), "", http.StatusNoContent, nil)
	if lastActive() == nil {
		t.Fatal("the admitted guest's token left without marking the room active")
	}
}

// The sweep is an anonymous deployment's alone: a deployment with sign-in
// keeps its rooms however long they go unused.
func TestOnlyAnAnonymousDeploymentSweeps(t *testing.T) {
	db, _ := openStore(t)
	for _, anonymous := range []bool{false, true} {
		cfg := config.Config{
			BaseURL: "http://127.0.0.1", SessionSecret: "test-session-secret-long-enough-for-tide",
			LiveKitURL: "ws://livekit.example", LiveKitAPIKey: "devkey",
			LiveKitAPISecret: "test-livekit-secret-with-enough-bytes", Anonymous: anonymous,
		}
		_, background, err := New(cfg, nil, db, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if (background.sweeper != nil) != anonymous {
			t.Errorf("anonymous=%v: sweeper %v", anonymous, background.sweeper)
		}
	}
}
