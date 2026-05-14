package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mqConnector/internal/storage"
)

func TestFilterStage_JSON(t *testing.T) {
	s := &FilterStage{Paths: []string{"phone", "address.zip"}}
	in := []byte(`{"name":"a","phone":"123","address":{"city":"x","zip":"00000"}}`)
	out, _, _, err := s.Execute(context.Background(), in, FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "phone") {
		t.Error("phone should be removed")
	}
	if strings.Contains(string(out), "zip") {
		t.Error("zip should be removed")
	}
	if !strings.Contains(string(out), "city") {
		t.Error("city should remain")
	}
}

func TestFilterStage_XML(t *testing.T) {
	s := &FilterStage{Paths: []string{"phone"}}
	in := []byte(`<root><name>a</name><phone>123</phone></root>`)
	out, _, _, err := s.Execute(context.Background(), in, FormatXML)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "<phone>") {
		t.Error("phone element should be removed")
	}
}

func TestTransformStage_Rename(t *testing.T) {
	s := &TransformStage{Rules: []*storage.Transform{
		{TransformType: "rename", SourcePath: "phone", TargetPath: "contact"},
	}}
	in := []byte(`{"phone":"123"}`)
	out, _, _, err := s.Execute(context.Background(), in, FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if _, ok := m["contact"]; !ok || m["contact"] != "123" {
		t.Errorf("rename failed: %v", m)
	}
}

func TestTransformStage_Mask(t *testing.T) {
	s := &TransformStage{Rules: []*storage.Transform{
		{TransformType: "mask", SourcePath: "ssn", MaskPattern: `\d{3}`, MaskReplace: "***", Order: 1},
	}}
	out, _, _, err := s.Execute(context.Background(), []byte(`{"ssn":"123-45"}`), FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["ssn"] != "***-45" {
		t.Errorf("mask: %v", m["ssn"])
	}
}

func TestRouteStage_Eq(t *testing.T) {
	s := &RouteStage{Rules: []*storage.RoutingRule{
		{ConditionPath: "region", ConditionOperator: "eq", ConditionValue: "EU", DestinationID: "eu", Enabled: true},
		{ConditionPath: "region", ConditionOperator: "eq", ConditionValue: "US", DestinationID: "us", Enabled: true},
	}}
	out, _, res, err := s.Execute(context.Background(), []byte(`{"region":"EU"}`), FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) == "" {
		t.Error("message should pass through")
	}
	if res == nil || len(res.Destinations) != 1 || res.Destinations[0] != "eu" {
		t.Errorf("unexpected destinations: %+v", res)
	}
}

func TestRouteStage_NoMatch(t *testing.T) {
	s := &RouteStage{Rules: []*storage.RoutingRule{
		{ConditionPath: "region", ConditionOperator: "eq", ConditionValue: "EU", DestinationID: "eu", Enabled: true},
	}}
	_, _, res, err := s.Execute(context.Background(), []byte(`{"region":"APAC"}`), FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	if res != nil && len(res.Destinations) > 0 {
		t.Errorf("expected no destinations on no-match, got %+v", res)
	}
}

func TestRouteStage_DisabledIgnored(t *testing.T) {
	s := &RouteStage{Rules: []*storage.RoutingRule{
		{ConditionPath: "x", ConditionOperator: "eq", ConditionValue: "y", DestinationID: "z", Enabled: false},
	}}
	_, _, res, err := s.Execute(context.Background(), []byte(`{"x":"y"}`), FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	if res != nil && len(res.Destinations) > 0 {
		t.Error("disabled rule matched")
	}
}

func TestScriptStage_Assign(t *testing.T) {
	s := &ScriptStage{Script: `msg.processed = true; msg.total = msg.price * msg.qty`}
	out, _, _, err := s.Execute(context.Background(), []byte(`{"price":10,"qty":5}`), FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["processed"] != true {
		t.Errorf("processed: %v", m["processed"])
	}
	if m["total"] != float64(50) {
		t.Errorf("total: %v", m["total"])
	}
}

func TestScriptStage_Delete(t *testing.T) {
	s := &ScriptStage{Script: "delete msg.secret"}
	out, _, _, err := s.Execute(context.Background(), []byte(`{"keep":1,"secret":"x"}`), FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if _, ok := m["secret"]; ok {
		t.Error("secret should be deleted")
	}
	if m["keep"] != float64(1) {
		t.Errorf("keep: %v", m["keep"])
	}
}

func TestValidateStage_JSONSchema_RequiredOK(t *testing.T) {
	s := &ValidateStage{SchemaType: "json_schema", Content: `{"type":"object","required":["email"]}`}
	_, _, _, err := s.Execute(context.Background(), []byte(`{"email":"a@b.c"}`), FormatJSON)
	if err != nil {
		t.Errorf("expected ok, got %v", err)
	}
}

func TestValidateStage_JSONSchema_MissingRequired(t *testing.T) {
	s := &ValidateStage{SchemaType: "json_schema", Content: `{"type":"object","required":["email"]}`}
	_, _, _, err := s.Execute(context.Background(), []byte(`{}`), FormatJSON)
	if err == nil {
		t.Error("expected validation error")
	}
}

func TestValidateStage_XSD_MissingElement(t *testing.T) {
	s := &ValidateStage{SchemaType: "xsd", Content: "name\nphone"}
	_, _, _, err := s.Execute(context.Background(), []byte(`<r><name>n</name></r>`), FormatXML)
	if err == nil {
		t.Error("expected missing-element error")
	}
}
