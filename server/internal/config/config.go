package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Dev-profile secret defaults. These values are publicly known — they are
// committed to this repo and mirrored in deploy/ — so they are only ever
// applied when KLISI_DEV_MODE=true, and production explicitly refuses them.
const (
	devSessionSecret = "klisi-dev-session-secret-please-change"
	devLiveKitAPIKey = "devkey"
	devLiveKitSecret = "klisi-dev-secret-please-change-0000000000"
	devOIDCSecret    = "klisi-dev-oidc-secret"
	devS3AccessKey   = "klisi"
	devS3SecretKey   = "klisi-dev-minio"
)

type Config struct {
	Addr              string
	BaseURL           string
	SessionSecret     string
	DBPath            string
	LiveKitURL        string
	LiveKitPublicURL  string
	LiveKitAPIKey     string
	LiveKitAPISecret  string
	OIDCIssuer        string
	OIDCClientID      string
	OIDCClientSecret  string
	S3Endpoint        string
	S3PublicEndpoint  string
	S3EgressEndpoint  string
	S3Bucket          string
	S3AccessKey       string
	S3SecretKey       string
	S3Region          string
	EgressTemplateURL string
	DevMode           bool
}

// Load reads configuration from the environment. Dev mode is opt-in
// (KLISI_DEV_MODE=true); outside it, secrets have no defaults and Load
// refuses to produce a config that is missing, weak, or equal to a
// publicly-known dev value — the server must fail closed, not open.
func Load() (Config, error) {
	dev := envBool("KLISI_DEV_MODE", false)
	// Secrets fall back to the dev profile only in dev mode.
	secret := func(name, devFallback string) string {
		if dev {
			return env(name, devFallback)
		}
		return env(name, "")
	}
	baseURL := env("KLISI_BASE_URL", "http://localhost:8080")
	cfg := Config{
		Addr:              env("KLISI_ADDR", ":8080"),
		BaseURL:           baseURL,
		SessionSecret:     secret("KLISI_SESSION_SECRET", devSessionSecret),
		DBPath:            env("KLISI_DB_PATH", "./data/klisi.db"),
		LiveKitURL:        env("KLISI_LIVEKIT_URL", "ws://localhost:7880"),
		LiveKitPublicURL:  env("KLISI_LIVEKIT_PUBLIC_URL", "ws://localhost:7880"),
		LiveKitAPIKey:     secret("KLISI_LIVEKIT_API_KEY", devLiveKitAPIKey),
		LiveKitAPISecret:  secret("KLISI_LIVEKIT_API_SECRET", devLiveKitSecret),
		OIDCIssuer:        env("KLISI_OIDC_ISSUER", "http://localhost:5556/dex"),
		OIDCClientID:      env("KLISI_OIDC_CLIENT_ID", "klisi"),
		OIDCClientSecret:  secret("KLISI_OIDC_CLIENT_SECRET", devOIDCSecret),
		S3Endpoint:        env("KLISI_S3_ENDPOINT", "http://localhost:9000"),
		S3PublicEndpoint:  env("KLISI_S3_PUBLIC_ENDPOINT", "http://localhost:9000"),
		S3EgressEndpoint:  env("KLISI_S3_EGRESS_ENDPOINT", "http://minio:9000"),
		S3Bucket:          env("KLISI_S3_BUCKET", "klisi-recordings"),
		S3AccessKey:       secret("KLISI_S3_ACCESS_KEY", devS3AccessKey),
		S3SecretKey:       secret("KLISI_S3_SECRET_KEY", devS3SecretKey),
		S3Region:          env("KLISI_S3_REGION", "us-east-1"),
		EgressTemplateURL: env("KLISI_EGRESS_TEMPLATE_URL", baseURL+"/egress-template"),
		DevMode:           dev,
	}
	if !dev {
		if err := cfg.validateProduction(); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

// validateProduction rejects any secret that is missing, a shipped dev
// default, or too short. All problems are reported at once.
func (c Config) validateProduction() error {
	devValues := map[string]bool{
		devSessionSecret: true,
		devLiveKitAPIKey: true,
		devLiveKitSecret: true,
		devOIDCSecret:    true,
		devS3AccessKey:   true,
		devS3SecretKey:   true,
	}
	var problems []string
	check := func(name, value string, minLen int) {
		switch {
		case value == "":
			problems = append(problems, name+" must be set")
		case devValues[value]:
			problems = append(problems, name+" is a publicly-known dev default")
		case len(value) < minLen:
			problems = append(problems, fmt.Sprintf("%s must be at least %d characters", name, minLen))
		}
	}
	check("KLISI_SESSION_SECRET", c.SessionSecret, 32)
	check("KLISI_LIVEKIT_API_KEY", c.LiveKitAPIKey, 1)
	check("KLISI_LIVEKIT_API_SECRET", c.LiveKitAPISecret, 32)
	check("KLISI_OIDC_CLIENT_SECRET", c.OIDCClientSecret, 16)
	check("KLISI_S3_ACCESS_KEY", c.S3AccessKey, 1)
	check("KLISI_S3_SECRET_KEY", c.S3SecretKey, 16)
	if len(problems) > 0 {
		return errors.New("refusing to start: " + strings.Join(problems, "; ") +
			" (set KLISI_DEV_MODE=true only for local development)")
	}
	return nil
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
