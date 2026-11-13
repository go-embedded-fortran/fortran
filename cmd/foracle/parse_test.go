package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/oracle"
)

func TestParseRateNoOracle(t *testing.T) {
	t.Setenv(oracle.EnvHome, "")
	t.Setenv(oracle.EnvGfortran, "")
	dir := t.TempDir()
	for name, src := range map[string]string{
		"good.f90": "program p\n  print *, 1\nend program p\n",
		"good.f":   "      PROGRAM P\n      END\n",
		"bad.f90":  "program p\n  x = = 1\nend program p\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tsv := filepath.Join(dir, "out.tsv")
	out, _, err := exec(t, "parse-rate", "--dir", dir, "--no-oracle", "--errors", "3", "--out", tsv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "3 files: we parse 2") || !strings.Contains(out, "unexpected") && !strings.Contains(out, "expected") {
		t.Errorf("output %q", out)
	}
	b, err := os.ReadFile(tsv)
	if err != nil || !strings.Contains(string(b), "bad.f90\t") || !strings.Contains(string(b), "refuse") {
		t.Errorf("report %q %v", b, err)
	}
}

func TestParseCommandsUsage(t *testing.T) {
	for _, args := range [][]string{{"parse-rate"}, {"parse-bench"}} {
		t.Setenv(oracle.EnvHome, "")
		t.Setenv(oracle.EnvGfortran, "")
		if _, _, err := exec(t, args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}
