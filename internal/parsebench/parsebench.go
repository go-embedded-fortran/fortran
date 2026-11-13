// Package parsebench measures the syntax package against the oracle: which
// files of a corpus parse (and which of those gfortran accepts with
// -fsyntax-only), and how fast, one core, interleaved with gfortran's own
// -fsyntax-only on the same files.
package parsebench

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/go-embedded-fortran/fortran/internal/corpus"
	"github.com/go-embedded-fortran/fortran/internal/oracle"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// Verdict compares our parser with gfortran -fsyntax-only on one file
// (bib/fortran.md §8: pass / fail-only-ours / fail-both).
type Verdict string

// The verdicts.
const (
	Both     Verdict = "both-accept"
	OursOnly Verdict = "fail-gfortran-only" // gfortran refuses, we parse (the file is meant to be refused, or we are lenient)
	GfOnly   Verdict = "fail-only-ours"     // gfortran accepts, we refuse: a defect of ours
	Neither  Verdict = "fail-both"
	NoOracle Verdict = "ours-only" // no gfortran available: only our verdict
)

// Rec is the verdict on one file.
type Rec struct {
	Path    string
	Bytes   int
	OursOK  bool
	First   string // our first error
	Gf      *bool  // gfortran accepted, nil when not run
	Verdict Verdict
}

// ParseOne parses one entry exactly as the harness does: includes resolve
// through the file's own directory.
func ParseOne(root string, e corpus.Entry) (*syntax.File, error) {
	dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(e.Path)))
	f, err := syntax.Parse(filepath.Base(e.Path), e.Source, &syntax.Options{FS: os.DirFS(dir)})
	return f, err
}

// Gfortran runs "gfortran -fsyntax-only" on a file with its directory as the
// include path; it reports acceptance. The run is bounded like every run of
// the oracle (timeout, address space where the OS enforces it): a few
// gfortran.dg files make the compiler allocate without bound while folding
// constants, which took a CI runner down. It needs oracle.Init in main.
func Gfortran(ctx context.Context, tc *oracle.Toolchain, root string, e corpus.Entry) (bool, time.Duration) {
	full, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(e.Path)))
	if err != nil {
		return false, 0
	}
	t := time.Now()
	res, err := oracle.Run(ctx, tc.Gfortran, filepath.Dir(full), []string{"-fsyntax-only", "-w", "-I", filepath.Dir(full), full}, nil,
		oracle.Limits{Timeout: 30 * time.Second, MemoryBytes: 2 << 30})
	return err == nil && res.Exit == 0 && res.Signal == "" && !res.TimedOut, time.Since(t)
}

// gfortranTimed is Gfortran without the bounding helper, for the benchmark:
// the timing must not include a re-executed helper process. Only the largest
// files that were already accepted once are given to it.
func gfortranTimed(ctx context.Context, tc *oracle.Toolchain, root string, e corpus.Entry) (bool, time.Duration) {
	full := filepath.Join(root, filepath.FromSlash(e.Path))
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, tc.Gfortran, "-fsyntax-only", "-w", "-I", filepath.Dir(full), full)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.Stdin = nil
	t := time.Now()
	err := cmd.Run()
	return err == nil, time.Since(t)
}

// Rate parses every file of es; when tc is not nil each file is also given to
// gfortran, with jobs concurrent gfortran processes.
func Rate(ctx context.Context, tc *oracle.Toolchain, root string, es []corpus.Entry, jobs int) []Rec {
	return RateCached(ctx, tc, root, es, jobs, nil)
}

// ReadCache reads the gfortran verdicts (path -> accepted) of a report
// written by Report, so that a re-run need not start gfortran again.
func ReadCache(r io.Reader) map[string]bool {
	m := map[string]bool{}
	b, _ := io.ReadAll(r)
	for _, line := range bytes.Split(b, []byte("\n")) {
		f := bytes.Split(line, []byte("\t"))
		if len(f) >= 4 && (string(f[3]) == "accept" || string(f[3]) == "refuse") {
			m[string(f[0])] = string(f[3]) == "accept"
		}
	}
	return m
}

// RateCached is Rate with gfortran's verdicts taken from cache where present.
func RateCached(ctx context.Context, tc *oracle.Toolchain, root string, es []corpus.Entry, jobs int, cache map[string]bool) []Rec {
	recs := make([]Rec, len(es))
	sem := make(chan struct{}, max(1, jobs))
	var wg sync.WaitGroup
	for i := range es {
		e := es[i]
		r := Rec{Path: e.Path, Bytes: len(e.Source)}
		_, err := ParseOne(root, e)
		r.OursOK = err == nil
		if err != nil {
			r.First = firstLine(err.Error())
		}
		recs[i] = r
		if tc == nil {
			recs[i].Verdict = NoOracle
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			ok, cached := cache[es[i].Path]
			if !cached {
				ok, _ = Gfortran(ctx, tc, root, es[i])
			}
			recs[i].Gf = &ok
			switch {
			case recs[i].OursOK && ok:
				recs[i].Verdict = Both
			case recs[i].OursOK:
				recs[i].Verdict = OursOnly
			case ok:
				recs[i].Verdict = GfOnly
			default:
				recs[i].Verdict = Neither
			}
		}(i)
	}
	wg.Wait()
	return recs
}

func firstLine(s string) string {
	if i := bytes.IndexByte([]byte(s), '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Report writes one tab-separated line per file: path, bytes, our verdict,
// gfortran's verdict, comparison, our first error.
func Report(w io.Writer, recs []Rec) {
	for _, r := range recs {
		ours, gf := "refuse", "-"
		if r.OursOK {
			ours = "parse"
		}
		if r.Gf != nil {
			gf = "refuse"
			if *r.Gf {
				gf = "accept"
			}
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n", r.Path, r.Bytes, ours, gf, r.Verdict, r.First)
	}
}

// Summary counts the verdicts and our parses.
type Summary struct {
	Files, Parsed          int
	Both, GfOnly, OursOnly int
	Neither                int
	GfAccepted             int
}

// Summarize counts recs.
func Summarize(recs []Rec) Summary {
	var s Summary
	s.Files = len(recs)
	for _, r := range recs {
		if r.OursOK {
			s.Parsed++
		}
		if r.Gf != nil && *r.Gf {
			s.GfAccepted++
		}
		switch r.Verdict {
		case Both:
			s.Both++
		case GfOnly:
			s.GfOnly++
		case OursOnly:
			s.OursOnly++
		case Neither:
			s.Neither++
		}
	}
	return s
}

// Row is one line of the throughput table.
type Row struct {
	Path          string
	Bytes         int
	OursSec       float64 // median parse time
	GfortranSec   float64 // median -fsyntax-only wall time
	StartupSec    float64 // median of gfortran -fsyntax-only on an empty file
	OursMBps      float64
	Ratio         float64 // gfortran / ours, both raw
	RatioNetStart float64 // (gfortran - startup) / ours
}

// ErrLoad refuses a measurement at high load.
type ErrLoad struct{ Load float64 }

func (e ErrLoad) Error() string {
	return fmt.Sprintf("1-minute load %.1f is above %.0f: measurement refused (the fleet's rule)", e.Load, oracle.MaxLoad)
}

// Bench times the parser against gfortran -fsyntax-only on the n largest
// files of es that both accept, one core (parsing in this goroutine, gfortran
// one process at a time), rounds interleaved: in each round every file is
// parsed by us then by gfortran. It refuses when the 1-minute load is above
// oracle.MaxLoad, before and after the run.
func Bench(ctx context.Context, tc *oracle.Toolchain, root string, es []corpus.Entry, recs []Rec, n, rounds int) ([]Row, error) {
	if l, err := oracle.Load1(ctx); err != nil {
		return nil, err
	} else if l > oracle.MaxLoad {
		return nil, ErrLoad{l}
	}
	idx := make([]int, 0, len(recs))
	for i, r := range recs {
		if r.Verdict == Both {
			idx = append(idx, i)
		}
	}
	sort.Slice(idx, func(a, b int) bool { return recs[idx[a]].Bytes > recs[idx[b]].Bytes })
	if len(idx) > n {
		idx = idx[:n]
	}
	empty := corpus.Entry{Path: "empty.f90"}
	home := os.Getenv(oracle.EnvHome)
	if home == "" {
		return nil, fmt.Errorf("$%s is not set: the scratch directory of the benchmark must be durable", oracle.EnvHome)
	}
	dir, err := os.MkdirTemp(home, "parsebench-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "empty.f90"), []byte("end\n"), 0o644); err != nil {
		return nil, err
	}
	ours := make([][]float64, len(idx))
	gf := make([][]float64, len(idx))
	var startup []float64
	for r := 0; r < rounds; r++ {
		_, d := gfortranTimed(ctx, tc, dir, empty)
		startup = append(startup, d.Seconds())
		for k, i := range idx {
			e := es[i]
			ours[k] = append(ours[k], timeParse(root, e))
			_, d := gfortranTimed(ctx, tc, root, e)
			gf[k] = append(gf[k], d.Seconds())
		}
	}
	if l, err := oracle.Load1(ctx); err == nil && l > oracle.MaxLoad {
		return nil, ErrLoad{l}
	}
	st := oracle.Median(startup)
	rows := make([]Row, len(idx))
	for k, i := range idx {
		o, g := oracle.Median(ours[k]), oracle.Median(gf[k])
		rows[k] = Row{Path: es[i].Path, Bytes: len(es[i].Source), OursSec: o, GfortranSec: g, StartupSec: st,
			OursMBps: float64(len(es[i].Source)) / o / 1e6, Ratio: g / o, RatioNetStart: (g - st) / o}
	}
	return rows, nil
}

// timeParse returns the seconds of one parse, measured over enough
// repetitions to take at least 20 ms.
func timeParse(root string, e corpus.Entry) float64 {
	reps := 1
	for {
		t := time.Now()
		for i := 0; i < reps; i++ {
			ParseOne(root, e)
		}
		d := time.Since(t)
		if d >= 20*time.Millisecond || reps >= 1<<16 {
			return d.Seconds() / float64(reps)
		}
		reps *= 4
	}
}

// Table writes the throughput table.
func Table(w io.Writer, rows []Row) {
	fmt.Fprintf(w, "%-46s %9s %10s %12s %9s %9s %12s\n", "file", "bytes", "ours ms", "gfortran ms", "MB/s", "gf/ours", "(gf-start)/ours")
	for _, r := range rows {
		fmt.Fprintf(w, "%-46s %9d %10.3f %12.1f %9.1f %9.0f %12.0f\n", r.Path, r.Bytes, r.OursSec*1e3, r.GfortranSec*1e3, r.OursMBps, r.Ratio, r.RatioNetStart)
	}
	if len(rows) > 0 {
		fmt.Fprintf(w, "gfortran -fsyntax-only on an empty file (process start): %.1f ms\n", rows[0].StartupSec*1e3)
	}
}

// ErrCount is a normalised first-error message and how many files have it.
type ErrCount struct {
	Msg   string
	Count int
	Eg    string
}

// TopErrors groups the first errors of the refused files by message, quoted
// names and numbers replaced, most frequent first.
func TopErrors(recs []Rec, n int) []ErrCount {
	m := map[string]*ErrCount{}
	for _, r := range recs {
		if r.OursOK {
			continue
		}
		msg := r.First
		// drop "file:line:col: "
		for k, colons := 0, 0; k < len(msg); k++ {
			if msg[k] == ':' {
				if colons++; colons == 3 {
					msg = msg[k+1:]
					break
				}
			}
		}
		norm := make([]byte, 0, len(msg))
		inq := false
		for k := 0; k < len(msg); k++ {
			c := msg[k]
			switch {
			case c == '\'' && !inq:
				inq = true
				norm = append(norm, "'_'"...)
			case c == '\'' && inq:
				inq = false
			case inq:
			default:
				norm = append(norm, c)
			}
		}
		key := string(norm)
		if e, ok := m[key]; ok {
			e.Count++
		} else {
			m[key] = &ErrCount{Msg: key, Count: 1, Eg: r.Path + ": " + r.First}
		}
	}
	out := make([]ErrCount, 0, len(m))
	for _, e := range m {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Count > out[j].Count || (out[i].Count == out[j].Count && out[i].Msg < out[j].Msg)
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
