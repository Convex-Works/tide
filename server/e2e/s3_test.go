package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// An objectStore is S3-compatible storage, as tide's configuration names
// it, with a client for the tests to look into it.
type objectStore struct {
	endpoint, accessKey, secretKey, bucket, region string
	client                                         *minio.Client
}

func newObjectStore(endpoint, accessKey, secretKey, bucket, region string) (*objectStore, error) {
	host, secure := strings.CutPrefix(endpoint, "https://")
	if !secure {
		host = strings.TrimPrefix(endpoint, "http://")
	}
	client, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure, Region: region, BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, err
	}
	return &objectStore{
		endpoint: endpoint, accessKey: accessKey, secretKey: secretKey,
		bucket: bucket, region: region, client: client,
	}, nil
}

// s3FromEnv returns the store $TIDE_E2E_S3_ENDPOINT names, if it's set.
// Machines take plain http only from loopback, so it must be https or on
// 127.0.0.1.
func s3FromEnv() (*objectStore, error) {
	endpoint := os.Getenv("TIDE_E2E_S3_ENDPOINT")
	if endpoint == "" {
		return nil, nil
	}
	accessKey, secretKey := os.Getenv("TIDE_E2E_S3_ACCESS_KEY"), os.Getenv("TIDE_E2E_S3_SECRET_KEY")
	if accessKey == "" || secretKey == "" {
		return nil, errors.New("TIDE_E2E_S3_ENDPOINT needs TIDE_E2E_S3_ACCESS_KEY and TIDE_E2E_S3_SECRET_KEY")
	}
	return newObjectStore(endpoint, accessKey, secretKey,
		envOr("TIDE_E2E_S3_BUCKET", "tide-e2e"), envOr("TIDE_E2E_S3_REGION", "us-east-1"))
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// startMinIO runs the MinIO that deploy/compose.yaml pins, in a container
// called name, published on a random loopback port.
func startMinIO(name string) (*objectStore, error) {
	image, err := minioImage()
	if err != nil {
		return nil, err
	}
	user, password := "tide-e2e", rand.Text()
	run := exec.Command("docker", "run", "--detach", "--rm", "--name", name,
		"--publish", "127.0.0.1::9000",
		"--env", "MINIO_ROOT_USER="+user, "--env", "MINIO_ROOT_PASSWORD="+password,
		image, "server", "/data")
	if out, err := run.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("starting MinIO (%s): %w\n%s", image, err, out)
	}
	store, err := awaitMinIO(name, user, password)
	if err != nil {
		removeContainer(name)
		return nil, err
	}
	return store, nil
}

// awaitMinIO waits until MinIO in the container is ready, and returns it.
func awaitMinIO(container, user, password string) (*objectStore, error) {
	out, err := exec.Command("docker", "port", container, "9000/tcp").Output()
	if err != nil {
		return nil, fmt.Errorf("finding MinIO's port: %w", err)
	}
	address, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	store, err := newObjectStore("http://"+address, user, password, "tide-recordings", "us-east-1")
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(patience)
	for {
		response, err := http.Get(store.endpoint + "/minio/health/ready")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return store, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("MinIO at %s wasn't ready after %v: %v", store.endpoint, patience, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// minioImage is the MinIO image deploy/compose.yaml pins.
func minioImage() (string, error) {
	compose, err := os.ReadFile("../../deploy/compose.yaml")
	if err != nil {
		return "", err
	}
	match := regexp.MustCompile(`(?m)^\s*image:\s*(minio/minio:\S+)\s*$`).FindSubmatch(compose)
	if match == nil {
		return "", errors.New("deploy/compose.yaml pins no minio/minio image")
	}
	return string(match[1]), nil
}

func removeContainer(name string) {
	if out, err := exec.Command("docker", "rm", "--force", name).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: removing container %s: %v\n%s", name, err, out)
	}
}

func (s *objectStore) createBucket() error {
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil || exists {
		return err
	}
	return s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region})
}

// put stores an object, as egress does with a recording.
func (s *objectStore) put(t *testing.T, key string, data []byte, contentType string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	if _, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType}); err != nil {
		t.Fatalf("storing %s: %v", key, err)
	}
}

// keys returns the keys of the objects under prefix, sorted.
func (s *objectStore) keys(t *testing.T, prefix string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	var keys []string
	for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			t.Fatalf("listing %s: %v", prefix, object.Err)
		}
		keys = append(keys, object.Key)
	}
	slices.Sort(keys)
	return keys
}
