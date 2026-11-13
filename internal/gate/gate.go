package gate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/go-embedded-fortran/fortran/eval"
	"github.com/go-embedded-fortran/fortran/internal/oracle"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// Contender names, as printed.
const (
	CA    = "A"
	CB0   = "B-near"
	CB2   = "B-upper"
	CGF   = "gfortran"
	CDire = "direct"
)

// Options configure one gate run.
type Options struct {
	Dir    string // the kernels directory (testdata/gate)
	Rounds int
	Smoke  bool // tiny sizes, correctness only
	Force  bool // measure above the load limit
	// Coracle is CORACLE_HOME.
	Coracle string
	// Gfortran is the located and verified reference.
	Gfortran *oracle.Toolchain
	// Load reads the 1-minute load average (oracle.Load1 when nil).
	Load func(context.Context) (float64, error)
	Log  io.Writer
}

// Cell is the result of one contender on one kernel.
type Cell struct {
	Seconds     []float64 // one per round
	CopySeconds []float64 // B on the array kernels: time spent in copies alone
	Checksum    string
	Copies      int64 // A: array elements copied by the evaluator (K3-matmul)
}

// Median is the median time.
func (c *Cell) Median() float64 { return oracle.Median(c.Seconds) }

// Spread is max - min.
func (c *Cell) Spread() float64 {
	if len(c.Seconds) == 0 {
		return 0
	}
	return slices.Max(c.Seconds) - slices.Min(c.Seconds)
}

// Result is a finished gate run.
type Result struct {
	Smoke   bool
	Rounds  int
	Kernels []Kernel
	Sizes   []Size
	Exact   []float64
	Bound   []float64
	Cells   map[string]map[string]*Cell // kernel ID -> contender -> cell
	Load    []float64                   // 1-minute load samples: before round 1 and after every round
	Inputs  Inputs
	Verdict Verdict
	// Decided is false for a smoke run: correctness only, no verdict.
	Decided bool
}

type runner struct {
	contender string
	prep      func(ctx context.Context) error // nothing to prepare when nil
	run       func(ctx context.Context) (checksum string, secs, copySecs float64, copies int64, err error)
}

// Run runs every contender on every kernel, checks each checksum against the
// bound, and applies the rule. A checksum out of bound is an error, not a time.
func Run(ctx context.Context, o Options) (*Result, error) {
	if o.Rounds < 1 {
		return nil, errors.New("gate: rounds must be at least 1")
	}
	if o.Load == nil {
		o.Load = oracle.Load1
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	res := &Result{Smoke: o.Smoke, Rounds: o.Rounds, Kernels: Kernels(), Cells: map[string]map[string]*Cell{}}
	sample := func() error {
		l, err := o.Load(ctx)
		if err != nil {
			if o.Force || o.Smoke {
				return nil
			}
			return fmt.Errorf("gate: the load average is unknown (%v); --force measures anyway", err)
		}
		res.Load = append(res.Load, l)
		if l > oracle.MaxLoad && !o.Force && !o.Smoke {
			return fmt.Errorf("gate: 1-minute load %.1f is above %.0f: not measuring, and figures taken at this load are not valid (--force overrides)", l, oracle.MaxLoad)
		}
		return nil
	}
	if err := sample(); err != nil {
		return nil, err
	}
	clang, err := LocateClang(ctx, o.Coracle)
	if err != nil {
		return nil, err
	}
	eng, err := NewEngine(ctx)
	if err != nil {
		return nil, err
	}
	defer eng.Close(ctx)
	build, err := os.MkdirTemp("", "foracle-gate-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(build)

	// Sizes, exact checksums and bounds: outside every timed region.
	for _, k := range res.Kernels {
		s := k.Full
		if o.Smoke {
			s = k.Smoke
		}
		fmt.Fprintf(o.Log, "exact %s (math/big)...\n", k.ID)
		ex, bd := k.Bound(s)
		res.Sizes = append(res.Sizes, s)
		res.Exact = append(res.Exact, ex)
		res.Bound = append(res.Bound, bd)
		res.Cells[k.ID] = map[string]*Cell{}
	}

	type slot struct {
		ki int
		r  runner
	}
	var slots []slot
	for ki, k := range res.Kernels {
		rs, err := runnersFor(ctx, o, k, res.Sizes[ki], clang, eng, filepath.Join(build, k.ID))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k.ID, err)
		}
		for _, r := range rs {
			slots = append(slots, slot{ki, r})
		}
	}
	// Interleaved rounds: in every round, every kernel, every contender.
	for round := 1; round <= o.Rounds; round++ {
		for _, sl := range slots {
			k := res.Kernels[sl.ki]
			cs, secs, copySecs, copies, err := sl.r.run(ctx)
			if err != nil {
				return nil, fmt.Errorf("%s %s round %d: %w", k.ID, sl.r.contender, round, err)
			}
			if err := k.Check(cs, res.Exact[sl.ki], res.Bound[sl.ki]); err != nil {
				return nil, fmt.Errorf("%s %s: out of bound, a failure and not a time: %w", k.ID, sl.r.contender, err)
			}
			c := res.Cells[k.ID][sl.r.contender]
			if c == nil {
				c = &Cell{}
				res.Cells[k.ID][sl.r.contender] = c
			}
			c.Seconds = append(c.Seconds, secs)
			if copySecs > 0 {
				c.CopySeconds = append(c.CopySeconds, copySecs)
			}
			c.Checksum, c.Copies = cs, copies
		}
		fmt.Fprintf(o.Log, "round %d/%d done\n", round, o.Rounds)
		if err := sample(); err != nil {
			return nil, err
		}
	}
	if o.Smoke {
		return res, nil
	}
	res.Decided = true
	res.Inputs, res.Verdict, err = res.decide()
	return res, err
}

func (r *Result) decide() (Inputs, Verdict, error) {
	var in Inputs
	gA, gB0 := 1.0, 1.0
	nLoop := 0
	for _, k := range r.Kernels {
		if !k.Loop {
			continue
		}
		row := r.Cells[k.ID]
		gf := row[CGF].Median()
		gA *= row[CA].Median() / gf
		gB0 *= row[CB0].Median() / gf
		nLoop++
	}
	gA, gB0 = math.Pow(gA, 1/float64(nLoop)), math.Pow(gB0, 1/float64(nLoop))
	in.Desktop = Home{Name: "desktop", Run: true, GA: gA, GB0: gB0}
	in.Playground = Home{Name: "playground"}
	for _, k := range r.Kernels {
		switch k.ID {
		case "K4":
			in.K4 = r.Cells[k.ID][CA].Median() / r.Cells[k.ID][CDire].Median()
		case "K3M":
			in.Matmul = r.Cells[k.ID][CA].Median() / r.Cells[k.ID][CDire].Median()
		}
	}
	v, err := Decide(in)
	return in, v, err
}

func runnersFor(ctx context.Context, o Options, k Kernel, s Size, clang *Clang, eng *Engine, dir string) ([]runner, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	src, err := k.Fortran(o.Dir, s)
	if err != nil {
		return nil, err
	}
	var rs []runner

	// A: the evaluator. Compiled once, outside the timing.
	file, perr := syntax.Parse(k.Stem+".f90", src, nil)
	if perr != nil {
		return nil, fmt.Errorf("parse: %w", perr)
	}
	prog, err := eval.Compile(file)
	if err != nil {
		return nil, err
	}
	rs = append(rs, runner{contender: CA, run: func(context.Context) (string, float64, float64, int64, error) {
		var out bytes.Buffer
		st, err := prog.Run(&out)
		if err != nil {
			return "", 0, 0, 0, err
		}
		cs, secs, err := oracle.ParseKernel(out.Bytes())
		return cs, secs, 0, st.Copies, err
	}})

	// B-near and B-upper: the C transliteration on wazero.
	csrc, _ := k.CSource(o.Dir)
	for _, b := range []struct{ name, opt string }{{CB0, "-O0"}, {CB2, "-O2"}} {
		wasm, err := clang.Compile(ctx, csrc, b.opt, k.Defs(s))
		if err != nil {
			return nil, err
		}
		mod, err := eng.Compile(ctx, wasm)
		if err != nil {
			return nil, fmt.Errorf("wazero compile (%s): %w", b.name, err)
		}
		rs = append(rs, runner{contender: b.name, run: func(ctx context.Context) (string, float64, float64, int64, error) {
			out, copySecs, err := mod.Run(ctx)
			if err != nil {
				return "", 0, 0, 0, err
			}
			cs, secs, err := oracle.ParseKernel(bytes.TrimSpace(out))
			return cs, secs, copySecs, 0, err
		}})
	}

	// The reference: gfortran -O2, native.
	exe, err := o.Gfortran.Compile(ctx, dir, src, oracle.CompileOptions{Opt: oracle.BenchOpt, Timeout: 2 * time.Minute})
	if err != nil {
		return nil, fmt.Errorf("gfortran: %w", err)
	}
	rs = append(rs, runner{contender: CGF, run: func(ctx context.Context) (string, float64, float64, int64, error) {
		r, err := oracle.Run(ctx, exe, filepath.Dir(exe), nil, nil, oracle.Limits{Timeout: 5 * time.Minute, MemoryBytes: 4 << 30})
		if err != nil {
			return "", 0, 0, 0, err
		}
		if r.TimedOut || r.Truncated || r.Signal != "" || r.Exit != 0 {
			return "", 0, 0, 0, fmt.Errorf("did not finish cleanly (exit %d, signal %q, timed out %v)", r.Exit, r.Signal, r.TimedOut)
		}
		cs, secs, err := oracle.ParseKernel(bytes.TrimSpace(r.Stdout))
		return cs, secs, 0, 0, err
	}})

	// The array kernels' baseline: go-ndarray called directly.
	if k.Host {
		d, err := Direct(k, s)
		if err != nil {
			return nil, err
		}
		rs = append(rs, runner{contender: CDire, run: func(context.Context) (string, float64, float64, int64, error) {
			cs, secs, err := d()
			return cs, secs, 0, 0, err
		}})
	}
	return rs, nil
}

// Report prints the table, the inputs to the rule and the verdict.
func (r *Result) Report(w io.Writer) {
	if r.Smoke {
		fmt.Fprintln(w, "gate smoke: correctness only; every checksum is within its bound; no time is printed or published")
		for i, k := range r.Kernels {
			cs := make([]string, 0, 5)
			for _, name := range []string{CA, CB0, CB2, CGF, CDire} {
				if c := r.Cells[k.ID][name]; c != nil {
					cs = append(cs, name)
				}
			}
			fmt.Fprintf(w, "%-4s n=%-9d bound %.3g  ok: %s\n", k.ID, r.Sizes[i].N, r.Bound[i], strings.Join(cs, " "))
		}
		return
	}
	fmt.Fprintf(w, "# gate of bib/fortran.md 4.2: desktop %s, %d rounds interleaved, one core; seconds, median and spread (max-min)\n", hostName(), r.Rounds)
	fmt.Fprintf(w, "# 1-minute load samples: %s\n", fmtLoads(r.Load))
	names := []string{CA, CB0, CB2, CGF, CDire}
	fmt.Fprintf(w, "%-4s", "")
	for _, n := range names {
		fmt.Fprintf(w, " %20s", n)
	}
	fmt.Fprintln(w)
	for i, k := range r.Kernels {
		fmt.Fprintf(w, "%-4s", k.ID)
		for _, n := range names {
			c := r.Cells[k.ID][n]
			if c == nil {
				fmt.Fprintf(w, " %20s", "-")
				continue
			}
			fmt.Fprintf(w, " %12.4f +%6.4f", c.Median(), c.Spread())
		}
		fmt.Fprintf(w, "   checksum bound %.3g\n", r.Bound[i])
	}
	fmt.Fprintln(w, "# time over gfortran -O2 (r), loop kernels:")
	for _, k := range r.Kernels {
		if !k.Loop {
			continue
		}
		gf := r.Cells[k.ID][CGF].Median()
		fmt.Fprintf(w, "%-4s rA %8.2f  rB0 %8.2f  rB2 %8.2f\n", k.ID,
			r.Cells[k.ID][CA].Median()/gf, r.Cells[k.ID][CB0].Median()/gf, r.Cells[k.ID][CB2].Median()/gf)
	}
	for _, k := range r.Kernels {
		if !k.Host {
			continue
		}
		a := r.Cells[k.ID][CA]
		fmt.Fprintf(w, "# %s array kernel: A copied %d elements; B copies alone: near %.4f s, upper %.4f s (medians, included in B's time)\n",
			k.ID, a.Copies, oracle.Median(r.Cells[k.ID][CB0].CopySeconds), oracle.Median(r.Cells[k.ID][CB2].CopySeconds))
	}
	fmt.Fprintln(w, "# inputs to the rule:")
	h := r.Inputs.Desktop
	fmt.Fprintf(w, "desktop    g(rA) = %.4f  g(rB0) = %.4f  g(rA)/g(rB0) = %.3f   (B if >= %g; A needs <= %g)\n", h.GA, h.GB0, h.Ratio(), SwitchToB, AllowA)
	fmt.Fprintf(w, "playground not run (no headless-browser lane yet: the rule cannot pick A)\n")
	fmt.Fprintf(w, "K4         A / direct go-ndarray = %.3f   (A needs <= %g)\n", r.Inputs.K4, ArrayWithin)
	fmt.Fprintf(w, "K3-matmul  A / direct go-ndarray = %.3f   (A needs <= %g)\n", r.Inputs.Matmul, ArrayWithin)
	fmt.Fprintf(w, "VERDICT: %s\n", r.Verdict.Decision)
	for _, why := range r.Verdict.Reasons {
		fmt.Fprintf(w, "  - %s\n", why)
	}
	fmt.Fprintf(w, "# in every case the gap to gfortran (rA, rB2) is published and is an open defect (bib 4.2)\n")
}

func fmtLoads(l []float64) string {
	s := make([]string, len(l))
	for i, v := range l {
		s[i] = fmt.Sprintf("%.1f", v)
	}
	return strings.Join(s, " ")
}

func hostName() string { return runtime.GOOS + "/" + runtime.GOARCH }
