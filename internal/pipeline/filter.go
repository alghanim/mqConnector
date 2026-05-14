package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/beevik/etree"
)

// FilterStage removes specified dot-separated paths from the message.
type FilterStage struct {
	Paths []string
}

func (s *FilterStage) Name() string { return "filter" }

func (s *FilterStage) Execute(_ context.Context, message []byte, format Format) ([]byte, Format, *Result, error) {
	if len(s.Paths) == 0 {
		return message, format, nil, nil
	}
	switch format {
	case FormatJSON:
		var data map[string]any
		if err := json.Unmarshal(message, &data); err != nil {
			return nil, format, nil, fmt.Errorf("filter: parse json: %w", err)
		}
		removeJSONPaths(data, s.Paths)
		out, err := json.Marshal(data)
		if err != nil {
			return nil, format, nil, fmt.Errorf("filter: marshal json: %w", err)
		}
		return out, format, nil, nil
	case FormatXML:
		doc := etree.NewDocument()
		if err := doc.ReadFromBytes(message); err != nil {
			return nil, format, nil, fmt.Errorf("filter: parse xml: %w", err)
		}
		root := doc.Root()
		if root == nil {
			return message, format, nil, nil
		}
		ns := root.Space
		for _, p := range s.Paths {
			removeXMLElements(root, ns, p)
		}
		out, err := doc.WriteToBytes()
		if err != nil {
			return nil, format, nil, fmt.Errorf("filter: write xml: %w", err)
		}
		return out, format, nil, nil
	default:
		return message, format, nil, nil
	}
}

func removeXMLElements(elem *etree.Element, namespace, tag string) {
	for _, child := range elem.ChildElements() {
		if child.Space == namespace && child.Tag == tag {
			elem.RemoveChild(child)
			continue
		}
		removeXMLElements(child, namespace, tag)
	}
}
