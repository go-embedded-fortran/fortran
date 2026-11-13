package sema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/syntax"
)

// FuzzSession feeds arbitrary cells to a session, two in a row: no panic, no
// diagnostic without a message, and a cell with errors leaves the state alone.
func FuzzSession(f *testing.F) {
	for _, s := range []string{
		"integer :: n\nn = 1\n",
		"subroutine s()\nend subroutine\ncall s()\n",
		"module m\n integer :: k\nend module\nx = 1\n",
		"real :: a(3)\na = [1., 2., 3.]\nprint *, sum(a)\n",
	} {
		f.Add(s, "integer :: m\nm = n + 1\n")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if len(a)+len(b) > 1<<15 {
			return
		}
		s := NewSession(SessionOptions{Limits: Limits{MaxSymbols: 5000, MaxConstChars: 1 << 14}})
		st := s.Begin()
		for i, src := range []string{a, b} {
			ci, next := s.AnalyzeNext(st, i, src)
			for _, e := range ci.Errors {
				if strings.HasPrefix(e.Msg, "internal error") || strings.Contains(e.Msg, "internal parser error") {
					t.Fatalf("panic recovered: %v", e)
				}
			}
			if len(ci.Errors) > 0 && next != st {
				t.Fatal("a cell with errors changed the state")
			}
			st = next
		}
	})
}

// FuzzAnalyze feeds arbitrary text to the parser and, when it parses, to the
// analyser: no input may panic, hang, or leave a diagnostic without a
// position. The committed corpus is testdata/fuzz/FuzzAnalyze plus the seeds
// below.
func FuzzAnalyze(f *testing.F) {
	for _, s := range []string{
		"program p\n implicit none\n integer :: i\n i = 1\nend\n",
		"module m\n integer, parameter :: k = selected_int_kind(9)\n type t\n  integer(k) :: a\n end type\nend\n",
		"program p\n x = f(1)\n a(1,2) = x\n call s(a, x)\nend\n",
		"subroutine s(a, n)\n real :: a(n)\n integer, intent(in) :: n\n n = 1\nend\n",
		"program p\n character(len=*), parameter :: s = 'ab' // 'cd'\n integer, parameter :: n = len(s) ** 3\nend\n",
		"program p\n type t\n  type(t), pointer :: next\n end type\n type(t) :: x\n x%next => x\nend\n",
		"module m\n interface g\n  module procedure a, b\n end interface\ncontains\n subroutine a(x)\n  integer :: x\n end subroutine\n subroutine b(y)\n  integer :: y\n end subroutine\nend module\nprogram p\n use m\n call g(1)\nend\n",
		"module m\n type t\n  integer :: i\n end type\n interface operator(+)\n  module procedure add\n end interface\n interface assignment(=)\n  module procedure asg\n end interface\ncontains\n function add(a, b)\n  type(t), intent(in) :: a, b\n  type(t) :: add\n end function\n subroutine asg(a, b)\n  type(t), intent(out) :: a\n  integer, intent(in) :: b\n end subroutine\nend module\nprogram p\n use m\n type(t) :: x\n x = 1\n x = x + x\n x = x - x\nend\n",
		"program p\n real :: a(3, 4), b(4)\n a(1:2, 1) = b(1:3:2)\n b = [(real(i), i = 1, 4)]\n b = sum(a, dim=1)\n a = reshape(b, [3, 4])\nend\n",
		"subroutine s(a, b)\n real :: a(:)\n real, optional, intent(out) :: b\nend\nprogram p\n real :: r(3)\n call s(r, b=1.0)\n call s(a=r)\n call s(1.0, 2.0, 3.0)\nend\n",
	} {
		f.Add(s)
	}
	files, _ := filepath.Glob("../testdata/programs/*.f90")
	for _, name := range files {
		if b, err := os.ReadFile(name); err == nil {
			f.Add(string(b))
		}
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1<<16 {
			return
		}
		file, err := syntax.Parse("f.f90", []byte(src), nil)
		if err != nil || file == nil {
			return
		}
		lim := DefaultLimits()
		lim.MaxSymbols, lim.MaxConstChars = 5000, 1<<14
		_, errs := Analyze(file, &Options{Limits: lim})
		for _, e := range errs {
			if strings.HasPrefix(e.Msg, "internal error") {
				t.Fatalf("the analyser panicked: %v", e)
			}
			if e.Msg == "" {
				t.Fatalf("empty diagnostic %v", e)
			}
			if e.Pos.Line < 0 {
				t.Fatalf("bad position %v", e)
			}
		}
	})
}
