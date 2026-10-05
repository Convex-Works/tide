package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Dev-profile secret defaults. These values are publicly known — they are
// committed to this repo and mirrored in deploy/ — so they are only ever
// applied when TIDE_DEV_MODE=true, and production explicitly refuses them.
const (
	devSessionSecret = "tide-dev-session-secret-please-change"
	devLiveKitAPIKey = "devkey"
	devLiveKitSecret = "tide-dev-secret-please-change-0000000000"
	devOIDCSecret    = "tide-dev-oidc-secret"
	devS3AccessKey   = "tide"
	devS3SecretKey   = "tide-dev-minio"
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
	UserGroups        []string
	AdminGroups       []string
	S3Endpoint        string
	S3PublicEndpoint  string
	S3EgressEndpoint  string
	S3Bucket          string
	S3AccessKey       string
	S3SecretKey       string
	S3Region          string
	EgressTemplateURL string
	// TrustedProxies are CIDRs (or bare IPs) of reverse proxies whose
	// X-Forwarded-For may be believed. Empty means no proxy is trusted and
	// the TCP peer address is always the client.
	TrustedProxies []*net.IPNet
	DevMode        bool
	// Per-IP request ceilings, each over a one-minute window. A zero value
	// means "unset" and callers fall back to the Default* constants, so a
	// zero-valued Config never silently denies every request.
	JoinRateLimit  int
	WaitRateLimit  int
	LoginRateLimit int
	// PairRateLimit bounds machines starting a moil pairing, the one moil
	// endpoint that takes no credentials.
	PairRateLimit int
	// Transcripts turns on transcripts and machine pairing through moil
	// (TIDE_TRANSCRIPTS, ARCHITECTURE.md §8.1). Off by default: moil is
	// alpha, so an operator opts in.
	Transcripts bool
}

// Public per-IP defaults, over a one-minute window.
const (
	DefaultJoinRateLimit  = 10
	DefaultWaitRateLimit  = 20
	DefaultLoginRateLimit = 10
	DefaultPairRateLimit  = 10
)

// Load reads configuration from the environment. Dev mode is opt-in
// (TIDE_DEV_MODE=true); outside it, secrets have no defaults and Load
// refuses to produce a config that is missing, weak, or equal to a
// publicly-known dev value — the server must fail closed, not open.
func Load() (Config, error) {
	dev := envBool("TIDE_DEV_MODE", false)
	// Secrets fall back to the dev profile only in dev mode.
	secret := func(name, devFallback string) string {
		if dev {
			return env(name, devFallback)
		}
		return env(name, "")
	}
	baseURL := env("TIDE_BASE_URL", "http://localhost:8080")
	cfg := Config{
		Addr:              env("TIDE_ADDR", ":8080"),
		BaseURL:           baseURL,
		SessionSecret:     secret("TIDE_SESSION_SECRET", devSessionSecret),
		DBPath:            env("TIDE_DB_PATH", "./data/tide.db"),
		LiveKitURL:        env("TIDE_MEDIA_URL", "ws://localhost:7880"),
		LiveKitPublicURL:  env("TIDE_MEDIA_PUBLIC_URL", "ws://localhost:7880"),
		LiveKitAPIKey:     secret("TIDE_MEDIA_API_KEY", devLiveKitAPIKey),
		LiveKitAPISecret:  secret("TIDE_MEDIA_API_SECRET", devLiveKitSecret),
		OIDCIssuer:        env("TIDE_OIDC_ISSUER", "http://localhost:5556/dex"),
		OIDCClientID:      env("TIDE_OIDC_CLIENT_ID", "tide"),
		OIDCClientSecret:  secret("TIDE_OIDC_CLIENT_SECRET", devOIDCSecret),
		UserGroups:        parseList(env("TIDE_USER_GROUPS", "")),
		AdminGroups:       parseList(env("TIDE_ADMIN_GROUPS", "")),
		S3Endpoint:        env("TIDE_S3_ENDPOINT", "http://localhost:9000"),
		S3PublicEndpoint:  env("TIDE_S3_PUBLIC_ENDPOINT", "http://localhost:9000"),
		S3EgressEndpoint:  env("TIDE_S3_RECORDER_ENDPOINT", "http://minio:9000"),
		S3Bucket:          env("TIDE_S3_BUCKET", "tide-recordings"),
		S3AccessKey:       secret("TIDE_S3_ACCESS_KEY", devS3AccessKey),
		S3SecretKey:       secret("TIDE_S3_SECRET_KEY", devS3SecretKey),
		S3Region:          env("TIDE_S3_REGION", "us-east-1"),
		EgressTemplateURL: env("TIDE_RECORDER_TEMPLATE_URL", baseURL+"/egress-template"),
		DevMode:           dev,
		JoinRateLimit:     envPositiveInt("TIDE_JOIN_RATE_LIMIT", DefaultJoinRateLimit),
		WaitRateLimit:     envPositiveInt("TIDE_WAIT_RATE_LIMIT", DefaultWaitRateLimit),
		LoginRateLimit:    envPositiveInt("TIDE_LOGIN_RATE_LIMIT", DefaultLoginRateLimit),
		PairRateLimit:     envPositiveInt("TIDE_PAIR_RATE_LIMIT", DefaultPairRateLimit),
		Transcripts:       envBool("TIDE_TRANSCRIPTS", false),
	}
	trusted, err := parseTrustedProxies(env("TIDE_TRUSTED_PROXIES", ""))
	if err != nil {
		return Config{}, fmt.Errorf("TIDE_TRUSTED_PROXIES: %w", err)
	}
	cfg.TrustedProxies = trusted
	if !dev {
		if err := cfg.validateProduction(); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

// parseList returns a stable, deduplicated comma-separated configuration list.
// OIDC group names are case-sensitive, so values are trimmed but not folded.
func parseList(raw string) []string {
	seen := make(map[string]bool)
	values := make([]string, 0)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" || seen[entry] {
			continue
		}
		seen[entry] = true
		values = append(values, entry)
	}
	return values
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
	check("TIDE_SESSION_SECRET", c.SessionSecret, 32)
	check("TIDE_MEDIA_API_KEY", c.LiveKitAPIKey, 1)
	check("TIDE_MEDIA_API_SECRET", c.LiveKitAPISecret, 32)
	check("TIDE_OIDC_CLIENT_SECRET", c.OIDCClientSecret, 16)
	check("TIDE_S3_ACCESS_KEY", c.S3AccessKey, 1)
	check("TIDE_S3_SECRET_KEY", c.S3SecretKey, 16)
	if len(problems) > 0 {
		return errors.New("refusing to start: " + strings.Join(problems, "; ") +
			" (set TIDE_DEV_MODE=true only for local development)")
	}
	return nil
}

// parseTrustedProxies accepts a comma-separated list of CIDRs; a bare IP is
// treated as a single-host network. Invalid entries refuse startup rather
// than silently trusting the wrong hosts.
func parseTrustedProxies(raw string) ([]*net.IPNet, error) {
	var networks []*net.IPNet
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("invalid IP %q", entry)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			entry = fmt.Sprintf("%s/%d", ip, bits)
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q", entry)
		}
		networks = append(networks, network)
	}
	return networks, nil
}

func env(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return fallback
}

// envPositiveInt reads a positive integer, falling back on anything unusable.
// Rate limits are deployment configuration: a single-tenant install behind a
// VPN, and the media gate where every browser shares one container IP, both
// need higher ceilings than the public default.
func envPositiveInt(name string, fallback int) int {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
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
