package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTranslateFormat_SameFormat(t *testing.T) {
	msg := []byte(`{"name":"test"}`)

	result, err := TranslateFormat(msg, "JSON", "same")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(msg) {
		t.Error("same format should return unchanged message")
	}

	result, err = TranslateFormat(msg, "JSON", "JSON")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(msg) {
		t.Error("matching format should return unchanged message")
	}

	result, err = TranslateFormat(msg, "JSON", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(msg) {
		t.Error("empty format should return unchanged message")
	}
}

func TestTranslateFormat_XMLToJSON(t *testing.T) {
	xmlMsg := []byte(`<root><name>John</name><age>30</age></root>`)

	result, err := TranslateFormat(xmlMsg, "XML", "JSON")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Result should be valid JSON
	var data map[string]interface{}
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	// Should contain root element
	if _, ok := data["root"]; !ok {
		t.Error("expected 'root' key in JSON output")
	}
}

func TestTranslateFormat_JSONToXML(t *testing.T) {
	jsonMsg := []byte(`{"order":{"item":"book","qty":5}}`)

	result, err := TranslateFormat(jsonMsg, "JSON", "XML")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Result should contain XML elements
	resultStr := string(result)
	if !strings.Contains(resultStr, "<order>") && !strings.Contains(resultStr, "<order ") {
		t.Error("expected XML to contain <order> element")
	}
}

func TestTranslateFormat_UnsupportedTranslation(t *testing.T) {
	msg := []byte("test")
	_, err := TranslateFormat(msg, "YAML", "JSON")
	if err == nil {
		t.Error("expected error for unsupported translation")
	}
}
