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
