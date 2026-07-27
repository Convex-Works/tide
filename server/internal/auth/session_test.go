package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionRoundTripAndAttributes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	sessions := NewSessions("test-secret", "https://klisi.example", nil)
	sessions.now = func() time.Time { return now }
	recorder := httptest.NewRecorder()
	want := Session{Sub: "subject", Email: "host@example.com", Name: "Host", IsAdmin: true}
	if err := sessions.Set(recorder, want); err != nil {
		t.Fatal(err)
	}
	cookie := recorder.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite == 0 || cookie.Path != "/" {
		t.Fatalf("unexpected cookie attributes: %#v", cookie)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(cookie)
	got, err := sessions.Read(request)
	if err != nil {
		t.Fatal(err)
	}
	want.Exp = now.Add(sessionLifetime).Unix()
	want.SID = got.SID // random per session; presence is asserted in TestSessionRevocation
	if got != want {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
}

func TestSessionRejectsTampering(t *testing.T) {
	sessions := NewSessions("test-secret", "http://localhost:8080", nil)
	recorder := httptest.NewRecorder()
	if err := sessions.Set(recorder, Session{Sub: "subject"}); err != nil {
		t.Fatal(err)
	}
	cookie := recorder.Result().Cookies()[0]
	parts := strings.Split(cookie.Value, ".")
	if parts[1][0] == 'A' {
		parts[1] = "B" + parts[1][1:]
	} else {
		parts[1] = "A" + parts[1][1:]
	}
	cookie.Value = strings.Join(parts, ".")
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(cookie)
	if _, err := sessions.Read(request); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Read() error = %v, want ErrInvalidSession", err)
	}
}

func TestSessionRejectsExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	sessions := NewSessions("test-secret", "http://localhost:8080", nil)
	sessions.now = func() time.Time { return now }
	recorder := httptest.NewRecorder()
	if err := sessions.Set(recorder, Session{Sub: "subject"}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(recorder.Result().Cookies()[0])
	sessions.now = func() time.Time { return now.Add(sessionLifetime + time.Second) }
	if _, err := sessions.Read(request); !errors.Is(err, ErrExpiredSession) {
		t.Fatalf("Read() error = %v, want ErrExpiredSession", err)
	}
}

type fakeRevocations struct {
	revoked map[string]int64
}

func (f *fakeRevocations) RevokeSession(_ context.Context, sid string, expiresAt int64) error {
	if f.revoked == nil {
		f.revoked = map[string]int64{}
	}
	f.revoked[sid] = expiresAt
	return nil
}

func (f *fakeRevocations) IsSessionRevoked(_ context.Context, sid string) (bool, error) {
	_, ok := f.revoked[sid]
	return ok, nil
}

func TestSessionRevocation(t *testing.T) {
	revocations := &fakeRevocations{}
	sessions := NewSessions("test-secret", "http://localhost:8080", revocations)
	recorder := httptest.NewRecorder()
	if err := sessions.Set(recorder, Session{Sub: "user-1"}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(recorder.Result().Cookies()[0])

	session, err := sessions.Read(request)
	if err != nil {
		t.Fatal(err)
	}
	if session.SID == "" {
		t.Fatal("sessions must carry a revocation ID")
	}
	if err := sessions.Revoke(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Read(request); !errors.Is(err, ErrRevokedSession) {
		t.Fatalf("expected ErrRevokedSession after revoke, got %v", err)
	}
}
