package auth

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"
)

var anonymousSub = regexp.MustCompile(`^anon:[a-z2-7]{26}$`)

func TestIssueAnonymousMakesAThirtyDaySessionOfItsOwn(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	sessions := NewSessions("test-secret", "https://tide.example", nil)
	sessions.now = func() time.Time { return now }

	recorder := httptest.NewRecorder()
	issued, err := sessions.IssueAnonymous(recorder)
	if err != nil {
		t.Fatal(err)
	}
	if !anonymousSub.MatchString(issued.Sub) || !IsAnonymous(issued) {
		t.Fatalf("sub = %q, want anon: and 26 base32 characters", issued.Sub)
	}
	if issued.Email != "" || issued.Name != "" || issued.IsAdmin {
		t.Fatalf("anonymous session = %+v, want no email, name or administration", issued)
	}
	cookie := recorder.Result().Cookies()[0]
	if cookie.MaxAge != int((30*24*time.Hour)/time.Second) || !cookie.HttpOnly || !cookie.Secure {
		t.Fatalf("cookie = %+v, want a 30-day HttpOnly Secure cookie", cookie)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	read, err := sessions.Read(request)
	if err != nil {
		t.Fatal(err)
	}
	if read != issued || read.Exp != now.Add(30*24*time.Hour).Unix() || read.SID == "" {
		t.Fatalf("Read = %+v, issued %+v", read, issued)
	}
	// Each one is a different owner.
	other, err := sessions.IssueAnonymous(httptest.NewRecorder())
	if err != nil || other.Sub == issued.Sub {
		t.Fatalf("second session = %+v, %v", other, err)
	}
	if IsAnonymous(Session{Sub: "alice"}) {
		t.Fatal("a signed-in host's session is not anonymous")
	}
}

func TestAnonymousLoginKeepsASessionOrIssuesOne(t *testing.T) {
	sessions := NewSessions("test-secret", "http://localhost:8080", nil)

	recorder := httptest.NewRecorder()
	sessions.AnonymousLogin(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/login?next=/rooms/abc", nil))
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/rooms/abc" {
		t.Fatalf("login = %d to %q", recorder.Code, recorder.Header().Get("Location"))
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SessionCookieName {
		t.Fatalf("cookies = %+v, want the session", cookies)
	}

	// A browser that has one keeps it: a new one would orphan its rooms.
	request := httptest.NewRequest(http.MethodGet, "/api/auth/login?next=//evil.example", nil)
	request = request.WithContext(WithSession(request.Context(), Session{Sub: "anon:kept"}))
	recorder = httptest.NewRecorder()
	sessions.AnonymousLogin(recorder, request)
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/" {
		t.Fatalf("login = %d to %q, want / for an unsafe next", recorder.Code, recorder.Header().Get("Location"))
	}
	if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("cookies = %+v, want the session kept", cookies)
	}
}
