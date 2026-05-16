package pipeline

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Why dynamic descriptors
//
// mqConnector can't ship generated Go bindings for every customer's
// .proto file — operators bring their own schemas at runtime. So we
// load a FileDescriptorSet (compiled via `protoc --descriptor_set_out`),
// look up the message by fully-qualified name, and use
// dynamicpb.NewMessage to get a reflective message we can
// unmarshal/marshal at runtime.
//
// Schema storage: the existing `schemas` table holds text content for
// `json_schema` and `xsd` types. For protobuf we store the binary
// FileDescriptorSet (output of `protoc --descriptor_set_out=fds.bin
// --include_imports your.proto`) base64-encoded so it round-trips
// through TEXT columns cleanly. The schema's `Name` field is reused
// as the message's fully-qualified name (e.g. "acme.orders.Order").

// ProtoSchema bundles a compiled descriptor + the target message
// name. Built once when a stage is initialised; reused per message.
type ProtoSchema struct {
	files       *protoregistry.Files
	messageName protoreflect.FullName
}

// LoadProtoSchema parses a base64-encoded FileDescriptorSet and
// resolves the named message inside it. `messageName` must be the
// fully-qualified name (package + "." + message), e.g.
// "acme.orders.Order". Returns a ready-to-use ProtoSchema.
//
// Errors are wrapped with enough context that an operator pasting a
// bad descriptor sees which step failed (base64 decode / unmarshal /
// resolve).
func LoadProtoSchema(descriptorBase64, messageName string) (*ProtoSchema, error) {
	descriptorBase64 = strings.TrimSpace(descriptorBase64)
	messageName = strings.TrimSpace(messageName)
	if descriptorBase64 == "" {
		return nil, errors.New("proto schema: descriptor is empty")
	}
	if messageName == "" {
		return nil, errors.New("proto schema: message name is empty")
	}

	raw, err := base64.StdEncoding.DecodeString(descriptorBase64)
	if err != nil {
		// Tolerate raw bytes for callers that pass the binary set
		// directly (e.g. unit tests). Pure-binary descriptors will
		// fail base64 decode with "invalid byte" and fall through.
		raw = []byte(descriptorBase64)
	}

	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &fds); err != nil {
		return nil, fmt.Errorf("proto schema: unmarshal descriptor set: %w", err)
	}
	files, err := protodesc.NewFiles(&fds)
	if err != nil {
		return nil, fmt.Errorf("proto schema: register files: %w", err)
	}

	full := protoreflect.FullName(messageName)
	desc, err := files.FindDescriptorByName(full)
	if err != nil {
		return nil, fmt.Errorf("proto schema: find message %q: %w", messageName, err)
	}
	if _, ok := desc.(protoreflect.MessageDescriptor); !ok {
		return nil, fmt.Errorf("proto schema: %q is not a message", messageName)
	}
	return &ProtoSchema{files: files, messageName: full}, nil
}

// newMessage returns a fresh dynamic message bound to the schema's
// descriptor. Used internally by Decode / Encode.
func (s *ProtoSchema) newMessage() (*dynamicpb.Message, error) {
	desc, err := s.files.FindDescriptorByName(s.messageName)
	if err != nil {
		return nil, err
	}
	msg, ok := desc.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("proto schema: %q resolved to non-message", s.messageName)
	}
	return dynamicpb.NewMessage(msg), nil
}

// Validate checks that `body` parses as the schema's message type.
// Returns nil if the bytes are a well-formed instance, an error
// describing the offending field otherwise. Used by ValidateStage when
// SchemaType=="protobuf".
func (s *ProtoSchema) Validate(body []byte) error {
	msg, err := s.newMessage()
	if err != nil {
		return err
	}
	if err := proto.Unmarshal(body, msg); err != nil {
		return fmt.Errorf("proto validate: %w", err)
	}
	return nil
}

// ToJSON decodes protobuf bytes into the schema's message and emits
// JSON. Field names follow the JSON conventions defined in the
// .proto file (json_name option or camelCase by default).
func (s *ProtoSchema) ToJSON(body []byte) ([]byte, error) {
	msg, err := s.newMessage()
	if err != nil {
		return nil, err
	}
	if err := proto.Unmarshal(body, msg); err != nil {
		return nil, fmt.Errorf("proto→json: unmarshal proto: %w", err)
	}
	out, err := protojson.MarshalOptions{
		UseProtoNames:   false, // emit camelCase as JSON convention prefers
		EmitUnpopulated: false,
	}.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("proto→json: marshal json: %w", err)
	}
	return out, nil
}

// FromJSON encodes JSON into the schema's message type. The JSON
// must use the field names defined in the .proto file (or their
// proto-snake variants — protojson accepts both).
func (s *ProtoSchema) FromJSON(body []byte) ([]byte, error) {
	msg, err := s.newMessage()
	if err != nil {
		return nil, err
	}
	if err := (protojson.UnmarshalOptions{
		DiscardUnknown: false,
	}).Unmarshal(body, msg); err != nil {
		return nil, fmt.Errorf("json→proto: unmarshal json: %w", err)
	}
	out, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("json→proto: marshal proto: %w", err)
	}
	return out, nil
}
