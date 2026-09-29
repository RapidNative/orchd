package config

import "testing"

func TestLocalModeDefaults(t *testing.T) {
	t.Setenv("ORCHD_LOCAL", "1")
	c := Load()
	if c.BaseDomain != "localhost" {
		t.Errorf("BaseDomain = %q, want localhost", c.BaseDomain)
	}
	if c.PublicScheme != "http" {
		t.Errorf("PublicScheme = %q, want http", c.PublicScheme)
	}
	if c.PublicURL != "http://localhost:8081" {
		t.Errorf("PublicURL = %q, want http://localhost:8081", c.PublicURL)
	}
}

func TestLocalModeRespectsExplicitOverrides(t *testing.T) {
	t.Setenv("ORCHD_LOCAL", "1")
	t.Setenv("ORCHD_BASE_DOMAIN", "example.com")
	t.Setenv("ORCHD_PUBLIC_URL", "https://cloud.example.com")
	c := Load()
	if c.BaseDomain != "example.com" {
		t.Errorf("explicit BaseDomain not honored: %q", c.BaseDomain)
	}
	if c.PublicURL != "https://cloud.example.com" {
		t.Errorf("explicit PublicURL not honored: %q", c.PublicURL)
	}
}

func TestDefaultsWithoutLocalMode(t *testing.T) {
	// (t.Setenv is unset automatically; ensure ORCHD_LOCAL isn't inherited.)
	t.Setenv("ORCHD_LOCAL", "")
	c := Load()
	if c.BaseDomain != "lvh.me" {
		t.Errorf("BaseDomain = %q, want lvh.me", c.BaseDomain)
	}
	if c.PublicURL != "" {
		t.Errorf("PublicURL = %q, want empty", c.PublicURL)
	}
}

// ORCHD_WORKLOAD_ENV / ORCHD_BUILD_ENV parse "K=V,K2=V2"; malformed pairs are
// skipped so a typo degrades to a missing key rather than a broken boot.
func TestEnvMapParsing(t *testing.T) {
	t.Setenv("ORCHD_WORKLOAD_ENV", "npm_config_registry=http://172.17.0.1:4873, NPM_CONFIG_REGISTRY=http://172.17.0.1:4873 ,broken,=novalue")
	t.Setenv("ORCHD_BUILD_ENV", "")
	cfg := Load()
	if got := cfg.WorkloadEnv["npm_config_registry"]; got != "http://172.17.0.1:4873" {
		t.Fatalf("npm_config_registry = %q", got)
	}
	if got := cfg.WorkloadEnv["NPM_CONFIG_REGISTRY"]; got != "http://172.17.0.1:4873" {
		t.Fatalf("NPM_CONFIG_REGISTRY = %q (whitespace not trimmed?)", got)
	}
	if len(cfg.WorkloadEnv) != 2 {
		t.Fatalf("malformed pairs must be skipped, got %v", cfg.WorkloadEnv)
	}
	if cfg.BuildEnv != nil {
		t.Fatalf("empty ORCHD_BUILD_ENV must yield nil, got %v", cfg.BuildEnv)
	}
}

// A provider key in ORCHD_TINBASE_ENV alongside a configured relay would ride
// unused into every tinbase container. That is refused at boot, where the
// operator is looking, rather than discovered in a container inspect later.
func TestValidateRefusesResendKeyWithRelay(t *testing.T) {
	t.Setenv("ORCHD_TINBASE_SMTP_HOST", "services.example.com")
	t.Setenv("ORCHD_TINBASE_ENV", "TINBASE_RESEND_API_KEY=re_x,TINBASE_MAIL_FROM=App <a@example.com>")
	if err := Load().Validate(); err == nil {
		t.Fatal("expected Validate to refuse a Resend key next to a relay")
	}

	// Without the relay, a deployment sending directly is legitimate.
	t.Setenv("ORCHD_TINBASE_SMTP_HOST", "")
	if err := Load().Validate(); err != nil {
		t.Fatalf("no relay: direct sending must be allowed, got %v", err)
	}

	// The relay alone, with nothing extra for tinbase, is the intended shape.
	t.Setenv("ORCHD_TINBASE_SMTP_HOST", "services.example.com")
	t.Setenv("ORCHD_TINBASE_ENV", "")
	if err := Load().Validate(); err != nil {
		t.Fatalf("relay without a key must be allowed, got %v", err)
	}
}
