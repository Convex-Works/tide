package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"tide/internal/config"
)

const (
	// The policy's directives other than script-src and connect-src.
	policyFonts  = "font-src 'self' data:; img-src 'self' data:; media-src 'self' blob:"
	policyStyles = "style-src 'self' 'unsafe-inline'; frame-ancestors 'none'"
)

func wantPolicy(scriptSrc, connectSrc string) string {
	return "default-src 'self'; script-src " + scriptSrc + "; " + policyFonts +
		"; connect-src " + connectSrc + "; " + policyStyles
}

// sha256Source is the CSP source for a script's text, computed here rather
// than by the code under test.
func sha256Source(script string) string {
	sum := sha256.Sum256([]byte(script))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// inlineScript matches an inline script as SvelteKit writes it: a bare
// <script> tag. It is deliberately simpler than the tokenizer tide hashes
// with, so the two check each other.
var inlineScript = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// shellScripts returns the inline scripts of an HTML document.
func shellScripts(document string) []string {
	var scripts []string
	for _, match := range inlineScript.FindAllStringSubmatch(document, -1) {
		scripts = append(scripts, match[1])
	}
	return scripts
}

// A SvelteKit shell in miniature: a bootstrap script among tags that must
// not be hashed.
const testShellBootstrap = `
				{
					__sveltekit_test = { base: "" };
					const element = document.currentScript.parentElement;
					import("/_app/immutable/entry/start.js").then((kit) => kit.start(element));
				}
			`

const testShell = `<!doctype html>
<html lang="en">
  <head>
    <meta property="og:image" content="/og.png" />
    <link href="/_app/immutable/entry/start.js" rel="modulepreload">
    <script type="module" src="/_app/immutable/entry/app.js"></script>
  </head>
  <body>
    <div style="display: contents">
			<script>` + testShellBootstrap + `</script>
		</div>
  </body>
</html>`

func newSecurityTestHandler(t *testing.T, cfg config.Config, shell string) http.Handler {
	t.Helper()
	cfg.SessionSecret = "test-session-secret"
	cfg.LiveKitURL = "ws://livekit.example"
	cfg.LiveKitAPIKey = "devkey"
	cfg.LiveKitAPISecret = "test-livekit-secret-with-enough-bytes"
	web := fstest.MapFS{"index.html": {Data: []byte(shell)}}
	handler, background, err := New(cfg, web, nil, transcribeBundle(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	// New starts moil, which runs until closed.
	t.Cleanup(func() { _ = background.Close() })
	return handler
}

func get(handler http.Handler, path string) *http.Response {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response.Result()
}

func TestSecurityHeadersArePresentOnAllResponses(t *testing.T) {
	handler := newSecurityTestHandler(t, config.Config{
		BaseURL:          "http://localhost:8080",
		LiveKitPublicURL: "ws://localhost:8080",
	}, testShell)
	page := wantPolicy("'self' "+sha256Source(testShellBootstrap),
		"'self' ws://localhost:8080 http://localhost:8080")
	recorder := wantPolicy("'self' "+sha256Source(testShellBootstrap), "'self' ws: wss:")

	for path, policy := range map[string]string{
		"/":                page,
		"/m/abc-defg-hij":  page,
		"/healthz":         page,
		"/api/not-a-route": page,
		"/egress-template": recorder,
		"/egress-template?url=wss%3A%2F%2Fmedia&token=t": recorder,
	} {
		t.Run(path, func(t *testing.T) {
			response := get(handler, path)
			want := map[string]string{
				"Content-Security-Policy": policy,
				"X-Content-Type-Options":  "nosniff",
				"Referrer-Policy":         "strict-origin-when-cross-origin",
				"X-Frame-Options":         "DENY",
			}
			for name, value := range want {
				if got := response.Header.Get(name); got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
			}
		})
	}
}

// The policy allows each inline script of the shell exactly as tide serves
// it, and none that isn't there.
func TestContentSecurityPolicyHashesServedInlineScripts(t *testing.T) {
	handler := newSecurityTestHandler(t, config.Config{BaseURL: "https://meet.example.com"}, testShell)
	assertServedScriptsAllowed(t, handler, 1)
}

func assertServedScriptsAllowed(t *testing.T, handler http.Handler, count int) {
	t.Helper()
	response := get(handler, "/")
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	scripts := shellScripts(string(body))
	if len(scripts) != count {
		t.Fatalf("served shell has %d inline scripts, want %d", len(scripts), count)
	}
	sources := []string{"'self'"}
	for _, script := range scripts {
		sources = append(sources, sha256Source(script))
	}
	policy := response.Header.Get("Content-Security-Policy")
	if want := "script-src " + strings.Join(sources, " ") + ";"; !strings.Contains(policy, want) {
		t.Fatalf("policy %q lacks %q", policy, want)
	}
}

func TestInlineScriptHashes(t *testing.T) {
	for name, test := range map[string]struct {
		document string
		want     []string
	}{
		"none":            {`<!doctype html><title>tide</title>`, nil},
		"external only":   {`<script src="/a.js"></script><script type="module" SRC="/b.js"></script>`, nil},
		"sveltekit shell": {testShell, []string{sha256Source(testShellBootstrap)}},
		"two, in order": {
			`<script>one()</script><p>x</p><SCRIPT type="module">two()</SCRIPT>`,
			[]string{sha256Source("one()"), sha256Source("two()")},
		},
		"empty":            {`<script></script>`, []string{sha256Source("")}},
		"raw text":         {`<script>if (a < b && c) "&amp;"</script>`, []string{sha256Source(`if (a < b && c) "&amp;"`)}},
		"line endings":     {"<script>a()\r\nb()</script>", []string{sha256Source("a()\nb()")}},
		"comment not code": {`<!-- <script>no()</script> --><script>yes()</script>`, []string{sha256Source("yes()")}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := inlineScriptHashes([]byte(test.document))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, " ") != strings.Join(test.want, " ") {
				t.Fatalf("hashes = %q, want %q", got, test.want)
			}
		})
	}
}

// loadConfig loads a dev-mode configuration from env alone, whatever the
// environment the test runs in.
func loadConfig(t *testing.T, env map[string]string) config.Config {
	t.Helper()
	for _, name := range []string{
		"TIDE_BASE_URL", "TIDE_MEDIA_URL", "TIDE_MEDIA_PUBLIC_URL", "TIDE_OIDC_ISSUER", "TIDE_S3_ENDPOINT",
		"TIDE_TRANSCRIPTS", "TIDE_USER_GROUPS", "TIDE_ADMIN_GROUPS", "TIDE_TRUSTED_PROXIES",
		"TIDE_MEDIA_API_PORT", "TIDE_MEDIA_TCP_PORT", "TIDE_MEDIA_UDP_PORT", "TIDE_MEDIA_NODE_IP",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("TIDE_DEV_MODE", "true")
	for name, value := range env {
		t.Setenv(name, value)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// connect-src names tide's origin and the media server's signaling origin,
// as a WebSocket and for the LiveKit client's http(s) requests, and nothing
// else.
func TestContentSecurityPolicyConnectSources(t *testing.T) {
	for name, test := range map[string]struct {
		env  map[string]string
		want string
	}{
		"embedded, https": {
			map[string]string{"TIDE_BASE_URL": "https://meet.example.com"},
			"'self' wss://meet.example.com https://meet.example.com",
		},
		"embedded, loopback http": {
			map[string]string{"TIDE_BASE_URL": "http://localhost:8080"},
			"'self' ws://localhost:8080 http://localhost:8080",
		},
		"embedded, public URL set": {
			map[string]string{"TIDE_BASE_URL": "https://meet.example.com", "TIDE_MEDIA_PUBLIC_URL": "wss://Media.Example.com:7443"},
			"'self' wss://media.example.com:7443 https://media.example.com:7443",
		},
		"external media": {
			map[string]string{
				"TIDE_BASE_URL": "https://meet.example.com", "TIDE_MEDIA_URL": "ws://media:7880",
				"TIDE_MEDIA_PUBLIC_URL": "wss://media.example.com/livekit",
			},
			"'self' wss://media.example.com https://media.example.com",
		},
		"external media, https public URL": {
			map[string]string{
				"TIDE_BASE_URL": "https://meet.example.com", "TIDE_MEDIA_URL": "ws://media:7880",
				"TIDE_MEDIA_PUBLIC_URL": "https://media.example.com:8443",
			},
			"'self' wss://media.example.com:8443 https://media.example.com:8443",
		},
		"external media, plain ws": {
			map[string]string{
				"TIDE_BASE_URL": "http://localhost:8080", "TIDE_MEDIA_URL": "ws://127.0.0.1:7880",
				"TIDE_MEDIA_PUBLIC_URL": "ws://127.0.0.1:7880",
			},
			"'self' ws://127.0.0.1:7880 http://127.0.0.1:7880",
		},
	} {
		t.Run(name, func(t *testing.T) {
			policies, err := newContentSecurityPolicies(loadConfig(t, test.env), nil)
			if err != nil {
				t.Fatal(err)
			}
			// Without a shell (Vite serves it in development), no inline
			// script is allowed.
			if want := wantPolicy("'self'", test.want); policies.page != want {
				t.Fatalf("page policy\n got %q\nwant %q", policies.page, want)
			}
			if want := wantPolicy("'self'", "'self' ws: wss:"); policies.recorder != want {
				t.Fatalf("recorder policy\n got %q\nwant %q", policies.recorder, want)
			}
		})
	}
}

func TestContentSecurityPolicyRejectsUnusableMediaURL(t *testing.T) {
	for _, publicURL := range []string{"media.example.com", "ftp://media.example.com", "wss://"} {
		if _, err := newContentSecurityPolicies(config.Config{LiveKitPublicURL: publicURL}, nil); err == nil {
			t.Errorf("public URL %q: no error", publicURL)
		}
	}
}

// The real SvelteKit shell (web/build, from `vite build`) has exactly one
// inline script, its bootstrap, and nothing else a hash can't allow: no
// inline event handlers and no javascript: URLs. Another inline script would
// be hashed too, but this makes adding one a decision. CI builds the SPA and
// sets TIDE_REQUIRE_WEB_BUILD so this test can't skip there.
func TestBuiltShellInlineScripts(t *testing.T) {
	path := filepath.Join("..", "..", "..", "web", "build", "index.html")
	shell, err := os.ReadFile(path)
	if os.IsNotExist(err) && os.Getenv("TIDE_REQUIRE_WEB_BUILD") == "" {
		t.Skip("no SPA build at web/build; run `npx vite build` in web/")
	}
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.ToLower(string(shell)), "<script") != 1 {
		t.Fatalf("%s has %d script tags, want the bootstrap alone", path, strings.Count(string(shell), "<script"))
	}
	if handler := regexp.MustCompile(`(?i)\son[a-z]+\s*=`).FindString(string(shell)); handler != "" {
		t.Fatalf("%s has an inline event handler (%q), which script-src blocks", path, handler)
	}
	if strings.Contains(strings.ToLower(string(shell)), "javascript:") {
		t.Fatalf("%s has a javascript: URL, which script-src blocks", path)
	}
	handler := newSecurityTestHandler(t, config.Config{BaseURL: "https://meet.example.com"}, string(shell))
	assertServedScriptsAllowed(t, handler, 1)
}
