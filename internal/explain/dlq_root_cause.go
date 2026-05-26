package explain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"mqConnector/internal/storage"
)

// dlqRootCauseExplainer answers "why is this DLQ failure
// happening?". Two modes:
//
//   - dlqModeCluster — subject "dlq_cluster", id is the
//     error_fingerprint. Reads the cluster rollup + per-pipeline
//     audit to surface "all N failures hit stage X after edit at
//     T" when the timing correlates.
//   - dlqModeEntry — subject "dlq_entry", id is the entry id.
//     Reads the entry + its cluster + the pipeline's recent
//     revision history.
//
// Both modes emit a "fields" section listing the
// template-extracted variable fields (the <field>=customer.id
// style placeholders the fingerprint tokeniser produces) so the
// operator can see at a glance which dimensions changed.
type dlqRootCauseExplainer struct {
	e    *Engine
	mode dlqMode
}

type dlqMode int

const (
	dlqModeCluster dlqMode = iota
	dlqModeEntry
)

// dlqRecentLimit is the per-pipeline DLQ scan budget used to
// confirm the cluster's pipeline list and to surface a
// representative failure timestamp in entry mode.
const dlqRecentLimit = 25

// fieldExtractor matches placeholder tokens emitted by the
// fingerprint tokeniser — <uuid>, <int>, <field>, <path>, etc. —
// followed optionally by `=<value>`. The capture groups feed the
// "fields" section so the frontend can render them as a
// label/value table.
var fieldExtractor = regexp.MustCompile(`<([a-z_]+)>(?:=([^\s]+))?`)

// Explain dispatches per-subject mode; see Engine.Explain for the
// wire shape.
func (d *dlqRootCauseExplainer) Explain(ctx context.Context, id, tenantID string) (Explanation, error) {
	if d.e == nil || id == "" {
		return Explanation{}, ErrNotFound
	}
	if d.e.DLQ == nil {
		return Explanation{}, ErrNotFound
	}
	switch d.mode {
	case dlqModeCluster:
		return d.explainCluster(ctx, id, tenantID)
	case dlqModeEntry:
		return d.explainEntry(ctx, id, tenantID)
	}
	return Explanation{}, ErrUnknownSubject
}

// explainCluster covers the dlq_cluster subject. id is the
// fingerprint.
func (d *dlqRootCauseExplainer) explainCluster(ctx context.Context, fingerprint, tenantID string) (Explanation, error) {
	cluster, err := d.e.DLQ.ClusterByFingerprint(ctx, tenantID, fingerprint)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return Explanation{}, ErrNotFound
		}
		// Other repo error: degrade to an empty cluster rather
		// than failing the whole call. The handler's slog catches
		// the sources we report.
		cluster = storage.DLQClusterRow{Fingerprint: fingerprint}
	}

	// Recent DLQ entries across the tenant for this fingerprint
	// are surfaced via the cluster's representative pipeline
	// list. For the cluster mode we don't have a single pipeline
	// id, so we sample one entry per affected pipeline (if any)
	// to discover the failing stage + recent edits.
	var sampleEntries []*storage.DLQEntry
	for _, pid := range pipelinesFromCluster(cluster) {
		ents, _ := recentDLQEntries(ctx, d.e.DLQ, tenantID, pid, dlqRecentLimit)
		sampleEntries = append(sampleEntries, ents...)
	}

	// Audit context — the most recent edit on any of the
	// affected pipelines.
	var lastEdit *storage.AuditEntry
	for _, pid := range pipelinesFromCluster(cluster) {
		audits, _ := recentAuditForResource(ctx, d.e.Audit,
			tenantID, "/api/v1/pipelines/"+pid, 1)
		if len(audits) == 0 {
			continue
		}
		if lastEdit == nil || audits[0].At.After(lastEdit.At) {
			lastEdit = audits[0]
		}
	}

	failingStage := failingStageFromEntries(sampleEntries)
	headline, severity := dlqClusterHeadline(cluster, failingStage, lastEdit, d.e.now())

	facts := emptyFacts()
	facts = append(facts, Fact{
		Label:  "Cluster count",
		Value:  fmt.Sprintf("%d", cluster.Count),
		Source: "storage.DLQRepo.ClusterByFingerprint",
	})
	if failingStage != "" {
		facts = append(facts, Fact{
			Label:  "Failing stage",
			Value:  failingStage,
			Source: "storage.DLQEntry.FailingStageName",
		})
	}
	if !cluster.FirstSeen.IsZero() {
		facts = append(facts, Fact{
			Label:  "First seen",
			Value:  cluster.FirstSeen.UTC().Format(time.RFC3339),
			Source: "storage.DLQRepo",
		})
	}
	if !cluster.LastSeen.IsZero() {
		facts = append(facts, Fact{
			Label:  "Last seen",
			Value:  cluster.LastSeen.UTC().Format(time.RFC3339),
			Source: "storage.DLQRepo",
		})
	}
	if lastEdit != nil {
		facts = append(facts, Fact{
			Label:  "Last pipeline edit",
			Value:  fmt.Sprintf("%s by %s", lastEdit.Resource, lastEdit.Actor),
			Source: "storage.AuditRepo",
			AsOf:   lastEdit.At.UTC().Format(time.RFC3339),
		})
	}

	exp := Explanation{
		Subject:  "dlq_cluster",
		ID:       fingerprint,
		Headline: headline,
		Severity: severity,
		Facts:    facts,
		AsOf:     d.e.now(),
		Sources:  []string{"storage.DLQRepo.ClusterByFingerprint", "storage.DLQRepo.RecentForPipeline", "storage.AuditRepo"},
	}
	exp.Sections = append(exp.Sections, extractFieldsSection(cluster.Template))
	exp.Sections = append(exp.Sections, dlqClusterNarrativeSection(cluster, failingStage, lastEdit, d.e.now()))
	return exp, nil
}

// explainEntry covers the dlq_entry subject. id is the entry id.
func (d *dlqRootCauseExplainer) explainEntry(ctx context.Context, entryID, tenantID string) (Explanation, error) {
	entry, err := d.e.DLQ.GetEntry(ctx, tenantID, entryID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return Explanation{}, ErrNotFound
		}
		return Explanation{}, ErrNotFound
	}

	var cluster storage.DLQClusterRow
	if entry.ErrorFingerprint != "" {
		if c, cerr := d.e.DLQ.ClusterByFingerprint(ctx, tenantID, entry.ErrorFingerprint); cerr == nil {
			cluster = c
		}
	}

	// Revision context — compare the pipeline's currently-
	// deployed revision against the revision deployed at the
	// time the entry was created (if available). Today the
	// PipelinesSource only exposes LatestDeployedRevision; the
	// "revision at failure time" is approximated by checking
	// whether the latest deploy is before or after the entry's
	// created_at.
	var latestRev *storage.PipelineRevision
	if entry.PipelineID != "" && d.e.Pipelines != nil {
		if r, rerr := d.e.Pipelines.LatestDeployedRevision(ctx, tenantID, entry.PipelineID); rerr == nil {
			latestRev = r
		}
	}

	// Audit context — most recent edit on the pipeline.
	var lastEdit *storage.AuditEntry
	if entry.PipelineID != "" {
		audits, _ := recentAuditForResource(ctx, d.e.Audit,
			tenantID, "/api/v1/pipelines/"+entry.PipelineID, 1)
		if len(audits) > 0 {
			lastEdit = audits[0]
		}
	}

	headline, severity := dlqEntryHeadline(entry, lastEdit, d.e.now())

	facts := emptyFacts()
	if entry.FailingStageName != "" {
		facts = append(facts, Fact{
			Label:  "Failing stage",
			Value:  entry.FailingStageName,
			Source: "storage.DLQEntry.FailingStageName",
		})
	}
	if entry.PipelineID != "" {
		facts = append(facts, Fact{
			Label:  "Pipeline",
			Value:  entry.PipelineID,
			Source: "storage.DLQEntry.PipelineID",
		})
	}
	facts = append(facts, Fact{
		Label:  "Error reason",
		Value:  truncate(entry.ErrorReason, 240),
		Source: "storage.DLQEntry.ErrorReason",
	})
	facts = append(facts, Fact{
		Label:  "Created at",
		Value:  entry.CreatedAt.UTC().Format(time.RFC3339),
		Source: "storage.DLQEntry.CreatedAt",
	})
	if cluster.Count > 0 {
		facts = append(facts, Fact{
			Label:  "Cluster count (same fingerprint)",
			Value:  fmt.Sprintf("%d", cluster.Count),
			Source: "storage.DLQRepo.ClusterByFingerprint",
		})
	}
	if latestRev != nil && latestRev.DeployedAt != nil {
		facts = append(facts, Fact{
			Label:  "Latest deployed revision",
			Value:  fmt.Sprintf("rev %d", latestRev.RevisionNumber),
			Source: "storage.PipelineRevisions.LatestDeployed",
			AsOf:   latestRev.DeployedAt.UTC().Format(time.RFC3339),
		})
	}
	if lastEdit != nil {
		facts = append(facts, Fact{
			Label:  "Last pipeline edit",
			Value:  fmt.Sprintf("%s by %s", lastEdit.Resource, lastEdit.Actor),
			Source: "storage.AuditRepo",
			AsOf:   lastEdit.At.UTC().Format(time.RFC3339),
		})
	}

	exp := Explanation{
		Subject:  "dlq_entry",
		ID:       entryID,
		Headline: headline,
		Severity: severity,
		Facts:    facts,
		AsOf:     d.e.now(),
		Sources:  []string{"storage.DLQRepo.GetEntry", "storage.DLQRepo.ClusterByFingerprint", "storage.AuditRepo", "storage.PipelineRevisions"},
	}
	exp.Sections = append(exp.Sections, extractFieldsSection(entry.ErrorTemplate))
	exp.Sections = append(exp.Sections, dlqEntryNarrativeSection(entry, lastEdit, latestRev, d.e.now()))
	return exp, nil
}

// dlqClusterHeadline phrases the cluster headline based on
// whether a correlated edit / failing-stage attribution is
// available.
func dlqClusterHeadline(c storage.DLQClusterRow, failingStage string, lastEdit *storage.AuditEntry, now time.Time) (string, Severity) {
	if c.Count == 0 {
		return "Cluster not found or empty.", SeverityInfo
	}
	severity := SeverityWarning
	if c.Count >= 50 {
		severity = SeverityCritical
	}
	stagePhrase := "the failing stage"
	if failingStage != "" {
		stagePhrase = "the " + failingStage + " stage"
	}
	if lastEdit != nil && now.Sub(lastEdit.At) < 24*time.Hour {
		return fmt.Sprintf(
				"All %d failures hit %s after pipeline edit at %s.",
				c.Count, stagePhrase, lastEdit.At.UTC().Format(time.RFC3339)),
			severity
	}
	return fmt.Sprintf("%d failures clustered on %s.", c.Count, stagePhrase), severity
}

// dlqEntryHeadline phrases the per-entry headline.
func dlqEntryHeadline(e *storage.DLQEntry, lastEdit *storage.AuditEntry, now time.Time) (string, Severity) {
	stagePhrase := "an unknown stage"
	if e.FailingStageName != "" {
		stagePhrase = "the " + e.FailingStageName + " stage"
	}
	severity := SeverityWarning
	if lastEdit != nil && now.Sub(lastEdit.At) < 24*time.Hour && lastEdit.At.Before(e.CreatedAt) {
		return fmt.Sprintf("Failure at %s; pipeline was edited %s earlier.",
				stagePhrase, e.CreatedAt.Sub(lastEdit.At).Round(time.Minute)),
			severity
	}
	return fmt.Sprintf("Failure at %s on %s.",
		stagePhrase, e.CreatedAt.UTC().Format(time.RFC3339)), severity
}

// pipelinesFromCluster is a placeholder for the cluster's
// pipelines_affected list. DLQClusterRow today doesn't carry it
// (it's a separate query in the cluster handler). When the
// explain Source surface grows ClusterPipelines we'll replace
// this; for now the explainer doesn't fan out per affected
// pipeline. Returns an empty slice — the caller's loop becomes
// a no-op rather than a crash.
func pipelinesFromCluster(_ storage.DLQClusterRow) []string {
	return nil
}

// failingStageFromEntries reads the failing-stage attribution
// from a sample. Returns the most common stage name; ties broken
// by alphabetical order so the output is deterministic.
func failingStageFromEntries(entries []*storage.DLQEntry) string {
	if len(entries) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, e := range entries {
		if e == nil || e.FailingStageName == "" {
			continue
		}
		counts[e.FailingStageName]++
	}
	if len(counts) == 0 {
		return ""
	}
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(counts))
	for k, v := range counts {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	return all[0].k
}

// extractFieldsSection parses the error template for placeholder
// tokens and renders them as a `fields` section. Operators see a
// label/value table of the variable parts the fingerprint
// collapsed — useful when the same template fires for many
// different concrete inputs.
func extractFieldsSection(template string) Section {
	fields := []map[string]string{}
	if template != "" {
		matches := fieldExtractor.FindAllStringSubmatch(template, -1)
		seen := map[string]bool{}
		for _, m := range matches {
			placeholder := m[1]
			value := ""
			if len(m) >= 3 {
				value = m[2]
			}
			key := placeholder + "=" + value
			if seen[key] {
				continue
			}
			seen[key] = true
			row := map[string]string{"kind": placeholder}
			if value != "" {
				row["value"] = value
			}
			fields = append(fields, row)
		}
	}
	data, _ := json.Marshal(map[string]any{"template": template, "fields": fields})
	return Section{
		Kind:  "fields",
		Title: "Template fields",
		Data:  data,
	}
}

// dlqClusterNarrativeSection emits the single-sentence hypothesis
// for the cluster mode. Hedged: "appears correlated with" rather
// than "caused by" — the explainer doesn't claim causality.
func dlqClusterNarrativeSection(c storage.DLQClusterRow, failingStage string, lastEdit *storage.AuditEntry, now time.Time) Section {
	var b strings.Builder
	fmt.Fprintf(&b, "Cluster of %d failures.", c.Count)
	if failingStage != "" {
		fmt.Fprintf(&b, " Most rows attribute the failure to the %s stage.", failingStage)
	}
	if lastEdit != nil && now.Sub(lastEdit.At) < 24*time.Hour {
		fmt.Fprintf(&b, " A recent pipeline edit at %s appears correlated.", lastEdit.At.UTC().Format(time.RFC3339))
	}
	data, _ := json.Marshal(map[string]string{"text": b.String()})
	return Section{Kind: "narrative", Title: "Root cause hypothesis", Data: data}
}

// dlqEntryNarrativeSection emits the per-entry hypothesis.
func dlqEntryNarrativeSection(e *storage.DLQEntry, lastEdit *storage.AuditEntry, rev *storage.PipelineRevision, now time.Time) Section {
	var b strings.Builder
	if e.FailingStageName != "" {
		fmt.Fprintf(&b, "Failure attributed to the %s stage.", e.FailingStageName)
	} else {
		b.WriteString("Failure has no stage attribution (likely a destination-send error).")
	}
	if rev != nil && rev.DeployedAt != nil {
		if rev.DeployedAt.Before(e.CreatedAt) {
			fmt.Fprintf(&b, " Latest deploy (rev %d) landed before this entry was created.", rev.RevisionNumber)
		} else {
			fmt.Fprintf(&b, " Latest deploy (rev %d) is more recent than this entry — re-running may now succeed.", rev.RevisionNumber)
		}
	}
	if lastEdit != nil && now.Sub(lastEdit.At) < 24*time.Hour {
		fmt.Fprintf(&b, " A recent pipeline edit at %s appears correlated.", lastEdit.At.UTC().Format(time.RFC3339))
	}
	data, _ := json.Marshal(map[string]string{"text": b.String()})
	return Section{Kind: "narrative", Title: "Root cause hypothesis", Data: data}
}
