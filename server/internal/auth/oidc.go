package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"klisi/internal/api"
	"klisi/internal/config"
)

const (
	oidcStateCookieName = "klisi_oidc_state"
	oidcStateLifetime   = 10 * time.Minute
)

type oidcState struct {
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
	Exp      int64  `json:"exp"`
}

type OIDC struct {
	issuer       string
	clientID     string
	clientSecret string
	redirectURL  string
	sessions     *Sessions

	mu       sync.Mutex
	provider *oidc.Provider
}

func NewOIDC(cfg config.Config, sessions *Sessions) *OIDC {
	return &OIDC{
		issuer:       cfg.OIDCIssuer,
		clientID:     cfg.OIDCClientID,
		clientSecret: cfg.OIDCClientSecret,
		redirectURL:  strings.TrimRight(cfg.BaseURL, "/") + api.AuthCallbackPath,
		sessions:     sessions,
	}
}

func (o *OIDC) Login(w http.ResponseWriter, r *http.Request) {
	provider, err := o.getProvider(r.Context())
	if err != nil {
		writeAuthError(w, http.StatusBadGateway, "Could not contact the identity provider. Try again.")
		return
	}
	state, err := randomToken(32)
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "Could not start sign in. Try again.")
		return
	}
	verifier := oauth2.GenerateVerifier()
	expires := o.sessions.now().Add(oidcStateLifetime)
	value, err := o.sessions.sign(oidcState{
		State: state, Verifier: verifier, Next: safeNext(r.URL.Query().Get("next")), Exp: expires.Unix(),
	})
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "Could not start sign in. Try again.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookieName, Value: value, Path: "/api/auth", Expires: expires,
		MaxAge: int(oidcStateLifetime / time.Second), HttpOnly: true,
		Secure: o.sessions.secure, SameSite: http.SameSiteLaxMode,
	})
	location := o.oauthConfig(provider).AuthCodeURL(
		state,
		oauth2.AccessTypeOnline,
		oauth2.S256ChallengeOption(verifier),
	)
	http.Redirect(w, r, location, http.StatusFound)
}

func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) {
	o.clearState(w)
	stateCookie, err := r.Cookie(oidcStateCookieName)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "Sign-in state is missing or expired. Start again.")
		return
	}
	var saved oidcState
	if err := o.sessions.verify(stateCookie.Value, &saved); err != nil || saved.Exp <= o.sessions.now().Unix() {
		writeAuthError(w, http.StatusBadRequest, "Sign-in state is missing or expired. Start again.")
		return
	}
	gotState := r.URL.Query().Get("state")
	if len(gotState) != len(saved.State) || subtle.ConstantTimeCompare([]byte(gotState), []byte(saved.State)) != 1 {
		writeAuthError(w, http.StatusBadRequest, "Sign-in state did not match. Start again.")
		return
	}
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		writeAuthError(w, http.StatusUnauthorized, "Sign in was not completed.")
		return
	}

	provider, err := o.getProvider(r.Context())
	if err != nil {
		writeAuthError(w, http.StatusBadGateway, "Could not contact the identity provider. Try again.")
		return
	}
	token, err := o.oauthConfig(provider).Exchange(
		r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(saved.Verifier),
	)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "The identity provider rejected the sign in.")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		writeAuthError(w, http.StatusUnauthorized, "The identity provider did not return an identity token.")
		return
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: o.clientID}).Verify(r.Context(), rawIDToken)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "The identity token could not be verified.")
		return
	}
	var claims struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil || strings.TrimSpace(claims.Sub) == "" {
		writeAuthError(w, http.StatusUnauthorized, "The identity token is missing required claims.")
		return
	}
	if strings.TrimSpace(claims.Name) == "" {
		claims.Name = claims.Email
	}
	if err := o.sessions.Set(w, Session{Sub: claims.Sub, Email: claims.Email, Name: claims.Name}); err != nil {
		writeAuthError(w, http.StatusInternalServerError, "Could not create a session. Try again.")
		return
	}
	http.Redirect(w, r, safeNext(saved.Next), http.StatusFound)
}

func (o *OIDC) Logout(w http.ResponseWriter, r *http.Request) {
	// Revoke server-side first: clearing the cookie only helps this browser,
	// while a copied cookie would otherwise stay valid until it expires.
	if session, ok := SessionFromContext(r.Context()); ok {
		if err := o.sessions.Revoke(r.Context(), session); err != nil {
			log.Printf("auth: could not revoke session: %v", err)
		}
	}
	o.sessions.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

func (o *OIDC) Me(w http.ResponseWriter, r *http.Request) {
	session, ok := SessionFromContext(r.Context())
	if !ok {
		writeAuthError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	writeAuthJSON(w, http.StatusOK, api.Me{Sub: session.Sub, Email: session.Email, Name: session.Name})
}

func (o *OIDC) getProvider(ctx context.Context) (*oidc.Provider, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider != nil {
		return o.provider, nil
	}
	provider, err := oidc.NewProvider(ctx, o.issuer)
	if err != nil {
		return nil, err
	}
	o.provider = provider
	return provider, nil
}

func (o *OIDC) oauthConfig(provider *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID: o.clientID, ClientSecret: o.clientSecret, RedirectURL: o.redirectURL,
		Endpoint: provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
	}
}

func (o *OIDC) clearState(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookieName, Value: "", Path: "/api/auth", Expires: time.Unix(1, 0),
		MaxAge: -1, HttpOnly: true, Secure: o.sessions.secure, SameSite: http.SameSiteLaxMode,
	})
}

func safeNext(raw string) string {
	if raw == "" || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") {
		return "/"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" ||
		!strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") ||
		strings.Contains(parsed.Path, "\\") {
		return "/"
	}
	return parsed.String()
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func writeAuthError(w http.ResponseWriter, status int, message string) {
	writeAuthJSON(w, status, api.ErrorResponse{Error: message})
}

func writeAuthJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
