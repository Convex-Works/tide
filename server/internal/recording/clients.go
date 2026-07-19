package recording

import (
	"context"
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

	"klisi/internal/config"
	"klisi/internal/store"
)

func New(cfg config.Config, recordings *store.Store) *Handler {
	egress := lksdk.NewEgressClient(cfg.LiveKitURL, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	rooms := lksdk.NewRoomServiceClient(liveKitHTTPURL(cfg.LiveKitURL), cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	provider := protocolauth.NewSimpleKeyProvider(cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	handler := NewHandler(
		recordings, egress, rooms, NewMinIOStore(cfg), cfg.EgressTemplateURL,
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

type MinIOStore struct {
	endpoint       string
	publicEndpoint string
	bucket         string
	accessKey      string
	secretKey      string
	region         string
}

func NewMinIOStore(cfg config.Config) *MinIOStore {
	return &MinIOStore{
		endpoint: cfg.S3Endpoint, publicEndpoint: cfg.S3PublicEndpoint,
		bucket: cfg.S3Bucket, accessKey: cfg.S3AccessKey,
		secretKey: cfg.S3SecretKey, region: cfg.S3Region,
	}
}

func (s *MinIOStore) Remove(ctx context.Context, key string) error {
	client, err := s.client(s.endpoint)
	if err != nil {
		return err
	}
	err = client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
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
	client, err := s.client(s.publicEndpoint)
	if err != nil {
		return "", err
	}
	location, err := client.PresignedGetObject(ctx, s.bucket, key, expiry, nil)
	if err != nil {
		return "", err
	}
	return location.String(), nil
}

func (s *MinIOStore) client(rawEndpoint string) (*minio.Client, error) {
	parsed, err := url.Parse(rawEndpoint)
	if err != nil {
		return nil, err
	}
	secure := parsed.Scheme == "https"
	endpoint := parsed.Host
	if endpoint == "" {
		endpoint = strings.TrimPrefix(strings.TrimPrefix(rawEndpoint, "http://"), "https://")
	}
	return minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(s.accessKey, s.secretKey, ""),
		Secure: secure, Region: s.region, BucketLookup: minio.BucketLookupPath,
	})
}
