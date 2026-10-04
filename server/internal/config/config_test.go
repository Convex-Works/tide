package config

import (
	"os"
	"strings"
	"testing"
)

// clearKlisiEnv unsets every variable Load reads so tests are hermetic.
func clearKlisiEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"KLISI_ADDR", "KLISI_BASE_URL", "KLISI_SESSION_SECRET", "KLISI_DB_PATH",
		"KLISI_LIVEKIT_URL", "KLISI_LIVEKIT_PUBLIC_URL", "KLISI_LIVEKIT_API_KEY",
		"KLISI_LIVEKIT_API_SECRET", "KLISI_OIDC_ISSUER", "KLISI_OIDC_CLIENT_ID",
		"KLISI_OIDC_CLIENT_SECRET", "KLISI_USER_GROUPS", "KLISI_ADMIN_GROUPS",
		"KLISI_S3_ENDPOINT", "KLISI_S3_PUBLIC_ENDPOINT",
		"KLISI_S3_EGRESS_ENDPOINT", "KLISI_S3_BUCKET", "KLISI_S3_ACCESS_KEY",
		"KLISI_S3_SECRET_KEY", "KLISI_S3_REGION", "KLISI_EGRESS_TEMPLATE_URL",
		"KLISI_DEV_MODE", "KLISI_JOIN_RATE_LIMIT", "KLISI_WAIT_RATE_LIMIT",
		"KLISI_LOGIN_RATE_LIMIT", "KLISI_PAIR_RATE_LIMIT", "KLISI_TRANSCRIPTS",
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
	clearKlisiEnv(t)
	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to fail closed with no env set")
	}
	for _, want := range []string{"KLISI_SESSION_SECRET", "KLISI_LIVEKIT_API_SECRET", "KLISI_OIDC_CLIENT_SECRET", "KLISI_S3_SECRET_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s, got: %v", want, err)
		}
	}
}

func TestLoadRejectsDevDefaultsInProduction(t *testing.T) {
	clearKlisiEnv(t)
	t.Setenv("KLISI_SESSION_SECRET", devSessionSecret)
	t.Setenv("KLISI_LIVEKIT_API_KEY", "prodkey")
	t.Setenv("KLISI_LIVEKIT_API_SECRET", strings.Repeat("a", 40))
	t.Setenv("KLISI_OIDC_CLIENT_SECRET", strings.Repeat("b", 20))
	t.Setenv("KLISI_S3_ACCESS_KEY", "prod-access")
	t.Setenv("KLISI_S3_SECRET_KEY", strings.Repeat("c", 20))
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "publicly-known dev default") {
		t.Fatalf("expected dev-default rejection, got: %v", err)
	}
}

func TestLoadRejectsShortSecrets(t *testing.T) {
	clearKlisiEnv(t)
	t.Setenv("KLISI_SESSION_SECRET", "short")
	t.Setenv("KLISI_LIVEKIT_API_KEY", "prodkey")
	t.Setenv("KLISI_LIVEKIT_API_SECRET", strings.Repeat("a", 40))
	t.Setenv("KLISI_OIDC_CLIENT_SECRET", strings.Repeat("b", 20))
	t.Setenv("KLISI_S3_ACCESS_KEY", "prod-access")
	t.Setenv("KLISI_S3_SECRET_KEY", strings.Repeat("c", 20))
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "at least 32 characters") {
		t.Fatalf("expected short-secret rejection, got: %v", err)
	}
}

func TestLoadAcceptsStrongProductionSecrets(t *testing.T) {
	clearKlisiEnv(t)
	t.Setenv("KLISI_SESSION_SECRET", strings.Repeat("s", 40))
	t.Setenv("KLISI_LIVEKIT_API_KEY", "prodkey")
	t.Setenv("KLISI_LIVEKIT_API_SECRET", strings.Repeat("a", 40))
	t.Setenv("KLISI_OIDC_CLIENT_SECRET", strings.Repeat("b", 20))
	t.Setenv("KLISI_S3_ACCESS_KEY", "prod-access")
	t.Setenv("KLISI_S3_SECRET_KEY", strings.Repeat("c", 20))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected valid production config, got: %v", err)
	}
	if cfg.DevMode {
		t.Fatal("dev mode must default to false")
	}
}

func TestLoadDevModeAppliesDevDefaults(t *testing.T) {
	clearKlisiEnv(t)
	t.Setenv("KLISI_DEV_MODE", "true")
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
	clearKlisiEnv(t)
	t.Setenv("KLISI_DEV_MODE", "true")
	t.Setenv("KLISI_USER_GROUPS", " klisi-users,staff,klisi-users ")
	t.Setenv("KLISI_ADMIN_GROUPS", "admins, klisi-admins")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.UserGroups, ","); got != "klisi-users,staff" {
		t.Fatalf("UserGroups = %q", got)
	}
	if got := strings.Join(cfg.AdminGroups, ","); got != "admins,klisi-admins" {
		t.Fatalf("AdminGroups = %q", got)
	}
}

func TestLoadRateLimits(t *testing.T) {
	t.Run("defaults when unset", func(t *testing.T) {
		clearKlisiEnv(t)
		t.Setenv("KLISI_DEV_MODE", "true")
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
		clearKlisiEnv(t)
		t.Setenv("KLISI_DEV_MODE", "true")
		t.Setenv("KLISI_JOIN_RATE_LIMIT", "5000")
		t.Setenv("KLISI_PAIR_RATE_LIMIT", "300")
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
			clearKlisiEnv(t)
			t.Setenv("KLISI_DEV_MODE", "true")
			t.Setenv("KLISI_JOIN_RATE_LIMIT", value)
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
		t.Run("KLISI_TRANSCRIPTS="+test.value, func(t *testing.T) {
			clearKlisiEnv(t)
			t.Setenv("KLISI_DEV_MODE", "true")
			if test.value != "" {
				t.Setenv("KLISI_TRANSCRIPTS", test.value)
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
