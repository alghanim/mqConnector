package storage

import (
	"context"
	"testing"
)

func TestTransforms_ReplaceForPipeline(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	src := &Connection{Name: "src", Type: "rabbitmq"}
	dst := &Connection{Name: "dst", Type: "kafka"}
	_ = s.Connections.Create(ctx, src)
	_ = s.Connections.Create(ctx, dst)

	p := &Pipeline{Name: "p", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	if err := s.Pipelines.Create(ctx, p); err != nil {
		t.Fatal(err)
	}

	rules := []*Transform{
		{TransformType: "rename", SourcePath: "a", TargetPath: "b", Order: 1},
		{TransformType: "mask", SourcePath: "c", MaskPattern: ".", MaskReplace: "*", Order: 2},
	}
	if err := s.Transforms.ReplaceForPipeline(ctx, p.ID, rules); err != nil {
		t.Fatal(err)
	}
	got, err := s.Transforms.ListByPipeline(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 transforms, got %d", len(got))
	}
	if got[0].Order != 1 || got[1].Order != 2 {
		t.Errorf("order not preserved: %d, %d", got[0].Order, got[1].Order)
	}

	// Replace shrinks/clears prior rules.
	if err := s.Transforms.ReplaceForPipeline(ctx, p.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Transforms.ListByPipeline(ctx, p.ID)
	if len(got) != 0 {
		t.Errorf("expected empty after replace, got %d", len(got))
	}
}

func TestRoutingRules_ReplaceForPipeline(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	src := &Connection{Name: "src", Type: "rabbitmq"}
	dst1 := &Connection{Name: "dst1", Type: "kafka"}
	dst2 := &Connection{Name: "dst2", Type: "kafka"}
	for _, c := range []*Connection{src, dst1, dst2} {
		if err := s.Connections.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	p := &Pipeline{Name: "p", SourceID: src.ID, DestinationID: dst1.ID, Enabled: true}
	if err := s.Pipelines.Create(ctx, p); err != nil {
		t.Fatal(err)
	}

	rules := []*RoutingRule{
		{ConditionPath: "region", ConditionOperator: "eq", ConditionValue: "EU", DestinationID: dst1.ID, Priority: 10, Enabled: true},
		{ConditionPath: "region", ConditionOperator: "eq", ConditionValue: "US", DestinationID: dst2.ID, Priority: 20, Enabled: true},
	}
	if err := s.RoutingRules.ReplaceForPipeline(ctx, p.ID, rules); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RoutingRules.ListByPipeline(ctx, p.ID)
	if len(got) != 2 {
		t.Fatalf("expected 2 routing rules, got %d", len(got))
	}
	if got[0].Priority != 10 {
		t.Errorf("priority order: %d", got[0].Priority)
	}
}

func TestScripts_CRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	sc := &Script{Name: "noop", Body: "msg.x = 1;", Enabled: true}
	if err := s.Scripts.Create(ctx, sc); err != nil {
		t.Fatal(err)
	}
	got, err := s.Scripts.Get(ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != sc.Body {
		t.Errorf("body roundtrip failed")
	}

	got.Body = "msg.y = 2;"
	if err := s.Scripts.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err := s.Scripts.Delete(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scripts.Get(ctx, got.ID); err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete: %v", err)
	}
}

func TestSchemas_CRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	sc := &Schema{Name: "order", SchemaType: "json_schema", Content: `{"type":"object"}`}
	if err := s.Schemas.Create(ctx, sc); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Schemas.List(ctx)
	if len(all) != 1 {
		t.Errorf("expected 1 schema, got %d", len(all))
	}
	sc.Content = `{"type":"object","required":["id"]}`
	if err := s.Schemas.Update(ctx, sc); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Schemas.Get(ctx, sc.ID)
	if got.Content != sc.Content {
		t.Errorf("content roundtrip failed")
	}
	_ = s.Schemas.Delete(ctx, sc.ID)
}

func TestStages_ReplaceForPipeline(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	src := &Connection{Name: "src", Type: "rabbitmq"}
	dst := &Connection{Name: "dst", Type: "kafka"}
	_ = s.Connections.Create(ctx, src)
	_ = s.Connections.Create(ctx, dst)
	p := &Pipeline{Name: "p", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	_ = s.Pipelines.Create(ctx, p)

	stages := []*Stage{
		{StageOrder: 1, StageType: "filter", StageConfig: `{"paths":["a"]}`, Enabled: true},
		{StageOrder: 2, StageType: "transform", Enabled: true},
	}
	if err := s.Stages.ReplaceForPipeline(ctx, p.ID, stages); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Stages.ListByPipeline(ctx, p.ID)
	if len(got) != 2 {
		t.Errorf("expected 2 stages, got %d", len(got))
	}

	// Replace clears and rewrites.
	stages = []*Stage{{StageOrder: 1, StageType: "translate", Enabled: true}}
	if err := s.Stages.ReplaceForPipeline(ctx, p.ID, stages); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Stages.ListByPipeline(ctx, p.ID)
	if len(got) != 1 || got[0].StageType != "translate" {
		t.Errorf("replace did not overwrite: %v", got)
	}
}

func TestPipelines_DeleteCascadesStages(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	src := &Connection{Name: "src", Type: "rabbitmq"}
	dst := &Connection{Name: "dst", Type: "kafka"}
	_ = s.Connections.Create(ctx, src)
	_ = s.Connections.Create(ctx, dst)

	p := &Pipeline{Name: "p", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	_ = s.Pipelines.Create(ctx, p)

	_ = s.Stages.ReplaceForPipeline(ctx, p.ID, []*Stage{
		{StageOrder: 1, StageType: "filter", Enabled: true},
	})
	_ = s.Pipelines.Delete(ctx, p.ID)

	// Stages should be gone via ON DELETE CASCADE.
	got, _ := s.Stages.ListByPipeline(ctx, p.ID)
	if len(got) != 0 {
		t.Errorf("expected stages to be cascade-deleted, got %d", len(got))
	}
}
