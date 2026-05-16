package pipeline

import (
	"context"
	"fmt"
)

// TranslateStage converts the message format. Target may be "same" to act as
// a no-op (useful when the stage is left in a pipeline but its config defers
// to the pipeline-level output_format).
//
// Proto is non-nil only when the operator wired a protobuf schema into
// the stage config. Routing logic:
//
//	src=protobuf, dst=json  → Proto.ToJSON(message)
//	src=json,     dst=protobuf → Proto.FromJSON(message)
//	src=anything, dst=protobuf without Proto → error (we can't encode
//	  to a wire format we don't have a schema for)
//
// Anything else falls through to TranslateFormat for the existing
// JSON/XML matrix.
type TranslateStage struct {
	Target Format
	Proto  *ProtoSchema // optional; only used when protobuf is on either side
}

func (s *TranslateStage) Name() string { return "translate" }

func (s *TranslateStage) Execute(_ context.Context, message []byte, format Format) ([]byte, Format, *Result, error) {
	target := s.Target
	if target == "" || target == "same" || target == format {
		return message, format, nil, nil
	}

	// Protobuf branches first — they need the schema; the others are
	// schema-free.
	switch {
	case format == FormatProtobuf && target == FormatJSON:
		if s.Proto == nil {
			return nil, format, nil, fmt.Errorf("translate proto→json: no schema configured")
		}
		out, err := s.Proto.ToJSON(message)
		if err != nil {
			return nil, format, nil, err
		}
		return out, FormatJSON, nil, nil

	case format == FormatJSON && target == FormatProtobuf:
		if s.Proto == nil {
			return nil, format, nil, fmt.Errorf("translate json→proto: no schema configured")
		}
		out, err := s.Proto.FromJSON(message)
		if err != nil {
			return nil, format, nil, err
		}
		return out, FormatProtobuf, nil, nil

	case target == FormatProtobuf:
		return nil, format, nil, fmt.Errorf("translate %s→protobuf: no schema configured", format)
	}

	// Fall through to the JSON/XML matrix.
	out, newFormat, err := TranslateFormat(message, format, target)
	if err != nil {
		return nil, format, nil, err
	}
	return out, newFormat, nil, nil
}
