package transcripts_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 is object storage machines and browsers reach over real HTTP, as
// they reach S3: it keeps objects in memory and serves them only through
// URLs it presigned, which name one method and one object and expire by
// the storage's own clock.
type fakeS3 struct {
	server *httptest.Server
	secret []byte

	mu      sync.Mutex
	now     time.Time
	objects map[string][]byte
}

// fakeS3 is the ObjectStore of both the transcripts service and the
// recording handler.
func newFakeS3(t *testing.T) *fakeS3 {
	s := &fakeS3{
		secret:  []byte("fake S3 signing key"),
		now:     time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
		objects: make(map[string][]byte),
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.server.Close)
	return s
}

// Advance moves the storage's clock, which its URLs expire by.
func (s *fakeS3) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = s.now.Add(d)
}

// Put stores an object, as egress does with a recording.
func (s *fakeS3) Put(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = slices.Clone(data)
}

// Object returns a stored object.
func (s *fakeS3) Object(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	return data, ok
}

// Keys returns the keys of the stored objects under prefix, sorted.
func (s *fakeS3) Keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

func (s *fakeS3) Remove(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func (s *fakeS3) PresignedGet(_ context.Context, key string, expiry time.Duration) (string, error) {
	return s.presign(http.MethodGet, key, expiry, nil), nil
}

func (s *fakeS3) PresignedPut(_ context.Context, key string, expiry time.Duration) (string, error) {
	return s.presign(http.MethodPut, key, expiry, nil), nil
}

// PresignedDownload signs S3's response overrides into the URL, as
// MinIOStore does.
func (s *fakeS3) PresignedDownload(_ context.Context, key string, expiry time.Duration, filename, contentType string) (string, error) {
	return s.presign(http.MethodGet, key, expiry, url.Values{
		"response-content-disposition": {mime.FormatMediaType("attachment", map[string]string{"filename": filename})},
		"response-content-type":        {contentType},
	}), nil
}

func (s *fakeS3) presign(method, key string, expiry time.Duration, params url.Values) string {
	query := url.Values{}
	for name, values := range params {
		query[name] = values
	}
	s.mu.Lock()
	expires := s.now.Add(expiry)
	s.mu.Unlock()
	query.Set("X-Method", method)
	query.Set("X-Expires", expires.Format(time.RFC3339Nano))
	query.Set("X-Signature", s.sign(key, query))
	location := url.URL{Path: "/bucket/" + key, RawQuery: query.Encode()}
	return s.server.URL + location.String()
}

// sign is the signature of an object key and every query parameter but the
// signature itself.
func (s *fakeS3) sign(key string, query url.Values) string {
	signed := url.Values{}
	for name, values := range query {
		if name != "X-Signature" {
			signed[name] = values
		}
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(key + "\n" + signed.Encode()))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	key, ok := strings.CutPrefix(r.URL.Path, "/bucket/")
	query := r.URL.Query()
	if !ok || query.Get("X-Method") != r.Method ||
		!hmac.Equal([]byte(query.Get("X-Signature")), []byte(s.sign(key, query))) {
		http.Error(w, "SignatureDoesNotMatch", http.StatusForbidden)
		return
	}
	expires, err := time.Parse(time.RFC3339Nano, query.Get("X-Expires"))
	s.mu.Lock()
	now := s.now
	s.mu.Unlock()
	if err != nil || !now.Before(expires) {
		http.Error(w, "AccessDenied: Request has expired", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		data, ok := s.Object(key)
		if !ok {
			http.Error(w, "NoSuchKey", http.StatusNotFound)
			return
		}
		if contentType := query.Get("response-content-type"); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if disposition := query.Get("response-content-disposition"); disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
		_, _ = w.Write(data)
	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.Put(key, data)
	}
}
