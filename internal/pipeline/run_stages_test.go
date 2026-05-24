package pipeline

import (
	"bytes"
	"context"
	"testing"
)

// mutatingStage replaces the body wholesale with the bytes in Out. Useful
// for exercising the per-stage snapshot logic: each stage's recorded
// Body should reflect what THAT stage produced, not the final state.
type mutatingStage struct {
	name string
	out  []byte
	fmt  Format
}

func (m *mutatingStage) Name() string { return m.name }
func (m *mutatingStage) Execute(_ context.Context, _ []byte, _ Format) ([]byte, Format, *Result, error) {
	// Return a fresh slice — caller takes ownership.
	return append([]byte(nil), m.out...), m.fmt, nil, nil
}

// namedErrStage fails with a stage-specific error message so we can
// distinguish it from other failing stages in assertions.
type namedErrStage struct {
	name string
	msg  string
}

func (n *namedErrStage) Name() string { return n.name }
func (n *namedErrStage) Execute(_ context.Context, _ []byte, f Format) ([]byte, Format, *Result, error) {
	return nil, f, nil, errSimulated(n.msg)
}

// TestRunStages_StageRun_BodyCapturedOnSuccess verifies a single-stage
// chain populates Body and Format on the recorded StageRun and leaves
// Err empty / Failed false.
func TestRunStages_StageRun_BodyCapturedOnSuccess(t *testing.T) {
	input := []byte(`{"a":1}`)
	out, err := RunStages(context.Background(), []Stage{&okStage{}}, input)
	if err != nil {
		t.Fatalf("RunStages: %v", err)
	}
	if len(out.Runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(out.Runs))
	}
	run := out.Runs[0]
	if run.Failed {
		t.Fatalf("Failed = true on success path")
	}
	if run.Err != "" {
		t.Fatalf("Err = %q on success path", run.Err)
	}
	// okStage passes input through unchanged.
	if !bytes.Equal(run.Body, input) {
		t.Fatalf("Body = %q, want %q", run.Body, input)
	}
	if run.Format == "" {
		t.Fatalf("Format empty on success — Detect should have classified the input")
	}
	if run.Format != FormatJSON {
		t.Fatalf("Format = %q, want json", run.Format)
	}
}

// TestRunStages_StageRun_MultiStageCapturesPerStageState verifies that
// in a multi-stage chain, each StageRun.Body reflects that stage's
// output (not the final state).
func TestRunStages_StageRun_MultiStageCapturesPerStageState(t *testing.T) {
	s1 := &mutatingStage{name: "first", out: []byte(`after-first`), fmt: FormatBytes}
	s2 := &mutatingStage{name: "second", out: []byte(`after-second`), fmt: FormatBytes}

	out, err := RunStages(context.Background(), []Stage{s1, s2}, []byte(`in`))
	if err != nil {
		t.Fatalf("RunStages: %v", err)
	}
	if len(out.Runs) != 2 {
		t.Fatalf("want 2 runs, got %d", len(out.Runs))
	}
	if got, want := string(out.Runs[0].Body), "after-first"; got != want {
		t.Errorf("Runs[0].Body = %q, want %q", got, want)
	}
	if got, want := string(out.Runs[1].Body), "after-second"; got != want {
		t.Errorf("Runs[1].Body = %q, want %q", got, want)
	}
	if bytes.Equal(out.Runs[0].Body, out.Runs[1].Body) {
		t.Errorf("per-stage Body snapshots are identical; expected divergence")
	}
	if out.Runs[0].Name != "first" || out.Runs[1].Name != "second" {
		t.Errorf("stage names not preserved in order: %v / %v",
			out.Runs[0].Name, out.Runs[1].Name)
	}
}

// TestRunStages_StageRun_BodyIsCopy proves that mutating the captured
// per-stage body after RunStages returns does NOT corrupt the final
// outcome body or the next stage's snapshot. Catches the classic
// shared-slice bug where every stage records the same backing buffer.
func TestRunStages_StageRun_BodyIsCopy(t *testing.T) {
	s1 := &mutatingStage{name: "first", out: []byte(`hello world`), fmt: FormatBytes}
	s2 := &mutatingStage{name: "second", out: []byte(`final body!`), fmt: FormatBytes}

	out, err := RunStages(context.Background(), []Stage{s1, s2}, []byte(`in`))
	if err != nil {
		t.Fatalf("RunStages: %v", err)
	}
	if len(out.Runs) != 2 {
		t.Fatalf("want 2 runs, got %d", len(out.Runs))
	}
	// Snapshot the pre-mutation state so we can diff after.
	want1 := append([]byte(nil), out.Runs[1].Body...)
	wantFinal := append([]byte(nil), out.Body...)

	// Aggressively overwrite the first stage's recorded body.
	for i := range out.Runs[0].Body {
		out.Runs[0].Body[i] = 'X'
	}

	if !bytes.Equal(out.Runs[1].Body, want1) {
		t.Errorf("mutating Runs[0].Body changed Runs[1].Body: got %q, want %q",
			out.Runs[1].Body, want1)
	}
	if !bytes.Equal(out.Body, wantFinal) {
		t.Errorf("mutating Runs[0].Body changed outcome.Body: got %q, want %q",
			out.Body, wantFinal)
	}
}

// TestRunStages_StageRun_FailedStageStopsDownstream verifies that when
// stage 2 of 3 fails:
//   - Runs has exactly 2 entries (stage 3 never ran)
//   - Runs[1].Failed is true and Err carries the underlying error text
//   - Runs[1].Body shows the INPUT to the failed stage (the output of
//     stage 1) so the operator can see what the stage choked on
func TestRunStages_StageRun_FailedStageStopsDownstream(t *testing.T) {
	s1 := &mutatingStage{name: "first", out: []byte(`stage1-output`), fmt: FormatBytes}
	s2 := &namedErrStage{name: "second", msg: "boom: bad input"}
	s3Sentinel := &okStage{} // must NOT execute

	out, err := RunStages(context.Background(), []Stage{s1, s2, s3Sentinel}, []byte(`in`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(out.Runs) != 2 {
		t.Fatalf("want 2 runs (downstream skipped), got %d", len(out.Runs))
	}
	if s3Sentinel.count.Load() != 0 {
		t.Errorf("downstream stage executed after failure: count=%d",
			s3Sentinel.count.Load())
	}
	failed := out.Runs[1]
	if !failed.Failed {
		t.Errorf("Runs[1].Failed = false, want true")
	}
	if failed.Err == "" {
		t.Errorf("Runs[1].Err empty, want %q", "boom: bad input")
	}
	if failed.Err != "boom: bad input" {
		t.Errorf("Runs[1].Err = %q, want %q", failed.Err, "boom: bad input")
	}
	// Body reflects the INPUT to the failing stage (stage 1's output).
	if got, want := string(failed.Body), "stage1-output"; got != want {
		t.Errorf("Runs[1].Body = %q, want %q (input to failing stage)", got, want)
	}
	// Stage 1 ran cleanly.
	if out.Runs[0].Failed || out.Runs[0].Err != "" {
		t.Errorf("Runs[0] should be clean: %+v", out.Runs[0])
	}
}

// TestRunStages_StageRun_DurationStillPopulated re-asserts the original
// StageRun contract (Name + Duration + Failed) survives the field
// additions. Guards against accidental regressions of the metrics path
// that the executor depends on.
func TestRunStages_StageRun_DurationStillPopulated(t *testing.T) {
	out, err := RunStages(context.Background(), []Stage{&okStage{}, &okStage{}}, []byte(`x`))
	if err != nil {
		t.Fatalf("RunStages: %v", err)
	}
	if len(out.Runs) != 2 {
		t.Fatalf("want 2 runs, got %d", len(out.Runs))
	}
	for i, r := range out.Runs {
		if r.Name != "okstage" {
			t.Errorf("Runs[%d].Name = %q, want %q", i, r.Name, "okstage")
		}
		if r.Duration < 0 {
			t.Errorf("Runs[%d].Duration = %v, want >= 0", i, r.Duration)
		}
		if r.Failed {
			t.Errorf("Runs[%d].Failed = true", i)
		}
	}
}
