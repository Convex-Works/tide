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
		"TIDE_LIVEKIT_URL", "TIDE_LIVEKIT_PUBLIC_URL", "TIDE_LIVEKIT_API_KEY",
		"TIDE_LIVEKIT_API_SECRET", "TIDE_OIDC_ISSUER", "TIDE_OIDC_CLIENT_ID",
		"TIDE_OIDC_CLIENT_SECRET", "TIDE_USER_GROUPS", "TIDE_ADMIN_GROUPS",
		"TIDE_S3_ENDPOINT", "TIDE_S3_PUBLIC_ENDPOINT",
		"TIDE_S3_EGRESS_ENDPOINT", "TIDE_S3_BUCKET", "TIDE_S3_ACCESS_KEY",
		"TIDE_S3_SECRET_KEY", "TIDE_S3_REGION", "TIDE_EGRESS_TEMPLATE_URL",
		"TIDE_DEV_MODE", "TIDE_JOIN_RATE_LIMIT", "TIDE_WAIT_RATE_LIMIT",
		"TIDE_LOGIN_RATE_LIMIT", "TIDE_PAIR_RATE_LIMIT", "TIDE_TRANSCRIPTS",
	} {
		// t.Setenv registers restoration of the original value; the explicit
		// Unsetenv afterwards gives LookupEnv-miss semantics during the test.
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadRefusesToStartWithoutSecrets(t *testing.T) {
	clearTideEnv(t)
	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to fail closed with no env set")
	}
	for _, want := range []string{"TIDE_SESSION_SECRET", "TIDE_LIVEKIT_API_SECRET", "TIDE_OIDC_CLIENT_SECRET", "TIDE_S3_SECRET_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s, got: %v", want, err)
		}
	}
}

func TestLoadRejectsDevDefaultsInProduction(t *testing.T) {
	clearTideEnv(t)
	t.Setenv("TIDE_SESSION_SECRET", devSessionSecret)
	t.Setenv("TIDE_LIVEKIT_API_KEY", "prodkey")
	t.Setenv("TIDE_LIVEKIT_API_SECRET", strings.Repeat("a", 40))
	t.Setenv("TIDE_OIDC_CLIENT_SECRET", strings.Repeat("b", 20))
	t.Setenv("TIDE_S3_ACCESS_KEY", "prod-access")
	t.Setenv("TIDE_S3_SECRET_KEY", strings.Repeat("c", 20))
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "publicly-known dev default") {
		t.Fatalf("expected dev-default rejection, got: %v", err)
	}
}

func TestLoadRejectsShortSecrets(t *testing.T) {
	clearTideEnv(t)
	t.Setenv("TIDE_SESSION_SECRET", "short")
	t.Setenv("TIDE_LIVEKIT_API_KEY", "prodkey")
	t.Setenv("TIDE_LIVEKIT_API_SECRET", strings.Repeat("a", 40))
	t.Setenv("TIDE_OIDC_CLIENT_SECRET", strings.Repeat("b", 20))
	t.Setenv("TIDE_S3_ACCESS_KEY", "prod-access")
	t.Setenv("TIDE_S3_SECRET_KEY", strings.Repeat("c", 20))
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "at least 32 characters") {
		t.Fatalf("expected short-secret rejection, got: %v", err)
	}
}

func TestLoadAcceptsStrongProductionSecrets(t *testing.T) {
	clearTideEnv(t)
	t.Setenv("TIDE_SESSION_SECRET", strings.Repeat("s", 40))
	t.Setenv("TIDE_LIVEKIT_API_KEY", "prodkey")
	t.Setenv("TIDE_LIVEKIT_API_SECRET", strings.Repeat("a", 40))
	t.Setenv("TIDE_OIDC_CLIENT_SECRET", strings.Repeat("b", 20))
	t.Setenv("TIDE_S3_ACCESS_KEY", "prod-access")
	t.Setenv("TIDE_S3_SECRET_KEY", strings.Repeat("c", 20))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected valid production config, got: %v", err)
	}
	if cfg.DevMode {
		t.Fatal("dev mode must default to false")
	}
}

func TestLoadDevModeAppliesDevDefaults(t *testing.T) {
	clearTideEnv(t)
	t.Setenv("TIDE_DEV_MODE", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("dev mode should load with defaults, got: %v", err)
	}
	if !cfg.DevMode {
		t.Fatal("expected DevMode true")
	}
	if cfg.SessionSecret != devSessionSecret {
		t.Fatalf("expected dev session secret fallback, got %q", cfg.SessionSecret)
	}
}

func TestLoadParsesOIDCGroups(t *testing.T) {
	clearTideEnv(t)
	t.Setenv("TIDE_DEV_MODE", "true")
	t.Setenv("TIDE_USER_GROUPS", " tide-users,staff,tide-users ")
	t.Setenv("TIDE_ADMIN_GROUPS", "admins, tide-admins")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.UserGroups, ","); got != "tide-users,staff" {
		t.Fatalf("UserGroups = %q", got)
	}
	if got := strings.Join(cfg.AdminGroups, ","); got != "admins,tide-admins" {
		t.Fatalf("AdminGroups = %q", got)
	}
}

func TestLoadRateLimits(t *testing.T) {
	t.Run("defaults when unset", func(t *testing.T) {
		clearTideEnv(t)
		t.Setenv("TIDE_DEV_MODE", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.JoinRateLimit != DefaultJoinRateLimit ||
			cfg.WaitRateLimit != DefaultWaitRateLimit ||
			cfg.LoginRateLimit != DefaultLoginRateLimit ||
			cfg.PairRateLimit != DefaultPairRateLimit {
			t.Fatalf("expected published defaults, got %d/%d/%d/%d",
				cfg.JoinRateLimit, cfg.WaitRateLimit, cfg.LoginRateLimit, cfg.PairRateLimit)
		}
	})

	t.Run("overridden by the environment", func(t *testing.T) {
		clearTideEnv(t)
		t.Setenv("TIDE_DEV_MODE", "true")
		t.Setenv("TIDE_JOIN_RATE_LIMIT", "5000")
		t.Setenv("TIDE_PAIR_RATE_LIMIT", "300")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.JoinRateLimit != 5000 || cfg.PairRateLimit != 300 {
			t.Fatalf("JoinRateLimit = %d, PairRateLimit = %d", cfg.JoinRateLimit, cfg.PairRateLimit)
		}
	})

	// A misconfigured ceiling must never become zero: that would deny every
	// request rather than fall back to the published default.
	for _, value := range []string{"0", "-1", "many"} {
		t.Run("rejects "+value, func(t *testing.T) {
			clearTideEnv(t)
			t.Setenv("TIDE_DEV_MODE", "true")
			t.Setenv("TIDE_JOIN_RATE_LIMIT", value)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
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
			clearTideEnv(t)
			t.Setenv("TIDE_DEV_MODE", "true")
			if test.value != "" {
				t.Setenv("TIDE_TRANSCRIPTS", test.value)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Transcripts != test.want {
				t.Fatalf("Transcripts = %v, want %v", cfg.Transcripts, test.want)
			}
		})
	}
}
