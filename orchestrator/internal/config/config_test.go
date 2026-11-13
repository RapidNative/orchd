package config

import (
	"os"
	"testing"
)

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

func TestImageMinMemoryParsing(t *testing.T) {
	t.Setenv("ORCHD_IMAGE_MIN_MEMORY_MB", "rn-run:dev=2048, rn-web:dev=abc, x=0")
	cfg := Load()
	if len(cfg.ImageMinMemoryMB) != 1 || cfg.ImageMinMemoryMB["rn-run:dev"] != 2048 {
		t.Fatalf("ImageMinMemoryMB = %v, want only rn-run:dev=2048", cfg.ImageMinMemoryMB)
	}
}

// A provider key in ORCHD_TINBASE_ENV alongside a configured relay would ride
// unused into every tinbase container. That is refused at boot, where the
// operator is looking, rather than discovered in a container inspect later.
func TestValidateRefusesResendKeyWithRelay(t *testing.T) {
	t.Setenv("ORCHD_ADOPTER_CONFIG", rapidNativeAdopterFile) // names the services key the relay reads
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

// The same refusal for SMS: a Twilio credential in ORCHD_TINBASE_ENV next to a
// configured gateway would override the per-project auth token in every
// container with one shared provider secret.
func TestValidateRefusesTwilioTokenWithGateway(t *testing.T) {
	t.Setenv("ORCHD_ADOPTER_CONFIG", rapidNativeAdopterFile) // names the services key the relay reads
	t.Setenv("ORCHD_TINBASE_SMS_API_URL", "https://services.example.com/twilio")
	t.Setenv("ORCHD_TINBASE_ENV", "TINBASE_SMS_TWILIO_AUTH_TOKEN=tok")
	if err := Load().Validate(); err == nil {
		t.Fatal("expected Validate to refuse a Twilio token next to the gateway")
	}
	t.Setenv("ORCHD_TINBASE_ENV", "TINBASE_SMS_TWILIO_ACCOUNT_SID=ACx")
	if err := Load().Validate(); err == nil {
		t.Fatal("expected Validate to refuse a Twilio account sid next to the gateway")
	}

	// Without the gateway, a deployment pointing every tenant at one Twilio
	// account directly is its own decision.
	t.Setenv("ORCHD_TINBASE_SMS_API_URL", "")
	if err := Load().Validate(); err != nil {
		t.Fatalf("no gateway: direct Twilio must be allowed, got %v", err)
	}

	// The gateway alone is the intended shape.
	t.Setenv("ORCHD_TINBASE_SMS_API_URL", "https://services.example.com/twilio")
	t.Setenv("ORCHD_TINBASE_ENV", "")
	if err := Load().Validate(); err != nil {
		t.Fatalf("gateway without a token must be allowed, got %v", err)
	}
}

// The override of the webhook's website for traffic reporting.
func TestWebsiteReporting(t *testing.T) {
	t.Setenv("ORCHD_WEBSITE_URL", "")
	t.Setenv("ORCHD_WEBSITE_INTERNAL_TOKEN", "")
	t.Setenv("WEBSITE_URL", "https://www.rapidnative.com/")
	t.Setenv("WEBSITE_INTERNAL_TOKEN", "shared")
	c := Load()
	if c.WebsiteURL != "https://www.rapidnative.com" || c.WebsiteToken != "shared" {
		t.Errorf("shared producer env: %q %q", c.WebsiteURL, c.WebsiteToken)
	}
	if c.UsageInterval.Seconds() != 60 || c.UsageBuffer.Hours() != 1 {
		t.Errorf("defaults: interval %s buffer %s", c.UsageInterval, c.UsageBuffer)
	}

	t.Setenv("ORCHD_WEBSITE_URL", "http://localhost:3000")
	t.Setenv("ORCHD_WEBSITE_INTERNAL_TOKEN", "own")
	if c := Load(); c.WebsiteURL != "http://localhost:3000" || c.WebsiteToken != "own" {
		t.Errorf("ORCHD_ env must win: %q %q", c.WebsiteURL, c.WebsiteToken)
	}
}

const rapidNativeAdopterFile = "../../../deploy/adopters/rapidnative/adopter.json"

// A relay or gateway needs the adopter to name the project env variable that
// carries a project's services key; without it the relay would never apply.
func TestValidateRelayNeedsServicesKeyEnv(t *testing.T) {
	t.Setenv("ORCHD_ADOPTER_CONFIG", "")
	t.Setenv("ORCHD_TINBASE_SMTP_HOST", "services.example.com")
	if err := Load().Validate(); err == nil {
		t.Fatal("expected Validate to refuse a relay with no servicesKeyEnv")
	}
	t.Setenv("ORCHD_TINBASE_SMTP_HOST", "")
	t.Setenv("ORCHD_TINBASE_SMS_API_URL", "https://services.example.com/twilio")
	if err := Load().Validate(); err == nil {
		t.Fatal("expected Validate to refuse an SMS gateway with no servicesKeyEnv")
	}
	t.Setenv("ORCHD_TINBASE_SMS_API_URL", "")
	if err := Load().Validate(); err != nil {
		t.Fatalf("neutral engine without relays must load, got %v", err)
	}
}

// A bad adopter file stops the boot instead of dropping settings quietly.
func TestValidateRejectsBadAdopterFile(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/adopter.json"
	if err := os.WriteFile(p, []byte(`{"presetz": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORCHD_ADOPTER_CONFIG", p)
	if err := Load().Validate(); err == nil {
		t.Fatal("expected an unknown key in the adopter file to be refused")
	}
	t.Setenv("ORCHD_ADOPTER_CONFIG", dir+"/missing.json")
	if err := Load().Validate(); err == nil {
		t.Fatal("expected a missing adopter file to be refused")
	}
	t.Setenv("ORCHD_ADOPTER_CONFIG", rapidNativeAdopterFile)
	c := Load()
	if err := c.Validate(); err != nil {
		t.Fatalf("RapidNative's adopter file must load: %v", err)
	}
	if c.Adopter.AppType() != "rapidnative-dev" || len(c.Adopter.Presets) != 3 || len(c.Adopter.ProcessRecipes) != 3 {
		t.Fatalf("RapidNative adopter = %+v", c.Adopter)
	}
}
