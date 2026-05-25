package cluster

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
)

// Result is the structured output of fingerprinting a single DLQ error
// string. Two errors that share a Template share a Fingerprint, and
// equality on the Fingerprint string is the canonical cluster test —
// Tokens is for display + ranking only.
type Result struct {
	// Fingerprint is a stable, opaque 16-character hex SimHash of the
	// tokenised template. Two errors that share a template share this
	// value; minor variation in numbers/UUIDs/timestamps does NOT
	// change the fingerprint.
	Fingerprint string

	// Template is the human-readable error with variable parts
	// collapsed to placeholders. "validation: missing field
	// customer.id" and "validation: missing field order.id" both
	// template to "validation: missing field <field>".
	Template string

	// Tokens is the tokenised error after placeholder substitution —
	// useful for ranking/display but NOT load-bearing on equality.
	// Equivalent to strings.Fields(Template).
	Tokens []string
}

// Substitution regexes. All compiled once at package init. Order
// matters: earlier substitutions claim spans first, so UUIDs (which
// match the hex-segment pattern fully) consume those bytes before
// the integer pattern gets a chance to scan them. See the
// substitutions slice below for the canonical ordering — that's the
// only order the engine actually runs.
var (
	// 8-4-4-4-12 hex segments. Case is already lowered by the time
	// the regex runs, so [0-9a-f] is sufficient.
	reUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

	// ISO-8601 timestamps. Lowered "t" / "z" — the input is already
	// lowercased. Accepts millisecond fractions and timezone
	// suffixes (Z / ±HH:MM / ±HHMM).
	reTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}t\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:z|[+-]\d{2}:?\d{2})?`)

	// Integers of 3+ digits. Short numbers (port 80, errno 2) stay
	// literal so "connect: refused on port 80" and "connect: refused
	// on port 25" template apart from "connect: refused on port
	// <int>" with a 5-digit ephemeral port.
	reInt = regexp.MustCompile(`\b\d{3,}\b`)

	// RFC-5321-ish email. Top-level domain ≥ 2 letters.
	reEmail = regexp.MustCompile(`[a-z0-9._-]+@[a-z0-9._-]+\.[a-z]{2,}`)

	// IPv4 with optional :port. IPv6 isn't tokenised — operators
	// almost never see literal v6 in error strings; if they do, the
	// JSON-pointer / FIELD pattern will swallow most of it.
	reHost = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d+)?`)

	// Unix-style and Windows drive-letter paths. Minimum length 3
	// after the leading slash so single-segment "/" or "/a" don't
	// false-positive.
	rePath = regexp.MustCompile(`(?:[a-z]:)?/[a-z0-9._/-]{3,}`)

	// JSON-pointer / dot-path field references. At least one dot, all
	// segments start with a letter or underscore. Runs after UUID +
	// PATH so it can't swallow them. The trailing segments allow
	// single-character names (foo.x, customer.id) — operators often
	// use one-letter aliases.
	reField = regexp.MustCompile(`\b[a-z_][a-z0-9_]*(?:\.[a-z_][a-z0-9_]*)+\b`)

	// Quoted strings. Both single- and double-quoted. Non-greedy so
	// "x" "y" doesn't collapse to one span. Backslash escapes are
	// not honoured — DLQ error strings don't carry them in practice.
	reStrDouble = regexp.MustCompile(`"[^"]*"`)
	reStrSingle = regexp.MustCompile(`'[^']*'`)

	// Whitespace collapse. Tabs / newlines / multi-space runs all
	// fold to a single space so a payload-included error doesn't
	// fragment into N templates that differ only in whitespace.
	reWS = regexp.MustCompile(`\s+`)
)

// substitution is one (regex, placeholder) pair applied in the order
// declared in substitutions below.
type substitution struct {
	re          *regexp.Regexp
	placeholder string
}

// substitutions is the ordered substitution pipeline. The order is
// load-bearing: UUID and TIME run first so they claim their byte
// spans before INT could match the digit-run inside them; FIELD
// runs after PATH so dotted paths inside a path don't double-match;
// STR runs last so a quoted UUID still tokenises as <UUID> and not
// <STR>.
var substitutions = []substitution{
	{re: reUUID, placeholder: "<uuid>"},
	{re: reTime, placeholder: "<time>"},
	{re: reEmail, placeholder: "<email>"},
	{re: reHost, placeholder: "<host>"},
	{re: rePath, placeholder: "<path>"},
	{re: reField, placeholder: "<field>"},
	{re: reInt, placeholder: "<int>"},
	{re: reStrDouble, placeholder: "<str>"},
	{re: reStrSingle, placeholder: "<str>"},
}

// Fingerprint produces the Result for a single error string. Pure
// function — same input always returns the same Result. Empty input
// returns a zero-value Result.
func Fingerprint(errStr string) Result {
	template, tokens := tokenise(errStr)
	if template == "" {
		return Result{}
	}
	return Result{
		Fingerprint: simhashHex(tokens),
		Template:    template,
		Tokens:      tokens,
	}
}

// FingerprintWithStage is the same as Fingerprint but the failing
// stage name is folded into the template + fingerprint so two
// pipelines with identical errors at different stages cluster apart.
// An empty stage name is equivalent to Fingerprint.
func FingerprintWithStage(errStr, stageName string) Result {
	if stageName == "" {
		return Fingerprint(errStr)
	}
	template, tokens := tokenise(errStr)
	if template == "" {
		return Result{}
	}
	stage := strings.ToLower(strings.TrimSpace(stageName))
	// Prefix the stage as a synthetic token "[stage:<name>]" so the
	// rendered template tells the operator which stage failed and
	// the SimHash includes the stage in its bit budget — two
	// otherwise-identical errors at different stages produce
	// distinct fingerprints.
	stageToken := "[stage:" + stage + "]"
	tokens = append([]string{stageToken}, tokens...)
	template = stageToken + " " + template
	return Result{
		Fingerprint: simhashHex(tokens),
		Template:    template,
		Tokens:      tokens,
	}
}

// tokenise applies the lowercase + whitespace + substitution pipeline
// and returns the template string + the strings.Fields-split tokens.
// Returns ("", nil) for an input that's empty after trim+collapse.
func tokenise(errStr string) (string, []string) {
	s := strings.ToLower(errStr)
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	s = reWS.ReplaceAllString(s, " ")
	for _, sub := range substitutions {
		s = sub.re.ReplaceAllString(s, sub.placeholder)
	}
	// Re-collapse whitespace in case a substitution introduced a
	// double space (it shouldn't, but the cost of the second pass is
	// negligible and the invariant is worth defending).
	s = reWS.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	return s, strings.Fields(s)
}

// simhashHex computes the SimHash of the token list and returns the
// first 16 hex characters of the resulting uint64. The algorithm is
// the standard Charikar SimHash with FNV-1a-64 as the per-token
// hash:
//
//   - For each token, hash it to 64 bits.
//   - For each bit position 0..63, add +1 to the bucket if the
//     token's hash has that bit set, else -1.
//   - After all tokens, set bit i of the output if bucket[i] >= 0.
//
// Hand-rolled (no library) because the algorithm is ~30 lines and
// pulling in a dependency for it would violate the no-new-deps
// constraint.
func simhashHex(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	var buckets [64]int
	for _, t := range tokens {
		h := fnv.New64a()
		_, _ = h.Write([]byte(t))
		sum := h.Sum64()
		for i := 0; i < 64; i++ {
			if sum&(1<<uint(i)) != 0 {
				buckets[i]++
			} else {
				buckets[i]--
			}
		}
	}
	var out uint64
	for i := 0; i < 64; i++ {
		if buckets[i] >= 0 {
			out |= 1 << uint(i)
		}
	}
	return fmt.Sprintf("%016x", out)
}
