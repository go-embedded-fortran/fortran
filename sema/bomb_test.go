package sema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/syntax"
)

// Bomb tests assert a REFUSAL, not a crash or an out-of-memory: run under
// GOMEMLIMIT and ulimit -v in CI (see the workflow), never against the host's
// whole memory.

func TestBombDeepExpression(t *testing.T) {
	// 5000 nested binary operations: either the parser's depth bound or the
	// analyser's refuses; nothing recurses without limit.
	var b strings.Builder
	b.WriteString("program p\n integer :: i\n i = 1")
	for i := 0; i < 5000; i++ {
		b.WriteString(" + 1")
	}
	b.WriteString("\nend\n")
	f, err := syntax.Parse("t.f90", []byte(b.String()), nil)
	if err != nil {
		return // refused by the parser
	}
	_, errs := Analyze(f, &Options{Limits: Limits{MaxDepth: 100}})
	if len(errs) == 0 || !strings.Contains(errs.Error(), "deeper than the analyser's bound") {
		t.Fatalf("no refusal: %v", errs)
	}
}

func TestBombManySymbols(t *testing.T) {
	var b strings.Builder
	b.WriteString("program p\n implicit none\n")
	for i := 0; i < 20000; i++ {
		b.WriteString(" integer :: v" + itoa(i) + "\n")
	}
	b.WriteString("end\n")
	f, err := syntax.Parse("t.f90", []byte(b.String()), nil)
	if err != nil {
		t.Skipf("parser refuses: %v", err)
	}
	_, errs := Analyze(f, &Options{Limits: Limits{MaxSymbols: 1000}})
	if len(errs) == 0 || !strings.Contains(errs.Error(), "too many symbols") {
		t.Fatalf("no refusal: %v", errs)
	}
}

func TestBombConstantGrowth(t *testing.T) {
	// 40 doublings of a character constant would be 8 TB
	var b strings.Builder
	b.WriteString("program p\n character(len=*), parameter :: s0 = 'abcdefgh'\n")
	for i := 1; i <= 40; i++ {
		b.WriteString(" character(len=*), parameter :: s" + itoa(i) + " = s" + itoa(i-1) + " // s" + itoa(i-1) + "\n")
	}
	b.WriteString("end\n")
	_, errs := analyze(t, "t.f90", b.String(), nil)
	if len(errs) == 0 || !strings.Contains(errs.Error(), "longer than the analyser's bound") {
		t.Fatalf("no refusal: %v", errs)
	}
}

func TestBombIntegerPower(t *testing.T) {
	_, errs := analyze(t, "t.f90", "program p\n integer, parameter :: a = 3 ** 1000000000\nend\n", nil)
	if len(errs) == 0 || !strings.Contains(errs.Error(), "overflow") {
		t.Fatalf("no refusal: %v", errs)
	}
}

func BenchmarkAnalyze(b *testing.B) {
	files, _ := filepath.Glob("../testdata/programs/*.f90")
	type unit struct {
		name string
		f    *syntax.File
		size int
	}
	var us []unit
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			b.Fatal(err)
		}
		f, err := syntax.Parse(filepath.Base(name), src, nil)
		if err != nil {
			b.Fatal(err)
		}
		us = append(us, unit{filepath.Base(name), f, len(src)})
	}
	total := 0
	for _, u := range us {
		total += u.size
	}
	b.SetBytes(int64(total))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, u := range us {
			if _, errs := Analyze(u.f, nil); len(errs) != 0 {
				b.Fatal(errs)
			}
		}
	}
}
