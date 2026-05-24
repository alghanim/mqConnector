package server

import (
	"encoding/json"
	"testing"
	"time"

	"mqConnector/internal/storage"
)

// DiffSnapshots unit tests — pure helper, no server fixture.
//
// The matching strategy is positional by the executor-visible ordering
// field (stage_order / order / priority), so these tests deliberately
// pin down both the "structural" cases (add / remove / modify) and the
// "reordering looks like two modifies" case the algorithm doc-commented
// for the UX team to be aware of.

// pipeFixture returns a minimal Pipeline with stable fields the tests
// can mutate. Timestamps are non-zero so we can assert the diff
// ignores them (case 10).
func pipeFixture() *storage.Pipeline {
	return &storage.Pipeline{
		ID:            "p-1",
		TenantID:      "tenant-a",
		Name:          "pipe-original",
		SourceID:      "src-1",
		DestinationID: "dst-1",
		OutputFormat:  "same",
		Enabled:       true,
		FilterPaths:   []string{},
		CreatedAt:     time.Unix(1700000000, 0).UTC(),
		UpdatedAt:     time.Unix(1700000001, 0).UTC(),
	}
}

func stageFixture(id string, order int, cfg string) *storage.Stage {
	return &storage.Stage{
		ID:          id,
		TenantID:    "tenant-a",
		PipelineID:  "p-1",
		StageOrder:  order,
		StageType:   "filter",
		StageConfig: cfg,
		Enabled:     true,
	}
}

func transformFixture(id string, order int, source string) *storage.Transform {
	return &storage.Transform{
		ID:            id,
		TenantID:      "tenant-a",
		PipelineID:    "p-1",
		TransformType: "rename",
		SourcePath:    source,
		TargetPath:    "renamed",
		Order:         order,
	}
}

func routingRuleFixture(id string, priority int, value string) *storage.RoutingRule {
	return &storage.RoutingRule{
		ID:                id,
		TenantID:          "tenant-a",
		PipelineID:        "p-1",
		ConditionPath:     "$.kind",
		ConditionOperator: "eq",
		ConditionValue:    value,
		DestinationID:     "dst-2",
		Priority:          priority,
		Enabled:           true,
	}
}

func snapshotFixture() *storage.PipelineSnapshot {
	return &storage.PipelineSnapshot{
		Pipeline:      pipeFixture(),
		Stages:        []*storage.Stage{stageFixture("s-1", 0, `{"k":"v1"}`)},
		Transforms:    []*storage.Transform{transformFixture("t-1", 0, "a")},
		RoutingRules:  []*storage.RoutingRule{routingRuleFixture("r-1", 0, "x")},
		SchemaVersion: 1,
	}
}

// 1. No-op diff — identical snapshots produce empty diff.
func TestDiffSnapshots_NoOp(t *testing.T) {
	before := snapshotFixture()
	after := snapshotFixture()
	d := DiffSnapshots(before, after)
	if len(d.PipelineFields) != 0 {
		t.Errorf("pipeline_fields = %d, want 0 (%+v)",
			len(d.PipelineFields), d.PipelineFields)
	}
	if len(d.Stages.Added)+len(d.Stages.Removed)+len(d.Stages.Modified) != 0 {
		t.Errorf("stages diff = %+v, want empty", d.Stages)
	}
	if len(d.Transforms.Added)+len(d.Transforms.Removed)+len(d.Transforms.Modified) != 0 {
		t.Errorf("transforms diff = %+v, want empty", d.Transforms)
	}
	if len(d.RoutingRules.Added)+len(d.RoutingRules.Removed)+len(d.RoutingRules.Modified) != 0 {
		t.Errorf("routing_rules diff = %+v, want empty", d.RoutingRules)
	}
	if d.SchemaVersion != nil {
		t.Errorf("schema_version = %+v, want nil", d.SchemaVersion)
	}
	// Wire-shape: empty slices, not nil — assertion on serialised JSON.
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal diff: %v", err)
	}
	// Each child diff must emit [] not null.
	for _, key := range []string{`"added":[]`, `"removed":[]`, `"modified":[]`} {
		if !contains(raw, key) {
			t.Errorf("missing %q in wire shape: %s", key, raw)
		}
	}
}

// 2. Pipeline field change — rename pipeline.
func TestDiffSnapshots_PipelineFieldChange_Name(t *testing.T) {
	before := snapshotFixture()
	after := snapshotFixture()
	after.Pipeline.Name = "pipe-renamed"

	d := DiffSnapshots(before, after)
	if len(d.PipelineFields) != 1 {
		t.Fatalf("pipeline_fields = %d, want 1 (%+v)",
			len(d.PipelineFields), d.PipelineFields)
	}
	fc := d.PipelineFields[0]
	if fc.Path != "name" {
		t.Errorf("path = %q, want %q", fc.Path, "name")
	}
	if string(fc.Before) != `"pipe-original"` {
		t.Errorf("before = %s, want %q", fc.Before, `"pipe-original"`)
	}
	if string(fc.After) != `"pipe-renamed"` {
		t.Errorf("after = %s, want %q", fc.After, `"pipe-renamed"`)
	}
}

// Pipeline field change with non-string scalar (max_msgs_per_minute).
func TestDiffSnapshots_PipelineFieldChange_Int(t *testing.T) {
	before := snapshotFixture()
	after := snapshotFixture()
	after.Pipeline.MaxMsgsPerMinute = 500

	d := DiffSnapshots(before, after)
	if len(d.PipelineFields) != 1 {
		t.Fatalf("pipeline_fields = %d, want 1", len(d.PipelineFields))
	}
	fc := d.PipelineFields[0]
	if fc.Path != "max_msgs_per_minute" {
		t.Errorf("path = %q, want max_msgs_per_minute", fc.Path)
	}
	// omitempty zero on Before drops the key entirely — Before should
	// be nil in that case (zero int with omitempty is absent in the
	// marshalled JSON). The presence of After is what matters.
	if string(fc.After) != "500" {
		t.Errorf("after = %s, want 500", fc.After)
	}
}

// 3. Stage added (tail).
func TestDiffSnapshots_StageAdded(t *testing.T) {
	before := snapshotFixture()
	after := snapshotFixture()
	after.Stages = append(after.Stages,
		stageFixture("s-new", 1, `{"k":"v2"}`))

	d := DiffSnapshots(before, after)
	if len(d.Stages.Added) != 1 {
		t.Fatalf("added = %d, want 1 (%+v)", len(d.Stages.Added), d.Stages.Added)
	}
	if len(d.Stages.Modified) != 0 {
		t.Errorf("modified = %d, want 0", len(d.Stages.Modified))
	}
	if len(d.Stages.Removed) != 0 {
		t.Errorf("removed = %d, want 0", len(d.Stages.Removed))
	}
	ce := d.Stages.Added[0]
	if ce.ID != "s-new" {
		t.Errorf("added.id = %q, want s-new", ce.ID)
	}
	if ce.Order != 1 {
		t.Errorf("added.order = %d, want 1", ce.Order)
	}
}

// 4. Stage removed.
func TestDiffSnapshots_StageRemoved(t *testing.T) {
	before := snapshotFixture()
	before.Stages = append(before.Stages,
		stageFixture("s-old", 1, `{"k":"v2"}`))
	after := snapshotFixture() // only s-1

	d := DiffSnapshots(before, after)
	if len(d.Stages.Removed) != 1 {
		t.Fatalf("removed = %d, want 1 (%+v)", len(d.Stages.Removed), d.Stages.Removed)
	}
	if len(d.Stages.Added) != 0 {
		t.Errorf("added = %d, want 0", len(d.Stages.Added))
	}
	if len(d.Stages.Modified) != 0 {
		t.Errorf("modified = %d, want 0", len(d.Stages.Modified))
	}
	ce := d.Stages.Removed[0]
	if ce.ID != "s-old" {
		t.Errorf("removed.id = %q, want s-old", ce.ID)
	}
}

//  5. Stage modified at same position — same length, different content,
//     BeforeID populated, ID matches the After stage.
func TestDiffSnapshots_StageModifiedSamePosition(t *testing.T) {
	before := snapshotFixture()
	after := snapshotFixture()
	after.Stages[0] = stageFixture("s-after", 0, `{"k":"v-changed"}`)

	d := DiffSnapshots(before, after)
	if len(d.Stages.Modified) != 1 {
		t.Fatalf("modified = %d, want 1", len(d.Stages.Modified))
	}
	cc := d.Stages.Modified[0]
	if cc.ID != "s-after" {
		t.Errorf("modified.id = %q, want s-after (after's id wins)", cc.ID)
	}
	if cc.BeforeID != "s-1" {
		t.Errorf("modified.before_id = %q, want s-1", cc.BeforeID)
	}
	if cc.Order != 0 {
		t.Errorf("modified.order = %d, want 0", cc.Order)
	}
	// Spot-check that the raw Before/After values round-trip.
	var bs, as storage.Stage
	if err := json.Unmarshal(cc.Before, &bs); err != nil {
		t.Fatalf("decode before: %v", err)
	}
	if err := json.Unmarshal(cc.After, &as); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if bs.StageConfig != `{"k":"v1"}` {
		t.Errorf("before.stage_config = %q, want original", bs.StageConfig)
	}
	if as.StageConfig != `{"k":"v-changed"}` {
		t.Errorf("after.stage_config = %q, want changed", as.StageConfig)
	}
}

// 6. Stages reordered — Before [A, B]; After [B, A] looks like two
// Modified entries under positional matching. Documents the
// behaviour rather than papering over it.
func TestDiffSnapshots_StagesReordered(t *testing.T) {
	a := stageFixture("s-a", 0, `{"k":"a"}`)
	b := stageFixture("s-b", 1, `{"k":"b"}`)
	before := snapshotFixture()
	before.Stages = []*storage.Stage{a, b}

	// Swap content and adjust stage_order to match the new positions.
	aSwapped := stageFixture("s-a", 1, `{"k":"a"}`)
	bSwapped := stageFixture("s-b", 0, `{"k":"b"}`)
	after := snapshotFixture()
	after.Stages = []*storage.Stage{bSwapped, aSwapped}

	d := DiffSnapshots(before, after)
	if len(d.Stages.Modified) != 2 {
		t.Fatalf("modified = %d, want 2 (positional diff treats swap as two modifies)",
			len(d.Stages.Modified))
	}
	if len(d.Stages.Added) != 0 || len(d.Stages.Removed) != 0 {
		t.Errorf("expected only Modified entries; got %+v", d.Stages)
	}
}

// 7. Transforms diff (same shape as stages).
func TestDiffSnapshots_TransformsDiff(t *testing.T) {
	before := snapshotFixture()
	after := snapshotFixture()
	// Append one, then modify the first.
	after.Transforms[0] = transformFixture("t-after", 0, "b")
	after.Transforms = append(after.Transforms,
		transformFixture("t-new", 1, "c"))

	d := DiffSnapshots(before, after)
	if len(d.Transforms.Modified) != 1 {
		t.Errorf("transforms.modified = %d, want 1", len(d.Transforms.Modified))
	}
	if len(d.Transforms.Added) != 1 {
		t.Errorf("transforms.added = %d, want 1", len(d.Transforms.Added))
	}
	if len(d.Transforms.Removed) != 0 {
		t.Errorf("transforms.removed = %d, want 0", len(d.Transforms.Removed))
	}
	if d.Transforms.Modified[0].BeforeID != "t-1" {
		t.Errorf("modified.before_id = %q, want t-1",
			d.Transforms.Modified[0].BeforeID)
	}
}

// 8. Routing rules diff (same shape as stages).
func TestDiffSnapshots_RoutingRulesDiff(t *testing.T) {
	before := snapshotFixture()
	before.RoutingRules = append(before.RoutingRules,
		routingRuleFixture("r-old", 1, "y"))
	after := snapshotFixture()
	// Length shrinks → tail of before becomes Removed.
	d := DiffSnapshots(before, after)
	if len(d.RoutingRules.Removed) != 1 {
		t.Fatalf("routing_rules.removed = %d, want 1", len(d.RoutingRules.Removed))
	}
	if d.RoutingRules.Removed[0].ID != "r-old" {
		t.Errorf("removed.id = %q, want r-old", d.RoutingRules.Removed[0].ID)
	}
}

// 9. SchemaVersion change.
func TestDiffSnapshots_SchemaVersionChange(t *testing.T) {
	before := snapshotFixture()
	after := snapshotFixture()
	after.SchemaVersion = 2
	d := DiffSnapshots(before, after)
	if d.SchemaVersion == nil {
		t.Fatal("schema_version diff is nil; want populated")
	}
	if string(d.SchemaVersion.Before) != "1" {
		t.Errorf("schema_version.before = %s, want 1", d.SchemaVersion.Before)
	}
	if string(d.SchemaVersion.After) != "2" {
		t.Errorf("schema_version.after = %s, want 2", d.SchemaVersion.After)
	}
}

//  10. CreatedAt/UpdatedAt are excluded — different timestamps on
//     otherwise identical Pipelines must produce zero pipeline_fields
//     entries. This pins down the symmetry with the snapshot-hash
//     projection (created_at/updated_at zeroed pre-hash).
func TestDiffSnapshots_ExcludesTimestamps(t *testing.T) {
	before := snapshotFixture()
	after := snapshotFixture()
	after.Pipeline.CreatedAt = time.Unix(1800000000, 0).UTC()
	after.Pipeline.UpdatedAt = time.Unix(1800000001, 0).UTC()
	d := DiffSnapshots(before, after)
	if len(d.PipelineFields) != 0 {
		t.Errorf("pipeline_fields = %+v, want empty (timestamps must be ignored)",
			d.PipelineFields)
	}
}

// Nil-tolerance: a nil snapshot is treated as empty so the diff
// degrades to "everything is added" or "everything is removed".
func TestDiffSnapshots_NilInputs(t *testing.T) {
	full := snapshotFixture()
	// nil before → everything is added.
	d := DiffSnapshots(nil, full)
	if len(d.Stages.Added) != 1 {
		t.Errorf("nil before: stages.added = %d, want 1", len(d.Stages.Added))
	}
	// nil after → everything is removed.
	d2 := DiffSnapshots(full, nil)
	if len(d2.Stages.Removed) != 1 {
		t.Errorf("nil after: stages.removed = %d, want 1", len(d2.Stages.Removed))
	}
	// Both nil → empty diff, no panic.
	d3 := DiffSnapshots(nil, nil)
	if d3 == nil || len(d3.PipelineFields) != 0 {
		t.Errorf("both nil: want empty non-nil diff, got %+v", d3)
	}
}

// contains is a tiny helper to avoid pulling in bytes/strings for one assertion.
func contains(haystack []byte, needle string) bool {
	n := []byte(needle)
	for i := 0; i+len(n) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(n); j++ {
			if haystack[i+j] != n[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
