package auth

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionRoundTripAndAttributes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	sessions := NewSessions("test-secret", "https://klisi.example")
	sessions.now = func() time.Time { return now }
	recorder := httptest.NewRecorder()
	want := Session{Sub: "subject", Email: "host@example.com", Name: "Host"}
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
	if got != want {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
}

func TestSessionRejectsTampering(t *testing.T) {
	sessions := NewSessions("test-secret", "http://localhost:8080")
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
	sessions := NewSessions("test-secret", "http://localhost:8080")
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
