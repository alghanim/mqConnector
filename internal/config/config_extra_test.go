package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidate_RejectsEmptyCookieName(t *testing.T) {
	cfg := Default()
	cfg.Server.TLS.CertFile = "c"
	cfg.Server.TLS.KeyFile = "k"
	cfg.Auth.SimpleAuthURL = "https://x"
	cfg.Auth.CookieName = ""
	if err := cfg.Validate(); err == nil {
		t.Error("expected error on empty cookie_name")
	}
}

func TestValidate_RejectsZeroSessionTTL(t *testing.T) {
	cfg := Default()
	cfg.Server.TLS.CertFile = "c"
	cfg.Server.TLS.KeyFile = "k"
	cfg.Auth.SimpleAuthURL = "https://x"
	cfg.Auth.SessionTTL = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error on session_ttl = 0")
	}
}

func TestValidate_RejectsBadFormat(t *testing.T) {
	cfg := Default()
	cfg.Server.TLS.CertFile = "c"
	cfg.Server.TLS.KeyFile = "k"
	cfg.Auth.SimpleAuthURL = "https://x"
	cfg.Logging.Format = "xml"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error on logging.format = xml")
	}
}

func TestValidate_RejectsWorkers0(t *testing.T) {
	cfg := Default()
	cfg.Server.TLS.CertFile = "c"
	cfg.Server.TLS.KeyFile = "k"
	cfg.Auth.SimpleAuthURL = "https://x"
	cfg.Pipeline.WorkersPerPipeline = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error on workers_per_pipeline = 0")
	}
}

func TestValidate_RejectsMaxBody0(t *testing.T) {
	cfg := Default()
	cfg.Server.MaxBodyBytes = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error on max_body_bytes = 0")
	}
}

func TestEnsureDirs_CreatesStorageDir(t *testing.T) {
	tmp := t.TempDir()
	cfg := Default()
	cfg.Storage.DSN = "file:" + filepath.Join(tmp, "sub", "x.db") + "?_pragma=journal_mode(WAL)"
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	info, err := os.Stat(filepath.Join(tmp, "sub"))
	if err != nil || !info.IsDir() {
		t.Errorf("expected created dir, got err %v", err)
	}
}

func TestApplyEnv_NestedDurations(t *testing.T) {
	t.Setenv("SERVER_MODE", "dev")
	t.Setenv("AUTH_SESSION_TTL", "30m")
	t.Setenv("MQ_POOL_IDLE_TIMEOUT", "10m")
	t.Setenv("MQ_POOL_HEALTH_INTERVAL", "5s")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.SessionTTL != 30*time.Minute {
		t.Errorf("session_ttl override: %v", cfg.Auth.SessionTTL)
	}
	if cfg.MQ.Pool.IdleTimeout != 10*time.Minute {
		t.Errorf("idle_timeout override: %v", cfg.MQ.Pool.IdleTimeout)
	}
	if cfg.MQ.Pool.HealthInterval != 5*time.Second {
		t.Errorf("health_interval override: %v", cfg.MQ.Pool.HealthInterval)
	}
}

func TestApplyEnv_SliceFromCommaList(t *testing.T) {
	t.Setenv("SERVER_MODE", "dev")
	t.Setenv("SERVER_CORS_ORIGINS", "https://a.example, https://b.example")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Server.CORSOrigins) != 2 {
		t.Fatalf("expected 2 origins, got %v", cfg.Server.CORSOrigins)
	}
	if !strings.Contains(cfg.Server.CORSOrigins[0], "a.example") {
		t.Errorf("first origin wrong: %s", cfg.Server.CORSOrigins[0])
	}
}

func TestIsDev_CaseInsensitive(t *testing.T) {
	cfg := Default()
	cfg.Server.Mode = "DEV"
	if !cfg.IsDev() {
		t.Error("expected DEV to count as dev")
	}
}
