package recording

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"tide/internal/config"
)

// MinIOStore checks and copies transcripts through tide's own endpoint
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
		case r.Method == http.MethodHead && r.URL.Path == "/tide/transcripts-staging/rec/1-ab/transcript.vtt":
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
		S3Endpoint: s3.URL, S3PublicEndpoint: "https://s3.public.example", S3Bucket: "tide",
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
	if path, _ := url.PathUnescape(copied.URL.EscapedPath()); path != "/tide/"+dst {
		t.Fatalf("copied to %q", path)
	}
	for header, want := range map[string]string{
		"X-Amz-Copy-Source":          "/tide/transcripts-staging/rec/1-ab/transcript.vtt",
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
	if err != nil || !strings.HasPrefix(location, "https://s3.public.example/tide/transcripts-staging/") {
		t.Fatalf("PresignedPut() = %q, %v", location, err)
	}
}

// MinIOStore makes its clients once: calls after the first reuse the
// connection it opened, rather than dial storage again each time.
func TestMinIOStoreKeepsItsConnections(t *testing.T) {
	var mu sync.Mutex
	dials := 0
	s3 := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Length", "42")
			w.Header().Set("ETag", `"0123abcd"`)
			w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	s3.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			mu.Lock()
			dials++
			mu.Unlock()
		}
	}
	s3.Start()
	defer s3.Close()
	store := NewMinIOStore(config.Config{
		S3Endpoint: s3.URL, S3PublicEndpoint: "https://s3.public.example", S3Bucket: "tide",
		S3AccessKey: "key", S3SecretKey: "secret", S3Region: "us-east-1",
	})
	ctx := context.Background()
	for range 5 {
		if _, _, err := store.Stat(ctx, "recordings/standup/rec/x.ogg"); err != nil {
			t.Fatal(err)
		}
		if err := store.Remove(ctx, "transcripts-staging/rec/1-ab/transcript.txt"); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if dials != 1 {
		t.Fatalf("ten calls dialled storage %d times, want once", dials)
	}
}

// An endpoint minio can't use fails every call that needs it, naming the
// setting and what it holds; the other endpoint's calls still work.
func TestMinIOStoreNamesAnEndpointItCantUse(t *testing.T) {
	store := NewMinIOStore(config.Config{
		S3Endpoint: "http://minio host:9000", S3PublicEndpoint: "https://s3.public.example", S3Bucket: "tide",
		S3AccessKey: "key", S3SecretKey: "secret", S3Region: "us-east-1",
	})
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"Remove": func() error { return store.Remove(ctx, "k") },
		"Stat":   func() error { _, _, err := store.Stat(ctx, "k"); return err },
		"Copy":   func() error { return store.Copy(ctx, "a", "", "b", "text/plain") },
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), `TIDE_S3_ENDPOINT="http://minio host:9000"`) {
			t.Errorf("%s() = %v, want an error naming TIDE_S3_ENDPOINT and its value", name, err)
		}
	}
	if location, err := store.PresignedGet(ctx, "k", time.Minute); err != nil || !strings.HasPrefix(location, "https://s3.public.example/tide/k") {
		t.Fatalf("PresignedGet() = %q, %v", location, err)
	}

	store = NewMinIOStore(config.Config{S3Endpoint: "http://minio:9000", S3PublicEndpoint: "", S3Bucket: "tide", S3Region: "us-east-1"})
	if _, err := store.PresignedPut(ctx, "k", time.Minute); err == nil || !strings.Contains(err.Error(), `TIDE_S3_PUBLIC_ENDPOINT=""`) {
		t.Fatalf("PresignedPut() with no public endpoint = %v, want an error naming TIDE_S3_PUBLIC_ENDPOINT", err)
	}
}
