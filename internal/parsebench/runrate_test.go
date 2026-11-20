package parsebench

import (
	"context"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/corpus"
)

func TestClassify(t *testing.T) {
	ok := Outcome{Stdout: []byte("1\n"), Stderr: []byte("STOP 2\n"), Exit: 2}
	pass := Outcome{Stdout: []byte("1\n"), Exit: 0}
	for _, tc := range []struct {
		name     string
		gf, ours Outcome
		want     RunVerdict
	}{
		{"identical", pass, pass, RunSame},
		// equal, but the reference stopped itself: the test failed on both sides
		{"both failed", ok, ok, RunBothFail},
		{"a test gfortran fails and eval passes differs", ok, pass, RunDiffers},
		{"gfortran failed wins over anything", Outcome{Failure: "x"}, Outcome{Refused: true, Failure: "y"}, RunGfFailed},
		{"refused", ok, Outcome{Refused: true, Failure: "real(16) is not supported"}, RunRefused},
		{"failed while running", ok, Outcome{Failure: "subscript out of range"}, RunDiffers},
		{"stdout", ok, Outcome{Stdout: []byte("2\n"), Stderr: ok.Stderr, Exit: 2}, RunDiffers},
		{"stderr", ok, Outcome{Stdout: ok.Stdout, Stderr: nil, Exit: 2}, RunDiffers},
		{"exit", ok, Outcome{Stdout: ok.Stdout, Stderr: ok.Stderr, Exit: 0}, RunDiffers},
	} {
		if got, note := Classify(tc.gf, tc.ours); got != tc.want {
			t.Errorf("%s: %s (%s), want %s", tc.name, got, note, tc.want)
		}
	}
}

// RunEval tells a refusal (nothing ran) from a failure while running, and
// reports the exit status of STOP: the three outcomes the differential sorts.
func TestRunEvalSortsRefusalFromFailure(t *testing.T) {
	ctx := context.Background()
	if o := RunEval(ctx, []byte("program p\nreal(16) :: q\nq = 1\nprint *, q\nend program\n"), RunLimits{}); !o.Refused || o.Failure == "" {
		t.Errorf("an unsupported kind: %+v, want a refusal", o)
	}
	if o := RunEval(ctx, []byte("program p\ninteger :: a(3), i\ni = 4\na(i) = 1\nend program\n"), RunLimits{}); o.Refused || o.Failure == "" {
		t.Errorf("a subscript out of range: %+v, want a failure while running, not a refusal", o)
	}
	o := RunEval(ctx, []byte("program p\nn = 6\nprint *, n * 7\nstop 3\nend program\n"), RunLimits{})
	if o.Failure != "" || strings.TrimSpace(string(o.Stdout)) != "42" || o.Exit != 3 || strings.TrimSpace(string(o.Stderr)) != "STOP 3" {
		t.Errorf("implicit typing and STOP 3: %+v", o)
	}
}

// The note of a differing stream names the first differing line of each side,
// so a report sorts its differences by cause.
func TestClassifyNamesTheFirstDifferingLine(t *testing.T) {
	_, note := Classify(Outcome{Stdout: []byte("a\nb\n")}, Outcome{Stdout: []byte("a\nc\n"), Stderr: nil})
	if note != `stdout line 2: ours "c", gfortran "b"` {
		t.Errorf("stdout: %q", note)
	}
	_, note = Classify(Outcome{}, Outcome{Stderr: []byte("STOP 1\n")})
	if note != `stderr line 1: ours "STOP 1", gfortran ""` {
		t.Errorf("stderr: %q", note)
	}
}

func TestSkipReason(t *testing.T) {
	if skipReason(corpus.Entry{Form: corpus.Fixed}) == "" {
		t.Error("fixed form is not skipped")
	}
	if skipReason(corpus.Entry{Form: corpus.Free, Source: []byte("! { dg-do run }\n! { dg-shouldfail \"x\" }\n")}) == "" {
		t.Error("dg-shouldfail is not skipped")
	}
	if skipReason(corpus.Entry{Form: corpus.Free, Source: []byte("! { dg-do run \"xfail *-*-*\" }\n")}) == "" {
		t.Error("a run test expected to fail everywhere is not skipped")
	}
	if why := skipReason(corpus.Entry{Form: corpus.Free, Source: []byte("! { dg-do run { xfail powerpc*-*-* } }\n")}); why != "" {
		t.Errorf("a run test expected to fail on powerpc only is skipped: %s", why)
	}
	if why := skipReason(corpus.Entry{Form: corpus.Free, Source: []byte("! { dg-do run }\n")}); why != "" {
		t.Errorf("a plain run test is skipped: %s", why)
	}
}

// Every class is in the count, zero included.
func TestCountRunHasEveryClass(t *testing.T) {
	c := CountRun(nil)
	for _, v := range []RunVerdict{RunSame, RunBothFail, RunDiffers, RunRefused, RunGfFailed, RunSkipped} {
		if n, ok := c[v]; !ok || n != 0 {
			t.Errorf("%s: %d, %v", v, n, ok)
		}
	}
}
