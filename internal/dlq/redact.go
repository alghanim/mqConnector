package dlq

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"mqConnector/internal/storage"
)

// Redactor applies an ordered list of redaction rules to a DLQ payload
// before it's persisted. Rules come in two flavours:
//
//   - "jsonpath": a small dot/bracket path expression (a strict subset
//     of JSONPath sufficient for the redaction use case). The matched
//     value is replaced with mask_replace. Non-JSON payloads cause the
//     JSONPath rule to be a no-op rather than an error — the goal is
//     "redact what you can find, persist the result," not "fail closed
//     on unparseable input."
//   - "regex": a Go regexp; every match is replaced with mask_replace
//     via ReplaceAllString on a byte-string view of the payload.
//
// Engine semantics:
//
//   - Rules are applied in `Order` order.
//   - Disabled rules are skipped.
//   - The engine reports `didRedact = true` iff at least one rule
//     actually mutated the payload. The DLQ Push path uses this to
//     decide whether to seal the raw form into raw_msg.
//   - A malformed pattern is logged but doesn't break the chain — the
//     other rules still apply. Callers can validate the ruleset at
//     Replace time (see DLQRedactionRepo.Replace, which compiles
//     patterns up-front) to surface errors before persistence.
//
// The engine is safe for concurrent use; compiled regexps live in a
// cache keyed by pattern so a hot pipeline doesn't recompile on every
// failure.
type Redactor struct {
	regexCache sync.Map // pattern → *regexp.Regexp
}

// NewRedactor constructs a Redactor.
func NewRedactor() *Redactor { return &Redactor{} }

// Apply runs the rules in order against payload. Returns the
// post-redaction bytes and a boolean reporting whether at least one
// rule actually modified the payload.
func (r *Redactor) Apply(payload []byte, rules []storage.DLQRedactionRule) ([]byte, bool) {
	if len(rules) == 0 {
		return payload, false
	}
	out := payload
	mutated := false
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		mask := rule.MaskReplace
		if mask == "" {
			mask = "[REDACTED]"
		}
		switch rule.RuleKind {
		case "regex":
			re := r.compileRegex(rule.Pattern)
			if re == nil {
				continue
			}
			replaced := re.ReplaceAll(out, []byte(mask))
			if !bytes.Equal(replaced, out) {
				out = replaced
				mutated = true
			}
		case "jsonpath":
			replaced, ok := applyJSONPath(out, rule.Pattern, mask)
			if ok {
				out = replaced
				mutated = true
			}
		}
	}
	return out, mutated
}

func (r *Redactor) compileRegex(pattern string) *regexp.Regexp {
	if v, ok := r.regexCache.Load(pattern); ok {
		if re, _ := v.(*regexp.Regexp); re != nil {
			return re
		}
		return nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		r.regexCache.Store(pattern, (*regexp.Regexp)(nil))
		return nil
	}
	r.regexCache.Store(pattern, re)
	return re
}

// applyJSONPath replaces every value matched by `path` inside payload
// (interpreted as JSON) with mask. Returns the rewritten bytes and a
// boolean indicating whether at least one value was matched.
//
// Path grammar (intentionally minimal):
//
//   - "$"          — the root document (replace the entire document
//                    with a single masked value)
//   - "$.a.b"      — field "a" then field "b"
//   - "$.a.b[*]"   — every element of array "b"
//   - "$.a[0]"     — element 0 of array "a"
//   - "$..secret"  — recursive descent: every "secret" key, anywhere
//
// A non-JSON payload returns (payload, false) — the caller gets the
// original bytes back unchanged and the engine records no redaction.
// Anything outside this grammar is treated as a literal field name
// match against the root.
func applyJSONPath(payload []byte, path, mask string) ([]byte, bool) {
	if len(payload) == 0 {
		return payload, false
	}
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload, false
	}
	maskVal := any(mask)
	hit := false
	root = walkPath(root, parsePath(path), 0, maskVal, &hit)
	if !hit {
		return payload, false
	}
	out, err := json.Marshal(root)
	if err != nil {
		return payload, false
	}
	return out, true
}

type pathSegment struct {
	// kind: "key", "index", "wildcard", "recursive"
	kind  string
	key   string
	index int
}

func parsePath(path string) []pathSegment {
	if path == "" {
		return nil
	}
	if path == "$" {
		return []pathSegment{{kind: "root"}}
	}
	// Strip leading "$." or "$"
	p := strings.TrimPrefix(path, "$")
	if strings.HasPrefix(p, "..") {
		// Recursive descent: "$..name" → recursive lookup for "name".
		key := strings.TrimPrefix(p, "..")
		return []pathSegment{{kind: "recursive", key: key}}
	}
	p = strings.TrimPrefix(p, ".")
	var out []pathSegment
	for len(p) > 0 {
		// Handle [n] / [*]
		if p[0] == '[' {
			end := strings.IndexByte(p, ']')
			if end < 0 {
				break
			}
			body := p[1:end]
			if body == "*" {
				out = append(out, pathSegment{kind: "wildcard"})
			} else {
				idx := 0
				neg := false
				if strings.HasPrefix(body, "-") {
					neg = true
					body = body[1:]
				}
				for _, c := range body {
					if c < '0' || c > '9' {
						idx = -1
						break
					}
					idx = idx*10 + int(c-'0')
				}
				if neg {
					idx = -idx
				}
				out = append(out, pathSegment{kind: "index", index: idx})
			}
			p = p[end+1:]
			if strings.HasPrefix(p, ".") {
				p = p[1:]
			}
			continue
		}
		// Key — read until next '.' or '['
		next := strings.IndexAny(p, ".[")
		if next < 0 {
			out = append(out, pathSegment{kind: "key", key: p})
			break
		}
		out = append(out, pathSegment{kind: "key", key: p[:next]})
		p = p[next:]
		if strings.HasPrefix(p, ".") {
			p = p[1:]
		}
	}
	return out
}

func walkPath(node any, segs []pathSegment, depth int, mask any, hit *bool) any {
	if len(segs) == 0 {
		return node
	}
	seg := segs[depth]
	last := depth == len(segs)-1

	switch seg.kind {
	case "root":
		*hit = true
		return mask
	case "recursive":
		return walkRecursive(node, seg.key, mask, hit)
	case "key":
		obj, ok := node.(map[string]any)
		if !ok {
			return node
		}
		if v, present := obj[seg.key]; present {
			if last {
				obj[seg.key] = mask
				*hit = true
			} else {
				obj[seg.key] = walkPath(v, segs, depth+1, mask, hit)
			}
		}
		return obj
	case "wildcard":
		arr, ok := node.([]any)
		if !ok {
			return node
		}
		for i := range arr {
			if last {
				arr[i] = mask
				*hit = true
			} else {
				arr[i] = walkPath(arr[i], segs, depth+1, mask, hit)
			}
		}
		return arr
	case "index":
		arr, ok := node.([]any)
		if !ok {
			return node
		}
		idx := seg.index
		if idx < 0 {
			idx = len(arr) + idx
		}
		if idx < 0 || idx >= len(arr) {
			return node
		}
		if last {
			arr[idx] = mask
			*hit = true
		} else {
			arr[idx] = walkPath(arr[idx], segs, depth+1, mask, hit)
		}
		return arr
	}
	return node
}

func walkRecursive(node any, key string, mask any, hit *bool) any {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			if k == key {
				v[k] = mask
				*hit = true
			} else {
				v[k] = walkRecursive(child, key, mask, hit)
			}
		}
		return v
	case []any:
		for i, child := range v {
			v[i] = walkRecursive(child, key, mask, hit)
		}
		return v
	}
	return node
}

// ValidateRules type-checks a ruleset without persisting it. Callers
// (the HTTP layer's Replace handler) use it to reject malformed
// patterns at edit time rather than discovering them on the next
// failure-path push.
func ValidateRules(rules []storage.DLQRedactionRule) error {
	for i, rule := range rules {
		switch rule.RuleKind {
		case "regex":
			if _, err := regexp.Compile(rule.Pattern); err != nil {
				return fmt.Errorf("rule %d (regex): %w", i, err)
			}
		case "jsonpath":
			if rule.Pattern == "" || (rule.Pattern[0] != '$' && !strings.Contains(rule.Pattern, ".")) {
				// A bare field name is also valid (interpreted as a
				// root-level key match). Anything else with no '$' or
				// '.' is almost certainly a typo.
				if !isSimpleFieldName(rule.Pattern) {
					return fmt.Errorf("rule %d (jsonpath): pattern %q must start with $ or be a simple field name", i, rule.Pattern)
				}
			}
		default:
			return fmt.Errorf("rule %d: rule_kind must be 'jsonpath' or 'regex', got %q", i, rule.RuleKind)
		}
	}
	return nil
}

func isSimpleFieldName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}
