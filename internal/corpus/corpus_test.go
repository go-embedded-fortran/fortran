package corpus

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/golden"
	"github.com/go-embedded-fortran/fortran/internal/oracle"
	"github.com/go-embedded-fortran/fortran/internal/oracle/oracletest"
)

func TestMain(m *testing.M) {
	oracle.Init()
	os.Exit(m.Run())
}

func TestFormOf(t *testing.T) {
	for name, want := range map[string]Form{
		"a.f90": Free, "a.F90": Free, "a.f95": Free, "a.f03": Free, "a.f08": Free,
		"a.f": Fixed, "a.F": Fixed, "a.for": Fixed, "a.c": NotFortran, "f90": NotFortran, "a.f77": NotFortran,
	} {
		if got := FormOf(name); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"b.f90", "a.f", "sub/c.f08", "sub/skip.txt", ".git/d.f90"} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("end\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	es, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range es {
		got = append(got, e.Path)
	}
	if strings.Join(got, ",") != "a.f,b.f90,sub/c.f08" {
		t.Fatal(got)
	}
	if es[0].Form != Fixed || es[1].Form != Free {
		t.Fatal("forms")
	}
}

func TestJudge(t *testing.T) {
	for _, c := range []struct {
		res  oracle.Result
		want Outcome
	}{
		{oracle.Result{}, Pass},
		{oracle.Result{Exit: 2}, Fail},
		{oracle.Result{Signal: "aborted"}, Signal},
		{oracle.Result{TimedOut: true}, Timeout},
		{oracle.Result{Truncated: true}, Harness},
	} {
		if got := Judge("x", c.res).Outcome; got != c.want {
			t.Errorf("%+v: %s, want %s", c.res, got, c.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	d := "f.f90:2:4:\n\n    2 |   x = \n      |       1\nError: Invalid expression at (1)\n"
	if got := firstLine(d); got != "Error: Invalid expression at (1)" {
		t.Fatal(got)
	}
	if got := firstLine("only\n"); got != "only" {
		t.Fatal(got)
	}
}

func TestReport(t *testing.T) {
	var b bytes.Buffer
	Report(&b, []Rec{{Path: "a.f90", Outcome: Pass}, {Path: "b.f", Outcome: Fail, Exit: 3, Detail: "x\ny"}})
	want := "a.f90\tpass\t0\t\nb.f\tfail\t3\tx y\n# total 2 pass=1 compile-fail=0 fail=1 signal=0 timeout=0 harness=0\n"
	if b.String() != want {
		t.Fatalf("%q", b.String())
	}
}

func TestRunOutcomes(t *testing.T) {
	tc := oracletest.Require(t)
	es := []Entry{
		{Path: "ok.f90", Source: []byte("program p\nend\n"), Form: Free},
		{Path: "bad.f90", Source: []byte("program p\nx = \nend\n"), Form: Free},
		{Path: "stop.f", Source: []byte("      PROGRAM P\n      STOP 2\n      END\n"), Form: Fixed},
	}
	recs := Run(context.Background(), tc, "", es, oracle.CompileOptions{}, oracle.Limits{}, 2)
	want := []Outcome{Pass, CompileFail, Fail}
	for i, r := range recs {
		if r.Outcome != want[i] {
			t.Errorf("%s: %s (%s), want %s", r.Path, r.Outcome, r.Detail, want[i])
		}
	}
}

// The committed programs, run by the oracle now, say what the committed
// golden file says.
func TestGoldenPrograms(t *testing.T) {
	tc := oracletest.Require(t)
	got, err := Programs(context.Background(), tc, filepath.Join("..", "..", "testdata", "programs"), oracle.CompileOptions{}, oracle.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join("..", "..", "testdata", "programs.golden"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want, err := golden.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d programs, golden has %d (run `foracle golden`)", len(got), len(want))
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("%s differs from the golden file:\n got %+v\nwant %+v", want[i].Name, got[i], want[i])
		}
	}
}

// The golden file is checked without a toolchain too: it has the programs the
// directory has, and covers the exit statuses and the stderr the plan asks for.
func TestGoldenFileShape(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "programs.golden"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := golden.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Join("..", "..", "testdata", "programs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != len(ents) || len(recs) < 12 {
		t.Fatalf("%d records, %d programs", len(recs), len(ents))
	}
	var exits, stderr bool
	for _, r := range recs {
		exits = exits || r.Exit != 0
		stderr = stderr || len(r.Stderr) > 0
	}
	if !exits || !stderr {
		t.Fatal("no nonzero exit status or no stderr in the goldens")
	}
}
