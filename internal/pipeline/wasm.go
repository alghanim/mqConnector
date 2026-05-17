// WASM pipeline stage. Operators write a small Go (or Rust /
// AssemblyScript / TinyGo) function that compiles to WebAssembly,
// upload the .wasm via POST /api/v1/plugins, and reference it from
// a stage with stage_type=wasm.
//
// Contract (see docs/PLUGIN_DESIGN.md §3.1 for the rationale):
//
//   //go:wasmexport execute
//   func execute(inPtr, inLen uint32) (outPtr, outLen, errPtr, errLen uint32)
//
//   //go:wasmexport allocate
//   func allocate(size uint32) uint32
//
// The host:
//   1. Calls allocate(len(in)) to reserve a buffer in linear memory
//      and gets back the pointer.
//   2. Writes the input bytes into linear memory at that pointer.
//   3. Calls execute(inPtr, inLen). The plugin processes the bytes
//      and returns (outPtr, outLen, errPtr, errLen).
//   4. Reads outLen bytes at outPtr (the new message body) OR
//      reads errLen bytes at errPtr (utf-8 error string; the
//      message goes to DLQ with this as ErrorReason).
//
// Sandboxing: the plugin is loaded into a wazero runtime with NO
// imports. The plugin cannot call out — no filesystem, no network,
// no syscalls, no host time. Memory is hard-capped (default 32 MiB).
// Wall-clock execution is bounded by ctx — handlers wrap the call
// in context.WithTimeout.
//
// This is the design from docs/PLUGIN_DESIGN.md option B (WASM via
// wazero); option A (plugin.Open) is too fragile cross-platform and
// option C (gRPC sidecar) adds IPC cost not justified at our
// throughput tier.

package pipeline

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero"
)

// WasmStageLimits caps the resources a plugin can consume per message.
// Tripping any cap aborts execution and lands the message in DLQ.
type WasmStageLimits struct {
	// MaxMemoryPages caps the WASM linear memory in 64-KiB pages.
	// 512 pages = 32 MiB. wazero enforces this at compile time so a
	// plugin that declares more than the cap fails to load.
	MaxMemoryPages uint32
	// MaxOutputBytes caps the response bytes from a single execute()
	// call. A plugin that allocates 10 MiB of output gets DLQ'd
	// rather than balloon the pipeline's memory.
	MaxOutputBytes uint32
}

// DefaultWasmLimits is the safe baseline. Tunable per-stage via the
// stage_config JSON.
var DefaultWasmLimits = WasmStageLimits{
	MaxMemoryPages: 512,             // 32 MiB
	MaxOutputBytes: 4 * 1024 * 1024, // 4 MiB
}

// WasmStage runs a precompiled wazero module against the message
// bytes. Built by Build() when a stage row has stage_type=wasm and
// the runtime has loaded the referenced plugin.
type WasmStage struct {
	PluginName string
	Module     wazero.CompiledModule
	Runtime    wazero.Runtime
	Limits     WasmStageLimits

	// mu serialises Instantiate; wazero modules instantiate
	// independently but we want one instance per Execute call and
	// not to leak instances. A pool could replace this later.
	mu sync.Mutex
}

// Name satisfies pipeline.Stage. Includes the plugin name so logs
// and traces distinguish between WASM stages on the same pipeline.
func (s *WasmStage) Name() string {
	if s.PluginName == "" {
		return "wasm"
	}
	return "wasm:" + s.PluginName
}

// Execute instantiates a fresh module instance, copies the message
// bytes in, calls execute(), and reads the result out. One instance
// per call is the simple-and-safe option: each message gets a fresh
// linear-memory state, so a plugin can't leak data between messages.
// Performance is fine — wazero instantiation is well under 1ms for a
// small module.
func (s *WasmStage) Execute(ctx context.Context, message []byte, format Format) ([]byte, Format, *Result, error) {
	if s.Module == nil || s.Runtime == nil {
		return nil, format, nil, errors.New("wasm: runtime not initialised")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	mod, err := s.Runtime.InstantiateModule(ctx, s.Module, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, format, nil, fmt.Errorf("wasm: instantiate: %w", err)
	}
	defer func() { _ = mod.Close(ctx) }()

	mem := mod.Memory()
	if mem == nil {
		return nil, format, nil, errors.New("wasm: module has no memory export")
	}

	allocate := mod.ExportedFunction("allocate")
	execute := mod.ExportedFunction("execute")
	if allocate == nil || execute == nil {
		return nil, format, nil, errors.New("wasm: module missing required exports allocate + execute")
	}

	// 1. allocate(len(message)) → in_ptr
	allocRes, err := allocate.Call(ctx, uint64(len(message)))
	if err != nil {
		return nil, format, nil, fmt.Errorf("wasm: allocate: %w", err)
	}
	if len(allocRes) != 1 {
		return nil, format, nil, fmt.Errorf("wasm: allocate returned %d values, want 1", len(allocRes))
	}
	inPtr := uint32(allocRes[0])

	// 2. write the input
	if ok := mem.Write(inPtr, message); !ok {
		return nil, format, nil, errors.New("wasm: input write out of bounds")
	}

	// 3. execute(inPtr, inLen) → (outPtr, outLen, errPtr, errLen)
	execRes, err := execute.Call(ctx, uint64(inPtr), uint64(len(message)))
	if err != nil {
		return nil, format, nil, fmt.Errorf("wasm: execute: %w", err)
	}
	if len(execRes) != 4 {
		return nil, format, nil, fmt.Errorf("wasm: execute returned %d values, want 4", len(execRes))
	}
	outPtr := uint32(execRes[0])
	outLen := uint32(execRes[1])
	errPtr := uint32(execRes[2])
	errLen := uint32(execRes[3])

	// 4a. error path
	if errLen > 0 {
		errBytes, ok := mem.Read(errPtr, errLen)
		if !ok {
			return nil, format, nil, errors.New("wasm: error region out of bounds")
		}
		return nil, format, nil, fmt.Errorf("wasm: %s", string(errBytes))
	}

	// 4b. success path
	cap := s.Limits.MaxOutputBytes
	if cap == 0 {
		cap = DefaultWasmLimits.MaxOutputBytes
	}
	if outLen > cap {
		return nil, format, nil, fmt.Errorf("wasm: output %d bytes exceeds cap %d", outLen, cap)
	}
	out, ok := mem.Read(outPtr, outLen)
	if !ok {
		return nil, format, nil, errors.New("wasm: output region out of bounds")
	}
	// Copy because wazero may reuse the underlying memory after the
	// module closes.
	result := make([]byte, len(out))
	copy(result, out)
	return result, format, nil, nil
}

// CompileWasm loads + validates a plugin blob with the given limits.
// Returns a CompiledModule that callers stash in a WasmStage. Failed
// compile returns an error and the caller should reject the upload.
func CompileWasm(ctx context.Context, runtime wazero.Runtime, blob []byte, limits WasmStageLimits) (wazero.CompiledModule, error) {
	if limits.MaxMemoryPages == 0 {
		limits.MaxMemoryPages = DefaultWasmLimits.MaxMemoryPages
	}
	mod, err := runtime.CompileModule(ctx, blob)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	// Refuse plugins that declare more memory than we allow. wazero
	// caps memory at the runtime level too, but failing fast at
	// upload time gives the operator a clearer error than a
	// runtime-time fault later. Max() returns (pages, set) — pages
	// is only meaningful when set is true.
	for _, mem := range mod.ExportedMemories() {
		maxPages, set := mem.Max()
		if set && maxPages > limits.MaxMemoryPages {
			_ = mod.Close(ctx)
			return nil, fmt.Errorf("plugin declares max %d memory pages; cap is %d",
				maxPages, limits.MaxMemoryPages)
		}
	}
	// Refuse plugins that import anything. The sandbox property is
	// only meaningful if the host doesn't expose function imports.
	if imports := mod.ImportedFunctions(); len(imports) > 0 {
		names := make([]string, 0, len(imports))
		for _, im := range imports {
			module, name, _ := im.Import()
			names = append(names, module+"."+name)
		}
		_ = mod.Close(ctx)
		return nil, fmt.Errorf("plugin imports host functions (not allowed): %v", names)
	}
	// Confirm the required exports exist.
	if mod.ExportedFunctions()["execute"] == nil {
		_ = mod.Close(ctx)
		return nil, errors.New("plugin missing 'execute' export")
	}
	if mod.ExportedFunctions()["allocate"] == nil {
		_ = mod.Close(ctx)
		return nil, errors.New("plugin missing 'allocate' export")
	}
	return mod, nil
}

// NewWasmRuntime returns a wazero runtime configured for our sandbox
// posture: no host imports, memory cap at the runtime level. One
// runtime is shared across all WasmStage instances; modules are
// independent within it.
func NewWasmRuntime(ctx context.Context, limits WasmStageLimits) wazero.Runtime {
	if limits.MaxMemoryPages == 0 {
		limits.MaxMemoryPages = DefaultWasmLimits.MaxMemoryPages
	}
	cfg := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(limits.MaxMemoryPages)
	return wazero.NewRuntimeWithConfig(ctx, cfg)
}

// Ensure the binary encoder linkage stays — used by callers that
// compose the 4 return values for tests.
var _ = binary.LittleEndian
