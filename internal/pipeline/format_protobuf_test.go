package pipeline

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// buildTestDescriptor synthesises a minimal FileDescriptorSet for a
// message:
//
//	syntax = "proto3";
//	package mqctest;
//	message Order {
//	  string id   = 1;
//	  string name = 2;
//	  int32  qty  = 3;
//	}
//
// We build the descriptor in code (rather than calling protoc) so the
// test has zero out-of-tree deps. Returns the base64-encoded FDS and
// the FQN of the message.
func buildTestDescriptor(t *testing.T) (string, string) {
	t.Helper()
	pkg := "mqctest"
	syntax := "proto3"
	msgName := "Order"
	fqn := pkg + "." + msgName

	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	i32 := descriptorpb.FieldDescriptorProto_TYPE_INT32
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	num := func(n int32) *int32 { return &n }

	fileProto := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("order.proto"),
		Package: proto.String(pkg),
		Syntax:  proto.String(syntax),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String(msgName),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("id"), Number: num(1), Type: &str, Label: &opt, JsonName: proto.String("id")},
					{Name: proto.String("name"), Number: num(2), Type: &str, Label: &opt, JsonName: proto.String("name")},
					{Name: proto.String("qty"), Number: num(3), Type: &i32, Label: &opt, JsonName: proto.String("qty")},
				},
			},
		},
	}
	fds := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fileProto}}
	raw, err := proto.Marshal(fds)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw), fqn
}

// buildOrderMessage returns the wire-format bytes of a sample Order
// message. We use dynamicpb so the test doesn't need the generated
// Go bindings.
func buildOrderMessage(t *testing.T, fdsBase64, fqn, id, name string, qty int32) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(fdsBase64)
	if err != nil {
		t.Fatal(err)
	}
	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &fds); err != nil {
		t.Fatal(err)
	}
	files, err := protodesc.NewFiles(&fds)
	if err != nil {
		t.Fatal(err)
	}
	desc, err := files.FindDescriptorByName(protoreflect.FullName(fqn))
	if err != nil {
		t.Fatal(err)
	}
	md := desc.(protoreflect.MessageDescriptor)
	msg := dynamicpb.NewMessage(md)
	msg.Set(md.Fields().ByName("id"), protoreflect.ValueOfString(id))
	msg.Set(md.Fields().ByName("name"), protoreflect.ValueOfString(name))
	msg.Set(md.Fields().ByName("qty"), protoreflect.ValueOfInt32(qty))
	out, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProtoSchema_LoadAndValidate(t *testing.T) {
	fds, fqn := buildTestDescriptor(t)

	schema, err := LoadProtoSchema(fds, fqn)
	if err != nil {
		t.Fatal(err)
	}
	body := buildOrderMessage(t, fds, fqn, "abc", "widget", 7)
	if err := schema.Validate(body); err != nil {
		t.Errorf("validate good message: %v", err)
	}
	// Garbage bytes must fail.
	if err := schema.Validate([]byte("not-a-proto-message")); err == nil {
		t.Error("expected validate to reject garbage bytes")
	}
}

func TestProtoSchema_RoundTripJSON(t *testing.T) {
	fds, fqn := buildTestDescriptor(t)
	schema, err := LoadProtoSchema(fds, fqn)
	if err != nil {
		t.Fatal(err)
	}
	body := buildOrderMessage(t, fds, fqn, "abc", "widget", 7)

	jsonBytes, err := schema.ToJSON(body)
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	got := string(jsonBytes)
	for _, want := range []string{`"id":"abc"`, `"name":"widget"`, `"qty":7`} {
		if !strings.Contains(got, want) {
			t.Errorf("ToJSON missing %q: %s", want, got)
		}
	}

	// JSON → proto, then back to JSON should be stable.
	protoBytes, err := schema.FromJSON(jsonBytes)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	jsonAgain, err := schema.ToJSON(protoBytes)
	if err != nil {
		t.Fatalf("ToJSON (round 2): %v", err)
	}
	if string(jsonAgain) != got {
		t.Errorf("round-trip drift:\n got  %s\n want %s", jsonAgain, got)
	}
}

// TestValidateStage_Protobuf wires the proto schema into the existing
// ValidateStage and confirms it rejects bad bytes.
func TestValidateStage_Protobuf(t *testing.T) {
	fds, fqn := buildTestDescriptor(t)
	stage := &ValidateStage{
		SchemaType:   "protobuf",
		Content:      fds,
		ProtoMessage: fqn,
	}
	good := buildOrderMessage(t, fds, fqn, "abc", "x", 1)
	if _, _, _, err := stage.Execute(context.Background(), good, FormatProtobuf); err != nil {
		t.Errorf("expected good message to pass: %v", err)
	}
	if _, _, _, err := stage.Execute(context.Background(), []byte("garbage"), FormatProtobuf); err == nil {
		t.Error("expected garbage to be rejected")
	}
}

// TestTranslateStage_ProtoToJSON + JSONToProto wires the schema into a
// TranslateStage and confirms both directions work.
func TestTranslateStage_ProtoToJSON(t *testing.T) {
	fds, fqn := buildTestDescriptor(t)
	schema, _ := LoadProtoSchema(fds, fqn)
	body := buildOrderMessage(t, fds, fqn, "abc", "widget", 7)

	stage := &TranslateStage{Target: FormatJSON, Proto: schema}
	out, fmt, _, err := stage.Execute(context.Background(), body, FormatProtobuf)
	if err != nil {
		t.Fatal(err)
	}
	if fmt != FormatJSON {
		t.Errorf("format: %s", fmt)
	}
	if !strings.Contains(string(out), `"name":"widget"`) {
		t.Errorf("missing field in JSON: %s", out)
	}
}

func TestTranslateStage_JSONToProto(t *testing.T) {
	fds, fqn := buildTestDescriptor(t)
	schema, _ := LoadProtoSchema(fds, fqn)
	in := []byte(`{"id":"abc","name":"widget","qty":7}`)

	stage := &TranslateStage{Target: FormatProtobuf, Proto: schema}
	out, fmt, _, err := stage.Execute(context.Background(), in, FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	if fmt != FormatProtobuf {
		t.Errorf("format: %s", fmt)
	}
	// Round-trip back via schema.ToJSON to confirm wire format is real.
	jsonAgain, err := schema.ToJSON(out)
	if err != nil {
		t.Fatalf("decode produced bytes: %v", err)
	}
	if !strings.Contains(string(jsonAgain), `"id":"abc"`) {
		t.Errorf("round trip lost field: %s", jsonAgain)
	}
}

func TestTranslateStage_ProtoWithoutSchema(t *testing.T) {
	stage := &TranslateStage{Target: FormatProtobuf}
	_, _, _, err := stage.Execute(context.Background(), []byte(`{"a":1}`), FormatJSON)
	if err == nil {
		t.Fatal("expected error: translating to protobuf without a schema must fail")
	}
}
