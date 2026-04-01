package tools

import (
	"encoding/json"
	"mqConnector/models"
	"testing"
)

func TestApplyTransforms_Rename(t *testing.T) {
	input := []byte(`{"name":"John","phone":"123-456-7890"}`)
	rules := []models.TransformRule{
		{TransformType: "rename", SourcePath: "phone", TargetPath: "contact_number", Order: 1},
	}

	result, err := ApplyTransforms(input, "JSON", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data map[string]interface{}
	json.Unmarshal(result, &data)

	if _, ok := data["phone"]; ok {
		t.Error("phone should be renamed")
	}
	if _, ok := data["contact_number"]; !ok {
		t.Error("contact_number should exist after rename")
	}
	if data["contact_number"] != "123-456-7890" {
		t.Errorf("contact_number value mismatch: got %v", data["contact_number"])
	}
}

func TestApplyTransforms_Mask(t *testing.T) {
	input := []byte(`{"name":"John","ssn":"123-45-6789"}`)
	rules := []models.TransformRule{
		{TransformType: "mask", SourcePath: "ssn", MaskPattern: `\d{3}-\d{2}`, MaskReplace: "***-**", Order: 1},
	}

	result, err := ApplyTransforms(input, "JSON", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data map[string]interface{}
	json.Unmarshal(result, &data)

	ssn := data["ssn"].(string)
	if ssn != "***-**-6789" {
		t.Errorf("expected '***-**-6789', got '%s'", ssn)
	}
}

func TestApplyTransforms_Move(t *testing.T) {
	input := []byte(`{"user":{"name":"John","email":"john@test.com"}}`)
	rules := []models.TransformRule{
		{TransformType: "move", SourcePath: "user.email", TargetPath: "contact.email", Order: 1},
	}

	result, err := ApplyTransforms(input, "JSON", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data map[string]interface{}
	json.Unmarshal(result, &data)

	// email should be moved from user to contact
	user := data["user"].(map[string]interface{})
	if _, ok := user["email"]; ok {
		t.Error("email should be removed from user")
	}

	contact, ok := data["contact"].(map[string]interface{})
	if !ok {
		t.Fatal("contact object should exist")
	}
	if contact["email"] != "john@test.com" {
		t.Errorf("expected email in contact, got %v", contact["email"])
	}
}

func TestApplyTransforms_EmptyRules(t *testing.T) {
	input := []byte(`{"name":"test"}`)
	result, err := ApplyTransforms(input, "JSON", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(input) {
		t.Error("empty rules should return unchanged message")
	}
}

func TestApplyTransforms_MultipleRules(t *testing.T) {
	input := []byte(`{"first":"John","last":"Doe","ssn":"123-45-6789"}`)
	rules := []models.TransformRule{
		{TransformType: "rename", SourcePath: "first", TargetPath: "firstName", Order: 1},
		{TransformType: "rename", SourcePath: "last", TargetPath: "lastName", Order: 2},
		{TransformType: "mask", SourcePath: "ssn", MaskPattern: `\d`, MaskReplace: "*", Order: 3},
	}

	result, err := ApplyTransforms(input, "JSON", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data map[string]interface{}
	json.Unmarshal(result, &data)

	if _, ok := data["firstName"]; !ok {
		t.Error("firstName should exist")
	}
	if _, ok := data["lastName"]; !ok {
		t.Error("lastName should exist")
	}
	if data["ssn"] != "***-**-****" {
		t.Errorf("ssn should be masked, got %v", data["ssn"])
	}
}

func TestGetNestedValue(t *testing.T) {
	data := map[string]interface{}{
		"user": map[string]interface{}{
			"name": "John",
			"address": map[string]interface{}{
				"city": "NYC",
			},
		},
	}

	val, err := getNestedValue(data, "user.name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "John" {
		t.Errorf("expected 'John', got %v", val)
	}

	val, err = getNestedValue(data, "user.address.city")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "NYC" {
		t.Errorf("expected 'NYC', got %v", val)
	}

	_, err = getNestedValue(data, "user.nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent path")
	}
}

func TestSetNestedValue(t *testing.T) {
	data := map[string]interface{}{}

	err := setNestedValue(data, "user.name", "John")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	val, err := getNestedValue(data, "user.name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "John" {
		t.Errorf("expected 'John', got %v", val)
	}
}
