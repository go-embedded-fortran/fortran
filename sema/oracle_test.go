package sema_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/oracle"
	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// negatives are programs gfortran 15.2.0 refuses; we must refuse them too
// (bib/fortran.md §8: "refuse where gfortran refuses"). A program of the
// positive list must be accepted by both.
var negatives = []struct{ name, src string }{
	{"type mismatch", "program p\n integer :: i\n character(len=3) :: s\n i = s\nend\n"},
	{"undeclared under implicit none", "program p\n implicit none\n i = 1\nend\n"},
	{"rank mismatch", "program p\n real :: a(3,3)\n a(1) = 1.0\nend\n"},
	{"rank assign", "program p\n real :: a(3), s\n s = a\nend\n"},
	{"intent(in) assignment", "subroutine s(x)\n integer, intent(in) :: x\n x = 1\nend\n"},
	{"named constant assignment", "program p\n integer, parameter :: n = 1\n n = 2\nend\n"},
	{"do variable", "program p\n integer :: i\n do i = 1, 3\n  i = 5\n end do\nend\n"},
	{"numeric operator", "program p\n logical :: l\n integer :: i\n i = 1 + l\nend\n"},
	{"duplicate type", "program p\n integer :: i\n real :: i\nend\n"},
	{"bad member", "program p\n type t\n  integer :: a\n end type\n type(t) :: x\n x%b = 1\nend\n"},
	{"unknown type", "program p\n type(nope) :: x\nend\n"},
	{"not a constant", "program p\n integer :: n\n integer, parameter :: a = n + 1\nend\n"},
	{"division by zero", "program p\n integer, parameter :: a = 1 / 0\nend\n"},
	{"bad kind", "program p\n real(3) :: x\nend\n"},
	{"only missing", "module m\n integer :: a\nend\nprogram p\n use m, only: b\nend\n"},
	{"if not logical", "program p\n integer :: i\n i = 1\n if (i) i = 2\nend\n"},
}

var positives = []struct{ name, src string }{
	{"implicit typing", "program p\n x = 1\n i = 2\n print *, x, i\nend\n"},
	{"kinds", "program p\n integer, parameter :: dp = selected_real_kind(15)\n real(dp) :: x\n x = 1.0_dp\n print *, x\nend\n"},
	{"function", "integer function f(x)\n integer :: x\n f = x + 1\nend\nprogram p\n integer :: f\n print *, f(1)\nend\n"},
	{"derived", "program p\n implicit none\n type pt\n  real :: x\n end type\n type(pt) :: a\n a%x = 1.0\n print *, a%x\nend\n"},
	{"module use", "module m\n integer :: a = 1\nend\nprogram p\n use m, only: b => a\n print *, b\nend\n"},
}

func gfortranAccepts(t *testing.T, gf, src string) bool {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "t.f90")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(gf, "-fsyntax-only", "-w", path)
	cmd.Dir = dir
	cmd.Stdin = nil
	return cmd.Run() == nil
}

func ours(t *testing.T, src string) syntax.ErrorList {
	t.Helper()
	f, err := syntax.Parse("t.f90", []byte(src), nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, errs := sema.Analyze(f, nil)
	return errs
}

func TestNegativesAgreeWithGfortran(t *testing.T) {
	tc, err := oracle.Locate()
	if err != nil {
		t.Skipf("no oracle: %v", err)
	}
	for _, c := range negatives {
		t.Run(c.name, func(t *testing.T) {
			gf := gfortranAccepts(t, tc.Gfortran, c.src)
			errs := ours(t, c.src)
			if gf {
				t.Fatalf("gfortran accepts this program: the list is wrong")
			}
			if len(errs) == 0 {
				t.Errorf("gfortran refuses, we accept:\n%s", c.src)
			}
			for _, e := range errs {
				if !strings.Contains(e.Pos.String(), ":") {
					t.Errorf("diagnostic without file:line:col: %v", e)
				}
			}
		})
	}
	for _, c := range positives {
		t.Run("ok "+c.name, func(t *testing.T) {
			if !gfortranAccepts(t, tc.Gfortran, c.src) {
				t.Fatalf("gfortran refuses this program: the list is wrong")
			}
			if errs := ours(t, c.src); len(errs) != 0 {
				t.Errorf("gfortran accepts, we refuse: %v", errs)
			}
		})
	}
}

// TestCallCasesAgreeWithGfortran cross-checks the argument association and
// generic resolution cases (call_test.go) against gfortran, in both directions.
func TestCallCasesAgreeWithGfortran(t *testing.T) { casesAgree(t, sema.CallCases) }

// TestShapeCasesAgreeWithGfortran does the same for the shape conformance
// cases (shape_test.go).
func TestShapeCasesAgreeWithGfortran(t *testing.T) { casesAgree(t, sema.ShapeCases) }

func casesAgree(t *testing.T, cases func() (neg, pos []sema.Case)) {
	tc, err := oracle.Locate()
	if err != nil {
		t.Skipf("no oracle: %v", err)
	}
	neg, pos := cases()
	for _, c := range neg {
		t.Run(c.Name, func(t *testing.T) {
			if gfortranAccepts(t, tc.Gfortran, c.Src) {
				t.Fatalf("gfortran accepts this program: the list is wrong")
			}
			if len(ours(t, c.Src)) == 0 {
				t.Errorf("gfortran refuses, we accept:\n%s", c.Src)
			}
		})
	}
	for _, c := range pos {
		t.Run("ok "+c.Name, func(t *testing.T) {
			if !gfortranAccepts(t, tc.Gfortran, c.Src) {
				t.Fatalf("gfortran refuses this program: the list is wrong")
			}
			if errs := ours(t, c.Src); len(errs) != 0 {
				t.Errorf("gfortran accepts, we refuse: %v", errs)
			}
		})
	}
}
