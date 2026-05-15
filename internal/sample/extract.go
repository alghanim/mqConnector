// Package sample walks an uploaded message and returns every dot-path
// reachable inside it. The 2024 prototype used a near-identical helper to
// populate the "Templates" collection so an operator could pick which paths
// to filter without typing them by hand; the rewrite returns the paths
// directly in the response — no persistence needed.
//
// Supported inputs:
//   - JSON object or array — nested objects yield dotted paths
//     (`customer.address.city`); arrays collapse to the parent name
//     (`items.item.price` for a list of items) so the path matches what the
//     pipeline's filter stage would accept.
//   - XML — root tag is returned separately; child paths use the same
//     dotted form.
//
// Anything else falls back to `FormatBytes` with an empty path list — the
// operator is expected to handle plain text by hand.
package sample

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/beevik/etree"

	"mqConnector/internal/pipeline"
)

// Result is what the HTTP handler returns to the caller.
type Result struct {
	Format  pipeline.Format `json:"format"`
	RootTag string          `json:"root_tag,omitempty"` // XML only
	Paths   []string        `json:"paths"`
}

// MaxSize caps payload size for path extraction; beyond this we refuse to
// even try. 4 MB is generous for a "representative sample" and well below
// the 10 MB request-body cap configured at the server level.
const MaxSize = 4 * 1024 * 1024

// ErrTooLarge is returned by Extract when the input exceeds MaxSize.
var ErrTooLarge = errors.New("sample: payload exceeds MaxSize")

// Extract identifies the input format and returns every distinct dot-path
// it could find. Paths are returned in stable lexical order so the
// editor's path picker doesn't shuffle between uploads.
func Extract(body []byte) (Result, error) {
	if len(body) > MaxSize {
		return Result{}, ErrTooLarge
	}
	format := pipeline.Detect(body)
	switch format {
	case pipeline.FormatJSON:
		paths, err := extractJSON(body)
		if err != nil {
			return Result{}, fmt.Errorf("extract json: %w", err)
		}
		return Result{Format: format, Paths: paths}, nil
	case pipeline.FormatXML:
		root, paths, err := extractXML(body)
		if err != nil {
			return Result{}, fmt.Errorf("extract xml: %w", err)
		}
		return Result{Format: format, RootTag: root, Paths: paths}, nil
	default:
		return Result{Format: format, Paths: []string{}}, nil
	}
}

func extractJSON(body []byte) ([]string, error) {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	walkJSON("", data, seen)
	return sortedKeys(seen), nil
}

func walkJSON(prefix string, v any, seen map[string]struct{}) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			seen[p] = struct{}{}
			walkJSON(p, val, seen)
		}
	case []any:
		// Arrays collapse into the parent — every element walks at the
		// same prefix. This matches what the filter stage's dot-path
		// matcher expects: `items.price` strips price from every item.
		for _, el := range t {
			walkJSON(prefix, el, seen)
		}
	}
}

func extractXML(body []byte) (string, []string, error) {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(body); err != nil {
		return "", nil, err
	}
	root := doc.Root()
	if root == nil {
		return "", nil, errors.New("no root element")
	}
	seen := map[string]struct{}{}
	walkXML("", root, seen)
	return root.Tag, sortedKeys(seen), nil
}

func walkXML(prefix string, el *etree.Element, seen map[string]struct{}) {
	for _, child := range el.ChildElements() {
		p := child.Tag
		if prefix != "" {
			p = prefix + "." + child.Tag
		}
		seen[p] = struct{}{}
		walkXML(p, child, seen)
	}
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
