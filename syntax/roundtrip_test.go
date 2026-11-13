package syntax

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// roundTrip checks Print/Parse idempotence: the AST of the printed source
// equals the AST of the original, and printing again is a fixed point.
func roundTrip(t *testing.T, name string, src []byte) {
	t.Helper()
	f1, err := Parse(name, src, nil)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out1 := Print(f1)
	f2, err := Parse(name+".printed.f90", []byte(out1), nil)
	if err != nil {
		t.Fatalf("%s: printed source does not parse: %v\n%s", name, err, out1)
	}
	if d1, d2 := DumpFile(f1), DumpFile(f2); d1 != d2 {
		t.Fatalf("%s: AST changed by print/parse:\n--- original\n%s\n--- reparsed\n%s\n--- printed\n%s", name, d1, d2, out1)
	}
	if out2 := Print(f2); out1 != out2 {
		t.Fatalf("%s: printing is not a fixed point", name)
	}
}

func TestRoundTripTestdata(t *testing.T) {
	for _, f := range testdataFiles(t) {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip(t, f, src)
	}
}

func TestGoldenAST(t *testing.T) {
	update := os.Getenv("SYNTAX_UPDATE_GOLDEN") != ""
	for _, name := range []string{"hello", "derived_types", "formatted_output", "arrays_sections", "recursion"} {
		src, err := os.ReadFile(filepath.Join("..", "testdata", "programs", name+".f90"))
		if err != nil {
			t.Fatal(err)
		}
		f, err := Parse(name+".f90", src, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := DumpFile(f)
		gp := filepath.Join("testdata", "golden", name+".ast")
		if update {
			if err := os.MkdirAll(filepath.Dir(gp), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(gp, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(gp)
		if err != nil {
			t.Fatalf("%v (regenerate with SYNTAX_UPDATE_GOLDEN=1)", err)
		}
		if string(want) != got {
			t.Errorf("%s: AST differs from %s (regenerate with SYNTAX_UPDATE_GOLDEN=1 and review the diff)", name, gp)
		}
	}
}

func TestPositions(t *testing.T) {
	src := "program p\n  integer :: x\n  x = 1 &\n    + 2\nend program p\n"
	f, err := Parse("p.f90", []byte(src), nil)
	if err != nil {
		t.Fatal(err)
	}
	u := f.Units[0]
	if got := f.Position(u.Pos).String(); got != "p.f90:1:1" {
		t.Errorf("unit at %s", got)
	}
	as := u.Body[1].(*Assignment)
	if got := f.Position(as.Pos).String(); got != "p.f90:3:3" {
		t.Errorf("assignment at %s", got)
	}
	bin := as.RHS.(*Binary)
	if got := f.Position(bin.Y.P()).String(); got != "p.f90:4:7" {
		t.Errorf("continued operand at %s", got)
	}
	if !strings.Contains(Print(f), "x = 1 + 2") {
		t.Errorf("print: %s", Print(f))
	}
}
