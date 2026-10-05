package config

import (
	"os"
	"strings"
	"testing"
)

// clearTideEnv unsets every variable Load reads so tests are hermetic.
func clearTideEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"TIDE_ADDR", "TIDE_BASE_URL", "TIDE_SESSION_SECRET", "TIDE_DB_PATH",
		"TIDE_MEDIA_URL", "TIDE_MEDIA_PUBLIC_URL", "TIDE_MEDIA_API_KEY",
		"TIDE_MEDIA_API_SECRET", "TIDE_MEDIA_NODE_IP", "TIDE_MEDIA_TCP_PORT", "TIDE_MEDIA_API_PORT",
		"TIDE_MEDIA_UDP_PORT", "TIDE_OIDC_ISSUER", "TIDE_OIDC_CLIENT_ID",
		"TIDE_OIDC_CLIENT_SECRET", "TIDE_USER_GROUPS", "TIDE_ADMIN_GROUPS",
		"TIDE_S3_ENDPOINT", "TIDE_S3_PUBLIC_ENDPOINT",
		"TIDE_S3_RECORDER_ENDPOINT", "TIDE_S3_BUCKET", "TIDE_S3_ACCESS_KEY",
		"TIDE_S3_SECRET_KEY", "TIDE_S3_REGION", "TIDE_RECORDER_TEMPLATE_URL",
		"TIDE_RECORDER_REDIS_ADDR", "TIDE_RECORDER_REDIS_PASSWORD",
		"TIDE_TRUSTED_PROXIES", "TIDE_DEV_MODE", "TIDE_JOIN_RATE_LIMIT",
		"TIDE_WAIT_RATE_LIMIT", "TIDE_LOGIN_RATE_LIMIT", "TIDE_PAIR_RATE_LIMIT",
		"TIDE_TRANSCRIPTS",
	} {
		// t.Setenv registers restoration of the original value; the explicit
		// Unsetenv afterwards gives LookupEnv-miss semantics during the test.
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for name, value := range env {
		t.Setenv(name, value)
	}
}

// Strong production values for every group, for tests to start from.
var (
	signedIn = map[string]string{
		"TIDE_OIDC_ISSUER":        "https://id.example.com",
		"TIDE_OIDC_CLIENT_SECRET": strings.Repeat("o", 20),
		"TIDE_SESSION_SECRET":     strings.Repeat("s", 40),
	}
	storage = map[string]string{
		"TIDE_S3_ENDPOINT":   "https://s3.example.com",
		"TIDE_S3_ACCESS_KEY": "prod-access",
		"TIDE_S3_SECRET_KEY": strings.Repeat("c", 20),
	}
	recorder = map[string]string{
		"TIDE_MEDIA_API_KEY":           "prodkey",
		"TIDE_MEDIA_API_SECRET":        strings.Repeat("a", 40),
		"TIDE_RECORDER_REDIS_PASSWORD": strings.Repeat("r", 40),
	}
	external = map[string]string{
		"TIDE_MEDIA_URL":        "ws://media:7880",
		"TIDE_MEDIA_PUBLIC_URL": "wss://media.example.com",
		"TIDE_MEDIA_API_KEY":    "prodkey",
		"TIDE_MEDIA_API_SECRET": strings.Repeat("a", 40),
	}
)

func merge(groups ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, group := range groups {
		for name, value := range group {
			merged[name] = value
		}
	}
	return merged
}

func load(t *testing.T, env map[string]string) Config {
	t.Helper()
	clearTideEnv(t)
	setEnv(t, env)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	return cfg
}

func loadErr(t *testing.T, env map[string]string) string {
	t.Helper()
	clearTideEnv(t)
	setEnv(t, env)
	cfg, err := Load()
	if err == nil {
		t.Fatalf("Load succeeded with %+v, want a refusal", cfg)
	}
	if !strings.HasPrefix(err.Error(), "refusing to start: ") {
		t.Fatalf("error = %v, want it to start with the refusal", err)
	}
	return err.Error()
}

// An empty environment is an anonymous deployment with its media server
// embedded, on localhost: everything it needs is made up for this start.
func TestLoadEmptyEnvironmentIsAnonymousAndEmbedded(t *testing.T) {
	cfg := load(t, nil)
	if !cfg.Anonymous || cfg.Recording || cfg.Transcripts || !cfg.MediaEmbedded || cfg.DevMode {
		t.Fatalf("modes = anonymous %v, recording %v, transcripts %v, embedded %v, dev %v",
			cfg.Anonymous, cfg.Recording, cfg.Transcripts, cfg.MediaEmbedded, cfg.DevMode)
	}
	if cfg.DBPath != ":memory:" {
		t.Fatalf("DBPath = %q, want :memory:", cfg.DBPath)
	}
	if cfg.Addr != ":8080" || cfg.BaseURL != "http://localhost:8080" {
		t.Fatalf("Addr, BaseURL = %q, %q", cfg.Addr, cfg.BaseURL)
	}
	if cfg.LiveKitURL != "" || cfg.LiveKitPublicURL != "ws://localhost:8080" {
		t.Fatalf("media URLs = %q, %q; want main to fill the first, the base URL's origin as the second",
			cfg.LiveKitURL, cfg.LiveKitPublicURL)
	}
	if cfg.MediaInternalPort != 7880 || cfg.MediaTCPPort != 7881 || cfg.MediaUDPPort != 7882 || cfg.MediaNodeIP != "" {
		t.Fatalf("media ports = %d/%d/%d, node IP %q", cfg.MediaInternalPort, cfg.MediaTCPPort, cfg.MediaUDPPort, cfg.MediaNodeIP)
	}
	if !strings.HasPrefix(cfg.LiveKitAPIKey, "tide") || len(cfg.LiveKitAPISecret) < 32 || len(cfg.SessionSecret) < 32 {
		t.Fatalf("generated key %q, secret of %d, session secret of %d", cfg.LiveKitAPIKey, len(cfg.LiveKitAPISecret), len(cfg.SessionSecret))
	}
	if cfg.RecorderRedisAddr != "" || cfg.RecorderRedisPassword != "" || cfg.S3PublicEndpoint != "" {
		t.Fatalf("recording settings without recording: %q %q %q", cfg.RecorderRedisAddr, cfg.RecorderRedisPassword, cfg.S3PublicEndpoint)
	}
	// Each start makes its own.
	again := load(t, nil)
	if again.SessionSecret == cfg.SessionSecret || again.LiveKitAPISecret == cfg.LiveKitAPISecret || again.LiveKitAPIKey == cfg.LiveKitAPIKey {
		t.Fatal("two starts generated the same secrets")
	}
	if got, want := cfg.Summary(), "anonymous, media server embedded (node IP 127.0.0.1), recording off"; got != want {
		t.Fatalf("Summary = %q, want %q", got, want)
	}
}

// Empty values mean unset, so a Compose file can leave a variable blank.
func TestLoadEmptyValuesAreUnset(t *testing.T) {
	cfg := load(t, map[string]string{
		"TIDE_OIDC_ISSUER": " ", "TIDE_MEDIA_URL": "", "TIDE_S3_ENDPOINT": "",
		"TIDE_SESSION_SECRET": "", "TIDE_MEDIA_API_SECRET": "",
	})
	if !cfg.Anonymous || !cfg.MediaEmbedded || cfg.Recording || len(cfg.SessionSecret) < 32 || len(cfg.LiveKitAPISecret) < 32 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadAnonymousIgnoresTheDatabasePath(t *testing.T) {
	cfg := load(t, map[string]string{"TIDE_DB_PATH": "/data/tide.db"})
	if cfg.DBPath != ":memory:" {
		t.Fatalf("DBPath = %q", cfg.DBPath)
	}
}

func TestLoadSignalingFollowsTheBaseURL(t *testing.T) {
	for _, test := range []struct {
		env      map[string]string
		public   string
		loopback bool
		summary  string
	}{
		{map[string]string{"TIDE_BASE_URL": "https://meet.example.com"}, "wss://meet.example.com", false,
			"anonymous, media server embedded (node IP discovered), recording off"},
		{map[string]string{"TIDE_BASE_URL": "https://meet.example.com:8443/"}, "wss://meet.example.com:8443", false,
			"anonymous, media server embedded (node IP discovered), recording off"},
		{map[string]string{"TIDE_BASE_URL": "http://127.0.0.1:5173"}, "ws://127.0.0.1:5173", true,
			"anonymous, media server embedded (node IP 127.0.0.1), recording off"},
		{map[string]string{"TIDE_BASE_URL": "http://[::1]:8080"}, "ws://[::1]:8080", true,
			"anonymous, media server embedded (node IP 127.0.0.1), recording off"},
		{map[string]string{"TIDE_BASE_URL": "https://meet.example.com", "TIDE_MEDIA_NODE_IP": "203.0.113.7"},
			"wss://meet.example.com", false, "anonymous, media server embedded (node IP 203.0.113.7), recording off"},
		{map[string]string{"TIDE_BASE_URL": "https://meet.example.com", "TIDE_MEDIA_PUBLIC_URL": "wss://sfu.example.com"},
			"wss://sfu.example.com", false, "anonymous, media server embedded (node IP discovered), recording off"},
	} {
		t.Run(test.env["TIDE_BASE_URL"], func(t *testing.T) {
			cfg := load(t, test.env)
			if cfg.LiveKitPublicURL != test.public || cfg.MediaLoopback() != test.loopback || cfg.Summary() != test.summary {
				t.Fatalf("public URL %q, loopback %v, summary %q; want %q, %v, %q",
					cfg.LiveKitPublicURL, cfg.MediaLoopback(), cfg.Summary(), test.public, test.loopback, test.summary)
			}
		})
	}
}

func TestLoadMediaPortsAndNodeIP(t *testing.T) {
	cfg := load(t, map[string]string{
		"TIDE_MEDIA_TCP_PORT": "17881", "TIDE_MEDIA_UDP_PORT": "443", "TIDE_MEDIA_NODE_IP": "2001:db8::7",
		"TIDE_MEDIA_API_PORT": "17880",
	})
	if cfg.MediaTCPPort != 17881 || cfg.MediaUDPPort != 443 || cfg.MediaNodeIP != "2001:db8::7" || cfg.MediaInternalPort != 17880 {
		t.Fatalf("ports %d/%d/%d, node IP %q", cfg.MediaInternalPort, cfg.MediaTCPPort, cfg.MediaUDPPort, cfg.MediaNodeIP)
	}
	if defaults := load(t, nil); defaults.MediaInternalPort != 7880 || defaults.MediaTCPPort != 7881 || defaults.MediaUDPPort != 7882 {
		t.Fatalf("default ports %d/%d/%d, want 7880/7881/7882", defaults.MediaInternalPort, defaults.MediaTCPPort, defaults.MediaUDPPort)
	}
}

// Sign-in turns on the database file and needs its secrets; it doesn't turn
// on recording.
func TestLoadSignedInWithoutStorage(t *testing.T) {
	cfg := load(t, signedIn)
	if cfg.Anonymous || cfg.Recording || !cfg.MediaEmbedded {
		t.Fatalf("anonymous %v, recording %v, embedded %v", cfg.Anonymous, cfg.Recording, cfg.MediaEmbedded)
	}
	if cfg.DBPath != "./data/tide.db" || cfg.OIDCIssuer != "https://id.example.com" || cfg.OIDCClientID != "tide" {
		t.Fatalf("DBPath %q, issuer %q, client %q", cfg.DBPath, cfg.OIDCIssuer, cfg.OIDCClientID)
	}
	// No recorder, so the media key and secret are still made up.
	if !strings.HasPrefix(cfg.LiveKitAPIKey, "tide") || len(cfg.LiveKitAPISecret) < 32 {
		t.Fatalf("key %q, secret of %d", cfg.LiveKitAPIKey, len(cfg.LiveKitAPISecret))
	}
	if got, want := cfg.Summary(), "signed in, media server embedded (node IP 127.0.0.1), recording off"; got != want {
		t.Fatalf("Summary = %q, want %q", got, want)
	}
	if path := load(t, merge(signedIn, map[string]string{"TIDE_DB_PATH": "/data/tide.db"})).DBPath; path != "/data/tide.db" {
		t.Fatalf("DBPath = %q", path)
	}
}

func TestLoadSignedInNeedsItsSecrets(t *testing.T) {
	message := loadErr(t, map[string]string{"TIDE_OIDC_ISSUER": "https://id.example.com"})
	for _, want := range []string{"TIDE_SESSION_SECRET must be set", "TIDE_OIDC_CLIENT_SECRET must be set"} {
		if !strings.Contains(message, want) {
			t.Errorf("error should say %q, got: %s", want, message)
		}
	}
	// The media server's are generated without a recorder.
	if strings.Contains(message, "TIDE_MEDIA_API") {
		t.Errorf("error names the media key or secret without a recorder: %s", message)
	}
}

func TestLoadRecordingWithEmbeddedMedia(t *testing.T) {
	cfg := load(t, merge(signedIn, storage, recorder))
	if !cfg.Recording || !cfg.MediaEmbedded || cfg.Anonymous {
		t.Fatalf("recording %v, embedded %v, anonymous %v", cfg.Recording, cfg.MediaEmbedded, cfg.Anonymous)
	}
	// The other two views of storage default to the one tide uses.
	if cfg.S3PublicEndpoint != "https://s3.example.com" || cfg.S3EgressEndpoint != "https://s3.example.com" {
		t.Fatalf("S3 views = %q, %q", cfg.S3PublicEndpoint, cfg.S3EgressEndpoint)
	}
	if cfg.S3Bucket != "tide-recordings" || cfg.S3Region != "us-east-1" {
		t.Fatalf("bucket %q, region %q", cfg.S3Bucket, cfg.S3Region)
	}
	if cfg.RecorderRedisAddr != "127.0.0.1:6379" || cfg.RecorderRedisPassword != strings.Repeat("r", 40) {
		t.Fatalf("redis = %q, %q", cfg.RecorderRedisAddr, cfg.RecorderRedisPassword)
	}
	if cfg.LiveKitAPIKey != "prodkey" || cfg.LiveKitAPISecret != strings.Repeat("a", 40) {
		t.Fatalf("media key %q", cfg.LiveKitAPIKey)
	}
	if got, want := cfg.Summary(), "signed in, media server embedded (node IP 127.0.0.1), recording on"; got != want {
		t.Fatalf("Summary = %q, want %q", got, want)
	}

	cfg = load(t, merge(signedIn, storage, recorder, map[string]string{
		"TIDE_S3_PUBLIC_ENDPOINT":   "https://files.example.com",
		"TIDE_S3_RECORDER_ENDPOINT": "http://minio:9000",
		"TIDE_RECORDER_REDIS_ADDR":  ":6379",
		"TIDE_TRANSCRIPTS":          "true",
	}))
	if cfg.S3PublicEndpoint != "https://files.example.com" || cfg.S3EgressEndpoint != "http://minio:9000" || cfg.RecorderRedisAddr != ":6379" {
		t.Fatalf("S3 views %q, %q, redis %q", cfg.S3PublicEndpoint, cfg.S3EgressEndpoint, cfg.RecorderRedisAddr)
	}
	if !cfg.Transcripts || !strings.HasSuffix(cfg.Summary(), "recording on, transcripts on") {
		t.Fatalf("transcripts %v, summary %q", cfg.Transcripts, cfg.Summary())
	}
}

// The recorder needs the key, the secret and the Redis password fixed, so
// they are no longer generated.
func TestLoadRecordingNeedsTheRecordersSecrets(t *testing.T) {
	message := loadErr(t, merge(signedIn, map[string]string{"TIDE_S3_ENDPOINT": "https://s3.example.com"}))
	for _, want := range []string{
		"TIDE_MEDIA_API_KEY must be set", "TIDE_MEDIA_API_SECRET must be set",
		"TIDE_RECORDER_REDIS_PASSWORD must be set", "TIDE_S3_ACCESS_KEY must be set",
		"TIDE_S3_SECRET_KEY must be set",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("error should say %q, got: %s", want, message)
		}
	}
	message = loadErr(t, merge(signedIn, storage, recorder, map[string]string{"TIDE_RECORDER_REDIS_PASSWORD": "short"}))
	if !strings.Contains(message, "TIDE_RECORDER_REDIS_PASSWORD must be at least 32 characters") {
		t.Fatalf("error = %s", message)
	}
}

func TestLoadRefusesContradictions(t *testing.T) {
	for _, test := range []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"storage without sign-in", storage,
			[]string{"TIDE_S3_ENDPOINT is set but sign-in is off: recording needs TIDE_OIDC_ISSUER"}},
		{"transcripts without sign-in", map[string]string{"TIDE_TRANSCRIPTS": "true"},
			[]string{"TIDE_TRANSCRIPTS=true needs recording"}},
		{"transcripts without storage", merge(signedIn, map[string]string{"TIDE_TRANSCRIPTS": "1"}),
			[]string{"TIDE_TRANSCRIPTS=true needs recording"}},
		{"external media without its public URL", merge(external, map[string]string{"TIDE_MEDIA_PUBLIC_URL": ""}),
			[]string{"TIDE_MEDIA_PUBLIC_URL must be set with an external media server"}},
		{"a node IP that isn't one", map[string]string{"TIDE_MEDIA_NODE_IP": "meet.example.com"},
			[]string{`TIDE_MEDIA_NODE_IP "meet.example.com" is not an IP address`}},
		{"ports out of range", map[string]string{"TIDE_MEDIA_TCP_PORT": "0", "TIDE_MEDIA_UDP_PORT": "70000"},
			[]string{`TIDE_MEDIA_TCP_PORT "0" is not a port`, `TIDE_MEDIA_UDP_PORT "70000" is not a port`}},
		{"a port that isn't a number", map[string]string{"TIDE_MEDIA_UDP_PORT": "udp"},
			[]string{`TIDE_MEDIA_UDP_PORT "udp" is not a port`}},
		{"the API and media sharing a TCP port", map[string]string{"TIDE_MEDIA_API_PORT": "7881"},
			[]string{"TIDE_MEDIA_API_PORT and TIDE_MEDIA_TCP_PORT are both 7881"}},
		{"an API port out of range", map[string]string{"TIDE_MEDIA_API_PORT": "-1"},
			[]string{`TIDE_MEDIA_API_PORT "-1" is not a port`}},
		{"a Redis address without a port", merge(signedIn, storage, recorder, map[string]string{"TIDE_RECORDER_REDIS_ADDR": "10.0.0.5"}),
			[]string{`TIDE_RECORDER_REDIS_ADDR "10.0.0.5" is not host:port`}},
		{"a Redis address with a name", merge(signedIn, storage, recorder, map[string]string{"TIDE_RECORDER_REDIS_ADDR": "redis:6379"}),
			[]string{`TIDE_RECORDER_REDIS_ADDR "redis:6379": the host must be an IP address`}},
		{"a base URL signaling can't follow", map[string]string{"TIDE_BASE_URL": "meet.example.com"},
			[]string{"TIDE_BASE_URL: \"meet.example.com\" is not an absolute http or https URL"}},
		{"a trusted proxy that isn't one", map[string]string{"TIDE_TRUSTED_PROXIES": "10.0.0.0/33"},
			[]string{"TIDE_TRUSTED_PROXIES: invalid CIDR"}},
		// Every problem at once, structure and secrets alike.
		{"several", merge(map[string]string{"TIDE_TRANSCRIPTS": "true", "TIDE_MEDIA_NODE_IP": "x"}, storage),
			[]string{"TIDE_S3_ENDPOINT is set but sign-in is off", "TIDE_TRANSCRIPTS=true needs recording", `TIDE_MEDIA_NODE_IP "x"`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := loadErr(t, test.env)
			for _, want := range test.want {
				if !strings.Contains(message, want) {
					t.Errorf("error should say %q, got: %s", want, message)
				}
			}
		})
	}
	// Dev mode supplies secrets; it doesn't make a contradiction consistent.
	message := loadErr(t, merge(storage, map[string]string{"TIDE_DEV_MODE": "true"}))
	if !strings.Contains(message, "recording needs TIDE_OIDC_ISSUER") || strings.Contains(message, "TIDE_DEV_MODE=true only") {
		t.Fatalf("error = %s", message)
	}
}

// With TIDE_MEDIA_URL, tide behaves as before 1.0: the embedded server's
// settings are unused, and the key and secret are required.
func TestLoadExternalMedia(t *testing.T) {
	cfg := load(t, merge(signedIn, storage, external, map[string]string{
		// Unused with an external media server, so not checked either.
		"TIDE_MEDIA_TCP_PORT": "not-a-port", "TIDE_MEDIA_NODE_IP": "nope", "TIDE_RECORDER_REDIS_ADDR": "nope",
	}))
	if cfg.MediaEmbedded || cfg.LiveKitURL != "ws://media:7880" || cfg.LiveKitPublicURL != "wss://media.example.com" {
		t.Fatalf("embedded %v, URLs %q %q", cfg.MediaEmbedded, cfg.LiveKitURL, cfg.LiveKitPublicURL)
	}
	if cfg.MediaTCPPort != 0 || cfg.MediaNodeIP != "" || cfg.RecorderRedisAddr != "" || cfg.RecorderRedisPassword != "" {
		t.Fatalf("embedded settings with an external server: %+v", cfg)
	}
	if got, want := cfg.Summary(), "signed in, media server external, recording on"; got != want {
		t.Fatalf("Summary = %q, want %q", got, want)
	}
	message := loadErr(t, map[string]string{"TIDE_MEDIA_URL": "ws://media:7880", "TIDE_MEDIA_PUBLIC_URL": "wss://media.example.com"})
	for _, want := range []string{"TIDE_MEDIA_API_KEY must be set", "TIDE_MEDIA_API_SECRET must be set"} {
		if !strings.Contains(message, want) {
			t.Errorf("error should say %q, got: %s", want, message)
		}
	}
	if strings.Contains(message, "TIDE_RECORDER_REDIS_PASSWORD") {
		t.Errorf("an external media server's Redis is the operator's: %s", message)
	}
}

func TestLoadRejectsDevDefaultsInProduction(t *testing.T) {
	for name, value := range map[string]string{
		"TIDE_SESSION_SECRET":          devSessionSecret,
		"TIDE_MEDIA_API_KEY":           devLiveKitAPIKey,
		"TIDE_MEDIA_API_SECRET":        devLiveKitSecret,
		"TIDE_OIDC_CLIENT_SECRET":      devOIDCSecret,
		"TIDE_S3_ACCESS_KEY":           devS3AccessKey,
		"TIDE_S3_SECRET_KEY":           devS3SecretKey,
		"TIDE_RECORDER_REDIS_PASSWORD": devRecorderRedisPassword,
	} {
		t.Run(name, func(t *testing.T) {
			message := loadErr(t, merge(signedIn, storage, recorder, map[string]string{name: value}))
			if !strings.Contains(message, name+" is a publicly-known dev default") ||
				!strings.HasSuffix(message, "(set TIDE_DEV_MODE=true only for local development)") {
				t.Fatalf("error = %s", message)
			}
		})
	}
	// An anonymous deployment checks the secrets it is given, too.
	message := loadErr(t, map[string]string{"TIDE_SESSION_SECRET": devSessionSecret})
	if !strings.Contains(message, "TIDE_SESSION_SECRET is a publicly-known dev default") {
		t.Fatalf("error = %s", message)
	}
}

func TestLoadRejectsShortSecrets(t *testing.T) {
	message := loadErr(t, merge(signedIn, map[string]string{"TIDE_SESSION_SECRET": "short"}))
	if !strings.Contains(message, "TIDE_SESSION_SECRET must be at least 32 characters") {
		t.Fatalf("error = %s", message)
	}
	message = loadErr(t, map[string]string{"TIDE_MEDIA_API_SECRET": "short"})
	if !strings.Contains(message, "TIDE_MEDIA_API_SECRET must be at least 32 characters") {
		t.Fatalf("error = %s", message)
	}
}

// Dev mode fills in the dev value of every secret, and turns nothing on.
func TestLoadDevModeSuppliesSecretsOnly(t *testing.T) {
	cfg := load(t, map[string]string{"TIDE_DEV_MODE": "true"})
	if !cfg.DevMode || !cfg.Anonymous || cfg.Recording || !cfg.MediaEmbedded {
		t.Fatalf("dev %v, anonymous %v, recording %v, embedded %v", cfg.DevMode, cfg.Anonymous, cfg.Recording, cfg.MediaEmbedded)
	}
	if cfg.OIDCIssuer != "" || cfg.S3Endpoint != "" {
		t.Fatalf("dev mode turned on sign-in %q or storage %q", cfg.OIDCIssuer, cfg.S3Endpoint)
	}
	if cfg.SessionSecret != devSessionSecret || cfg.LiveKitAPIKey != devLiveKitAPIKey || cfg.LiveKitAPISecret != devLiveKitSecret {
		t.Fatalf("expected the dev secrets, got %q %q", cfg.SessionSecret, cfg.LiveKitAPIKey)
	}

	cfg = load(t, map[string]string{
		"TIDE_DEV_MODE": "true", "TIDE_OIDC_ISSUER": "http://localhost:5556/dex",
		"TIDE_S3_ENDPOINT": "http://localhost:9000", "TIDE_S3_RECORDER_ENDPOINT": "http://minio:9000",
	})
	if !cfg.Recording || cfg.OIDCClientSecret != devOIDCSecret || cfg.S3AccessKey != devS3AccessKey ||
		cfg.S3SecretKey != devS3SecretKey || cfg.RecorderRedisPassword != devRecorderRedisPassword ||
		cfg.S3PublicEndpoint != "http://localhost:9000" || cfg.S3EgressEndpoint != "http://minio:9000" {
		t.Fatalf("dev stack config = %+v", cfg)
	}
}

func TestLoadParsesOIDCGroups(t *testing.T) {
	cfg := load(t, map[string]string{
		"TIDE_DEV_MODE":     "true",
		"TIDE_USER_GROUPS":  " tide-users,staff,tide-users ",
		"TIDE_ADMIN_GROUPS": "admins, tide-admins",
	})
	if got := strings.Join(cfg.UserGroups, ","); got != "tide-users,staff" {
		t.Fatalf("UserGroups = %q", got)
	}
	if got := strings.Join(cfg.AdminGroups, ","); got != "admins,tide-admins" {
		t.Fatalf("AdminGroups = %q", got)
	}
}

func TestLoadRateLimits(t *testing.T) {
	t.Run("defaults when unset", func(t *testing.T) {
		cfg := load(t, nil)
		if cfg.JoinRateLimit != DefaultJoinRateLimit ||
			cfg.WaitRateLimit != DefaultWaitRateLimit ||
			cfg.LoginRateLimit != DefaultLoginRateLimit ||
			cfg.PairRateLimit != DefaultPairRateLimit {
			t.Fatalf("expected published defaults, got %d/%d/%d/%d",
				cfg.JoinRateLimit, cfg.WaitRateLimit, cfg.LoginRateLimit, cfg.PairRateLimit)
		}
	})

	t.Run("overridden by the environment", func(t *testing.T) {
		cfg := load(t, map[string]string{"TIDE_JOIN_RATE_LIMIT": "5000", "TIDE_PAIR_RATE_LIMIT": "300"})
		if cfg.JoinRateLimit != 5000 || cfg.PairRateLimit != 300 {
			t.Fatalf("JoinRateLimit = %d, PairRateLimit = %d", cfg.JoinRateLimit, cfg.PairRateLimit)
		}
	})

	// A misconfigured ceiling must never become zero: that would deny every
	// request rather than fall back to the published default.
	for _, value := range []string{"0", "-1", "many"} {
		t.Run("rejects "+value, func(t *testing.T) {
			cfg := load(t, map[string]string{"TIDE_JOIN_RATE_LIMIT": value})
			if cfg.JoinRateLimit != DefaultJoinRateLimit {
				t.Fatalf("JoinRateLimit = %d, want default", cfg.JoinRateLimit)
			}
		})
	}
}

// Transcripts are off unless the operator turns them on, and a value
// strconv.ParseBool can't read means off rather than refusing to start.
func TestLoadTranscriptsSwitch(t *testing.T) {
	for _, test := range []struct {
		value string // "" leaves it unset
		want  bool
	}{
		{"", false}, {"true", true}, {"1", true}, {"TRUE", true},
		{"false", false}, {"0", false}, {"yes", false}, {"on", false},
	} {
		t.Run("TIDE_TRANSCRIPTS="+test.value, func(t *testing.T) {
			env := merge(signedIn, storage, recorder)
			if test.value != "" {
				env["TIDE_TRANSCRIPTS"] = test.value
			}
			if cfg := load(t, env); cfg.Transcripts != test.want {
				t.Fatalf("Transcripts = %v, want %v", cfg.Transcripts, test.want)
			}
		})
	}
}
