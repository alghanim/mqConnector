// Package pipeline implements the message-processing chain that mqConnector
// applies between source and destination MQs. Stages are sequentially
// executed; their order, configuration, and enablement are persisted in the
// database and reloaded on demand by the Manager.
package pipeline

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

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

func isJSON(b []byte) bool {
	b = trimLeftSpace(b)
	if len(b) == 0 {
		return false
	}
	if b[0] != '{' && b[0] != '[' && b[0] != '"' && (b[0] < '0' || b[0] > '9') && b[0] != 't' && b[0] != 'f' && b[0] != 'n' {
		return false
	}
	var raw json.RawMessage
	return json.Unmarshal(b, &raw) == nil
}

func isXML(b []byte) bool {
	b = trimLeftSpace(b)
	if len(b) == 0 || b[0] != '<' {
		return false
	}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false
		}
		if _, ok := tok.(xml.StartElement); ok {
			return true
		}
	}
	return false
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
