package audit

import (
	"strings"
	"testing"
	"time"

	"mqConnector/internal/storage"
)

func TestFormatCEF_HeaderShape(t *testing.T) {
	e := storage.AuditEntry{
		ID:       "audit-1",
		At:       time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC),
		Action:   "POST",
		Resource: "/api/v1/connections",
		Status:   200,
		Actor:    "alice",
		ActorSub: "user-1",
		RemoteIP: "10.0.0.1",
		RequestID: "req-1",
		TenantID:  "tenant-1",
	}
	out := formatCEF(e, "1.2.3")
	if !strings.HasPrefix(out, "CEF:0|mqconnector|mqconnector|1.2.3|POST|") {
		t.Fatalf("CEF header malformed: %s", out)
	}
	if !strings.Contains(out, "act=POST") {
		t.Errorf("missing act extension: %s", out)
	}
	if !strings.Contains(out, "suser=alice") {
		t.Errorf("missing suser extension: %s", out)
	}
	if !strings.Contains(out, "src=10.0.0.1") {
		t.Errorf("missing src extension: %s", out)
	}
	if !strings.Contains(out, "request=/api/v1/connections") {
		t.Errorf("missing request extension: %s", out)
	}
	if !strings.Contains(out, "outcome=success") {
		t.Errorf("expected outcome=success for 200: %s", out)
	}
}

func TestFormatCEF_SeverityFromStatus(t *testing.T) {
	cases := []struct {
		status   int
		severity int
	}{
		{200, 3},
		{204, 3},
		{400, 5},
		{404, 5},
		{500, 7},
		{503, 7},
	}
	for _, c := range cases {
		e := storage.AuditEntry{Action: "x", Resource: "/r", Status: c.status}
		out := formatCEF(e, "v")
		expect := "|x|/r|" + intStr(c.severity) + "|"
		if !strings.Contains(out, expect) {
			t.Errorf("status=%d: expected severity %d in %q (looking for %q)",
				c.status, c.severity, out, expect)
		}
	}
}

func TestFormatCEF_EscapesPipesAndEquals(t *testing.T) {
	e := storage.AuditEntry{
		Action:   "GET|injected",
		Resource: "/path=injected",
		Status:   200,
	}
	out := formatCEF(e, "v")
	// Pipe in action must be escaped in the header — it would
	// otherwise be parsed as a header-segment terminator.
	if strings.Contains(out, "GET|injected|") {
		t.Fatalf("CEF header pipe not escaped: %s", out)
	}
	if !strings.Contains(out, `GET\|injected`) {
		t.Fatalf("expected escaped pipe: %s", out)
	}
	// Equals in extension value: receivers would otherwise split on
	// it and corrupt the field map.
	if !strings.Contains(out, `request=/path\=injected`) {
		t.Fatalf("expected escaped equals in request: %s", out)
	}
}

func TestFormatCEF_OmitsBlankExtensions(t *testing.T) {
	e := storage.AuditEntry{Action: "POST", Resource: "/x", Status: 200}
	out := formatCEF(e, "v")
	// No Actor / ActorSub / RemoteIP / RequestID set — those k/v
	// pairs should be omitted entirely (not "suser= ").
	if strings.Contains(out, "suser=") {
		t.Errorf("blank suser should be omitted: %s", out)
	}
	if strings.Contains(out, "src=") {
		t.Errorf("blank src should be omitted: %s", out)
	}
}

func intStr(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return ""
}
