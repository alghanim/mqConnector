// Package config loads the application configuration from a YAML file with
// environment-variable overrides, and validates it before returning.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the full application configuration. Every field has a sane default
// applied by Load.
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Storage    StorageConfig    `yaml:"storage"`
	Auth       AuthConfig       `yaml:"auth"`
	Logging    LoggingConfig    `yaml:"logging"`
	Tracing    TracingConfig    `yaml:"tracing"`
	MQ         MQConfig         `yaml:"mq"`
	Pipeline   PipelineConfig   `yaml:"pipeline"`
	Script     ScriptConfig     `yaml:"script"`
	Leadership LeadershipConfig `yaml:"leadership"`
	Audit      AuditConfig      `yaml:"audit"`
}

// TracingConfig configures the OpenTelemetry OTLP/HTTP exporter. Empty
// OTLPEndpoint means tracing stays in the structured-log "span" form
// (one INFO line per Span.End()) without exporting anywhere.
type TracingConfig struct {
	// OTLPEndpoint is the OTLP/HTTP collector host:port. Standard
	// collectors listen on :4318. Empty disables OTLP export.
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	// ServiceName lands as service.name on every exported span.
	// Defaults to "mqconnector".
	ServiceName string `yaml:"service_name"`
	// Insecure switches the exporter to plaintext HTTP. TLS by default.
	Insecure bool `yaml:"insecure"`
	// SampleRatio is the head-based sample rate, 0 ≤ r ≤ 1. 0 means
	// "use the default" (1.0 — record every span). Once OTLP is
	// configured, sample-all matches operator expectations; dial down
	// for high-throughput pipelines.
	SampleRatio float64 `yaml:"sample_ratio"`
}

// AuditConfig controls the audit-log archival exporter. When ArchiveDir
// is non-empty AND MaxAge > 0, the audit sweeper streams rows older
// than MaxAge into per-day JSONL files under ArchiveDir, then deletes
// them from the live table.
type AuditConfig struct {
	ArchiveDir    string        `yaml:"archive_dir"`
	MaxAge        time.Duration `yaml:"max_age"`
	SweepInterval time.Duration `yaml:"sweep_interval"`
}

// LeadershipConfig controls the multi-replica safety lease. When Enabled
// is true, only the replica holding the lease starts pipeline workers;
// other replicas serve the admin UI but stay idle as standbys.
type LeadershipConfig struct {
	Enabled bool          `yaml:"enabled"`
	ID      string        `yaml:"id"` // empty → hostname
	TTL     time.Duration `yaml:"ttl"`
}

type ServerConfig struct {
	Listen       string        `yaml:"listen"`
	Mode         string        `yaml:"mode"` // "prod" (default) or "dev"
	MaxBodyBytes int64         `yaml:"max_body_bytes"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	IdleTimeout  time.Duration `yaml:"idle_timeout"`
	CORSOrigins  []string      `yaml:"cors_origins"`
	TLS          TLSConfig     `yaml:"tls"`
}

type TLSConfig struct {
	Enabled    bool   `yaml:"enabled"`
	CertFile   string `yaml:"cert_file"`
	KeyFile    string `yaml:"key_file"`
	MinVersion string `yaml:"min_version"`
}

type StorageConfig struct {
	DSN          string `yaml:"dsn"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
	// Backup runs an in-process snapshot worker that writes
	// VACUUM-INTO copies of the SQLite file at a fixed interval.
	// Optional; recommended on production deploys where there's no
	// external backup orchestrator. Empty Dir disables.
	Backup BackupConfig `yaml:"backup"`
}

// BackupConfig drives the in-process scheduled-backup worker. The
// worker runs only on the leader (so multi-replica deploys don't
// duplicate snapshots) and rotates older files past Keep.
type BackupConfig struct {
	// Dir is the destination directory for snapshots. Empty disables
	// the in-process worker entirely — operators using cron / a
	// sidecar to drive `mqconnector backup` should leave this off.
	Dir string `yaml:"dir"`
	// Interval is how often a snapshot runs. Defaults to 24h when
	// Dir is set and Interval is unset.
	Interval time.Duration `yaml:"interval"`
	// Keep is the number of recent snapshots to retain. Older files
	// matching mqconnector-*.db are deleted. Defaults to 7.
	Keep int `yaml:"keep"`
}

type AuthConfig struct {
	// SimpleAuthURL is the base URL of the SimpleAuth server that issues JWTs.
	SimpleAuthURL string `yaml:"simpleauth_url"`
	// AdminKey is the SimpleAuth admin API key, only required for admin-side
	// calls (e.g. listing users). May be empty for runtime auth.
	AdminKey string `yaml:"admin_key"`
	// InsecureSkipVerify disables TLS cert verification when calling SimpleAuth.
	// Only valid in dev mode.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
	// SessionTTL is the lifetime of the browser cookie that carries the JWT.
	// Should be ≤ the JWT's own expiry.
	SessionTTL time.Duration `yaml:"session_ttl"`
	// CookieName is the name of the session cookie.
	CookieName string `yaml:"cookie_name"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type MQConfig struct {
	Pool PoolConfig `yaml:"pool"`
}

type PoolConfig struct {
	IdleTimeout    time.Duration `yaml:"idle_timeout"`
	HealthInterval time.Duration `yaml:"health_interval"`
	IBMRecvBuffer  int           `yaml:"ibm_recv_buffer"`
}

type PipelineConfig struct {
	WorkersPerPipeline int       `yaml:"workers_per_pipeline"`
	DLQ                DLQConfig `yaml:"dlq"`
}

// DLQConfig holds DLQ retry + retention policy. A configurable retention
// keeps a long broker outage from filling the disk: entries older than
// MaxAge OR over MaxRows (whichever fires first) are pruned by the
// retention sweeper. Setting either limit to 0 disables that pruning rule.
type DLQConfig struct {
	MaxRetries    int           `yaml:"max_retries"`
	RetryBackoff  time.Duration `yaml:"retry_backoff"`
	MaxAge        time.Duration `yaml:"max_age"`
	MaxRows       int           `yaml:"max_rows"`
	SweepInterval time.Duration `yaml:"sweep_interval"`
}

type ScriptConfig struct {
	Timeout time.Duration `yaml:"timeout"`
}

// Default returns the configuration with all defaults applied. Used as the
// base before YAML and env overrides are applied.
func Default() Config {
	return Config{
		Server: ServerConfig{
			Listen:       "0.0.0.0:8443",
			Mode:         "prod",
			MaxBodyBytes: 10 * 1024 * 1024,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  120 * time.Second,
			TLS: TLSConfig{
				Enabled:    true,
				MinVersion: "1.2",
			},
		},
		Storage: StorageConfig{
			DSN:          "file:./data/mqconnector.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)",
			MaxOpenConns: 8,
			MaxIdleConns: 4,
		},
		Auth: AuthConfig{
			SimpleAuthURL: "",
			SessionTTL:    12 * time.Hour,
			CookieName:    "mqc_session",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		MQ: MQConfig{
			Pool: PoolConfig{
				IdleTimeout:    5 * time.Minute,
				HealthInterval: 30 * time.Second,
				IBMRecvBuffer:  4 * 1024 * 1024,
			},
		},
		Pipeline: PipelineConfig{
			WorkersPerPipeline: 1,
			DLQ: DLQConfig{
				MaxRetries:    3,
				RetryBackoff:  30 * time.Second,
				MaxAge:        30 * 24 * time.Hour, // 30 days
				MaxRows:       100000,
				SweepInterval: 10 * time.Minute,
			},
		},
		Script: ScriptConfig{
			Timeout: time.Second,
		},
		Leadership: LeadershipConfig{
			Enabled: false,
			ID:      "",
			TTL:     30 * time.Second,
		},
		Audit: AuditConfig{
			ArchiveDir:    "",                  // disabled by default
			MaxAge:        7 * 24 * time.Hour,  // archive rows older than a week
			SweepInterval: time.Hour,
		},
	}
}

// Load reads config from the given path (may be empty — defaults are then used)
// and applies environment-variable overrides. The result is validated before
// being returned.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return Config{}, fmt.Errorf("read config %s: %w", path, err)
			}
			// Missing file is fine; we proceed with defaults.
		} else {
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				return Config{}, fmt.Errorf("parse config %s: %w", path, err)
			}
		}
	}

	applyEnv(reflect.ValueOf(&cfg).Elem(), "")

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyEnv walks the config struct recursively. For each leaf field with a yaml
// tag, it constructs an env var name by joining ancestor yaml tags with `_` and
// uppercasing the result, and assigns the value if the env var is set.
func applyEnv(v reflect.Value, prefix string) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		envKey := name
		if prefix != "" {
			envKey = prefix + "_" + name
		}

		fv := v.Field(i)
		if fv.Kind() == reflect.Struct && field.Type != reflect.TypeOf(time.Duration(0)) {
			applyEnv(fv, envKey)
			continue
		}

		envVar := strings.ToUpper(envKey)
		raw, ok := os.LookupEnv(envVar)
		if !ok {
			continue
		}
		assign(fv, raw)
	}
}

func assign(fv reflect.Value, raw string) {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		if b, err := strconv.ParseBool(raw); err == nil {
			fv.SetBool(b)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if fv.Type() == reflect.TypeOf(time.Duration(0)) {
			if d, err := time.ParseDuration(raw); err == nil {
				fv.SetInt(int64(d))
			}
			return
		}
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			fv.SetInt(n)
		}
	case reflect.Slice:
		if fv.Type().Elem().Kind() == reflect.String {
			parts := strings.Split(raw, ",")
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					out = append(out, p)
				}
			}
			fv.Set(reflect.ValueOf(out))
		}
	}
}

// Validate enforces invariants that must hold before the app starts.
func (c Config) Validate() error {
	if c.Server.Listen == "" {
		return errors.New("server.listen is required")
	}
	if c.Server.MaxBodyBytes <= 0 {
		return errors.New("server.max_body_bytes must be > 0")
	}
	switch strings.ToLower(c.Server.Mode) {
	case "dev", "prod":
	default:
		return fmt.Errorf("server.mode must be dev or prod (got %q)", c.Server.Mode)
	}
	if !c.IsDev() {
		if !c.Server.TLS.Enabled {
			return errors.New("TLS is required in non-dev mode (set server.tls.enabled=true or server.mode=dev)")
		}
		if c.Server.TLS.CertFile == "" || c.Server.TLS.KeyFile == "" {
			return errors.New("server.tls.cert_file and server.tls.key_file are required in prod mode")
		}
	}
	if c.Storage.DSN == "" {
		return errors.New("storage.dsn is required")
	}
	if c.Auth.SessionTTL <= 0 {
		return errors.New("auth.session_ttl must be > 0")
	}
	if !c.IsDev() && c.Auth.SimpleAuthURL == "" {
		return errors.New("auth.simpleauth_url is required outside dev mode")
	}
	if !c.IsDev() && c.Auth.InsecureSkipVerify {
		return errors.New("auth.insecure_skip_verify is only allowed in dev mode")
	}
	if c.Auth.CookieName == "" {
		return errors.New("auth.cookie_name must not be empty")
	}
	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level must be one of debug|info|warn|error (got %q)", c.Logging.Level)
	}
	switch strings.ToLower(c.Logging.Format) {
	case "text", "json":
	default:
		return fmt.Errorf("logging.format must be text or json (got %q)", c.Logging.Format)
	}
	if c.Pipeline.WorkersPerPipeline < 1 {
		return errors.New("pipeline.workers_per_pipeline must be >= 1")
	}
	return nil
}

// IsDev reports whether the server is running in development mode.
func (c Config) IsDev() bool {
	return strings.EqualFold(c.Server.Mode, "dev")
}

// EnsureDirs creates parent directories for any file paths referenced by the
// config. This is safe to call repeatedly.
func (c Config) EnsureDirs() error {
	paths := []string{}
	// Storage DSN often starts with "file:./..." — extract just the path.
	if dsn := c.Storage.DSN; dsn != "" {
		path := dsn
		path = strings.TrimPrefix(path, "file:")
		if i := strings.Index(path, "?"); i >= 0 {
			path = path[:i]
		}
		if path != "" {
			paths = append(paths, filepath.Dir(path))
		}
	}
	for _, p := range paths {
		if p == "" || p == "." {
			continue
		}
		if err := os.MkdirAll(p, 0o750); err != nil {
			return fmt.Errorf("create dir %s: %w", p, err)
		}
	}
	return nil
}
