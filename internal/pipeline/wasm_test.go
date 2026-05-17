package pipeline

import (
	"context"
	"strings"
	"testing"
)

// End-to-end "identity plugin" execution coverage lives in the
// integration suite (a real .wasm fixture compiled via TinyGo /
// wat2wasm out-of-band). The unit tests in this file pin the
// validation gates that run BEFORE execution — those are the
// security-critical hooks (no host imports, required exports
// present, memory limits enforced). The actual host↔plugin memory
// + return-value protocol is exercised by wazero's own test suite.

// TestWasm_RejectsImports — a plugin that imports any host function
// must fail compile. The sandbox property is only meaningful if the
// host doesn't expose syscalls; this test pins the gate.
func TestWasm_RejectsImports(t *testing.T) {
	ctx := context.Background()
	rt := NewWasmRuntime(ctx, DefaultWasmLimits)
	defer rt.Close(ctx)

	// Minimal module that imports an env function — fails our gate.
	withImport := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		// type section: () -> ()
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
		// import section: env.exit ()->()
		0x02, 0x0c, 0x01, 0x03, 'e', 'n', 'v', 0x04, 'e', 'x', 'i', 't', 0x00, 0x00,
	}
	_, err := CompileWasm(ctx, rt, withImport, DefaultWasmLimits)
	if err == nil {
		t.Fatal("compile should reject modules with host imports")
	}
	if !strings.Contains(err.Error(), "imports") {
		t.Errorf("error should mention imports, got %v", err)
	}
}

// TestWasm_RejectsMissingExports — execute + allocate are both
// required. A blob lacking either fails compile rather than
// runtime, so the operator sees the problem at upload time.
func TestWasm_RejectsMissingExports(t *testing.T) {
	ctx := context.Background()
	rt := NewWasmRuntime(ctx, DefaultWasmLimits)
	defer rt.Close(ctx)

	// Bare module with just a memory export — no allocate, no execute.
	bare := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		// memory section: 1 page
		0x05, 0x03, 0x01, 0x00, 0x01,
		// export memory only
		0x07, 0x0a, 0x01, 0x06, 'm', 'e', 'm', 'o', 'r', 'y', 0x02, 0x00,
	}
	_, err := CompileWasm(ctx, rt, bare, DefaultWasmLimits)
	if err == nil {
		t.Fatal("compile should reject modules missing required exports")
	}
}
