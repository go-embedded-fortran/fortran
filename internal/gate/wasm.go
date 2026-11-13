package gate

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-ndarray/ndarray"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// The toolchain of contender B is go-embedded-c's oracle: clang 23.1.4
// compiling to wasm32-wasip1 against the pinned wasi-libc sysroot, installed by
// `coracle toolchain` (go install github.com/go-embedded-c/c/cmd/coracle@v0.2.1;
// CORACLE_HOME). go-embedded-c's oracle and provision packages are internal and
// cannot be imported, so this package does not copy them: it asks the coracle
// binary for the version and provisioning, and only uses the layout it
// installs (llvm/bin/clang, sysroot/). The wazero version is the one
// go-embedded-c pins (go.mod: v1.12.0).
const (
	ClangVersion  = "23.1.4"
	WasiTarget    = "wasm32-wasip1"
	EnvCoracle    = "CORACLE_HOME"
	stackBytes    = 1 << 20
	memoryPages   = 16384 // 1 GiB of linear memory, enough for K4's four 80 MB arrays
	hostModule    = "fortran_gate"
	compileBudget = 2 * time.Minute
)

// Clang is a located clang and sysroot.
type Clang struct {
	Exe, Sysroot string
}

// LocateClang finds the oracle toolchain below home (CORACLE_HOME) and checks
// that clang is the pinned release.
func LocateClang(ctx context.Context, home string) (*Clang, error) {
	if home == "" {
		return nil, fmt.Errorf("gate: set %s to a directory made by `coracle toolchain` (go install github.com/go-embedded-c/c/cmd/coracle@v0.2.1)", EnvCoracle)
	}
	c := &Clang{Exe: filepath.Join(home, "llvm", "bin", "clang"), Sysroot: filepath.Join(home, "sysroot")}
	if _, err := os.Stat(c.Exe); err != nil {
		return nil, fmt.Errorf("gate: no clang in %s (run `coracle toolchain` with %s set): %w", home, EnvCoracle, err)
	}
	out, err := exec.CommandContext(ctx, c.Exe, "--version").Output()
	if err != nil {
		return nil, fmt.Errorf("gate: %s --version: %w", c.Exe, err)
	}
	if !strings.Contains(string(out), "clang version "+ClangVersion) {
		first, _, _ := strings.Cut(string(out), "\n")
		return nil, fmt.Errorf("gate: %q is not clang %s", first, ClangVersion)
	}
	return c, nil
}

// Compile compiles the C transliteration at src to a wasm module with the
// optimisation flag opt (-O0 for B-near, -O2 for B-upper).
func (c *Clang) Compile(ctx context.Context, src, opt string, defs []string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "foracle-gate-wasm-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "k.wasm")
	args := []string{
		"--target=" + WasiTarget, "--sysroot=" + c.Sysroot, opt, "-std=gnu17", "-w",
		fmt.Sprintf("-Wl,-z,stack-size=%d", stackBytes),
	}
	args = append(args, defs...)
	args = append(args, src, "-o", out)
	ctx, cancel := context.WithTimeout(ctx, compileBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Exe, args...)
	cmd.Env = []string{"LC_ALL=C"}
	var diag bytes.Buffer
	cmd.Stdout, cmd.Stderr = &diag, &diag
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gate: clang %s %s: %w\n%s", opt, filepath.Base(src), err, diag.String())
	}
	return os.ReadFile(out)
}

// Engine runs wasm modules on wazero (the compiler engine where wazero has one:
// amd64 and arm64) with the host imports the array kernels use.
type Engine struct {
	rt   wazero.Runtime
	host hostState
}

// hostState is what the host imports record for the run in progress.
type hostState struct {
	copyNs int64 // time spent copying between linear memory and Go arrays
	err    error
}

// NewEngine creates the runtime and the host module.
func NewEngine(ctx context.Context) (*Engine, error) {
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(memoryPages).WithCloseOnContextDone(true))
	e := &Engine{rt: rt}
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		return nil, err
	}
	i32 := api.ValueTypeI32
	_, err := rt.NewHostModuleBuilder(hostModule).
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(e.matmul), []api.ValueType{i32, i32, i32, i32}, nil).Export("matmul").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(e.muladd), []api.ValueType{i32, i32, i32, i32, i32}, nil).Export("muladd").
		Instantiate(ctx)
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}
	return e, nil
}

// Close releases the runtime.
func (e *Engine) Close(ctx context.Context) error { return e.rt.Close(ctx) }

// Module is a compiled kernel.
type Module struct {
	e  *Engine
	cm wazero.CompiledModule
}

// Compile compiles a wasm module (outside any timing).
func (e *Engine) Compile(ctx context.Context, wasm []byte) (*Module, error) {
	cm, err := e.rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, err
	}
	return &Module{e: e, cm: cm}, nil
}

// Run instantiates and runs the module once. It returns what the program
// printed and the time the host imports spent copying arrays in and out of
// linear memory. The guest times itself with the monotonic clock
// (WithSysNanotime: wazero's default clock is not real).
func (m *Module) Run(ctx context.Context) (stdout []byte, copySeconds float64, err error) {
	var out, errb bytes.Buffer
	m.e.host = hostState{}
	cfg := wazero.NewModuleConfig().WithStdout(&out).WithStderr(&errb).WithSysNanotime().WithSysWalltime().WithName("")
	mod, err := m.e.rt.InstantiateModule(ctx, m.cm, cfg)
	if mod != nil {
		defer mod.Close(ctx)
	}
	var ee *sys.ExitError
	switch {
	case errors.As(err, &ee) && ee.ExitCode() == 0:
	case err != nil:
		return nil, 0, fmt.Errorf("gate: wasm run: %w (stderr %q)", err, errb.String())
	}
	if m.e.host.err != nil {
		return nil, 0, m.e.host.err
	}
	return out.Bytes(), float64(m.e.host.copyNs) * 1e-9, nil
}

// decode copies n float64 values out of linear memory at ptr into dst.
func decode(mem api.Memory, ptr uint32, dst []float64) error {
	b, ok := mem.Read(ptr, uint32(len(dst))*8)
	if !ok {
		return fmt.Errorf("gate: host import: %d doubles at %d are outside linear memory", len(dst), ptr)
	}
	for i := range dst {
		dst[i] = math.Float64frombits(binary.LittleEndian.Uint64(b[8*i:]))
	}
	return nil
}

// encode copies src into linear memory at ptr.
func encode(mem api.Memory, ptr uint32, src []float64) error {
	b, ok := mem.Read(ptr, uint32(len(src))*8)
	if !ok {
		return fmt.Errorf("gate: host import: %d doubles at %d are outside linear memory", len(src), ptr)
	}
	for i, v := range src {
		binary.LittleEndian.PutUint64(b[8*i:], math.Float64bits(v))
	}
	return nil
}

// newBuf is a go-ndarray array of the given shape and its own storage.
func newBuf(shape ...int) (*ndarray.Array, []float64, error) {
	a, err := ndarray.Zeros(shape...)
	if err != nil {
		return nil, nil, err
	}
	d, ok := ndarray.Data[float64](a)
	if !ok {
		return nil, nil, errors.New("gate: not a float64 array")
	}
	return a, d, nil
}

// matmul is host_matmul(a, b, c, n): c = a b for n x n column-major arrays in
// linear memory. They are copied into go-ndarray arrays (a column-major buffer
// read row-major is the transpose, so c' = b' a' needs no further work) and the
// result is copied back.
func (e *Engine) matmul(_ context.Context, m api.Module, st []uint64) {
	pa, pb, pc, n := uint32(st[0]), uint32(st[1]), uint32(st[2]), int(int32(st[3]))
	if e.host.err != nil {
		return
	}
	fail := func(err error) { e.host.err = err }
	if n < 0 || n > 1<<14 {
		fail(fmt.Errorf("gate: host matmul: n = %d out of range", n))
		return
	}
	mem := m.Memory()
	t := time.Now()
	A, da, err := newBuf(n, n)
	if err != nil {
		fail(err)
		return
	}
	B, db, err := newBuf(n, n)
	if err != nil {
		fail(err)
		return
	}
	if err := decode(mem, pa, da); err != nil {
		fail(err)
		return
	}
	if err := decode(mem, pb, db); err != nil {
		fail(err)
		return
	}
	e.host.copyNs += int64(time.Since(t))
	r, err := B.MatMul(A)
	if err != nil {
		fail(err)
		return
	}
	dr, _ := ndarray.Data[float64](r)
	t = time.Now()
	if err := encode(mem, pc, dr); err != nil {
		fail(err)
		return
	}
	e.host.copyNs += int64(time.Since(t))
}

// muladd is host_muladd(a, b, c, d, n): a = b*c + d for n-element arrays.
func (e *Engine) muladd(_ context.Context, m api.Module, st []uint64) {
	pa, pb, pc, pd, n := uint32(st[0]), uint32(st[1]), uint32(st[2]), uint32(st[3]), int(int32(st[4]))
	if e.host.err != nil {
		return
	}
	fail := func(err error) { e.host.err = err }
	if n < 0 || n > 1<<28 {
		fail(fmt.Errorf("gate: host muladd: n = %d out of range", n))
		return
	}
	mem := m.Memory()
	t := time.Now()
	var arr [4]*ndarray.Array
	var dat [4][]float64
	for i := range arr {
		var err error
		if arr[i], dat[i], err = newBuf(n); err != nil {
			fail(err)
			return
		}
	}
	// arr[0] is a, arr[1] b, arr[2] c, arr[3] d
	for i, p := range []uint32{pb, pc, pd} {
		if err := decode(mem, p, dat[i+1]); err != nil {
			fail(err)
			return
		}
	}
	e.host.copyNs += int64(time.Since(t))
	tmp, err := arr[1].Mul(arr[2])
	if err != nil {
		fail(err)
		return
	}
	if err := tmp.AddInto(arr[0], arr[3]); err != nil {
		fail(err)
		return
	}
	t = time.Now()
	if err := encode(mem, pa, dat[0]); err != nil {
		fail(err)
		return
	}
	e.host.copyNs += int64(time.Since(t))
}
