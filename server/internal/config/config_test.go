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
		"KLISI_OIDC_CLIENT_SECRET", "KLISI_S3_ENDPOINT", "KLISI_S3_PUBLIC_ENDPOINT",
		"KLISI_S3_EGRESS_ENDPOINT", "KLISI_S3_BUCKET", "KLISI_S3_ACCESS_KEY",
		"KLISI_S3_SECRET_KEY", "KLISI_S3_REGION", "KLISI_EGRESS_TEMPLATE_URL",
		"KLISI_DEV_MODE",
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
