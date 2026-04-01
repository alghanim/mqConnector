package validation

import (
	"testing"
)

func TestValidateJSONSchema_RequiredFields(t *testing.T) {
	schema := `{
		"type": "object",
		"required": ["name", "email"],
		"properties": {
			"name": {"type": "string"},
			"email": {"type": "string"}
		}
	}`

	// Valid message
	msg := []byte(`{"name":"John","email":"john@test.com"}`)
	err := ValidateJSONSchema(msg, "JSON", schema)
	if err != nil {
		t.Errorf("expected valid, got error: %v", err)
	}

	// Missing required field
	msg = []byte(`{"name":"John"}`)
	err = ValidateJSONSchema(msg, "JSON", schema)
	if err == nil {
		t.Error("expected error for missing required field 'email'")
	}
}

func TestValidateJSONSchema_TypeChecking(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"count": {"type": "number"},
			"active": {"type": "boolean"}
		}
	}`

	// Valid types
	msg := []byte(`{"count":42,"active":true}`)
	err := ValidateJSONSchema(msg, "JSON", schema)
	if err != nil {
		t.Errorf("expected valid, got error: %v", err)
	}

	// Wrong type
	msg = []byte(`{"count":"not a number","active":true}`)
	err = ValidateJSONSchema(msg, "JSON", schema)
	if err == nil {
		t.Error("expected error for wrong type")
	}
}

func TestValidateJSONSchema_NonJSON(t *testing.T) {
	schema := `{"type":"object"}`
	msg := []byte(`<xml/>`)
	err := ValidateJSONSchema(msg, "XML", schema)
	if err == nil {
		t.Error("expected error for non-JSON format")
	}
}

func TestValidateXMLStructure_WellFormed(t *testing.T) {
	// Well-formed XML with required elements
	schema := "name\nemail"
	msg := []byte(`<root><name>John</name><email>john@test.com</email></root>`)

	err := ValidateXMLStructure(msg, "XML", schema)
	if err != nil {
		t.Errorf("expected valid, got error: %v", err)
	}
}

func TestValidateXMLStructure_MissingElement(t *testing.T) {
	schema := "name\nemail\nphone"
	msg := []byte(`<root><name>John</name><email>john@test.com</email></root>`)

	err := ValidateXMLStructure(msg, "XML", schema)
	if err == nil {
		t.Error("expected error for missing required element 'phone'")
	}
}

func TestValidateXMLStructure_MalformedXML(t *testing.T) {
	schema := "name"
	msg := []byte(`<root><name>unclosed`)

	err := ValidateXMLStructure(msg, "XML", schema)
	if err == nil {
		t.Error("expected error for malformed XML")
	}
}

func TestValidateXMLStructure_NonXML(t *testing.T) {
	err := ValidateXMLStructure([]byte(`{"json":true}`), "JSON", "name")
	if err == nil {
		t.Error("expected error for non-XML format")
	}
}

func TestValidate_Dispatch(t *testing.T) {
	// Empty schema should pass
	err := Validate([]byte(`{"test":true}`), "JSON", "", "")
	if err != nil {
		t.Errorf("empty schema should pass, got error: %v", err)
	}

	// Unsupported schema type
	err = Validate([]byte(`test`), "JSON", "YAML_SCHEMA", "content")
	if err == nil {
		t.Error("expected error for unsupported schema type")
	}
}

func TestParseRequiredElements(t *testing.T) {
	content := "name\n# comment\nemail\n  \nphone"
	elements := parseRequiredElements(content)

	if len(elements) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(elements))
	}
	expected := []string{"name", "email", "phone"}
	for i, elem := range expected {
		if elements[i] != elem {
			t.Errorf("element[%d] = %s, want %s", i, elements[i], elem)
		}
	}
}
