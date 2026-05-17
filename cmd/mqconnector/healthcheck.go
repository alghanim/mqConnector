package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

// healthcheck is a tiny self-probe used by container HEALTHCHECK.
// We can't shell out to wget in a distroless image, so the binary
// has to do the probe itself.
//
// Usage:
//
//	mqconnector healthcheck                       # https://localhost:8443/api/health
//	mqconnector healthcheck --url=https://...     # custom endpoint
//	mqconnector healthcheck --insecure            # skip TLS verification (dev)
//
// Exit codes:
//
//	0  /api/health returned 200
//	1  any other status, connection error, or timeout
func healthcheck() error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	url := fs.String("url", "https://localhost:8443/api/health", "health endpoint")
	insecure := fs.Bool("insecure", true, "skip TLS verification (default true — dev certs are typically self-signed)")
	timeout := fs.Duration("timeout", 5*time.Second, "probe timeout")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	client := &http.Client{
		Timeout: *timeout,
		Transport: &http.Transport{
			// The bridge runs with a self-signed cert by default;
			// insecure=true is the right posture for a self-probe.
			// Operators on a publicly-trusted cert can flip the
			// flag if they want strict verification.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: *insecure}, // #nosec G402 — opt-in
		},
	}
	resp, err := client.Get(*url)
	if err != nil {
		return fmt.Errorf("probe %s: %w", *url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s returned %d", *url, resp.StatusCode)
	}
	return nil
}
