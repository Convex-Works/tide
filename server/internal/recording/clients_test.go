package recording

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"klisi/internal/config"
)

// MinIOStore checks and copies transcripts through klisi's own endpoint
// for storage, never the public one machines and browsers use, and copies
// only the object it checked, stored as the transcript's type.
func TestMinIOStoreStatsAndCopiesThroughTheServerEndpoint(t *testing.T) {
	var mu sync.Mutex
	var requests []*http.Request
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r)
		mu.Unlock()
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/klisi/transcripts-staging/rec/1-ab/transcript.vtt":
			w.Header().Set("Content-Length", "42")
			w.Header().Set("ETag", `"0123abcd"`)
			w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source-If-Match") != "0123abcd":
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>PreconditionFailed</Code><Message>At least one of the pre-conditions you specified did not hold</Message></Error>`))
		case r.Method == http.MethodPut:
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><CopyObjectResult><LastModified>2026-09-27T12:00:00.000Z</LastModified><ETag>"0123abcd"</ETag></CopyObjectResult>`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer s3.Close()
	store := NewMinIOStore(config.Config{
		S3Endpoint: s3.URL, S3PublicEndpoint: "https://s3.public.example", S3Bucket: "klisi",
		S3AccessKey: "key", S3SecretKey: "secret", S3Region: "us-east-1",
	})
	ctx := context.Background()

	size, etag, err := store.Stat(ctx, "transcripts-staging/rec/1-ab/transcript.vtt")
	if err != nil || size != 42 || etag != "0123abcd" {
		t.Fatalf("Stat() = %d, %q, %v", size, etag, err)
	}
	if _, _, err := store.Stat(ctx, "transcripts-staging/rec/1-ab/transcript.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat() of a missing object: %v, want fs.ErrNotExist", err)
	}
	const dst = "recordings/standup/rec/2026-09-27 14-00 - Standup.vtt"
	if err := store.Copy(ctx, "transcripts-staging/rec/1-ab/transcript.vtt", "0123abcd", dst, "text/vtt; charset=utf-8"); err != nil {
		t.Fatalf("Copy(): %v", err)
	}
	if err := store.Copy(ctx, "transcripts-staging/rec/1-ab/transcript.vtt", "replaced", dst, "text/vtt; charset=utf-8"); err == nil {
		t.Fatal("Copy() of an object replaced since it was checked succeeded")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 4 {
		t.Fatalf("storage got %d requests", len(requests))
	}
	copied := requests[2]
	if path, _ := url.PathUnescape(copied.URL.EscapedPath()); path != "/klisi/"+dst {
		t.Fatalf("copied to %q", path)
	}
	for header, want := range map[string]string{
		"X-Amz-Copy-Source":          "/klisi/transcripts-staging/rec/1-ab/transcript.vtt",
		"X-Amz-Copy-Source-If-Match": "0123abcd",
		"X-Amz-Metadata-Directive":   "REPLACE",
		"Content-Type":               "text/vtt; charset=utf-8",
	} {
		got := copied.Header.Get(header)
		if header == "X-Amz-Copy-Source" {
			got, _ = url.PathUnescape(got)
			got = "/" + strings.TrimPrefix(got, "/")
		}
		if got != want {
			t.Errorf("copy request %s = %q, want %q", header, got, want)
		}
	}

	// Machines and browsers get URLs for the public endpoint instead.
	location, err := store.PresignedPut(ctx, "transcripts-staging/rec/1-ab/transcript.vtt", time.Hour)
	if err != nil || !strings.HasPrefix(location, "https://s3.public.example/klisi/transcripts-staging/") {
		t.Fatalf("PresignedPut() = %q, %v", location, err)
	}
}
