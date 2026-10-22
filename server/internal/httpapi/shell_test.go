package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"tide/internal/config"
)

// Link previews need an absolute og:image, and the SPA's build doesn't know
// its origin: tide rewrites the shell's tag to TIDE_BASE_URL when it starts.
// The shell here is the real web/src/app.html, so this fails if the tag
// tide looks for changes there.
func TestTheShellsLinkPreviewImageIsAbsolute(t *testing.T) {
	shell, err := os.ReadFile("../../../web/src/app.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shell), ogImageTag) {
		t.Fatalf("web/src/app.html no longer contains %s: update ogImageTag with it", ogImageTag)
	}
	png := []byte("\x89PNG\r\n\x1a\n")
	web := fstest.MapFS{
		"index.html": {Data: shell},
		"og.png":     {Data: png},
	}
	db, _ := openStore(t)
	handler, background, err := New(config.Config{
		BaseURL: "https://meet.example.com/", SessionSecret: "test-session-secret",
		LiveKitURL: "ws://livekit.example", LiveKitAPIKey: "devkey",
		LiveKitAPISecret: "test-livekit-secret-with-enough-bytes",
	}, web, db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = background.Close() })

	want := `<meta property="og:image" content="https://meet.example.com/og.png" />`
	for _, path := range []string{"/", "/index.html", "/m/abc-defg-hij"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, want) || strings.Contains(body, ogImageTag) {
			t.Errorf("GET %s = %d, want the shell with %s:\n%s", path, response.Code, want, body)
		}
	}

	// The image itself is a PNG, not the SPA's fallback page.
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/og.png", nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || response.Body.String() != string(png) {
		t.Errorf("GET /og.png = %d %s, want the PNG", response.Code, response.Header().Get("Content-Type"))
	}
}
