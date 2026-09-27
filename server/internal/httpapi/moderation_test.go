package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"klisi/internal/config"
	"klisi/internal/store"
)

func TestModerationRouteRequiresCSRFAndAuthentication(t *testing.T) {
	db, err := store.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler, _, err := New(config.Config{
		BaseURL:          "http://localhost:8080",
		SessionSecret:    "test-session-secret",
		LiveKitURL:       "ws://livekit.example",
		LiveKitAPIKey:    "devkey",
		LiveKitAPISecret: "test-livekit-secret-with-enough-bytes",
	}, nil, db, transcribeBundle(t))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		csrfHeader string
		wantStatus int
	}{
		{name: "missing CSRF header", wantStatus: http.StatusForbidden},
		{name: "missing session", csrfHeader: "1", wantStatus: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/rooms/calm-otter-412/participants/guest%3A1234/kick",
				nil,
			)
			if test.csrfHeader != "" {
				request.Header.Set("X-Klisi-Csrf", test.csrfHeader)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
