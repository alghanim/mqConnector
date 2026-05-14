package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	tests := map[string]Format{
		`{"k":"v"}`:           FormatJSON,
		` [1,2,3] `:           FormatJSON,
		`<root><k>v</k></root>`: FormatXML,
		` <?xml ?><a/>`:       FormatXML,
		`hello world`:         FormatBytes,
		``:                    FormatBytes,
	}
	for in, want := range tests {
		got := Detect([]byte(in))
		if got != want {
			t.Errorf("Detect(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestTranslateFormat_SameNoop(t *testing.T) {
	msg := []byte(`{"a":1}`)
	out, f, err := TranslateFormat(msg, FormatJSON, "same")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(msg) || f != FormatJSON {
		t.Errorf("noop failed: %s / %s", out, f)
	}
}

func TestTranslateFormat_XMLToJSON(t *testing.T) {
	out, f, err := TranslateFormat([]byte(`<order><id>5</id></order>`), FormatXML, FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	if f != FormatJSON {
		t.Errorf("format = %s", f)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, out)
	}
	if _, ok := m["order"]; !ok {
		t.Errorf("missing order key: %s", out)
	}
}

func TestTranslateFormat_JSONToXML(t *testing.T) {
	out, f, err := TranslateFormat([]byte(`{"order":{"id":5}}`), FormatJSON, FormatXML)
	if err != nil {
		t.Fatal(err)
	}
	if f != FormatXML {
		t.Errorf("format = %s", f)
	}
	if !strings.Contains(string(out), "<order>") {
		t.Errorf("missing <order>: %s", out)
	}
}

func TestTranslateFormat_Unsupported(t *testing.T) {
	if _, _, err := TranslateFormat([]byte("x"), "yaml", FormatJSON); err == nil {
		t.Error("expected error for unsupported translation")
	}
}
