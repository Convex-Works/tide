package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"tide/internal/config"
)

// fakeProvider is an OpenID Connect provider with one client. Its token
// endpoint checks the code and the PKCE verifier as a real provider does,
// and answers with an ID token made from claims.
type fakeProvider struct {
	t      *testing.T
	server *httptest.Server
	key    *rsa.PrivateKey

	mu        sync.Mutex
	challenge string // from the authorization request
	claims    map[string]any
	exchanges int
}

const (
	fakeClientID     = "tide"
	fakeClientSecret = "client-secret"
	fakeCode         = "authorization-code"
)

func newFakeProvider(t *testing.T) *fakeProvider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{t: t, key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                p.server.URL,
			"authorization_endpoint":                p.server.URL + "/authorize",
			"token_endpoint":                        p.server.URL + "/token",
			"jwks_uri":                              p.server.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", p.token)
	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	p.claims = p.defaultClaims()
	return p
}

func (p *fakeProvider) defaultClaims() map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": p.server.URL, "aud": fakeClientID, "sub": "user-1",
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"email": "ada@example.com", "name": "Ada", "groups": []string{"staff"},
	}
}

func (p *fakeProvider) setClaim(name string, value any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if value == nil {
		delete(p.claims, name)
		return
	}
	p.claims[name] = value
}

func (p *fakeProvider) token(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exchanges++
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	verifier := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
	if id != fakeClientID || secret != fakeClientSecret ||
		r.PostFormValue("grant_type") != "authorization_code" || r.PostFormValue("code") != fakeCode ||
		base64.RawURLEncoding.EncodeToString(verifier[:]) != p.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	writeJSON(w, map[string]any{
		"access_token": "access", "token_type": "Bearer", "expires_in": 3600,
		"id_token": p.sign(p.claims),
	})
}

func (p *fakeProvider) sign(claims map[string]any) string {
	encode := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			p.t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	signed := encode(map[string]string{"alg": "RS256", "kid": "test", "typ": "JWT"}) + "." + encode(claims)
	digest := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

type signInTest struct {
	provider *fakeProvider
	oidc     *OIDC
	sessions *Sessions
}

func newSignInTest(t *testing.T, userGroups, adminGroups []string) *signInTest {
	provider := newFakeProvider(t)
	sessions := NewSessions("test-secret", "https://tide.example", nil)
	return &signInTest{
		provider: provider,
		sessions: sessions,
		oidc: NewOIDC(config.Config{
			BaseURL: "https://tide.example", OIDCIssuer: provider.server.URL,
			OIDCClientID: fakeClientID, OIDCClientSecret: fakeClientSecret,
			UserGroups: userGroups, AdminGroups: adminGroups,
		}, sessions),
	}
}

// login starts a sign-in and returns its state cookie and the state the
// provider would send back.
func (s *signInTest) login(t *testing.T, next string) (*http.Cookie, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	s.oidc.Login(recorder, httptest.NewRequest("GET", "/api/auth/login?next="+url.QueryEscape(next), nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("login status = %d: %s", recorder.Code, recorder.Body)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := location.Query()
	if location.Path != "/authorize" || query.Get("client_id") != fakeClientID ||
		query.Get("redirect_uri") != "https://tide.example/api/auth/callback" ||
		query.Get("code_challenge_method") != "S256" || query.Get("state") == "" {
		t.Fatalf("authorization request = %s", location)
	}
	s.provider.mu.Lock()
	s.provider.challenge = query.Get("code_challenge")
	s.provider.mu.Unlock()
	var state *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == oidcStateCookieName {
			state = cookie
		}
	}
	if state == nil || !state.HttpOnly || !state.Secure || state.Path != "/api/auth" {
		t.Fatalf("state cookie = %#v", state)
	}
	return state, query.Get("state")
}

// callback returns the provider's redirect back to tide, and the session it
// set, if any.
func (s *signInTest) callback(t *testing.T, cookie *http.Cookie, query url.Values) (*httptest.ResponseRecorder, *Session) {
	t.Helper()
	request := httptest.NewRequest("GET", "/api/auth/callback?"+query.Encode(), nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	s.oidc.Callback(recorder, request)
	clearsState := false
	var session *Session
	for _, cookie := range recorder.Result().Cookies() {
		switch cookie.Name {
		case oidcStateCookieName:
			clearsState = cookie.MaxAge < 0
		case SessionCookieName:
			read := httptest.NewRequest("GET", "/", nil)
			read.AddCookie(cookie)
			got, err := s.sessions.Read(read)
			if err != nil {
				t.Fatalf("session cookie does not read back: %v", err)
			}
			session = &got
		}
	}
	if !clearsState {
		t.Fatal("callback must clear the state cookie, whatever its outcome")
	}
	return recorder, session
}

func TestOIDCCallbackSignsIn(t *testing.T) {
	s := newSignInTest(t, []string{"staff"}, []string{"admins"})
	cookie, state := s.login(t, "/r/calm-otter-412?x=1")
	response, session := s.callback(t, cookie, url.Values{"code": {fakeCode}, "state": {state}})
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/r/calm-otter-412?x=1" {
		t.Fatalf("callback = %d to %q: %s", response.Code, response.Header().Get("Location"), response.Body)
	}
	if session == nil || session.Sub != "user-1" || session.Email != "ada@example.com" ||
		session.Name != "Ada" || session.IsAdmin {
		t.Fatalf("session = %#v", session)
	}

	// An admin group makes an admin; a missing name falls back to the email.
	s.provider.setClaim("groups", []string{"admins"})
	s.provider.setClaim("name", nil)
	cookie, state = s.login(t, "https://evil.example/")
	response, session = s.callback(t, cookie, url.Values{"code": {fakeCode}, "state": {state}})
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/" {
		t.Fatalf("callback = %d to %q, want an unsafe next to become /", response.Code, response.Header().Get("Location"))
	}
	if session == nil || !session.IsAdmin || session.Name != "ada@example.com" {
		t.Fatalf("admin session = %#v", session)
	}
}

func TestOIDCCallbackRefuses(t *testing.T) {
	for _, test := range []struct {
		name   string
		claims map[string]any // set on the ID token; nil deletes the claim
		query  func(state string) url.Values
		cookie func(*http.Cookie) *http.Cookie
		status int
		// exchanges says whether tide got as far as the token endpoint.
		exchanges bool
	}{
		{name: "no state cookie", cookie: func(*http.Cookie) *http.Cookie { return nil },
			status: http.StatusBadRequest},
		{name: "forged state cookie", cookie: func(c *http.Cookie) *http.Cookie {
			forged := *c
			forged.Value = strings.Replace(c.Value, ".", ".x", 1)
			return &forged
		}, status: http.StatusBadRequest},
		{name: "state from another sign-in", query: func(string) url.Values {
			return url.Values{"code": {fakeCode}, "state": {"other"}}
		}, status: http.StatusBadRequest},
		{name: "no state", query: func(string) url.Values {
			return url.Values{"code": {fakeCode}}
		}, status: http.StatusBadRequest},
		{name: "provider error", query: func(state string) url.Values {
			return url.Values{"error": {"access_denied"}, "state": {state}}
		}, status: http.StatusUnauthorized},
		{name: "wrong code", query: func(state string) url.Values {
			return url.Values{"code": {"guessed"}, "state": {state}}
		}, status: http.StatusUnauthorized, exchanges: true},
		{name: "token for another client", claims: map[string]any{"aud": "other-client"},
			status: http.StatusUnauthorized, exchanges: true},
		{name: "token from another issuer", claims: map[string]any{"iss": "https://id.evil.example"},
			status: http.StatusUnauthorized, exchanges: true},
		{name: "expired token", claims: map[string]any{"exp": time.Now().Add(-time.Hour).Unix()},
			status: http.StatusUnauthorized, exchanges: true},
		{name: "no subject", claims: map[string]any{"sub": " "},
			status: http.StatusUnauthorized, exchanges: true},
		{name: "not in a user group", claims: map[string]any{"groups": []string{"contractors"}},
			status: http.StatusForbidden, exchanges: true},
		{name: "no groups", claims: map[string]any{"groups": nil},
			status: http.StatusForbidden, exchanges: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newSignInTest(t, []string{"staff"}, []string{"admins"})
			for name, value := range test.claims {
				s.provider.setClaim(name, value)
			}
			cookie, state := s.login(t, "/")
			query := url.Values{"code": {fakeCode}, "state": {state}}
			if test.query != nil {
				query = test.query(state)
			}
			if test.cookie != nil {
				cookie = test.cookie(cookie)
			}
			response, session := s.callback(t, cookie, query)
			if response.Code != test.status || session != nil {
				t.Fatalf("callback = %d, session %#v, want %d and no session: %s",
					response.Code, session, test.status, response.Body)
			}
			if (s.provider.exchanges > 0) != test.exchanges {
				t.Fatalf("token requests = %d, want any: %t", s.provider.exchanges, test.exchanges)
			}
		})
	}
}

// A state cookie is good for one sign-in, for oidcStateLifetime.
func TestOIDCCallbackRefusesAnExpiredState(t *testing.T) {
	s := newSignInTest(t, nil, nil)
	cookie, state := s.login(t, "/")
	s.sessions.now = func() time.Time { return time.Now().Add(oidcStateLifetime + time.Second) }
	response, session := s.callback(t, cookie, url.Values{"code": {fakeCode}, "state": {state}})
	if response.Code != http.StatusBadRequest || session != nil || s.provider.exchanges != 0 {
		t.Fatalf("callback = %d, session %#v, token requests %d", response.Code, session, s.provider.exchanges)
	}
}
