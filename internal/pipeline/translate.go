package pipeline

import "context"

// TranslateStage converts the message format. Target may be "same" to act as
// a no-op (useful when the stage is left in a pipeline but its config defers
// to the pipeline-level output_format).
type TranslateStage struct {
	Target Format
}

func (s *TranslateStage) Name() string { return "translate" }

func (s *TranslateStage) Execute(_ context.Context, message []byte, format Format) ([]byte, Format, *Result, error) {
	out, newFormat, err := TranslateFormat(message, format, s.Target)
	if err != nil {
		return nil, format, nil, err
	}
	return out, newFormat, nil, nil
}
