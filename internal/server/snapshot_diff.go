package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"mqConnector/internal/storage"
)

// SnapshotDiff is the structured diff between two PipelineSnapshots.
// All slices are non-nil (so it serialises as `[]` not `null`); the
// UX renders them in fixed order. The diff is structural, not a
// textual JSON Patch — it answers "what changed across this
// pipeline's stages/transforms/routes" at the granularity the Studio
// diff viewer needs to render row-by-row.
//
// Direction is from-before → to-after. See diffResponse for how that
// maps onto the `{rev}` and `against` URL parameters.
type SnapshotDiff struct {
	// PipelineFields lists every changed scalar field on the top-level
	// Pipeline struct. CreatedAt / UpdatedAt are excluded for the same
	// reason they're zeroed before hashing: they churn on every PUT
	// without being a real configuration change.
	PipelineFields []FieldChange `json:"pipeline_fields"`
	Stages         ChildDiff     `json:"stages"`
	Transforms     ChildDiff     `json:"transforms"`
	RoutingRules   ChildDiff     `json:"routing_rules"`
	// SchemaVersion is non-nil only when it changed between the two
	// snapshots. omitempty so the common case (same version) doesn't
	// emit a null field.
	SchemaVersion *FieldChange `json:"schema_version,omitempty"`
}

// FieldChange describes one changed scalar field on the Pipeline
// struct (or the SchemaVersion). Path is the snake_case JSON tag —
// e.g. `name`, `source_id`, `max_msgs_per_minute`. Before / After are
// raw JSON values so the wire shape exactly mirrors how the field is
// serialised in the snapshot.
type FieldChange struct {
	Path   string          `json:"path"`
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
}

// ChildDiff is the grouping for one child collection (stages,
// transforms, routing rules). Slices are non-nil so the JSON shape is
// always `[]` and never `null`.
type ChildDiff struct {
	Added    []ChildEntry  `json:"added"`
	Removed  []ChildEntry  `json:"removed"`
	Modified []ChildChange `json:"modified"`
}

// ChildEntry is one added or removed child row. ID is the
// server-assigned identifier from the snapshot (may differ across
// PUTs of the same logical content — see the matching strategy
// below). Order is the positional hint: stage_order for stages,
// `order` for transforms, `priority` for routing rules.
type ChildEntry struct {
	ID    string          `json:"id"`
	Order int             `json:"order"`
	Value json.RawMessage `json:"value"`
}

// ChildChange is one modified child row matched by position. ID is
// the After row's identifier (the "new" id the editor would attach
// edits to); BeforeID is the Before row's identifier when the two
// differ (which they typically do, because the legacy "replace all
// children" repo regenerates UUIDs on every PUT). The UI uses
// BeforeID to highlight the corresponding row in the historical
// snapshot.
type ChildChange struct {
	ID       string          `json:"id"`
	BeforeID string          `json:"before_id,omitempty"`
	Order    int             `json:"order"`
	Before   json.RawMessage `json:"before"`
	After    json.RawMessage `json:"after"`
}

// DiffSnapshots is the pure helper that powers the diff endpoint.
// before represents the baseline ("from"); after is the proposed /
// target ("to"). nil inputs are tolerated: a nil snapshot is treated
// as an empty PipelineSnapshot — useful when the caller wants to
// describe a from-scratch pipeline as additions only.
//
// Matching strategy (per the plan §1.4):
//   - Child rows (stages / transforms / routing rules) are matched
//     positionally by the executor-visible ordering field. This is
//     pragmatic because:
//     1. The StageRepo / TransformRepo / RoutingRuleRepo replace
//     methods DELETE+INSERT with fresh uuid.NewString() IDs on
//     every PUT — so identity-based matching is impossible
//     across two saves of the same logical content.
//     2. Stages run in stage_order; transforms in `order`; routing
//     rules in `priority`. A position change is semantically a
//     real change (executor behaviour shifts), so treating it as
//     Modified rather than Unchanged matches the runtime view.
//   - Pipeline scalar fields are diffed by JSON-tag, excluding
//     CreatedAt / UpdatedAt (bookkeeping, not configuration; see the
//     snapshot-hash projection in pipeline_revisions.go).
//   - SchemaVersion is emitted only when the integers differ.
func DiffSnapshots(before, after *storage.PipelineSnapshot) *SnapshotDiff {
	if before == nil {
		before = &storage.PipelineSnapshot{}
	}
	if after == nil {
		after = &storage.PipelineSnapshot{}
	}
	out := &SnapshotDiff{
		PipelineFields: diffPipelineFields(before.Pipeline, after.Pipeline),
		Stages:         diffStages(before.Stages, after.Stages),
		Transforms:     diffTransforms(before.Transforms, after.Transforms),
		RoutingRules:   diffRoutingRules(before.RoutingRules, after.RoutingRules),
	}
	if before.SchemaVersion != after.SchemaVersion {
		b, _ := json.Marshal(before.SchemaVersion)
		a, _ := json.Marshal(after.SchemaVersion)
		out.SchemaVersion = &FieldChange{
			Path:   "snapshot_schema_version",
			Before: b,
			After:  a,
		}
	}
	return out
}

// diffPipelineFields produces one FieldChange per differing scalar
// field on the Pipeline struct. The implementation marshals both
// sides to a map[string]json.RawMessage (so JSON tags are honoured
// without a second source of truth) and diffs the keyspace. Volatile
// timestamp fields are stripped pre-marshal so they never appear in
// the output — symmetric with the snapshot-hash projection.
//
// A nil Pipeline on either side is treated as "no fields present" so
// every key on the other side becomes an add/remove. This isn't a
// real-world case (PipelineSnapshot.Pipeline is always set by the
// snapshot path) but it keeps the helper total.
func diffPipelineFields(before, after *storage.Pipeline) []FieldChange {
	out := []FieldChange{} // non-nil so JSON shape is [] not null
	beforeMap := pipelineToMap(before)
	afterMap := pipelineToMap(after)
	// Stable key iteration: walk the union in declaration order via
	// the canonical key list. The list is derived from the Pipeline
	// struct's JSON tags at init time so adding a field automatically
	// shows up here.
	for _, k := range pipelineFieldOrder {
		bRaw, bOK := beforeMap[k]
		aRaw, aOK := afterMap[k]
		if !bOK && !aOK {
			continue
		}
		if jsonRawEqual(bRaw, aRaw) {
			continue
		}
		out = append(out, FieldChange{
			Path:   k,
			Before: bRaw,
			After:  aRaw,
		})
	}
	return out
}

// pipelineFieldOrder is the canonical iteration order over the
// Pipeline struct's JSON-visible scalar fields. Built once at
// package-init time by reflecting on the Pipeline struct's tags so
// adding a new field to storage.Pipeline automatically shows up in
// the diff with no second source of truth to maintain. CreatedAt and
// UpdatedAt are excluded — see the symmetric stripping in
// pipelineToMap. omitempty fields are KEPT in the order list (unlike
// a naive marshal-zero-value approach) so a zero-on-one-side /
// non-zero-on-other-side comparison still produces a FieldChange.
var pipelineFieldOrder = computePipelineFieldOrder()

func computePipelineFieldOrder() []string {
	t := reflect.TypeOf(storage.Pipeline{})
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.SplitN(tag, ",", 2)[0]
		if name == "" || name == "created_at" || name == "updated_at" {
			continue
		}
		keys = append(keys, name)
	}
	return keys
}

// pipelineToMap marshals the Pipeline and decodes into a map keyed by
// JSON tag. Timestamp fields are zeroed on a copy first so they never
// influence the diff (encoding/json would otherwise render two
// always-equal "0001-01-01..." sentinels on both sides; the explicit
// delete below keeps them out of the output entirely so callers don't
// see a stray empty key). A nil pointer returns an empty map.
func pipelineToMap(p *storage.Pipeline) map[string]json.RawMessage {
	if p == nil {
		return map[string]json.RawMessage{}
	}
	cp := *p
	cp.CreatedAt = time.Time{}
	cp.UpdatedAt = time.Time{}
	raw, err := json.Marshal(&cp)
	if err != nil {
		return map[string]json.RawMessage{}
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]json.RawMessage{}
	}
	delete(m, "created_at")
	delete(m, "updated_at")
	return m
}

// jsonRawEqual treats two RawMessages as equal if either is missing
// on one side and equal-bytes otherwise. Byte equality is sufficient
// here because both sides come from json.Marshal of the same type, so
// the encoded representation is canonical.
func jsonRawEqual(a, b json.RawMessage) bool {
	if a == nil && b == nil {
		return true
	}
	return bytes.Equal(a, b)
}

// ---------------------------------------------------------------------
// Child diffing — positional matching by the executor-visible order.
// ---------------------------------------------------------------------

// diffStages positionally diffs two stage slices. Both inputs are
// assumed to be in stage_order already — every repo ListByPipeline
// returns them sorted that way, which is the same order the snapshot
// path captures. The Order field on emitted entries carries the
// stage's stage_order verbatim, so the UI can lay out the diff using
// the same numbering operators already see in the executor.
func diffStages(before, after []*storage.Stage) ChildDiff {
	bMaps := make([]map[string]json.RawMessage, len(before))
	bOrders := make([]int, len(before))
	bIDs := make([]string, len(before))
	bValues := make([]json.RawMessage, len(before))
	for i, s := range before {
		bMaps[i], bValues[i] = stageMapAndValue(s)
		if s != nil {
			bOrders[i] = s.StageOrder
			bIDs[i] = s.ID
		}
	}
	aMaps := make([]map[string]json.RawMessage, len(after))
	aOrders := make([]int, len(after))
	aIDs := make([]string, len(after))
	aValues := make([]json.RawMessage, len(after))
	for i, s := range after {
		aMaps[i], aValues[i] = stageMapAndValue(s)
		if s != nil {
			aOrders[i] = s.StageOrder
			aIDs[i] = s.ID
		}
	}
	return positionalDiff(bIDs, bOrders, bMaps, bValues, aIDs, aOrders, aMaps, aValues)
}

func diffTransforms(before, after []*storage.Transform) ChildDiff {
	bMaps := make([]map[string]json.RawMessage, len(before))
	bOrders := make([]int, len(before))
	bIDs := make([]string, len(before))
	bValues := make([]json.RawMessage, len(before))
	for i, t := range before {
		bMaps[i], bValues[i] = transformMapAndValue(t)
		if t != nil {
			bOrders[i] = t.Order
			bIDs[i] = t.ID
		}
	}
	aMaps := make([]map[string]json.RawMessage, len(after))
	aOrders := make([]int, len(after))
	aIDs := make([]string, len(after))
	aValues := make([]json.RawMessage, len(after))
	for i, t := range after {
		aMaps[i], aValues[i] = transformMapAndValue(t)
		if t != nil {
			aOrders[i] = t.Order
			aIDs[i] = t.ID
		}
	}
	return positionalDiff(bIDs, bOrders, bMaps, bValues, aIDs, aOrders, aMaps, aValues)
}

func diffRoutingRules(before, after []*storage.RoutingRule) ChildDiff {
	bMaps := make([]map[string]json.RawMessage, len(before))
	bOrders := make([]int, len(before))
	bIDs := make([]string, len(before))
	bValues := make([]json.RawMessage, len(before))
	for i, r := range before {
		bMaps[i], bValues[i] = routingRuleMapAndValue(r)
		if r != nil {
			bOrders[i] = r.Priority
			bIDs[i] = r.ID
		}
	}
	aMaps := make([]map[string]json.RawMessage, len(after))
	aOrders := make([]int, len(after))
	aIDs := make([]string, len(after))
	aValues := make([]json.RawMessage, len(after))
	for i, r := range after {
		aMaps[i], aValues[i] = routingRuleMapAndValue(r)
		if r != nil {
			aOrders[i] = r.Priority
			aIDs[i] = r.ID
		}
	}
	return positionalDiff(bIDs, bOrders, bMaps, bValues, aIDs, aOrders, aMaps, aValues)
}

// positionalDiff walks two pre-flattened child slices position by
// position. At each shared index it hashes both sides (excluding the
// volatile ID / TenantID / PipelineID fields) and emits a Modified
// entry on mismatch. Trailing entries on either side land in Added /
// Removed respectively.
//
// The helper takes flat slices instead of generic []T so we don't
// need a type parameter (Go 1.22 supports them but the rest of the
// file is concrete). Each caller passes the pre-extracted id/order
// slices plus the JSON map (for hashing) and the full marshalled
// value (for the wire payload).
func positionalDiff(
	bIDs []string, bOrders []int, bMaps []map[string]json.RawMessage, bValues []json.RawMessage,
	aIDs []string, aOrders []int, aMaps []map[string]json.RawMessage, aValues []json.RawMessage,
) ChildDiff {
	out := ChildDiff{
		Added:    []ChildEntry{},
		Removed:  []ChildEntry{},
		Modified: []ChildChange{},
	}
	common := len(bMaps)
	if len(aMaps) < common {
		common = len(aMaps)
	}
	for i := 0; i < common; i++ {
		if hashChildMap(bMaps[i]) == hashChildMap(aMaps[i]) {
			continue
		}
		out.Modified = append(out.Modified, ChildChange{
			ID:       aIDs[i],
			BeforeID: maybeBeforeID(aIDs[i], bIDs[i]),
			Order:    aOrders[i],
			Before:   bValues[i],
			After:    aValues[i],
		})
	}
	for i := common; i < len(bMaps); i++ {
		out.Removed = append(out.Removed, ChildEntry{
			ID:    bIDs[i],
			Order: bOrders[i],
			Value: bValues[i],
		})
	}
	for i := common; i < len(aMaps); i++ {
		out.Added = append(out.Added, ChildEntry{
			ID:    aIDs[i],
			Order: aOrders[i],
			Value: aValues[i],
		})
	}
	return out
}

// maybeBeforeID returns the Before row's ID only when it differs from
// the After row's ID. Equal IDs (the rare case where the editor
// somehow preserved them across a PUT, or two snapshots taken from
// the same underlying row) would just be noise in the wire payload.
func maybeBeforeID(afterID, beforeID string) string {
	if beforeID == afterID {
		return ""
	}
	return beforeID
}

// hashChildMap returns a stable hash over a JSON-decoded child row
// with the volatile identifier fields removed. Two rows with
// identical configuration content hash to the same value regardless
// of the regenerated UUIDs the repo Replace path assigns. We hash
// the canonical-marshal of a map keyed by sorted JSON tag, which
// encoding/json gives us for free (Marshal sorts map keys
// lexicographically).
func hashChildMap(m map[string]json.RawMessage) string {
	if m == nil {
		return ""
	}
	// Build a clean map without the id-ish keys.
	clean := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		switch k {
		case "id", "tenant_id", "pipeline_id":
			continue
		}
		clean[k] = v
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// stageMapAndValue marshals a Stage twice — once into a map (so the
// hash projection can strip identifier fields) and once into the
// full value (used as the wire payload). Both share the same source
// of truth (the encoding/json output) so they never disagree on
// what's a "field".
func stageMapAndValue(s *storage.Stage) (map[string]json.RawMessage, json.RawMessage) {
	if s == nil {
		return map[string]json.RawMessage{}, json.RawMessage("null")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return map[string]json.RawMessage{}, json.RawMessage("null")
	}
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &m)
	return m, raw
}

func transformMapAndValue(t *storage.Transform) (map[string]json.RawMessage, json.RawMessage) {
	if t == nil {
		return map[string]json.RawMessage{}, json.RawMessage("null")
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return map[string]json.RawMessage{}, json.RawMessage("null")
	}
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &m)
	return m, raw
}

func routingRuleMapAndValue(r *storage.RoutingRule) (map[string]json.RawMessage, json.RawMessage) {
	if r == nil {
		return map[string]json.RawMessage{}, json.RawMessage("null")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return map[string]json.RawMessage{}, json.RawMessage("null")
	}
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &m)
	return m, raw
}
