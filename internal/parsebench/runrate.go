package parsebench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/go-embedded-fortran/fortran/eval"
	"github.com/go-embedded-fortran/fortran/internal/corpus"
	"github.com/go-embedded-fortran/fortran/internal/oracle"
)

// RunVerdict compares a program run by eval with the same program built by
// gfortran and run (bib/fortran.md §10, M6: the gfortran.dg tests meant to be
// run, "dg-do run").
type RunVerdict string

// The verdicts of the execution differential.
const (
	RunSame     RunVerdict = "identical"       // stdout, stderr and exit status equal, and the status is 0
	RunBothFail RunVerdict = "both-failed"     // stdout, stderr and exit status equal, and the status is not 0
	RunDiffers  RunVerdict = "differs"         // eval ran it (or failed while running) and some of the three differ
	RunRefused  RunVerdict = "refused-by-us"   // the analyser or the compiler of eval refused it: nothing ran
	RunGfFailed RunVerdict = "gfortran-failed" // gfortran did not build it, or did not run it within the limits
	RunSkipped  RunVerdict = "skipped"         // not comparable: fixed form, or a test expected to fail
)

// RunRec is the verdict on one file; Note says why when it is not identical.
type RunRec struct {
	Path    string
	Verdict RunVerdict
	Note    string
}

// Outcome is what one side produced.
type Outcome struct {
	Stdout, Stderr []byte
	Exit           int
	// Failure is set when the side did not produce a comparable run: for
	// gfortran a compile error, a timeout, a signal or truncated output; for
	// eval a failure while running.
	Failure string
	// Refused is eval's alone: its analyser or compiler refused the source.
	Refused bool
}

// RunLimits bound one side of one file.
type RunLimits struct {
	Timeout   time.Duration // wall clock, each side (10 s when zero)
	MaxAlloc  int64         // eval's memory budget (1 GiB when zero)
	MaxOutput int64         // eval's output bound (1 MiB when zero)
}

func (l RunLimits) withDefaults() RunLimits {
	if l.Timeout == 0 {
		l.Timeout = 10 * time.Second
	}
	if l.MaxAlloc == 0 {
		l.MaxAlloc = 1 << 30
	}
	if l.MaxOutput == 0 {
		l.MaxOutput = 1 << 20
	}
	return l
}

// skipReason says why a file is not comparable, "" when it is.
func skipReason(e corpus.Entry) string {
	switch {
	case e.Form == corpus.Fixed:
		return "fixed form: eval.Session reads free form only"
	case bytes.Contains(e.Source, []byte("dg-shouldfail")):
		return "dg-shouldfail: the test is expected to fail"
	}
	return ""
}

// Classify compares the two outcomes.
func Classify(gf, ours Outcome) (RunVerdict, string) {
	switch {
	case gf.Failure != "":
		return RunGfFailed, gf.Failure
	case ours.Refused:
		return RunRefused, ours.Failure
	case ours.Failure != "":
		return RunDiffers, "eval failed while running: " + ours.Failure
	case !bytes.Equal(gf.Stdout, ours.Stdout):
		return RunDiffers, "stdout " + firstDiff(ours.Stdout, gf.Stdout)
	case !bytes.Equal(gf.Stderr, ours.Stderr):
		return RunDiffers, "stderr " + firstDiff(ours.Stderr, gf.Stderr)
	case gf.Exit != ours.Exit:
		return RunDiffers, fmt.Sprintf("exit %d, gfortran %d", ours.Exit, gf.Exit)
	case gf.Exit != 0:
		// The test stopped itself on both sides (a gfortran.dg test reports a
		// failed check with STOP n): gfortran, the reference, failed it too,
		// usually for want of the dg-options this differential does not pass.
		// Agreeing with a failing reference proves nothing, so it is not
		// counted as identical.
		return RunBothFail, fmt.Sprintf("both exit %d", gf.Exit)
	}
	return RunSame, ""
}

// firstDiff names the first line where ours and gfortran's differ, each side
// quoted and cut to 60 bytes, so that a report sorts its differences by cause.
func firstDiff(ours, gf []byte) string {
	a, b := bytes.Split(ours, []byte("\n")), bytes.Split(gf, []byte("\n"))
	cut := func(l [][]byte, i int) string {
		if i >= len(l) {
			return "(none)"
		}
		s := l[i]
		if len(s) > 60 {
			s = s[:60]
		}
		return fmt.Sprintf("%q", s)
	}
	for i := 0; i < max(len(a), len(b)); i++ {
		if i >= len(a) || i >= len(b) || !bytes.Equal(a[i], b[i]) {
			return fmt.Sprintf("line %d: ours %s, gfortran %s", i+1, cut(a, i), cut(b, i))
		}
	}
	return ""
}

// RunEval runs one source with eval, in one session with implicit typing (the
// gfortran.dg tests rely on it), under lim.
func RunEval(ctx context.Context, src []byte, lim RunLimits) Outcome {
	lim = lim.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()
	s := eval.NewSession(eval.SessionOptions{ImplicitTyping: true, MaxAlloc: lim.MaxAlloc, MaxOutput: lim.MaxOutput})
	var out, errb bytes.Buffer
	st, err := s.Run(ctx, string(src), &out, &errb)
	o := Outcome{Stdout: out.Bytes(), Stderr: errb.Bytes(), Exit: st.Exit}
	if err != nil {
		o.Failure = firstLine(err.Error())
		var e *eval.Error
		o.Refused = errors.As(err, &e) && e.Refused
	}
	return o
}

// RunGfortran builds one source with gfortran and runs it under lim.
func RunGfortran(ctx context.Context, tc *oracle.Toolchain, e corpus.Entry, lim RunLimits) Outcome {
	lim = lim.withDefaults()
	dir, err := os.MkdirTemp("", "foracle-run-")
	if err != nil {
		return Outcome{Failure: err.Error()}
	}
	defer os.RemoveAll(dir)
	exe, err := tc.Compile(ctx, dir, e.Source, oracle.CompileOptions{})
	if err != nil {
		return Outcome{Failure: "gfortran does not build it: " + firstLine(err.Error())}
	}
	res, err := oracle.Run(ctx, exe, dir, nil, nil, oracle.Limits{Timeout: lim.Timeout})
	switch {
	case err != nil:
		return Outcome{Failure: firstLine(err.Error())}
	case res.TimedOut:
		return Outcome{Failure: "gfortran's program timed out"}
	case res.Signal != "":
		return Outcome{Failure: "gfortran's program died: " + res.Signal}
	case res.Truncated:
		return Outcome{Failure: "gfortran's program wrote past the output bound"}
	}
	return Outcome{Stdout: res.Stdout, Stderr: res.Stderr, Exit: int(res.Exit & 0xff)} // the status as the OS reports it, as eval's Stats.Exit
}

// RateRun runs every entry with gfortran (jobs processes at a time) and with
// eval, and compares them. tc must be a located toolchain.
func RateRun(ctx context.Context, tc *oracle.Toolchain, es []corpus.Entry, jobs int, lim RunLimits) []RunRec {
	recs := make([]RunRec, len(es))
	sem := make(chan struct{}, max(1, jobs))
	var wg sync.WaitGroup
	for i := range es {
		e := es[i]
		r := &recs[i]
		r.Path = e.Path
		if why := skipReason(e); why != "" {
			r.Verdict, r.Note = RunSkipped, why
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r.Verdict, r.Note = Classify(RunGfortran(ctx, tc, e, lim), RunEval(ctx, e.Source, lim))
		}()
	}
	wg.Wait()
	return recs
}

// RunReport writes one tab-separated line per file: path, verdict, note.
func RunReport(w io.Writer, recs []RunRec) {
	for _, r := range recs {
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.Path, r.Verdict, r.Note)
	}
}

// CountRun counts the verdicts; every verdict is present, zero included, so a
// report never hides a class by leaving it out.
func CountRun(recs []RunRec) map[RunVerdict]int {
	c := map[RunVerdict]int{RunSame: 0, RunBothFail: 0, RunDiffers: 0, RunRefused: 0, RunGfFailed: 0, RunSkipped: 0}
	for _, r := range recs {
		c[r.Verdict]++
	}
	return c
}

// TopRunNotes groups the notes of the files of verdict v, most frequent first.
func TopRunNotes(recs []RunRec, v RunVerdict, n int) []ErrCount {
	var rs []Rec
	for _, r := range recs {
		if r.Verdict == v && r.Note != "" {
			rs = append(rs, Rec{Path: r.Path, First: r.Note})
		}
	}
	out := TopErrors(rs, n)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}
