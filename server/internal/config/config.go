package config

import (
	"os"
	"strconv"
)

type Config struct {
	Addr             string
	BaseURL          string
	SessionSecret    string
	DBPath           string
	LiveKitURL       string
	LiveKitAPIKey    string
	LiveKitAPISecret string
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	S3Endpoint       string
	S3Bucket         string
	S3AccessKey      string
	S3SecretKey      string
	DevMode          bool
}

func Load() Config {
	return Config{
		Addr:             env("KLISI_ADDR", ":8080"),
		BaseURL:          env("KLISI_BASE_URL", "http://localhost:8080"),
		SessionSecret:    env("KLISI_SESSION_SECRET", "klisi-dev-session-secret-please-change"),
		DBPath:           env("KLISI_DB_PATH", "./data/klisi.db"),
		LiveKitURL:       env("KLISI_LIVEKIT_URL", "ws://localhost:7880"),
		LiveKitAPIKey:    env("KLISI_LIVEKIT_API_KEY", "devkey"),
		LiveKitAPISecret: env("KLISI_LIVEKIT_API_SECRET", "klisi-dev-secret-please-change-0000000000"),
		OIDCIssuer:       env("KLISI_OIDC_ISSUER", "http://localhost:5556/dex"),
		OIDCClientID:     env("KLISI_OIDC_CLIENT_ID", "klisi"),
		OIDCClientSecret: env("KLISI_OIDC_CLIENT_SECRET", "klisi-dev-oidc-secret"),
		S3Endpoint:       env("KLISI_S3_ENDPOINT", "http://localhost:9000"),
		S3Bucket:         env("KLISI_S3_BUCKET", "klisi-recordings"),
		S3AccessKey:      env("KLISI_S3_ACCESS_KEY", "klisi"),
		S3SecretKey:      env("KLISI_S3_SECRET_KEY", "klisi-dev-minio"),
		DevMode:          envBool("KLISI_DEV_MODE", true),
	}
}

func env(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
