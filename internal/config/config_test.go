package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault_PassesValidation(t *testing.T) {
	cfg := Default()
	cfg.Server.TLS.CertFile = "cert"
	cfg.Server.TLS.KeyFile = "key"
	cfg.Auth.SimpleAuthURL = "https://auth.internal"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should validate: %v", err)
	}
}

func TestValidate_RequiresSimpleAuthInProd(t *testing.T) {
	cfg := Default()
	cfg.Server.TLS.CertFile = "c"
	cfg.Server.TLS.KeyFile = "k"
	cfg.Auth.SimpleAuthURL = ""
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when simpleauth_url missing in prod")
	}
}

func TestValidate_RejectsInsecureSkipInProd(t *testing.T) {
	cfg := Default()
	cfg.Server.TLS.CertFile = "c"
	cfg.Server.TLS.KeyFile = "k"
	cfg.Auth.SimpleAuthURL = "https://auth.internal"
	cfg.Auth.InsecureSkipVerify = true
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when insecure_skip_verify=true in prod")
	}
}

func TestValidate_RejectsMissingTLS(t *testing.T) {
	cfg := Default()
	cfg.Server.TLS.Enabled = false
	cfg.Server.Mode = "prod"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when TLS disabled outside dev mode")
	}
}

func TestValidate_AllowsTLSOffInDev(t *testing.T) {
	cfg := Default()
	cfg.Server.TLS.Enabled = false
	cfg.Server.Mode = "dev"
	if err := cfg.Validate(); err != nil {
		t.Errorf("dev mode should allow TLS off: %v", err)
	}
}

func TestValidate_RejectsBadLogLevel(t *testing.T) {
	cfg := Default()
	cfg.Server.TLS.CertFile = "c"
	cfg.Server.TLS.KeyFile = "k"
	cfg.Logging.Level = "verbose"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error on bogus log level")
	}
}

func TestLoad_AppliesEnvOverrides(t *testing.T) {
	t.Setenv("SERVER_LISTEN", "127.0.0.1:9000")
	t.Setenv("SERVER_MODE", "dev")
	t.Setenv("LOGGING_LEVEL", "debug")
	t.Setenv("PIPELINE_DLQ_MAX_RETRIES", "10")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:9000" {
		t.Errorf("listen override not applied: %s", cfg.Server.Listen)
	}
	if !cfg.IsDev() {
		t.Error("mode override not applied")
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("logging.level override: %s", cfg.Logging.Level)
	}
	if cfg.Pipeline.DLQ.MaxRetries != 10 {
		t.Errorf("pipeline.dlq.max_retries override: %d", cfg.Pipeline.DLQ.MaxRetries)
	}
}

func TestLoad_ParsesYAML(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	yaml := `
server:
  mode: dev
  listen: "127.0.0.1:7777"
  tls:
    enabled: false
logging:
  level: warn
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:7777" {
		t.Errorf("yaml listen: %s", cfg.Server.Listen)
	}
	if cfg.Logging.Level != "warn" {
		t.Errorf("yaml log level: %s", cfg.Logging.Level)
	}
}

func TestLoad_MissingFile_UsesDefaults(t *testing.T) {
	// Prod defaults intentionally require an operator-supplied cert/key and a
	// SimpleAuth URL. Flip to dev mode so validation can succeed against pure
	// defaults — this asserts only that defaults are *loaded*, not that they
	// are usable in prod without further configuration.
	t.Setenv("SERVER_MODE", "dev")
	cfg, err := Load("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("missing file should fall back to defaults, got: %v", err)
	}
	if cfg.Server.Listen != "0.0.0.0:8443" {
		t.Errorf("defaults not applied: %s", cfg.Server.Listen)
	}
}
