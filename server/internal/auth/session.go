package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	SessionCookieName = "klisi_session"
	sessionLifetime   = 7 * 24 * time.Hour
)

var (
	ErrNoSession      = errors.New("session cookie is missing")
	ErrInvalidSession = errors.New("session cookie is invalid")
	ErrExpiredSession = errors.New("session cookie has expired")
)

type Session struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Exp   int64  `json:"exp"`
}

type Sessions struct {
	secret []byte
	secure bool
	now    func() time.Time
}

func NewSessions(secret, baseURL string) *Sessions {
	parsed, err := url.Parse(baseURL)
	secure := err == nil && strings.EqualFold(parsed.Scheme, "https")
	return &Sessions{
		secret: []byte(secret),
		secure: secure,
		now:    time.Now,
	}
}

func (s *Sessions) Set(w http.ResponseWriter, session Session) error {
	expires := s.now().Add(sessionLifetime)
	session.Exp = expires.Unix()
	value, err := s.sign(session)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(sessionLifetime / time.Second),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Sessions) Read(r *http.Request) (Session, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		if errors.Is(err, http.ErrNoCookie) {
			return Session{}, ErrNoSession
		}
		return Session{}, ErrInvalidSession
	}
	var session Session
	if err := s.verify(cookie.Value, &session); err != nil {
		return Session{}, err
	}
	if session.Sub == "" {
		return Session{}, ErrInvalidSession
	}
	if session.Exp <= s.now().Unix() {
		return Session{}, ErrExpiredSession
	}
	return session, nil
}

func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(1, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Sessions) sign(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(encoded))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + signature, nil
}

func (s *Sessions) verify(value string, target any) error {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ErrInvalidSession
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ErrInvalidSession
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return ErrInvalidSession
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(payload, target) != nil {
		return ErrInvalidSession
	}
	return nil
}
