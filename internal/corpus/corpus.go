// Package corpus runs a directory of Fortran programs through the oracle.
//
// The corpora are never committed (the gcc testsuite is GPL; FCVS and the
// others carry their own terms): they are fetched by commit, see the
// revisions below and the CI workflow. Every .f90 / .f95 / .f03 / .f08 file
// (free source form) and every .f / .for file (fixed form) below a directory
// is one entry. The verdict is the oracle's own: compiled, and ran to a zero
// exit status within its limits. A test written to be refused, or to stop
// with a nonzero status, is reported as such, not hidden.
package corpus

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/go-embedded-fortran/fortran/internal/oracle"
)

// Pinned revisions of the corpora (bib/fortran.md §0 and §8 of
// go-scikit/roadmap).
const (
	GCCTag            = "releases/gcc-15.2.0"
	GCCCommit         = "5115c7e447fc07457443df874bf57840e8316d5f"
	GCCDgDir          = "gcc/testsuite/gfortran.dg"
	GCCTortureDir     = "gcc/testsuite/gfortran.fortran-torture"
	LLVMTestSuiteRepo = "https://github.com/llvm/llvm-test-suite"
	LLVMTestSuiteRev  = "19b349c5aa391729e688f9b4b53ded177e1531cb"
	FCVSDir           = "Fortran/UnitTests/fcvs21_f95"
	LFortranRepo      = "https://github.com/lfortran/lfortran"
	LFortranRev       = "4f2cd049c87f4413e00e7f8e5aefe354e63c79d1"
	LFortranDir       = "integration_tests"
)

// Form is a source form.
type Form int

// The source forms (F2018 6.3.2 free, 6.3.3 fixed).
const (
	NotFortran Form = iota
	Free
	Fixed
)

// FormOf returns the source form selected by a file name's extension, as
// gfortran selects it, or NotFortran. The match is case-insensitive.
func FormOf(name string) Form {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".f90", ".f95", ".f03", ".f08":
		return Free
	case ".f", ".for":
		return Fixed
	}
	return NotFortran
}

// Ext returns the extension the oracle must give the file to get the form.
func (f Form) Ext() string {
	if f == Fixed {
		return oracle.FixedForm
	}
	return oracle.FreeForm
}

// Outcome classifies one program.
type Outcome string

// The outcomes. Only Pass is success.
const (
	Pass        Outcome = "pass"
	CompileFail Outcome = "compile-fail" // gfortran refused the program
	Fail        Outcome = "fail"         // nonzero exit status
	Signal      Outcome = "signal"       // killed by a signal
	Timeout     Outcome = "timeout"      // the run exceeded its bound
	Harness     Outcome = "harness"      // the harness itself failed, or the output hit its cap
)

// Entry is one program of a corpus.
type Entry struct {
	Path   string // slash-separated, relative to the corpus root
	Source []byte
	Form   Form
}

// Load reads every Fortran file below root, sorted by path. Directories whose
// name starts with "." are skipped.
func Load(root string) ([]Entry, error) {
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != root && strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		if d.Type().IsRegular() && FormOf(p) != NotFortran {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	es := make([]Entry, 0, len(paths))
	for _, p := range paths {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			return nil, err
		}
		es = append(es, Entry{Path: p, Source: src, Form: FormOf(p)})
	}
	return es, nil
}

// Rec is the verdict on one entry.
type Rec struct {
	Path    string
	Outcome Outcome
	Exit    uint32
	Detail  string // the signal, or the first compiler diagnostic line
}

// Judge classifies a run that produced res.
func Judge(path string, res oracle.Result) Rec {
	r := Rec{Path: path, Exit: res.Exit}
	switch {
	case res.TimedOut:
		r.Outcome = Timeout
	case res.Truncated:
		r.Outcome, r.Detail = Harness, "output over the cap"
	case res.Signal != "":
		r.Outcome, r.Detail = Signal, res.Signal
	case res.Exit != 0:
		r.Outcome = Fail
	default:
		r.Outcome = Pass
	}
	return r
}

// Run compiles and runs every entry with the oracle, workers at a time, and
// returns the records in entry order. root, when not empty, is the corpus
// directory: each file's own directory is given to gfortran as an include
// path, so that INCLUDE lines find their files.
func Run(ctx context.Context, tc *oracle.Toolchain, root string, es []Entry, co oracle.CompileOptions, lim oracle.Limits, workers int) []Rec {
	if workers < 1 {
		workers = 1
	}
	recs := make([]Rec, len(es))
	var wg sync.WaitGroup
	idx := make(chan int)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				recs[i] = one(ctx, tc, root, es[i], co, lim)
			}
		}()
	}
	for i := range es {
		idx <- i
	}
	close(idx)
	wg.Wait()
	return recs
}

func one(ctx context.Context, tc *oracle.Toolchain, root string, e Entry, co oracle.CompileOptions, lim oracle.Limits) Rec {
	dir, err := os.MkdirTemp("", "foracle-")
	if err != nil {
		return Rec{Path: e.Path, Outcome: Harness, Detail: err.Error()}
	}
	defer os.RemoveAll(dir)
	co.Ext = e.Form.Ext()
	if root != "" {
		co.Extra = append(append([]string(nil), co.Extra...), "-I"+filepath.Dir(filepath.Join(root, filepath.FromSlash(e.Path))))
	}
	exe, err := tc.Compile(ctx, dir, e.Source, co)
	if err != nil {
		if ce, ok := err.(*oracle.CompileError); ok {
			return Rec{Path: e.Path, Outcome: CompileFail, Detail: firstLine(ce.Diagnostics)}
		}
		return Rec{Path: e.Path, Outcome: Harness, Detail: err.Error()}
	}
	res, err := oracle.Run(ctx, exe, dir, nil, nil, lim)
	if err != nil {
		return Rec{Path: e.Path, Outcome: Harness, Detail: err.Error()}
	}
	return Judge(e.Path, res)
}

// firstLine is the first non-empty line of a compiler's diagnostics that is
// not just a source excerpt or a position marker: gfortran writes the
// position, the line, a caret line, then "Error: …".
func firstLine(diag string) string {
	var last string
	for _, l := range strings.Split(strings.TrimSpace(diag), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "Error:") || strings.HasPrefix(l, "Fatal Error:") || strings.Contains(l, "Error:") {
			return l
		}
		if last == "" {
			last = l
		}
	}
	return last
}

// Summary counts records by outcome.
func Summary(recs []Rec) map[Outcome]int {
	m := map[Outcome]int{}
	for _, r := range recs {
		m[r.Outcome]++
	}
	return m
}

// Outcomes lists the outcomes in report order.
var Outcomes = []Outcome{Pass, CompileFail, Fail, Signal, Timeout, Harness}

// Report writes one line per record (path, outcome, exit, detail) and a
// summary; per-record, not only totals, so two runs can be diffed.
func Report(w io.Writer, recs []Rec) {
	for _, r := range recs {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", r.Path, r.Outcome, r.Exit, strings.ReplaceAll(r.Detail, "\n", " "))
	}
	sum := Summary(recs)
	fmt.Fprintf(w, "# total %d", len(recs))
	for _, o := range Outcomes {
		fmt.Fprintf(w, " %s=%d", o, sum[o])
	}
	fmt.Fprintln(w)
}
