package pipeline

import (
	"context"
	"path/filepath"
	"testing"

	"mqConnector/internal/storage"
)

func makeStoreWithSchema(t *testing.T) (*storage.Store, string) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "schema.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	s, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &storage.Schema{
		Name: "orders.v1", SchemaType: "json_schema",
		Content: `{"type":"object","required":["id"]}`,
	}
	if err := s.Schemas.Create(context.Background(), storage.DefaultTenantID, schema); err != nil {
		t.Fatal(err)
	}
	return s, schema.ID
}

func TestLoadReferencedSchemas_FromPipelineLevel(t *testing.T) {
	store, sid := makeStoreWithSchema(t)
	got, err := loadReferencedSchemas(context.Background(), store,
		&storage.Pipeline{SchemaID: sid}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[sid]; !ok {
		t.Errorf("expected schema %s in loaded map", sid)
	}
}

func TestLoadReferencedSchemas_FromStageConfig(t *testing.T) {
	store, sid := makeStoreWithSchema(t)
	rows := []*storage.Stage{
		{StageType: "validate", Enabled: true,
			StageConfig: `{"schema_id":"` + sid + `"}`},
	}
	got, err := loadReferencedSchemas(context.Background(), store,
		&storage.Pipeline{}, rows)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[sid]; !ok {
		t.Errorf("expected schema %s to be resolved from stage config", sid)
	}
}

func TestLoadReferencedSchemas_MissingErrors(t *testing.T) {
	store, _ := makeStoreWithSchema(t)
	_, err := loadReferencedSchemas(context.Background(), store,
		&storage.Pipeline{SchemaID: "nonexistent"}, nil)
	if err == nil {
		t.Error("expected error for missing schema reference")
	}
}

func TestBuild_ResolvesValidateStageBySchemaID(t *testing.T) {
	store, sid := makeStoreWithSchema(t)
	schema, _ := store.Schemas.Get(context.Background(), storage.DefaultTenantID, sid)

	stages, err := Build(BuildContext{
		Pipeline: &storage.Pipeline{SchemaID: sid},
		StageRows: []*storage.Stage{
			{StageType: "validate", Enabled: true},
		},
		Schemas: map[string]*storage.Schema{sid: schema},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
	vs, ok := stages[0].(*ValidateStage)
	if !ok {
		t.Fatalf("expected ValidateStage, got %T", stages[0])
	}
	if vs.Content == "" || vs.SchemaType != "json_schema" {
		t.Errorf("ValidateStage didn't inherit schema content: %+v", vs)
	}
}

func TestBuild_StageConfigSchemaIDBeatsPipelineLevel(t *testing.T) {
	store, sidPipe := makeStoreWithSchema(t)
	stageSchema := &storage.Schema{
		Name: "stage-level", SchemaType: "xsd", Content: "name",
	}
	if err := store.Schemas.Create(context.Background(), storage.DefaultTenantID, stageSchema); err != nil {
		t.Fatal(err)
	}
	pipeSchema, _ := store.Schemas.Get(context.Background(), storage.DefaultTenantID, sidPipe)

	stages, err := Build(BuildContext{
		Pipeline: &storage.Pipeline{SchemaID: sidPipe},
		StageRows: []*storage.Stage{
			{StageType: "validate", Enabled: true,
				StageConfig: `{"schema_id":"` + stageSchema.ID + `"}`},
		},
		Schemas: map[string]*storage.Schema{
			sidPipe:         pipeSchema,
			stageSchema.ID:  stageSchema,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	vs := stages[0].(*ValidateStage)
	if vs.SchemaType != "xsd" {
		t.Errorf("expected stage-level (xsd) to win, got %s", vs.SchemaType)
	}
}
