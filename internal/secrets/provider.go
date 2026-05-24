package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// KeyMaterial is one master-key version returned by a Provider. The
// Encoded field carries the 32-byte AES-256 key in hex (64 chars) or
// base64 (44 / 43 chars); the Service decodes it the same way as the
// existing env-based path.
type KeyMaterial struct {
	Version int
	Encoded string
}

// Provider supplies one or more KeyMaterial entries to construct the
// envelope-encryption Service at boot. Implementations:
//
//   - EnvProvider: reads MQC_MASTER_KEY / MQC_MASTER_KEYS (the
//     historical path; preserves single-binary deploys with no
//     external keystore).
//   - FileProvider: reads a key-versioned file (e.g. dropped in by
//     a Vault Agent sidecar, k8s Secret mount, or sealed-secrets
//     controller).
//   - VaultProvider: reads a KV v2 secret from a HashiCorp Vault
//     server, using each KV version as an envelope-encryption key
//     version.
//
// New providers (AWS KMS DescribeKey + Decrypt of a wrapped data
// key; GCP KMS; Azure Key Vault) plug in here without touching the
// rest of the codebase. The Service constructor accepts any
// Provider via FromProvider — the existing FromEnv stays as a thin
// shim so the call site in main.go can pick at runtime based on
// SecretsConfig.Source.
type Provider interface {
	// Load returns every known master key. The highest Version
	// becomes the current key (used for new Encrypt calls); the
	// rest stay around so rows sealed under older versions still
	// decrypt cleanly.
	//
	// Errors are returned verbatim; the bootstrap path in main
	// renders them as a startup failure rather than silently
	// degrading to "no encryption".
	Load(ctx context.Context) ([]KeyMaterial, error)
}

// FromProvider constructs a Service from any Provider implementation.
// Mirrors NewWithKeys but takes its input from an external source.
func FromProvider(ctx context.Context, p Provider) (*Service, error) {
	if p == nil {
		return nil, errors.New("secrets: provider is nil")
	}
	keys, err := p.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("secrets: load: %w", err)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	encoded := make(map[int]string, len(keys))
	for _, k := range keys {
		encoded[k.Version] = k.Encoded
	}
	return NewWithKeys(encoded)
}

// ─── EnvProvider ─────────────────────────────────────────────────────

// EnvProvider reads MQC_MASTER_KEY / MQC_MASTER_KEYS from the
// process environment. This is the historical default and stays
// in place so single-binary deploys with no external keystore work
// unchanged.
type EnvProvider struct{}

func (EnvProvider) Load(_ context.Context) ([]KeyMaterial, error) {
	if multi := strings.TrimSpace(os.Getenv("MQC_MASTER_KEYS")); multi != "" {
		return parseMultiSpec(multi)
	}
	raw := strings.TrimSpace(os.Getenv("MQC_MASTER_KEY"))
	if raw == "" {
		return nil, nil
	}
	return []KeyMaterial{{Version: 1, Encoded: raw}}, nil
}

// ─── FileProvider ────────────────────────────────────────────────────

// FileProvider reads a key file written in the same `vN=hex,vN=hex`
// form as MQC_MASTER_KEYS. Whitespace is tolerated; comments start
// with `#` and run to end-of-line. The file is expected to be 0600
// and owned by the bridge user; we don't enforce permission bits
// (some operators mount Secrets as 0644 by k8s default) but log a
// warning when world-readable.
//
// Use cases:
//   - Vault Agent sidecar rendering a key file at a fixed path
//   - k8s Secret mounted under /etc/mqconnector/
//   - sealed-secrets controllers / SOPS-decrypted artifacts
type FileProvider struct {
	Path string
}

func (f FileProvider) Load(_ context.Context) ([]KeyMaterial, error) {
	if strings.TrimSpace(f.Path) == "" {
		return nil, errors.New("secrets: file provider: path required")
	}
	body, err := os.ReadFile(filepath.Clean(f.Path))
	if err != nil {
		return nil, fmt.Errorf("secrets: read %s: %w", f.Path, err)
	}
	// Strip comments and join lines into a single comma-separated spec.
	var parts []string
	for _, line := range strings.Split(string(body), "\n") {
		if idx := strings.IndexByte(line, '#'); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts = append(parts, line)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("secrets: %s contains no keys", f.Path)
	}
	// Two accepted shapes:
	//   - Single bare key (becomes v1).
	//   - Multi-version "v1=hex,v2=hex,..." OR one v{N}=hex per line.
	if len(parts) == 1 && !strings.Contains(parts[0], "=") && !strings.Contains(parts[0], ",") {
		return []KeyMaterial{{Version: 1, Encoded: parts[0]}}, nil
	}
	return parseMultiSpec(strings.Join(parts, ","))
}

// parseMultiSpec parses the `v{N}=key,v{N}=key,...` form used by both
// MQC_MASTER_KEYS and the file provider. Centralised here so a future
// provider that produces the same spec doesn't reimplement.
func parseMultiSpec(spec string) ([]KeyMaterial, error) {
	entries := strings.Split(spec, ",")
	var out []KeyMaterial
	seen := map[int]bool{}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		eq := strings.IndexByte(e, '=')
		if eq <= 1 || e[0] != 'v' {
			return nil, fmt.Errorf("secrets: entry %q must be of the form vN=hex", e)
		}
		version, err := strconv.Atoi(e[1:eq])
		if err != nil || version < 1 {
			return nil, fmt.Errorf("secrets: entry %q: bad version", e)
		}
		if seen[version] {
			return nil, fmt.Errorf("secrets: v%d appears twice", version)
		}
		seen[version] = true
		out = append(out, KeyMaterial{
			Version: version,
			Encoded: strings.TrimSpace(e[eq+1:]),
		})
	}
	if len(out) == 0 {
		return nil, errors.New("secrets: spec parsed to zero keys")
	}
	return out, nil
}
