package sample

import (
	"strings"
	"testing"

	"mqConnector/internal/pipeline"
)

func TestExtract_JSON_NestedObject(t *testing.T) {
	body := []byte(`{
		"id": "order-1",
		"customer": {"name": "alice", "address": {"city": "doha"}},
		"items": [
			{"sku": "A", "price": 10},
			{"sku": "B", "price": 20}
		]
	}`)
	got, err := Extract(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != pipeline.FormatJSON {
		t.Errorf("format = %s", got.Format)
	}
	want := []string{
		"customer", "customer.address", "customer.address.city", "customer.name",
		"id", "items", "items.price", "items.sku",
	}
	if strings.Join(got.Paths, ",") != strings.Join(want, ",") {
		t.Errorf("paths = %v, want %v", got.Paths, want)
	}
}

func TestExtract_XML_RootAndPaths(t *testing.T) {
	body := []byte(`<order>
		<id>order-1</id>
		<customer><name>alice</name><address><city>doha</city></address></customer>
		<items><item><sku>A</sku><price>10</price></item></items>
	</order>`)
	got, err := Extract(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != pipeline.FormatXML {
		t.Errorf("format = %s", got.Format)
	}
	if got.RootTag != "order" {
		t.Errorf("root = %s", got.RootTag)
	}
	// Each unique dot-path appears exactly once.
	for _, want := range []string{
		"customer", "customer.name", "customer.address", "customer.address.city",
		"id", "items", "items.item", "items.item.sku", "items.item.price",
	} {
		found := false
		for _, p := range got.Paths {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("path %q missing from %v", want, got.Paths)
		}
	}
}

func TestExtract_PlainBytes(t *testing.T) {
	got, err := Extract([]byte("just text, no structure"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != pipeline.FormatBytes {
		t.Errorf("format = %s", got.Format)
	}
	if len(got.Paths) != 0 {
		t.Errorf("expected no paths, got %v", got.Paths)
	}
}

func TestExtract_TooLarge(t *testing.T) {
	body := make([]byte, MaxSize+1)
	_, err := Extract(body)
	if err != ErrTooLarge {
		t.Errorf("want ErrTooLarge, got %v", err)
	}
}

func TestExtract_BadJSON_ReturnsError(t *testing.T) {
	// Looks like JSON to the detector but isn't well-formed.
	_, err := Extract([]byte(`{"unterminated":`))
	if err == nil {
		t.Error("expected error on malformed JSON")
	}
}
