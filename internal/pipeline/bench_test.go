package pipeline

import (
	"context"
	"strings"
	"testing"

	"mqConnector/internal/storage"
)

// Benchmarks for the per-message pipeline stages. Each runs against a 4 KB
// JSON document representative of an order/customer payload.

func makeJSONPayload(b *testing.B) []byte {
	b.Helper()
	var sb strings.Builder
	sb.WriteString(`{"id":"O-1","customer":{"name":"Alice","phone":"+97412345678","email":"a@example.com"},`)
	sb.WriteString(`"items":[`)
	for i := 0; i < 30; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"sku":"SKU-` + strings.Repeat("X", 5) + `","qty":1,"price":12.5}`)
	}
	sb.WriteString(`],"total":375.0,"meta":{"channel":"web","locale":"en"}}`)
	return []byte(sb.String())
}

func makeXMLPayload(b *testing.B) []byte {
	b.Helper()
	var sb strings.Builder
	sb.WriteString(`<order><id>O-1</id><customer><name>Alice</name><phone>+97412345678</phone></customer><items>`)
	for i := 0; i < 30; i++ {
		sb.WriteString(`<item><sku>SKU-XXXXX</sku><qty>1</qty><price>12.5</price></item>`)
	}
	sb.WriteString(`</items><total>375.0</total></order>`)
	return []byte(sb.String())
}

func BenchmarkDetect_JSON(b *testing.B) {
	payload := makeJSONPayload(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Detect(payload)
	}
}

func BenchmarkDetect_XML(b *testing.B) {
	payload := makeXMLPayload(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Detect(payload)
	}
}

func BenchmarkFilterStage_JSON(b *testing.B) {
	stage := &FilterStage{Paths: []string{"customer.phone", "meta.locale"}}
	payload := makeJSONPayload(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = stage.Execute(ctx, payload, FormatJSON)
	}
}

func BenchmarkFilterStage_XML(b *testing.B) {
	stage := &FilterStage{Paths: []string{"phone"}}
	payload := makeXMLPayload(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = stage.Execute(ctx, payload, FormatXML)
	}
}

func BenchmarkTransformStage_RenameAndMask(b *testing.B) {
	rules := []*storage.Transform{
		{TransformType: "rename", SourcePath: "customer.phone", TargetPath: "contact_phone", Order: 1},
		{TransformType: "mask", SourcePath: "customer.email", MaskPattern: `[^@]+@`, MaskReplace: "***@", Order: 2},
	}
	stage := &TransformStage{Rules: rules}
	payload := makeJSONPayload(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = stage.Execute(ctx, payload, FormatJSON)
	}
}

func BenchmarkTranslateStage_JSONtoXML(b *testing.B) {
	stage := &TranslateStage{Target: FormatXML}
	payload := makeJSONPayload(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = stage.Execute(ctx, payload, FormatJSON)
	}
}

func BenchmarkRouteStage_FiveRules(b *testing.B) {
	rules := []*storage.RoutingRule{
		{ConditionPath: "meta.channel", ConditionOperator: "eq", ConditionValue: "web", DestinationID: "A", Priority: 1, Enabled: true},
		{ConditionPath: "meta.channel", ConditionOperator: "neq", ConditionValue: "phone", DestinationID: "B", Priority: 2, Enabled: true},
		{ConditionPath: "total", ConditionOperator: "gt", ConditionValue: "100", DestinationID: "C", Priority: 3, Enabled: true},
		{ConditionPath: "id", ConditionOperator: "contains", ConditionValue: "O-", DestinationID: "D", Priority: 4, Enabled: true},
		{ConditionPath: "id", ConditionOperator: "regex", ConditionValue: `^O-[0-9]+$`, DestinationID: "E", Priority: 5, Enabled: true},
	}
	stage := &RouteStage{Rules: rules}
	payload := makeJSONPayload(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = stage.Execute(ctx, payload, FormatJSON)
	}
}

func BenchmarkScriptStage_SimpleAssignAndDelete(b *testing.B) {
	stage := &ScriptStage{Script: `msg.processed = true; delete msg.customer.phone;`}
	payload := makeJSONPayload(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = stage.Execute(ctx, payload, FormatJSON)
	}
}

func BenchmarkValidateStage_JSONSchema(b *testing.B) {
	schema := `{"type":"object","required":["id","customer","items"],"properties":{` +
		`"id":{"type":"string"},"total":{"type":"number"},` +
		`"customer":{"type":"object","required":["name"]}}}`
	stage := &ValidateStage{SchemaType: "json_schema", Content: schema}
	payload := makeJSONPayload(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = stage.Execute(ctx, payload, FormatJSON)
	}
}
