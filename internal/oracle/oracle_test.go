package oracle_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-embedded-fortran/fortran/internal/oracle"
	"github.com/go-embedded-fortran/fortran/internal/oracle/oracletest"
)

func TestMain(m *testing.M) {
	oracle.Init()
	os.Exit(m.Run())
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]string{
		"GNU Fortran (conda-forge gcc 15.2.0-20) 15.2.0\nCopyright": "15.2.0",
		"GNU Fortran (Homebrew GCC 16.2.0) 16.2.0\n":                "16.2.0",
		"GNU Fortran (GCC) 15.2.0":                                  "15.2.0",
	} {
		got, err := oracle.ParseVersion([]byte(in))
		if err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "clang version 23.1.4", "GNU Fortran (x) abc", "GNU Fortran"} {
		if _, err := oracle.ParseVersion([]byte(in)); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestParseFlangVersion(t *testing.T) {
	got, err := oracle.ParseFlangVersion([]byte("flang version 23.1.4 (https://x)\n"))
	if err != nil || got != "23.1.4" {
		t.Fatalf("%q, %v", got, err)
	}
	if _, err := oracle.ParseFlangVersion([]byte("clang version 1")); err == nil {
		t.Fatal("accepted")
	}
}

func TestParseKernelAndLoad(t *testing.T) {
	c, s, err := oracle.ParseKernel([]byte("1.5E+00\ntime 0.250000\n"))
	if err != nil || c != "1.5E+00" || s != 0.25 {
		t.Fatalf("%q %v %v", c, s, err)
	}
	for _, in := range []string{"", "x\n", "x\nfoo 1\n", "x\ntime y\n", "x\ntime -1\n"} {
		if _, _, err := oracle.ParseKernel([]byte(in)); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
	for in, want := range map[string]float64{"{ 3.17 2.99 2.74 }": 3.17, "0.52 0.58 0.59 1/467 12345": 0.52} {
		if got, err := oracle.ParseLoad(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := oracle.ParseLoad("{ }"); err == nil {
		t.Error("empty load accepted")
	}
}

func TestMedian(t *testing.T) {
	if got := oracle.Median([]float64{3, 1, 2}); got != 2 {
		t.Fatal(got)
	}
	xs := []float64{4, 1, 3, 2}
	if got := oracle.Median(xs); got != 2.5 || xs[0] != 4 {
		t.Fatal(got, xs)
	}
}

func TestLocateNothing(t *testing.T) {
	t.Setenv(oracle.EnvHome, "")
	t.Setenv(oracle.EnvGfortran, "")
	if _, err := oracle.Locate(); err == nil {
		t.Fatal("located with nothing set")
	}
}

func TestVerifyRefusesMissing(t *testing.T) {
	tc := &oracle.Toolchain{Gfortran: "/nonexistent/gfortran"}
	if err := tc.Verify(context.Background()); err == nil {
		t.Fatal("accepted")
	}
}

func TestOracleHello(t *testing.T) {
	tc := oracletest.Require(t)
	res, err := tc.Output(context.Background(), []byte("program p\nprint '(a)', 'hi'\nstop 4\nend\n"), oracle.CompileOptions{}, oracle.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "hi\n" || res.Exit != 4 || string(res.Stderr) != "STOP 4\n" {
		t.Fatalf("%+v", res)
	}
}

func TestOracleFixedForm(t *testing.T) {
	tc := oracletest.Require(t)
	src := []byte("      PROGRAM P\n      PRINT '(A)', 'fixed'\n      END\n")
	res, err := tc.Output(context.Background(), src, oracle.CompileOptions{Ext: oracle.FixedForm}, oracle.Limits{})
	if err != nil || string(res.Stdout) != "fixed\n" {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestOracleCompileError(t *testing.T) {
	tc := oracletest.Require(t)
	_, err := tc.Output(context.Background(), []byte("program p\nx = \nend\n"), oracle.CompileOptions{}, oracle.Limits{})
	if _, ok := err.(*oracle.CompileError); !ok {
		t.Fatalf("%v", err)
	}
}

func run(t *testing.T, src string, lim oracle.Limits) oracle.Result {
	t.Helper()
	tc := oracletest.Require(t)
	res, err := tc.Output(context.Background(), []byte(src), oracle.CompileOptions{}, lim)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestOracleTimeout(t *testing.T) {
	res := run(t, "program p\ndo\nend do\nend\n", oracle.Limits{Timeout: 500 * time.Millisecond})
	if !res.TimedOut {
		t.Fatalf("%+v", res)
	}
}

func TestOracleOutputCap(t *testing.T) {
	res := run(t, "program p\ndo\nprint '(a)', 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'\nend do\nend\n", oracle.Limits{MaxOutput: 4096})
	if !res.Truncated || len(res.Stdout) != 4096 {
		t.Fatalf("truncated=%v len=%d", res.Truncated, len(res.Stdout))
	}
}

func TestOracleSignal(t *testing.T) {
	res := run(t, "program p\ncall abort()\nend\n", oracle.Limits{})
	if res.Signal == "" {
		t.Fatalf("%+v", res)
	}
}

// The memory limit is enforced on Linux only (macOS ignores RLIMIT_AS).
func TestOracleMemoryLimit(t *testing.T) {
	src := "program p\ninteger(1), allocatable :: a(:)\ninteger :: s\nallocate (a(400000000), stat = s)\nprint *, s /= 0\nend\n"
	res := run(t, src, oracle.Limits{MemoryBytes: 256 << 20})
	if !res.MemoryLimited {
		t.Skip("the address-space limit is not enforced on this OS")
	}
	if string(res.Stdout) != " T\n" {
		t.Fatalf("%+v", res)
	}
}
