package transcripts_test

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
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

// A clock is the time klisi and its storage share in these tests, which a
// test moves on.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock {
	return &clock{now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves time on, for klisi and storage alike.
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeS3 is object storage that machines and browsers reach over real HTTP,
// as they reach S3, and klisi calls as it calls MinIOStore. It keeps objects
// in memory and serves them only through URLs it presigned, which name one
// method and one object and expire by the clock. A test can break or hold
// what klisi asks of it.
type fakeS3 struct {
	server *httptest.Server
	secret []byte
	clock  *clock

	mu      sync.Mutex
	objects map[string]object
	broken  map[string]bool  // operations that fail, by name
	held    map[string]*hold // operations that wait, by name
	calls   map[string]int   // how often klisi asked for each operation
}

type object struct {
	data        []byte
	contentType string
}

// etag is the object's entity tag, as S3 makes it for a single-part upload.
func (o object) etag() string {
	sum := md5.Sum(o.data)
	return hex.EncodeToString(sum[:])
}

// The operations klisi asks of storage, for Break and Hold.
const (
	opRemove = "remove"
	opStat   = "stat"
	opCopy   = "copy"
)

// fakeS3 is the ObjectStore of the transcripts service, the recording
// handler and the rooms handler.
func newFakeS3(t *testing.T, clock *clock) *fakeS3 {
	s := &fakeS3{
		secret:  []byte("fake S3 signing key"),
		clock:   clock,
		objects: make(map[string]object),
		broken:  make(map[string]bool),
		held:    make(map[string]*hold),
		calls:   make(map[string]int),
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.server.Close)
	return s
}

// Break makes storage refuse op until Mend.
func (s *fakeS3) Break(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broken[op] = true
}

func (s *fakeS3) Mend(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.broken, op)
}

// Calls reports how often klisi asked storage for op.
func (s *fakeS3) Calls(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[op]
}

// A hold keeps klisi's calls of an operation waiting until it's released,
// as a slow storage would, or until their context ends.
type hold struct {
	entered  chan struct{}
	released chan struct{}
	once     sync.Once
}

// Hold makes klisi's calls of op wait, from now until the hold is released.
func (s *fakeS3) Hold(op string) *hold {
	h := &hold{entered: make(chan struct{}, 100), released: make(chan struct{})}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held[op] = h
	return h
}

// Entered waits until a call is waiting on the hold.
func (h *hold) Entered(t *testing.T) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(waitTimeout):
		t.Fatalf("waited %v for klisi to call storage", waitTimeout)
	}
}

func (h *hold) Release() { h.once.Do(func() { close(h.released) }) }

// enter is how every call klisi makes starts: it fails once its context is
// done, as a real client does, waits on a hold, and fails while op is
// broken.
func (s *fakeS3) enter(ctx context.Context, op string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.calls[op]++
	h := s.held[op]
	s.mu.Unlock()
	if h != nil {
		h.entered <- struct{}{}
		select {
		case <-h.released:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken[op] {
		return fmt.Errorf("fake S3: %s refused: InternalError", op)
	}
	return nil
}

// Put stores an object, as egress does with a recording.
func (s *fakeS3) Put(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = object{data: slices.Clone(data)}
}

// Object returns a stored object's bytes.
func (s *fakeS3) Object(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	return o.data, ok
}

// ContentType returns what a stored object is stored as.
func (s *fakeS3) ContentType(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[key].contentType
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

func (s *fakeS3) Remove(ctx context.Context, key string) error {
	if err := s.enter(ctx, opRemove); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func (s *fakeS3) Stat(ctx context.Context, key string) (int64, string, error) {
	if err := s.enter(ctx, opStat); err != nil {
		return 0, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if !ok {
		return 0, "", fmt.Errorf("fake S3: %s: NoSuchKey: %w", key, fs.ErrNotExist)
	}
	return int64(len(o.data)), o.etag(), nil
}

// Copy copies src to dst server-side, stored as contentType, as long as src
// still has the entity tag etag: S3's x-amz-copy-source-if-match.
func (s *fakeS3) Copy(ctx context.Context, src, etag, dst, contentType string) error {
	if err := s.enter(ctx, opCopy); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[src]
	switch {
	case !ok:
		return fmt.Errorf("fake S3: %s: NoSuchKey", src)
	case o.etag() != etag:
		return fmt.Errorf("fake S3: %s: PreconditionFailed", src)
	}
	s.objects[dst] = object{data: slices.Clone(o.data), contentType: contentType}
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
	query.Set("X-Method", method)
	query.Set("X-Expires", s.clock.Now().Add(expiry).Format(time.RFC3339Nano))
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

// Key is the object a URL this storage presigned names.
func (s *fakeS3) Key(t *testing.T, location string) string {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	key, ok := strings.CutPrefix(parsed.Path, "/bucket/")
	if !ok || !strings.HasPrefix(location, s.server.URL) {
		t.Fatalf("%s isn't a URL of this storage", location)
	}
	return key
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
	if err != nil || !s.clock.Now().Before(expires) {
		http.Error(w, "AccessDenied: Request has expired", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		o, ok := s.objects[key]
		s.mu.Unlock()
		if !ok {
			http.Error(w, "NoSuchKey", http.StatusNotFound)
			return
		}
		if o.contentType != "" {
			w.Header().Set("Content-Type", o.contentType)
		}
		if contentType := query.Get("response-content-type"); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if disposition := query.Get("response-content-disposition"); disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
		_, _ = w.Write(o.data)
	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.objects[key] = object{data: data, contentType: r.Header.Get("Content-Type")}
		s.mu.Unlock()
	}
}
