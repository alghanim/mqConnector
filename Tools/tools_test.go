package tools

import (
	"testing"
)

func TestDetectFormat_JSON(t *testing.T) {
	msg := []byte(`{"name": "test", "value": 123}`)
	format, err := DetectFormat(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if format != "JSON" {
		t.Errorf("expected JSON, got %s", format)
	}
}

func TestDetectFormat_XML(t *testing.T) {
	msg := []byte(`<?xml version="1.0"?><root><name>test</name></root>`)
	format, err := DetectFormat(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if format != "XML" {
		t.Errorf("expected XML, got %s", format)
	}
}

func TestDetectFormat_Unknown(t *testing.T) {
	msg := []byte(`this is not json or xml`)
	_, err := DetectFormat(msg)
	if err == nil {
		t.Error("expected error for unknown format")
	}
}

func TestIsJSON(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{`{"key": "value"}`, true},
		{`[1,2,3]`, true},
		{`"string"`, true},
		{`123`, true},
		{`not json`, false},
		{`<xml/>`, false},
	}
	for _, tt := range tests {
		result := IsJSON([]byte(tt.input))
		if result != tt.expected {
			t.Errorf("IsJSON(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

func TestRemoveJSONPaths(t *testing.T) {
	input := []byte(`{"name":"John","phone":"123","address":{"city":"NYC","zip":"10001"}}`)

	// Remove top-level field
	result, err := RemoveJSONPaths(input, []string{"phone"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if IsFieldPresent(result, "phone") {
		t.Error("phone field should be removed")
	}
	if !IsFieldPresent(result, "name") {
		t.Error("name field should still exist")
	}

	// Remove nested field
	result, err = RemoveJSONPaths(input, []string{"address.zip"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if IsFieldPresent(result, "zip") {
		t.Error("zip field should be removed")
	}
}

func TestRemoveJSONPaths_InvalidPath(t *testing.T) {
	input := []byte(`{"name":"John"}`)
	result, err := RemoveJSONPaths(input, []string{"nonexistent.path"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not error, just skip
	if string(result) != `{"name":"John"}` {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestGetRootTag(t *testing.T) {
	xml := []byte(`<?xml version="1.0"?><order><item>test</item></order>`)
	tag, err := GetRootTag(xml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "order" {
		t.Errorf("expected 'order', got '%s'", tag)
	}
}

func TestGetRootTag_NoRoot(t *testing.T) {
	_, err := GetRootTag([]byte("no xml here"))
	if err == nil {
		t.Error("expected error for non-XML input")
	}
}

func TestRemoveElements(t *testing.T) {
	xmlStr := `<?xml version="1.0"?><root xmlns:ns="http://test"><ns:name>John</ns:name><ns:phone>123</ns:phone></root>`

	doc := newEtreeDocument(xmlStr)
	if doc == nil {
		t.Fatal("failed to parse XML")
	}

	RemoveElements(doc.Root(), "ns", "phone")

	result, err := doc.WriteToString()
	if err != nil {
		t.Fatalf("error writing XML: %v", err)
	}

	if containsTag(result, "phone") {
		t.Error("phone element should be removed")
	}
	if !containsTag(result, "name") {
		t.Error("name element should still exist")
	}
}

func TestExtractPaths(t *testing.T) {
	xml := []byte(`<root><name>John</name><address><city>NYC</city></address></root>`)
	paths := ExtractPaths(xml)

	if len(paths) == 0 {
		t.Fatal("expected paths to be extracted")
	}

	pathMap := make(map[string]bool)
	for _, p := range paths {
		pathMap[p.FieldPath] = true
	}

	if !pathMap["name"] {
		t.Error("expected 'name' path")
	}
	if !pathMap["address.city"] {
		t.Error("expected 'address.city' path")
	}
}

// Helpers
func IsFieldPresent(jsonData []byte, field string) bool {
	return containsString(string(jsonData), `"`+field+`"`)
}

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func containsTag(xml, tag string) bool {
	return containsString(xml, "<") && containsString(xml, tag)
}

func newEtreeDocument(xmlStr string) *etreeDoc {
	doc := newEtreeDocFromString(xmlStr)
	return doc
}

type etreeDoc = etree.Document

func newEtreeDocFromString(xmlStr string) *etree.Document {
	doc := etree.NewDocument()
	if err := doc.ReadFromString(xmlStr); err != nil {
		return nil
	}
	return doc
}
