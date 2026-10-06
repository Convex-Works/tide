package recording

import (
	"context"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	protocolauth "github.com/livekit/protocol/auth"
	protocol "github.com/livekit/protocol/livekit"
	protocolwebhook "github.com/livekit/protocol/webhook"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"tide/internal/config"
	"tide/internal/store"
)

// New makes the recording handler for tide's LiveKit, with recordings kept
// in objects. objects is nil when recording is off (ARCHITECTURE.md §8):
// the handler then only receives webhooks, and its routes and reconciler,
// which need storage, must not run.
func New(cfg config.Config, recordings *store.Store, objects *MinIOStore) *Handler {
	egress := lksdk.NewEgressClient(cfg.LiveKitURL, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	rooms := lksdk.NewRoomServiceClient(liveKitHTTPURL(cfg.LiveKitURL), cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	provider := protocolauth.NewSimpleKeyProvider(cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	var storage ObjectStore // nil, not a nil *MinIOStore, without storage
	if objects != nil {
		storage = objects
	}
	handler := NewHandler(
		recordings, egress, rooms, storage, cfg.EgressTemplateURL,
		ReceiverFromAuthProvider(provider),
	)
	// The destination travels with each egress request; egress needs no global
	// storage config of its own. Endpoint is S3 as seen from the egress worker.
	handler.s3Output = &protocol.S3Upload{
		AccessKey:      cfg.S3AccessKey,
		Secret:         cfg.S3SecretKey,
		Region:         cfg.S3Region,
		Endpoint:       cfg.S3EgressEndpoint,
		Bucket:         cfg.S3Bucket,
		ForcePathStyle: true,
	}
	return handler
}

func liveKitHTTPURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.Scheme == "ws" {
		parsed.Scheme = "http"
	} else if parsed.Scheme == "wss" {
		parsed.Scheme = "https"
	}
	return parsed.String()
}

type WebhookReceiver interface {
	Receive(*http.Request) (*protocol.WebhookEvent, error)
}

type authWebhookReceiver struct {
	provider protocolauth.KeyProvider
}

// ReceiverFromAuthProvider is the narrow receiver seam used by tests. The
// pinned LiveKit SDK exposes webhook verification through protocol/webhook;
// this adapter gives the handler the receiver-style API without weakening
// signature verification.
func ReceiverFromAuthProvider(provider protocolauth.KeyProvider) WebhookReceiver {
	return authWebhookReceiver{provider: provider}
}

func (r authWebhookReceiver) Receive(request *http.Request) (*protocol.WebhookEvent, error) {
	return protocolwebhook.ReceiveWebhookEvent(request, r.provider)
}

// MinIOStore is tide's object storage, reached through two clients made
// once: one at TIDE_S3_ENDPOINT, where tide itself reaches storage to
// remove, check and copy objects, and one at TIDE_S3_PUBLIC_ENDPOINT, the
// address machines and browsers reach, for the URLs tide presigns. Each
// client keeps its connections open for the next call.
type MinIOStore struct {
	bucket string
	server storageClient
	public storageClient
}

// A storageClient is a client for one of storage's endpoints, or why there
// is none: the setting names an endpoint minio can't use.
type storageClient struct {
	client *minio.Client
	err    error
}

func NewMinIOStore(cfg config.Config) *MinIOStore {
	return &MinIOStore{
		bucket: cfg.S3Bucket,
		server: newStorageClient("TIDE_S3_ENDPOINT", cfg.S3Endpoint, cfg),
		public: newStorageClient("TIDE_S3_PUBLIC_ENDPOINT", cfg.S3PublicEndpoint, cfg),
	}
}

func newStorageClient(setting, rawEndpoint string, cfg config.Config) storageClient {
	parsed, err := url.Parse(rawEndpoint)
	if err != nil {
		return storageClient{err: fmt.Errorf("storage endpoint %s=%q is invalid: %w", setting, rawEndpoint, err)}
	}
	secure := parsed.Scheme == "https"
	endpoint := parsed.Host
	if endpoint == "" {
		endpoint = strings.TrimPrefix(strings.TrimPrefix(rawEndpoint, "http://"), "https://")
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Secure: secure, Region: cfg.S3Region, BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return storageClient{err: fmt.Errorf("storage endpoint %s=%q is invalid: %w", setting, rawEndpoint, err)}
	}
	return storageClient{client: client}
}

func (s *MinIOStore) Remove(ctx context.Context, key string) error {
	if s.server.err != nil {
		return s.server.err
	}
	err := s.server.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err == nil {
		return nil
	}
	response := minio.ToErrorResponse(err)
	if response.Code == "NoSuchKey" || response.Code == "NoSuchObject" || response.Code == "NotFound" {
		return nil
	}
	return err
}

func (s *MinIOStore) PresignedGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if s.public.err != nil {
		return "", s.public.err
	}
	location, err := s.public.client.PresignedGetObject(ctx, s.bucket, key, expiry, nil)
	if err != nil {
		return "", err
	}
	return location.String(), nil
}

// PresignedDownload is PresignedGet for a browser to save the object as a
// file called filename, served as contentType whatever it was stored with:
// a transcript sidecar a machine uploaded (ARCHITECTURE.md §8.1).
func (s *MinIOStore) PresignedDownload(ctx context.Context, key string, expiry time.Duration, filename, contentType string) (string, error) {
	if s.public.err != nil {
		return "", s.public.err
	}
	params := url.Values{}
	params.Set("response-content-disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	params.Set("response-content-type", contentType)
	location, err := s.public.client.PresignedGetObject(ctx, s.bucket, key, expiry, params)
	if err != nil {
		return "", err
	}
	return location.String(), nil
}

// PresignedPut lets whoever holds the URL upload the object at key, until
// expiry: a machine uploading a transcript sidecar (ARCHITECTURE.md §8.1).
func (s *MinIOStore) PresignedPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if s.public.err != nil {
		return "", s.public.err
	}
	location, err := s.public.client.PresignedPutObject(ctx, s.bucket, key, expiry)
	if err != nil {
		return "", err
	}
	return location.String(), nil
}

// Stat returns the size and entity tag of the object at key, or an error
// wrapping fs.ErrNotExist if there is none: tide checks a transcript a
// machine uploaded before copying it (ARCHITECTURE.md §8.1).
func (s *MinIOStore) Stat(ctx context.Context, key string) (int64, string, error) {
	if s.server.err != nil {
		return 0, "", s.server.err
	}
	info, err := s.server.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if code := minio.ToErrorResponse(err).Code; code == "NoSuchKey" || code == "NotFound" {
			return 0, "", fmt.Errorf("%s: %w", key, fs.ErrNotExist)
		}
		return 0, "", err
	}
	return info.Size, info.ETag, nil
}

// Copy copies the object at src to dst, within the bucket and without the
// bytes leaving storage, stored as contentType. It copies only while src's
// entity tag is still etag, so an object replaced since it was checked is
// never copied.
func (s *MinIOStore) Copy(ctx context.Context, src, etag, dst, contentType string) error {
	if s.server.err != nil {
		return s.server.err
	}
	_, err := s.server.client.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: s.bucket, Object: dst, ReplaceMetadata: true, ContentType: contentType},
		minio.CopySrcOptions{Bucket: s.bucket, Object: src, MatchETag: etag},
	)
	return err
}
