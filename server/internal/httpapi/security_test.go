package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"tide/internal/config"
)

func TestSecurityHeadersArePresentOnAllResponses(t *testing.T) {
	handler, background, err := New(config.Config{
		BaseURL:          "http://localhost:8080",
		SessionSecret:    "test-session-secret",
		LiveKitURL:       "ws://livekit.example",
		LiveKitAPIKey:    "devkey",
		LiveKitAPISecret: "test-livekit-secret-with-enough-bytes",
	}, nil, nil, transcribeBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	// New starts moil, which runs until closed.
	t.Cleanup(func() { _ = background.Close() })
	want := map[string]string{
		"Content-Security-Policy": contentSecurityPolicy,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
		"X-Frame-Options":         "DENY",
	}

	for _, path := range []string{"/healthz", "/api/not-a-route"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			for name, value := range want {
				if got := response.Header().Get(name); got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
			}
		})
	}
}
