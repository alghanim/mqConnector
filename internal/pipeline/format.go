// Package pipeline implements the message-processing chain that mqConnector
// applies between source and destination MQs. Stages are sequentially
// executed; their order, configuration, and enablement are persisted in the
// database and reloaded on demand by the Manager.
package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/clbanning/mxj/v2"
)

// Format identifies the wire format of a message.
type Format string

const (
	FormatJSON Format = "json"
	FormatXML  Format = "xml"
	// FormatBytes is the catch-all when neither JSON nor XML matched. Messages
	// in this format pass through stages largely unchanged.
	FormatBytes Format = "bytes"
)

// Detect classifies a message as JSON, XML, or raw bytes.
func Detect(message []byte) Format {
	if isJSON(message) {
		return FormatJSON
	}
	if isXML(message) {
		return FormatXML
	}
	return FormatBytes
}

// isJSON checks the first non-whitespace byte against the JSON grammar's
// possible starts. Detection is best-effort — well-formedness is enforced
// downstream by stages that actually decode the message. The previous
// implementation called json.Unmarshal into a RawMessage on every detection,
// which dominated profiles at ~5 µs / 1.6 KB / 6 allocs per call regardless
// of message size. This version is O(1) in both time and allocations.
func isJSON(b []byte) bool {
	b = trimLeftSpace(b)
	if len(b) == 0 {
		return false
	}
	switch b[0] {
	case '{', '[', '"', 't', 'f', 'n', '-':
		return true
	}
	return b[0] >= '0' && b[0] <= '9'
}

// isXML checks the first non-whitespace bytes for an XML opener. Like
// isJSON, this is intentionally best-effort: full well-formedness is the
// concern of stages that actually parse. The prior implementation ran
// xml.NewDecoder + Token() until it found a StartElement, which cost ~500 ns
// and 9 allocs per call on a 2 KB payload. This version is O(1).
func isXML(b []byte) bool {
	b = trimLeftSpace(b)
	if len(b) < 2 || b[0] != '<' {
		return false
	}
	// Permit: <tag>, <tag/>, <?xml...?>, <!DOCTYPE...>, <!--...-->
	c := b[1]
	if c == '?' || c == '!' {
		return true
	}
	// Tag names must start with a letter or underscore (XML 1.0 §2.3).
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func trimLeftSpace(b []byte) []byte {
	i := 0
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return b[i:]
		}
	}
	return nil
}

// TranslateFormat converts a message between JSON and XML. "same" or matching
// formats short-circuit and return the input unchanged.
func TranslateFormat(message []byte, src, dst Format) ([]byte, Format, error) {
	if dst == "" || dst == "same" || dst == src {
		return message, src, nil
	}
	switch {
	case src == FormatXML && dst == FormatJSON:
		mv, err := mxj.NewMapXml(message)
		if err != nil {
			return nil, src, fmt.Errorf("translate xml→json: parse: %w", err)
		}
		out, err := json.Marshal(mv)
		if err != nil {
			return nil, src, fmt.Errorf("translate xml→json: marshal: %w", err)
		}
		return out, FormatJSON, nil
	case src == FormatJSON && dst == FormatXML:
		var data map[string]any
		if err := json.Unmarshal(message, &data); err != nil {
			return nil, src, fmt.Errorf("translate json→xml: parse: %w", err)
		}
		out, err := mxj.Map(data).Xml()
		if err != nil {
			return nil, src, fmt.Errorf("translate json→xml: marshal: %w", err)
		}
		return out, FormatXML, nil
	default:
		return nil, src, fmt.Errorf("translate %s→%s: unsupported", src, dst)
	}
}

// ErrSkipMessage is returned by a stage to indicate the current message
// should be silently dropped (e.g. predicate filters that don't match).
var ErrSkipMessage = errors.New("pipeline: skip message")
