// GitOps sync — declarative configuration management for mqConnector.
//
// Operators store pipeline / connection / schema / script definitions
// as YAML files in a Git repo. This subcommand reads those files,
// computes the canonical bundle the running server would accept on
// /api/v1/config/import, optionally diffs against the server's current
// state via /api/v1/config/export, and applies the desired state.
//
// Invocation:
//
//	mqconnector gitops --dir=./config --url=https://mqc.svc \
//	    --token=$MQC_API_TOKEN
//	# default behaviour: dry-run, prints the plan
//
//	mqconnector gitops --dir=./config --url=https://mqc.svc \
//	    --token=$MQC_API_TOKEN --apply
//	# write the desired state
//
// The bundle format on disk matches what GET /api/v1/config/export
// emits. A single multi-doc YAML file works; or a directory of files
// that each contain a partial bundle (their `connections`, `pipelines`
// etc. arrays are concatenated). Top-level keys merge by appending.

package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// gitopsBundle is the shape the API speaks. Mirrors handlers_config.go
// configBundle exactly — kept here as a separate type so the client
// has no internal/* dependency and could be lifted into its own
// binary later without churn.
type gitopsBundle struct {
	Version     int                    `yaml:"version" json:"version"`
	TenantSlug  string                 `yaml:"tenant_slug" json:"tenant_slug"`
	Connections []map[string]any       `yaml:"connections,omitempty" json:"connections,omitempty"`
	Schemas     []map[string]any       `yaml:"schemas,omitempty" json:"schemas,omitempty"`
	Scripts     []map[string]any       `yaml:"scripts,omitempty" json:"scripts,omitempty"`
	Pipelines   []map[string]any       `yaml:"pipelines,omitempty" json:"pipelines,omitempty"`
}

func gitops() error {
	fs := flag.NewFlagSet("gitops", flag.ExitOnError)
	dir := fs.String("dir", "", "directory containing YAML config files (required)")
	apiURL := fs.String("url", "", "base URL of the mqconnector API, e.g. https://mqc.svc:8443 (required)")
	token := fs.String("token", "", "Bearer API token (required; create via UI under /tokens)")
	apply := fs.Bool("apply", false, "write changes; without this flag the command only prints the diff")
	insecure := fs.Bool("insecure", false, "skip TLS verification (dev only)")
	tenantSlug := fs.String("tenant", "", "tenant slug override; defaults to the bundle's tenant_slug")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *dir == "" || *apiURL == "" || *token == "" {
		fs.Usage()
		return fmt.Errorf("--dir, --url and --token are required")
	}

	desired, err := loadBundle(*dir)
	if err != nil {
		return fmt.Errorf("load bundle from %s: %w", *dir, err)
	}
	if *tenantSlug != "" {
		desired.TenantSlug = *tenantSlug
	}
	if desired.Version == 0 {
		desired.Version = 1
	}

	c := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: *insecure}, // #nosec G402 — opt-in
		},
	}
	current, err := fetchExport(c, *apiURL, *token, desired.TenantSlug)
	if err != nil {
		return fmt.Errorf("fetch current state: %w", err)
	}

	plan := diffBundles(current, &desired)
	printPlan(plan)
	if len(plan.toCreate)+len(plan.toUpdate)+len(plan.toDelete) == 0 {
		fmt.Println("no changes — server already matches desired state")
		return nil
	}

	if !*apply {
		fmt.Println("\n(dry-run: pass --apply to push these changes)")
		return nil
	}

	return applyBundle(c, *apiURL, *token, &desired)
}

// loadBundle reads every *.yaml / *.yml file in `dir` and merges them
// into one bundle. Top-level fields override the previous file's value
// (last write wins); list fields concatenate. The merge keeps a
// pure-additive read order so operators can split their config into
// per-resource files without thinking about precedence.
func loadBundle(dir string) (gitopsBundle, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return gitopsBundle{}, err
	}
	out := gitopsBundle{Version: 1}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return gitopsBundle{}, fmt.Errorf("read %s: %w", name, err)
		}
		var partial gitopsBundle
		if err := yaml.Unmarshal(body, &partial); err != nil {
			return gitopsBundle{}, fmt.Errorf("parse %s: %w", name, err)
		}
		if partial.Version != 0 {
			out.Version = partial.Version
		}
		if partial.TenantSlug != "" {
			out.TenantSlug = partial.TenantSlug
		}
		out.Connections = append(out.Connections, partial.Connections...)
		out.Schemas = append(out.Schemas, partial.Schemas...)
		out.Scripts = append(out.Scripts, partial.Scripts...)
		out.Pipelines = append(out.Pipelines, partial.Pipelines...)
	}
	return out, nil
}

// fetchExport pulls the server's current view of the named tenant's
// config. Bundle the response in the same shape as the desired bundle
// for symmetric diffing.
func fetchExport(c *http.Client, base, token, tenantSlug string) (*gitopsBundle, error) {
	u, _ := url.Parse(strings.TrimRight(base, "/") + "/api/v1/config/export")
	if tenantSlug != "" {
		q := u.Query()
		q.Set("tenant_slug", tenantSlug)
		u.RawQuery = q.Encode()
	}
	req, _ := http.NewRequest("GET", u.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	// /api/v1/config/export negotiates on Accept — default is YAML.
	// Ask for JSON so the existing json.Decoder below works.
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("export: %d: %s", resp.StatusCode, body)
	}
	var b gitopsBundle
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		return nil, err
	}
	return &b, nil
}

// applyBundle POSTs the desired state to the import endpoint. The
// server handles the actual upsert + tx semantics.
func applyBundle(c *http.Client, base, token string, b *gitopsBundle) error {
	body, err := json.Marshal(b)
	if err != nil {
		return err
	}
	req, _ := http.NewRequest("POST", strings.TrimRight(base, "/")+"/api/v1/config/import",
		bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("import: %d: %s", resp.StatusCode, b)
	}
	respBody, _ := io.ReadAll(resp.Body)
	fmt.Println("applied:", strings.TrimSpace(string(respBody)))
	return nil
}

// diffPlan is what we render for the operator.
type diffPlan struct {
	toCreate map[string][]string // resource → names
	toUpdate map[string][]string
	toDelete map[string][]string
}

// diffBundles is a name-based diff. Equal names mean "update"; the
// server-side import is responsible for content-level upsert (idempotent
// when names + contents match). A "delete" is a name present on the
// server but absent from the desired state — the server doesn't yet
// honour deletions from /config/import, but reporting them makes
// drift visible.
func diffBundles(current, desired *gitopsBundle) diffPlan {
	plan := diffPlan{
		toCreate: map[string][]string{},
		toUpdate: map[string][]string{},
		toDelete: map[string][]string{},
	}
	cmp := func(kind string, currentList, desiredList []map[string]any) {
		curNames := nameSet(currentList)
		desNames := nameSet(desiredList)
		for n := range desNames {
			if curNames[n] {
				plan.toUpdate[kind] = append(plan.toUpdate[kind], n)
			} else {
				plan.toCreate[kind] = append(plan.toCreate[kind], n)
			}
		}
		for n := range curNames {
			if !desNames[n] {
				plan.toDelete[kind] = append(plan.toDelete[kind], n)
			}
		}
	}
	cmp("connections", current.Connections, desired.Connections)
	cmp("schemas", current.Schemas, desired.Schemas)
	cmp("scripts", current.Scripts, desired.Scripts)
	cmp("pipelines", current.Pipelines, desired.Pipelines)
	return plan
}

func nameSet(rows []map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		if name, ok := r["name"].(string); ok && name != "" {
			out[name] = true
		}
	}
	return out
}

func printPlan(p diffPlan) {
	fmt.Println("plan:")
	for kind, names := range p.toCreate {
		for _, n := range names {
			fmt.Printf("  + %-12s %s\n", kind, n)
		}
	}
	for kind, names := range p.toUpdate {
		for _, n := range names {
			fmt.Printf("  ~ %-12s %s\n", kind, n)
		}
	}
	for kind, names := range p.toDelete {
		for _, n := range names {
			fmt.Printf("  - %-12s %s (not yet honoured by /config/import — drift only)\n", kind, n)
		}
	}
}
