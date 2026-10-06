package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
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
	// devRecorderRedisPassword is the requirepass of the Redis beside the
	// recorder in the dev and media gate stacks, whose recorder configs carry
	// it too.
	devRecorderRedisPassword = "tide-dev-recorder-redis-password-please-change"
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
	// alpha, so an operator opts in. It needs Recording.
	Transcripts bool

	// Anonymous is true when TIDE_OIDC_ISSUER is unset or empty: there is no
	// sign-in, anyone may create a room and owns it through an anonymous
	// session, and rooms live in memory (ARCHITECTURE.md §4.1). DBPath is
	// then ":memory:" whatever TIDE_DB_PATH says.
	Anonymous bool
	// Recording is true when hosts sign in and TIDE_S3_ENDPOINT is set
	// (ARCHITECTURE.md §8). Without it there is no recorder, no object
	// storage, and no recording route.
	Recording bool

	// MediaEmbedded is true when TIDE_MEDIA_URL is unset or empty: tide runs
	// the media server itself (ARCHITECTURE.md §2.1). LiveKitURL is then
	// filled in by main once the server is up, LiveKitPublicURL defaults to
	// the base URL's origin with a ws or wss scheme, and the API key and
	// secret are generated per start unless the recorder needs them fixed.
	MediaEmbedded bool
	// MediaNodeIP is the address advertised for media (TIDE_MEDIA_NODE_IP).
	// Empty means 127.0.0.1 when the base URL is loopback, else discovered.
	MediaNodeIP string
	// MediaTCPPort and MediaUDPPort carry WebRTC media
	// (TIDE_MEDIA_TCP_PORT, TIDE_MEDIA_UDP_PORT; 7881 and 7882).
	MediaTCPPort int
	MediaUDPPort int
	// MediaInternalPort is the embedded server's API and signaling port,
	// on IPv4 loopback only (TIDE_MEDIA_API_PORT; 7880). A recorder in tide's
	// network namespace signals there.
	MediaInternalPort int
	// RecorderRedisAddr and RecorderRedisPassword name the Redis the media
	// server shares with the recorder, which runs beside the recorder, when
	// recording is on and the media server is embedded
	// (TIDE_RECORDER_REDIS_ADDR, host:port to dial, default "127.0.0.1:6379";
	// TIDE_RECORDER_REDIS_PASSWORD, its requirepass, required then).
	RecorderRedisAddr     string
	RecorderRedisPassword string
}

// Public per-IP defaults, over a one-minute window.
const (
	DefaultJoinRateLimit  = 10
	DefaultWaitRateLimit  = 20
	DefaultLoginRateLimit = 10
	DefaultPairRateLimit  = 10
)

// The embedded media server's ports, and where it finds the Redis beside the
// recorder: in tide's network namespace (ARCHITECTURE.md §2.1).
const (
	DefaultMediaInternalPort = 7880
	DefaultMediaTCPPort      = 7881
	DefaultMediaUDPPort      = 7882
	DefaultRecorderRedisAddr = "127.0.0.1:6379"
)

// Load reads configuration from the environment (ARCHITECTURE.md §12).
// Every variable is optional: an empty environment is an anonymous
// deployment with its media server embedded. Each group of variables turns
// something on and is then checked as a whole. Load reports every problem
// at once, and refuses a configuration that contradicts itself or whose
// secrets are missing, weak, or equal to a publicly-known dev value — the
// server must fail closed, not open. Dev mode (TIDE_DEV_MODE=true) supplies
// the dev value of any secret left unset, and turns nothing else on.
func Load() (Config, error) {
	dev := envBool("TIDE_DEV_MODE", false)
	// A secret set to the empty string counts as unset. It falls back to the
	// dev profile only in dev mode.
	secret := func(name, devFallback string) string {
		if value := os.Getenv(name); value != "" {
			return value
		}
		if dev {
			return devFallback
		}
		return ""
	}
	baseURL := env("TIDE_BASE_URL", "http://localhost:8080")
	cfg := Config{
		Addr:              env("TIDE_ADDR", ":8080"),
		BaseURL:           baseURL,
		SessionSecret:     secret("TIDE_SESSION_SECRET", devSessionSecret),
		LiveKitURL:        strings.TrimSpace(os.Getenv("TIDE_MEDIA_URL")),
		LiveKitPublicURL:  strings.TrimSpace(os.Getenv("TIDE_MEDIA_PUBLIC_URL")),
		LiveKitAPIKey:     secret("TIDE_MEDIA_API_KEY", devLiveKitAPIKey),
		LiveKitAPISecret:  secret("TIDE_MEDIA_API_SECRET", devLiveKitSecret),
		OIDCIssuer:        strings.TrimSpace(os.Getenv("TIDE_OIDC_ISSUER")),
		OIDCClientID:      env("TIDE_OIDC_CLIENT_ID", "tide"),
		UserGroups:        parseList(env("TIDE_USER_GROUPS", "")),
		AdminGroups:       parseList(env("TIDE_ADMIN_GROUPS", "")),
		S3Endpoint:        strings.TrimSpace(os.Getenv("TIDE_S3_ENDPOINT")),
		S3Bucket:          env("TIDE_S3_BUCKET", "tide-recordings"),
		S3Region:          env("TIDE_S3_REGION", "us-east-1"),
		EgressTemplateURL: env("TIDE_RECORDER_TEMPLATE_URL", baseURL+"/egress-template"),
		DevMode:           dev,
		JoinRateLimit:     envPositiveInt("TIDE_JOIN_RATE_LIMIT", DefaultJoinRateLimit),
		WaitRateLimit:     envPositiveInt("TIDE_WAIT_RATE_LIMIT", DefaultWaitRateLimit),
		LoginRateLimit:    envPositiveInt("TIDE_LOGIN_RATE_LIMIT", DefaultLoginRateLimit),
		PairRateLimit:     envPositiveInt("TIDE_PAIR_RATE_LIMIT", DefaultPairRateLimit),
		Transcripts:       envBool("TIDE_TRANSCRIPTS", false),
	}
	var problems []string
	trusted, err := parseTrustedProxies(env("TIDE_TRUSTED_PROXIES", ""))
	if err != nil {
		problems = append(problems, "TIDE_TRUSTED_PROXIES: "+err.Error())
	}
	cfg.TrustedProxies = trusted

	// Sign-in, or anonymous mode with its rooms in memory (§4.1).
	cfg.Anonymous = cfg.OIDCIssuer == ""
	if cfg.Anonymous && (len(cfg.UserGroups) > 0 || len(cfg.AdminGroups) > 0) {
		// Groups mean the operator meant to restrict who hosts: a missing
		// issuer must not quietly make the deployment open instead.
		problems = append(problems,
			"TIDE_USER_GROUPS or TIDE_ADMIN_GROUPS is set but sign-in is off: groups need TIDE_OIDC_ISSUER")
	}
	if cfg.Anonymous {
		cfg.DBPath = ":memory:"
		if cfg.SessionSecret == "" {
			// Its sessions end with its rooms, at restart.
			cfg.SessionSecret = randomSecret()
		}
	} else {
		cfg.DBPath = env("TIDE_DB_PATH", "./data/tide.db")
		cfg.OIDCClientSecret = secret("TIDE_OIDC_CLIENT_SECRET", devOIDCSecret)
	}

	// Recording needs sign-in and object storage (§8).
	if cfg.S3Endpoint != "" {
		if cfg.Anonymous {
			problems = append(problems,
				"TIDE_S3_ENDPOINT is set but sign-in is off: recording needs TIDE_OIDC_ISSUER")
		} else {
			cfg.Recording = true
			cfg.S3PublicEndpoint = env("TIDE_S3_PUBLIC_ENDPOINT", cfg.S3Endpoint)
			cfg.S3EgressEndpoint = env("TIDE_S3_RECORDER_ENDPOINT", cfg.S3Endpoint)
			cfg.S3AccessKey = secret("TIDE_S3_ACCESS_KEY", devS3AccessKey)
			cfg.S3SecretKey = secret("TIDE_S3_SECRET_KEY", devS3SecretKey)
		}
	}
	if cfg.Transcripts && !cfg.Recording {
		problems = append(problems,
			"TIDE_TRANSCRIPTS=true needs recording (TIDE_OIDC_ISSUER and TIDE_S3_ENDPOINT)")
	}

	// The media server: embedded unless TIDE_MEDIA_URL names one (§2.1).
	cfg.MediaEmbedded = cfg.LiveKitURL == ""
	if cfg.MediaEmbedded {
		problems = append(problems, cfg.loadEmbeddedMedia(secret)...)
	} else if cfg.LiveKitPublicURL == "" {
		problems = append(problems,
			"TIDE_MEDIA_PUBLIC_URL must be set with an external media server (TIDE_MEDIA_URL)")
	}

	var secretProblems []string
	if !dev {
		secretProblems = cfg.validateSecrets()
	}
	if len(problems)+len(secretProblems) > 0 {
		message := "refusing to start: " + strings.Join(append(problems, secretProblems...), "; ")
		if len(secretProblems) > 0 {
			message += " (set TIDE_DEV_MODE=true only for local development)"
		}
		return Config{}, errors.New(message)
	}
	return cfg, nil
}

// loadEmbeddedMedia reads the embedded media server's settings into c and
// returns what is wrong with them. secret reads a secret as Load does.
func (c *Config) loadEmbeddedMedia(secret func(name, devFallback string) string) []string {
	var problems []string
	if c.LiveKitPublicURL == "" {
		publicURL, err := signalingOrigin(c.BaseURL)
		if err != nil {
			problems = append(problems, "TIDE_BASE_URL: "+err.Error())
		}
		c.LiveKitPublicURL = publicURL
	}
	var problem string
	c.MediaInternalPort, problem = envPort("TIDE_MEDIA_API_PORT", DefaultMediaInternalPort)
	problems = appendProblem(problems, problem)
	c.MediaNodeIP = strings.TrimSpace(os.Getenv("TIDE_MEDIA_NODE_IP"))
	if c.MediaNodeIP != "" && net.ParseIP(c.MediaNodeIP) == nil {
		problems = append(problems, fmt.Sprintf("TIDE_MEDIA_NODE_IP %q is not an IP address", c.MediaNodeIP))
	}
	c.MediaTCPPort, problem = envPort("TIDE_MEDIA_TCP_PORT", DefaultMediaTCPPort)
	problems = appendProblem(problems, problem)
	if c.MediaTCPPort == c.MediaInternalPort {
		problems = append(problems, fmt.Sprintf("TIDE_MEDIA_API_PORT and TIDE_MEDIA_TCP_PORT are both %d", c.MediaTCPPort))
	}
	c.MediaUDPPort, problem = envPort("TIDE_MEDIA_UDP_PORT", DefaultMediaUDPPort)
	problems = appendProblem(problems, problem)
	if c.Recording {
		// The recorder joins rooms with the key and secret, and coordinates
		// with the media server over the Redis beside it, so all three are
		// configured, and checked as secrets.
		c.RecorderRedisAddr = env("TIDE_RECORDER_REDIS_ADDR", DefaultRecorderRedisAddr)
		problems = appendProblem(problems, checkDialAddress("TIDE_RECORDER_REDIS_ADDR", c.RecorderRedisAddr))
		c.RecorderRedisPassword = secret("TIDE_RECORDER_REDIS_PASSWORD", devRecorderRedisPassword)
		return problems
	}
	// Nothing outside the process needs the key and secret, so a missing one
	// is made up for this start, and never printed.
	if c.LiveKitAPIKey == "" {
		c.LiveKitAPIKey = "tide" + randomHex(8)
	}
	if c.LiveKitAPISecret == "" {
		c.LiveKitAPISecret = randomSecret()
	}
	return problems
}

// Summary says in one line what this deployment is, for main to log at
// startup: its sign-in, its media server, and what it records.
func (c Config) Summary() string {
	parts := []string{"signed in"}
	if c.Anonymous {
		parts[0] = "anonymous"
	}
	switch {
	case !c.MediaEmbedded:
		parts = append(parts, "media server external")
	case c.MediaNodeIP != "":
		parts = append(parts, "media server embedded (node IP "+c.MediaNodeIP+")")
	case c.MediaLoopback():
		parts = append(parts, "media server embedded (node IP 127.0.0.1)")
	default:
		parts = append(parts, "media server embedded (node IP discovered)")
	}
	if c.Recording {
		parts = append(parts, "recording on")
	} else {
		parts = append(parts, "recording off")
	}
	if c.Transcripts {
		parts = append(parts, "transcripts on")
	}
	return strings.Join(parts, ", ")
}

// MediaLoopback reports whether the base URL's host is loopback, when the
// embedded media server advertises 127.0.0.1 unless TIDE_MEDIA_NODE_IP says
// otherwise.
func (c Config) MediaLoopback() bool {
	parsed, err := url.Parse(c.BaseURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// signalingOrigin is the base URL's origin with a ws or wss scheme: where
// browsers reach the embedded media server's signaling, which tide forwards
// at /rtc.
func signalingOrigin(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err == nil && parsed.Host != "" {
		switch strings.ToLower(parsed.Scheme) {
		case "http":
			return "ws://" + parsed.Host, nil
		case "https":
			return "wss://" + parsed.Host, nil
		}
	}
	return "", fmt.Errorf("%q is not an absolute http or https URL", baseURL)
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

// validateSecrets rejects each secret this configuration uses that is
// missing, a shipped dev default, or too short. A secret Load generated
// passes; one the mode doesn't use isn't checked.
func (c Config) validateSecrets() []string {
	devValues := map[string]bool{
		devSessionSecret:         true,
		devLiveKitAPIKey:         true,
		devLiveKitSecret:         true,
		devOIDCSecret:            true,
		devS3AccessKey:           true,
		devS3SecretKey:           true,
		devRecorderRedisPassword: true,
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
	if !c.Anonymous {
		check("TIDE_OIDC_CLIENT_SECRET", c.OIDCClientSecret, 16)
	}
	if c.Recording {
		check("TIDE_S3_ACCESS_KEY", c.S3AccessKey, 1)
		check("TIDE_S3_SECRET_KEY", c.S3SecretKey, 16)
		if c.MediaEmbedded {
			check("TIDE_RECORDER_REDIS_PASSWORD", c.RecorderRedisPassword, 32)
		}
	}
	return problems
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

// env reads a variable, trimmed; an empty value means unset, for every
// variable (ARCHITECTURE.md §12), so it takes fallback.
func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
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

// envPort reads a TCP or UDP port, returning the problem with it, if any.
// Unlike a rate limit, a port that can't be used refuses startup: falling
// back would open a port the operator didn't ask for.
func envPort(name string, fallback int) (int, string) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, ""
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return fallback, fmt.Sprintf("%s %q is not a port (1-65535)", name, value)
	}
	return port, ""
}

// checkDialAddress says what is wrong with an address tide connects to, if
// anything: host:port, where the host is a name or an IP address.
func checkDialAddress(name, address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Sprintf("%s %q is not host:port", name, address)
	}
	if host == "" {
		return fmt.Sprintf("%s %q: name the host Redis runs on", name, address)
	}
	if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
		return fmt.Sprintf("%s %q: the port must be 1-65535", name, address)
	}
	return ""
}

func appendProblem(problems []string, problem string) []string {
	if problem == "" {
		return problems
	}
	return append(problems, problem)
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

// randomHex is n random bytes, hex-encoded.
func randomHex(n int) string {
	value := make([]byte, n)
	_, _ = rand.Read(value) // crypto/rand.Read never fails
	return hex.EncodeToString(value)
}

// randomSecret is a secret for one start: 32 random bytes, 43 characters.
func randomSecret() string {
	value := make([]byte, 32)
	_, _ = rand.Read(value) // crypto/rand.Read never fails
	return base64.RawURLEncoding.EncodeToString(value)
}
