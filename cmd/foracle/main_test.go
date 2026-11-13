package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/oracle"
	"github.com/go-embedded-fortran/fortran/internal/oracle/oracletest"
)

func TestMain(m *testing.M) {
	oracle.Init()
	os.Exit(m.Run())
}

func exec(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	c := RootCmd()
	c.SetArgs(args)
	c.SetOut(&out)
	c.SetErr(&errb)
	err := c.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func TestVersion(t *testing.T) {
	out, _, err := exec(t, "version")
	if err != nil || !strings.Contains(out, "gfortran 15.2.0") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"toolchain", "--home", ""}, {"corpus"}, {"run"}, {"nosuch"}, {"bench", "--rounds", "0", "--force"}} {
		t.Setenv(oracle.EnvHome, "")
		t.Setenv(oracle.EnvGfortran, "")
		if _, _, err := exec(t, args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}

func TestMainExitCode(t *testing.T) {
	var got int
	old := osExit
	osExit = func(c int) { got = c }
	defer func() { osExit = old }()
	oldArgs := os.Args
	os.Args = []string{"foracle", "nosuch"}
	defer func() { os.Args = oldArgs }()
	main()
	if got != 1 {
		t.Fatalf("exit %d", got)
	}
}

func TestRunRelaysStatus(t *testing.T) {
	oracletest.Require(t)
	f := filepath.Join(t.TempDir(), "p.f90")
	if err := os.WriteFile(f, []byte("program p\nprint '(a)', 'out'\nerror stop 5\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errs, err := exec(t, "run", f)
	var ec exitCode
	if e, ok := err.(exitCode); ok {
		ec = e
	}
	if out != "out\n" || ec != 5 || !strings.Contains(errs, "ERROR STOP 5") {
		t.Fatalf("%q %q %v", out, errs, err)
	}
}

func TestCorpusCommand(t *testing.T) {
	oracletest.Require(t)
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "a.f90"), []byte("program p\nend\n"), 0o644)
	os.WriteFile(filepath.Join(d, "b.f"), []byte("      PROGRAM P\n      STOP 1\n      END\n"), 0o644)
	out, _, err := exec(t, "corpus", "--dir", d, "--jobs", "2")
	if err != nil || !strings.Contains(out, "a.f90\tpass") || !strings.Contains(out, "b.f\tfail\t1") || !strings.Contains(out, "2 files: pass=1") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestBenchRefusesUnderLoad(t *testing.T) {
	// Without a toolchain the command still refuses first on a bad load or
	// reaches the toolchain check: either way it must not measure.
	t.Setenv(oracle.EnvHome, "")
	t.Setenv(oracle.EnvGfortran, "")
	if _, _, err := exec(t, "bench", "--force"); err == nil {
		t.Fatal("benched without a toolchain")
	}
}
