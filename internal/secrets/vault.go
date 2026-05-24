package secrets

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// VaultProvider reads master keys from a HashiCorp Vault KV v2 secret.
//
// The secret at <Mount>/data/<Path> is expected to carry one or more
// data fields named "v1", "v2", ... each holding a 32-byte AES key
// encoded as hex or base64. Example:
//
//	vault kv put secret/mqconnector/master \
//	    v1=$(openssl rand -hex 32) \
//	    v2=$(openssl rand -hex 32)
//
// The KV v2 engine's own version metadata is independent of the
// in-row v1/v2/... labelling. We use the per-row labels because they
// give the operator explicit control over which version is current
// (the highest v{N} present) and let the bridge keep decrypting rows
// sealed under older keys after a rotation — even if Vault retains
// only the latest secret version internally.
//
// Why not transit-engine encrypt/decrypt? Round-tripping every value
// through Vault on every read would multiply hot-path latency. The
// envelope model — Vault holds the master key, bridge holds it in
// memory — gives the same custody guarantee with no per-message
// network call. Vault rotation is honoured by reloading the provider
// (POST /api/v1/secrets/rotate on the bridge re-pulls).
type VaultProvider struct {
	// Address is the Vault server, e.g. https://vault.local:8200.
	Address string
	// Token is the bridge's Vault token. Typically wrapped by a Vault
	// Agent sidecar or provided via VAULT_TOKEN. Required.
	Token string
	// Namespace is the optional Vault Enterprise namespace header.
	Namespace string
	// Mount is the KV v2 mount path, e.g. "secret" or "kv-mqconnector".
	// No leading slash.
	Mount string
	// Path is the KV v2 secret path, relative to Mount.
	Path string
	// CAFile is an optional PEM-encoded CA bundle pinning the Vault
	// TLS cert. Empty falls back to system roots.
	CAFile string
	// InsecureSkipVerify disables TLS verification. Dev only — the
	// loader logs a warning when set.
	InsecureSkipVerify bool
	// Timeout caps the HTTP round trip. Default 10s.
	Timeout time.Duration
	// HTTP override (tests). When nil, Load builds one from CAFile +
	// InsecureSkipVerify on every call.
	HTTP *http.Client
}

func (v *VaultProvider) Load(ctx context.Context) ([]KeyMaterial, error) {
	if strings.TrimSpace(v.Address) == "" {
		return nil, errors.New("secrets: vault: address required")
	}
	if strings.TrimSpace(v.Token) == "" {
		return nil, errors.New("secrets: vault: token required")
	}
	if strings.TrimSpace(v.Mount) == "" || strings.TrimSpace(v.Path) == "" {
		return nil, errors.New("secrets: vault: mount and path required")
	}

	client := v.HTTP
	if client == nil {
		c, err := v.buildHTTP()
		if err != nil {
			return nil, err
		}
		client = c
	}

	url := fmt.Sprintf("%s/v1/%s/data/%s",
		strings.TrimRight(v.Address, "/"),
		strings.Trim(v.Mount, "/"),
		strings.Trim(v.Path, "/"))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: vault: build request: %w", err)
	}
	req.Header.Set("X-Vault-Token", v.Token)
	if v.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", v.Namespace)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("secrets: vault: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("secrets: vault: read body: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("secrets: vault: secret not found at %s/%s", v.Mount, v.Path)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("secrets: vault: %s: %s",
			resp.Status, strings.TrimSpace(string(body)))
	}

	// KV v2 response shape:
	// { "data": { "data": { "v1": "...", "v2": "..." }, "metadata": {...} } }
	var envelope struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("secrets: vault: parse: %w", err)
	}
	if len(envelope.Data.Data) == 0 {
		return nil, fmt.Errorf("secrets: vault: secret %s/%s has no fields", v.Mount, v.Path)
	}

	var out []KeyMaterial
	for label, encoded := range envelope.Data.Data {
		if !strings.HasPrefix(label, "v") {
			// Tolerate unrelated metadata fields on the secret; only
			// v{N} entries are treated as key material.
			continue
		}
		version, err := strconv.Atoi(label[1:])
		if err != nil || version < 1 {
			return nil, fmt.Errorf("secrets: vault: bad version label %q on %s/%s",
				label, v.Mount, v.Path)
		}
		out = append(out, KeyMaterial{Version: version, Encoded: encoded})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("secrets: vault: secret %s/%s carries no v{N} fields", v.Mount, v.Path)
	}
	return out, nil
}

func (v *VaultProvider) buildHTTP() (*http.Client, error) {
	timeout := v.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: v.InsecureSkipVerify,
	}
	if v.CAFile != "" {
		pem, err := os.ReadFile(v.CAFile)
		if err != nil {
			return nil, fmt.Errorf("secrets: vault: read ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("secrets: vault: ca file %s has no PEM certs", v.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}, nil
}
