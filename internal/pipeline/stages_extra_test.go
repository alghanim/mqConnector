package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mqConnector/internal/storage"
)

func TestFilterStage_JSON_RemovesPath(t *testing.T) {
	stage := &FilterStage{Paths: []string{"phone", "address.zip"}}
	in := []byte(`{"name":"John","phone":"123","address":{"city":"NYC","zip":"10001"}}`)
	out, _, _, err := stage.Execute(context.Background(), in, FormatJSON)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if _, ok := got["phone"]; ok {
		t.Error("phone should be removed")
	}
	if addr, ok := got["address"].(map[string]any); ok {
		if _, ok := addr["zip"]; ok {
			t.Error("address.zip should be removed")
		}
	}
}

func TestFilterStage_XML_RemovesElement(t *testing.T) {
	stage := &FilterStage{Paths: []string{"phone"}}
	in := []byte(`<?xml version="1.0"?><root><name>John</name><phone>123</phone></root>`)
	out, _, _, err := stage.Execute(context.Background(), in, FormatXML)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(string(out), "<phone>") {
		t.Error("phone element should be removed")
	}
}

func TestFilterStage_PassThroughForUnknownFormat(t *testing.T) {
	stage := &FilterStage{Paths: []string{"x"}}
	in := []byte("just bytes")
	out, _, _, err := stage.Execute(context.Background(), in, FormatBytes)
	if err != nil || string(out) != "just bytes" {
		t.Errorf("expected pass-through, got %q (err %v)", out, err)
	}
}

func TestFilterStage_EmptyPathsIsNoOp(t *testing.T) {
	stage := &FilterStage{}
	in := []byte(`{"a":1}`)
	out, _, _, _ := stage.Execute(context.Background(), in, FormatJSON)
	if string(out) != string(in) {
		t.Errorf("expected unchanged, got %q", out)
	}
}

func TestTranslateStage_JSONtoXML(t *testing.T) {
	stage := &TranslateStage{Target: FormatXML}
	in := []byte(`{"root":{"item":"x"}}`)
	out, fmtOut, _, err := stage.Execute(context.Background(), in, FormatJSON)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fmtOut != FormatXML {
		t.Errorf("format = %s, want xml", fmtOut)
	}
	if !strings.Contains(string(out), "<root>") {
		t.Errorf("expected xml output, got %q", out)
	}
}

func TestTranslateStage_SameIsNoOp(t *testing.T) {
	stage := &TranslateStage{Target: "same"}
	in := []byte(`{"a":1}`)
	out, fmtOut, _, _ := stage.Execute(context.Background(), in, FormatJSON)
	if string(out) != string(in) || fmtOut != FormatJSON {
		t.Errorf("expected no-op, got %q format %s", out, fmtOut)
	}
}

func TestValidateStage_JSONSchema_AcceptsValid(t *testing.T) {
	s := &ValidateStage{SchemaType: "json_schema", Content: `{"type":"object","required":["name"]}`}
	_, _, _, err := s.Execute(context.Background(), []byte(`{"name":"x"}`), FormatJSON)
	if err != nil {
		t.Errorf("valid message rejected: %v", err)
	}
}

func TestValidateStage_JSONSchema_RejectsMissingField(t *testing.T) {
	s := &ValidateStage{SchemaType: "json_schema", Content: `{"type":"object","required":["name"]}`}
	_, _, _, err := s.Execute(context.Background(), []byte(`{"other":1}`), FormatJSON)
	if err == nil {
		t.Error("expected rejection of missing field")
	}
}

func TestValidateStage_JSONSchemaOnXMLMessageErrors(t *testing.T) {
	s := &ValidateStage{SchemaType: "json_schema", Content: `{"type":"object"}`}
	_, _, _, err := s.Execute(context.Background(), []byte(`<x/>`), FormatXML)
	if err == nil {
		t.Error("expected error: json_schema requires JSON")
	}
}

func TestValidateStage_XSD_ChecksRequiredElements(t *testing.T) {
	s := &ValidateStage{SchemaType: "xsd", Content: "name\nemail"}
	_, _, _, err := s.Execute(context.Background(), []byte(`<root><name>x</name><email>y</email></root>`), FormatXML)
	if err != nil {
		t.Errorf("valid xml rejected: %v", err)
	}
	_, _, _, err = s.Execute(context.Background(), []byte(`<root><name>x</name></root>`), FormatXML)
	if err == nil {
		t.Error("missing element should be rejected")
	}
}

func TestValidateStage_UnknownSchemaTypeErrors(t *testing.T) {
	s := &ValidateStage{SchemaType: "yaml_schema", Content: "anything"}
	_, _, _, err := s.Execute(context.Background(), []byte(`{}`), FormatJSON)
	if err == nil {
		t.Error("expected error for unknown schema_type")
	}
}

func TestValidateStage_EmptyContentIsNoOp(t *testing.T) {
	s := &ValidateStage{SchemaType: "json_schema", Content: ""}
	in := []byte(`{}`)
	out, _, _, err := s.Execute(context.Background(), in, FormatJSON)
	if err != nil || string(out) != string(in) {
		t.Errorf("expected no-op for empty content: err %v, out %q", err, out)
	}
}

func TestRouteStage_AllEnabledRulesAccumulate(t *testing.T) {
	rules := []*storage.RoutingRule{
		{ConditionPath: "x", ConditionOperator: "eq", ConditionValue: "1", DestinationID: "A", Priority: 1, Enabled: true},
		{ConditionPath: "x", ConditionOperator: "eq", ConditionValue: "1", DestinationID: "B", Priority: 2, Enabled: true},
	}
	stage := &RouteStage{Rules: rules}
	_, _, res, err := stage.Execute(context.Background(), []byte(`{"x":"1"}`), FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || len(res.Destinations) != 2 {
		t.Fatalf("expected 2 destinations, got %+v", res)
	}
}

func TestRouteStage_DisabledRulesAreIgnored(t *testing.T) {
	rules := []*storage.RoutingRule{
		{ConditionPath: "x", ConditionOperator: "eq", ConditionValue: "1", DestinationID: "A", Enabled: false},
	}
	stage := &RouteStage{Rules: rules}
	_, _, res, _ := stage.Execute(context.Background(), []byte(`{"x":"1"}`), FormatJSON)
	if res != nil {
		t.Errorf("expected no result, got %+v", res)
	}
}

func TestRouteStage_DeduplicatesDestinations(t *testing.T) {
	rules := []*storage.RoutingRule{
		{ConditionPath: "x", ConditionOperator: "eq", ConditionValue: "1", DestinationID: "A", Enabled: true},
		{ConditionPath: "x", ConditionOperator: "contains", ConditionValue: "1", DestinationID: "A", Enabled: true},
	}
	stage := &RouteStage{Rules: rules}
	_, _, res, _ := stage.Execute(context.Background(), []byte(`{"x":"1"}`), FormatJSON)
	if res == nil || len(res.Destinations) != 1 {
		t.Fatalf("expected dedup to 1, got %+v", res)
	}
}

func TestTransformStage_DeleteAndSet(t *testing.T) {
	rules := []*storage.Transform{
		{TransformType: "delete", SourcePath: "phone", Order: 1},
		{TransformType: "set", SourcePath: "status", SetValue: "verified", Order: 2},
	}
	stage := &TransformStage{Rules: rules}
	out, _, _, err := stage.Execute(context.Background(), []byte(`{"name":"x","phone":"123"}`), FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if _, ok := got["phone"]; ok {
		t.Error("delete failed")
	}
	if got["status"] != "verified" {
		t.Errorf("set failed: %v", got["status"])
	}
}

func TestBuild_SkipsDisabledStages(t *testing.T) {
	stages, err := Build(BuildContext{
		Pipeline: &storage.Pipeline{ID: "p", FilterPaths: []string{}, OutputFormat: "same"},
		StageRows: []*storage.Stage{
			{StageType: "filter", Enabled: false},
			{StageType: "translate", Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 1 || stages[0].Name() != "translate" {
		t.Errorf("expected disabled filter to be skipped, got %v", stages)
	}
}

func TestBuild_UnknownStageTypeErrors(t *testing.T) {
	_, err := Build(BuildContext{
		Pipeline: &storage.Pipeline{ID: "p"},
		StageRows: []*storage.Stage{
			{StageType: "gibberish", Enabled: true},
		},
	})
	if err == nil {
		t.Error("expected error for unknown stage type")
	}
}

// TestDetect already lives in format_test.go.
