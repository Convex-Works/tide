package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"tide/internal/config"
)

// recorderTemplatePath is the SPA route the recorder's browser opens to draw
// a meeting (TIDE_RECORDER_TEMPLATE_URL). The recorder hands it the media
// server address from its own configuration, which tide never sees, so that
// page alone may open a WebSocket to any host.
const recorderTemplatePath = "/egress-template"

// policy is the Content-Security-Policy with the given script-src and
// connect-src sources. The bundled UI fonts are data URLs, and Svelte sets
// inline styles. base-uri and form-action don't fall back to default-src, so
// they are named: the SPA has no <base>, and its forms submit through script.
func policy(scriptSources, connectSources []string) string {
	return "default-src 'self'; script-src " + strings.Join(scriptSources, " ") +
		"; font-src 'self' data:; img-src 'self' data:; media-src 'self' blob:; connect-src " +
		strings.Join(connectSources, " ") + "; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'" +
		"; base-uri 'none'; form-action 'self'"
}

// strictTransportSecurity is the Strict-Transport-Security tide sends when
// its base URL is https: a year, for this host only, so a subdomain served
// over plain http keeps working.
const strictTransportSecurity = "max-age=31536000"

// contentSecurityPolicies is the Content-Security-Policy tide sends: one for
// every page but the recorder's, and the recorder's.
type contentSecurityPolicies struct {
	page     string
	recorder string
}

// newContentSecurityPolicies builds the policies for cfg and the SPA shell in
// web, which is nil when Vite serves the SPA (in development) and these
// policies govern only API responses.
//
// script-src allows tide's own files and, by hash, the inline scripts of the
// shell: SvelteKit's bootstrap, which differs on every build, so it is
// hashed here, once, at startup. connect-src allows tide's own origin and the
// media server's signaling origin (LiveKitPublicURL): its WebSocket, and the
// http(s) requests the LiveKit client makes beside it. The origin is named
// even when it is tide's own, because older Safari doesn't match ws: and wss:
// against 'self'. WebRTC media is not governed by connect-src.
func newContentSecurityPolicies(cfg config.Config, web fs.FS) (contentSecurityPolicies, error) {
	scriptSources := []string{"'self'"}
	if web != nil {
		shell, err := fs.ReadFile(web, "index.html")
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return contentSecurityPolicies{}, fmt.Errorf("read the SPA shell: %w", err)
		}
		hashes, err := inlineScriptHashes(shell)
		if err != nil {
			return contentSecurityPolicies{}, fmt.Errorf("hash the SPA shell's inline scripts: %w", err)
		}
		scriptSources = append(scriptSources, hashes...)
	}
	signaling, err := signalingSources(cfg.LiveKitPublicURL)
	if err != nil {
		return contentSecurityPolicies{}, err
	}
	return contentSecurityPolicies{
		page:     policy(scriptSources, append([]string{"'self'"}, signaling...)),
		recorder: policy(scriptSources, []string{"'self'", "ws:", "wss:"}),
	}, nil
}

// signalingSources returns the CSP sources for the media server's public
// signaling URL: its origin as a WebSocket and as http(s), with the scheme
// the URL names (ws and http, or wss and https).
func signalingSources(publicURL string) ([]string, error) {
	if publicURL == "" {
		return nil, nil
	}
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("media public URL %q is not an absolute URL", publicURL)
	}
	host := strings.ToLower(parsed.Host)
	switch strings.ToLower(parsed.Scheme) {
	case "ws", "http":
		return []string{"ws://" + host, "http://" + host}, nil
	case "wss", "https":
		return []string{"wss://" + host, "https://" + host}, nil
	}
	return nil, fmt.Errorf("media public URL %q is not a ws, wss, http or https URL", publicURL)
}

// inlineScriptHashes returns a CSP 'sha256-…' source for each inline script
// in an HTML document, in document order: the text a browser hashes, which is
// the script element's contents as parsed.
func inlineScriptHashes(document []byte) ([]string, error) {
	var hashes []string
	tokens := html.NewTokenizer(bytes.NewReader(document))
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			if errors.Is(tokens.Err(), io.EOF) {
				return hashes, nil
			}
			return nil, tokens.Err()
		case html.StartTagToken:
			token := tokens.Token()
			if token.DataAtom != atom.Script || hasAttribute(token, "src") {
				continue
			}
			var text []byte
			if tokens.Next() == html.TextToken {
				text = tokens.Text()
			}
			sum := sha256.Sum256(text)
			hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
		}
	}
}

func hasAttribute(token html.Token, name string) bool {
	for _, attribute := range token.Attr {
		if attribute.Key == name {
			return true
		}
	}
	return false
}

// securityHeaders sets the security headers on every response, with
// Strict-Transport-Security when https is true: when the base URL is https,
// and so a browser only ever reaches tide through TLS.
func securityHeaders(policies contentSecurityPolicies, https bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policy := policies.page
		if path.Clean(r.URL.Path) == recorderTemplatePath {
			policy = policies.recorder
		}
		w.Header().Set("Content-Security-Policy", policy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		if https {
			w.Header().Set("Strict-Transport-Security", strictTransportSecurity)
		}
		next.ServeHTTP(w, r)
	})
}
