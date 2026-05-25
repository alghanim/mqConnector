package cluster

import (
	"strings"
	"testing"
)

// Each pair test asserts: both inputs produce the same fingerprint AND
// the same template. The wantTemplate field documents what that
// template should be — wired into the assertion so a tokenisation
// regression that silently changes the template (without changing the
// fingerprint) still fails the test.
type pairCase struct {
	name         string
	a, b         string
	wantSame     bool
	wantTemplate string // checked against the canonicalised form of a + b when wantSame
}

func TestFingerprint_Pairs(t *testing.T) {
	cases := []pairCase{
		{
			name:         "validation_field_paths_collapse",
			a:            "validation: missing field customer.id",
			b:            "validation: missing field order.id",
			wantSame:     true,
			wantTemplate: "validation: missing field <field>",
		},
		{
			name:         "connection_refused_ip_port_collapses",
			a:            "connection refused: 127.0.0.1:5672",
			b:            "connection refused: 10.0.0.1:5672",
			wantSame:     true,
			wantTemplate: "connection refused: <host>",
		},
		{
			name:         "job_int_and_timestamp_collapse",
			a:            "job 1729439123 failed at 2026-05-25t12:34:56z",
			b:            "job 998877 failed at 2026-05-25t12:35:00z",
			wantSame:     true,
			wantTemplate: "job <int> failed at <time>",
		},
		{
			name:         "email_collapses",
			a:            "send to user alice@example.com failed",
			b:            "send to user bob@elsewhere.org failed",
			wantSame:     true,
			wantTemplate: "send to user <email> failed",
		},
		{
			name:         "distinct_errors_distinct_fingerprints",
			a:            "validation failed",
			b:            "connection refused",
			wantSame:     false,
			wantTemplate: "",
		},
		{
			name:         "whitespace_insensitive",
			a:            "  validation:   missing field x.y  ",
			b:            "validation: missing field x.y",
			wantSame:     true,
			wantTemplate: "validation: missing field <field>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := Fingerprint(tc.a)
			b := Fingerprint(tc.b)

			if a.Fingerprint == "" {
				t.Fatalf("input %q produced empty fingerprint", tc.a)
			}
			if b.Fingerprint == "" {
				t.Fatalf("input %q produced empty fingerprint", tc.b)
			}
			if len(a.Fingerprint) != 16 {
				t.Fatalf("fingerprint length = %d, want 16 (got %q)", len(a.Fingerprint), a.Fingerprint)
			}

			if tc.wantSame {
				if a.Fingerprint != b.Fingerprint {
					t.Errorf("expected SAME fingerprint:\n  a=%q → fp=%s tmpl=%q\n  b=%q → fp=%s tmpl=%q",
						tc.a, a.Fingerprint, a.Template, tc.b, b.Fingerprint, b.Template)
				}
				if a.Template != b.Template {
					t.Errorf("expected SAME template:\n  a=%q\n  b=%q", a.Template, b.Template)
				}
				if tc.wantTemplate != "" && a.Template != tc.wantTemplate {
					t.Errorf("template mismatch:\n  got  %q\n  want %q", a.Template, tc.wantTemplate)
				}
			} else {
				if a.Fingerprint == b.Fingerprint {
					t.Errorf("expected DIFFERENT fingerprints, both = %s:\n  a=%q tmpl=%q\n  b=%q tmpl=%q",
						a.Fingerprint, tc.a, a.Template, tc.b, b.Template)
				}
			}
		})
	}
}

// TestFingerprintWithStage_StageScopesCluster covers the case where two
// pipelines emit byte-identical errors at different stages: the
// console must show them as separate clusters so an operator can drill
// into "validate failures" vs "transform failures" without conflation.
func TestFingerprintWithStage_StageScopesCluster(t *testing.T) {
	errStr := "missing field customer.id"
	a := FingerprintWithStage(errStr, "validate")
	b := FingerprintWithStage(errStr, "transform")

	if a.Fingerprint == "" || b.Fingerprint == "" {
		t.Fatalf("empty fingerprint(s): a=%q b=%q", a.Fingerprint, b.Fingerprint)
	}
	if a.Fingerprint == b.Fingerprint {
		t.Errorf("expected different fingerprints when stage differs, both = %s", a.Fingerprint)
	}
	if !strings.Contains(a.Template, "[stage:validate]") {
		t.Errorf("expected validate stage marker in template, got %q", a.Template)
	}
	if !strings.Contains(b.Template, "[stage:transform]") {
		t.Errorf("expected transform stage marker in template, got %q", b.Template)
	}

	// Equality with the same stage must still hold — stage scoping
	// only differentiates ACROSS stages, never within the same stage.
	c := FingerprintWithStage(errStr, "validate")
	if a.Fingerprint != c.Fingerprint {
		t.Errorf("same-stage same-input should match: a=%s c=%s", a.Fingerprint, c.Fingerprint)
	}

	// Empty stage falls back to Fingerprint exactly.
	d := FingerprintWithStage(errStr, "")
	e := Fingerprint(errStr)
	if d.Fingerprint != e.Fingerprint || d.Template != e.Template {
		t.Errorf("empty stage should match plain Fingerprint:\n  d.fp=%s d.tmpl=%q\n  e.fp=%s e.tmpl=%q",
			d.Fingerprint, d.Template, e.Fingerprint, e.Template)
	}
}

func TestFingerprint_Empty(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n\t"} {
		r := Fingerprint(in)
		if r.Fingerprint != "" {
			t.Errorf("empty input %q produced non-empty fingerprint %q", in, r.Fingerprint)
		}
		if r.Template != "" {
			t.Errorf("empty input %q produced non-empty template %q", in, r.Template)
		}
		if len(r.Tokens) != 0 {
			t.Errorf("empty input %q produced tokens %#v", in, r.Tokens)
		}
	}
}

// TestFingerprint_Deterministic guards the no-map-iteration-leak
// invariant. The package must produce byte-identical Results on
// repeated calls; failing this test means a non-deterministic
// container (map ordering, slice append racing) crept in. Combined
// with `go test -count=10` the upstream harness pins it across runs.
func TestFingerprint_Deterministic(t *testing.T) {
	inputs := []string{
		"validation: missing field customer.id",
		"connection refused: 10.0.0.1:5672",
		"job 1729439123 failed at 2026-05-25t12:34:56z",
		"send to user alice@example.com failed",
		`failed to parse "{not json"`,
	}
	for _, in := range inputs {
		first := Fingerprint(in)
		for i := 0; i < 100; i++ {
			got := Fingerprint(in)
			if got.Fingerprint != first.Fingerprint {
				t.Fatalf("non-deterministic fingerprint for %q: iter %d = %s, want %s",
					in, i, got.Fingerprint, first.Fingerprint)
			}
			if got.Template != first.Template {
				t.Fatalf("non-deterministic template for %q: iter %d = %q, want %q",
					in, i, got.Template, first.Template)
			}
		}
	}
}

// TestFingerprint_TokensMatchTemplate is a sanity assertion that the
// Tokens slice is exactly strings.Fields(Template) — callers
// (display + ranking) rely on this property.
func TestFingerprint_TokensMatchTemplate(t *testing.T) {
	r := Fingerprint("validation: missing field customer.id")
	if got, want := strings.Join(r.Tokens, " "), r.Template; got != want {
		t.Errorf("Tokens vs Template mismatch:\n  Tokens   joined = %q\n  Template        = %q", got, want)
	}
}

// TestFingerprint_PathCollapses verifies the file-path substitution.
// Filesystem paths are a frequent ingredient in I/O errors and
// should not split clusters per-file.
func TestFingerprint_PathCollapses(t *testing.T) {
	a := Fingerprint("read /var/lib/mqconnector/queue/0001.dat: no such file")
	b := Fingerprint("read /var/lib/mqconnector/queue/9999.dat: no such file")
	if a.Fingerprint == "" || b.Fingerprint == "" {
		t.Fatal("empty fingerprint")
	}
	if a.Fingerprint != b.Fingerprint {
		t.Errorf("path-only variation should collapse:\n  a.fp=%s tmpl=%q\n  b.fp=%s tmpl=%q",
			a.Fingerprint, a.Template, b.Fingerprint, b.Template)
	}
	if !strings.Contains(a.Template, "<path>") {
		t.Errorf("expected <path> placeholder in template, got %q", a.Template)
	}
}

// TestFingerprint_UUIDCollapses verifies the UUID substitution
// outright — UUIDs are the most common splitter of otherwise
// identical errors so the test asserts it directly rather than
// only through the table.
func TestFingerprint_UUIDCollapses(t *testing.T) {
	a := Fingerprint("job 11111111-2222-3333-4444-555555555555 failed")
	b := Fingerprint("job aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee failed")
	if a.Fingerprint != b.Fingerprint {
		t.Errorf("uuid variation should collapse:\n  a.fp=%s tmpl=%q\n  b.fp=%s tmpl=%q",
			a.Fingerprint, a.Template, b.Fingerprint, b.Template)
	}
	if !strings.Contains(a.Template, "<uuid>") {
		t.Errorf("expected <uuid> placeholder, got %q", a.Template)
	}
}
