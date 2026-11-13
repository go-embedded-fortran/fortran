package sema

import (
	"reflect"
	"strings"
	"testing"
)

func run(t *testing.T, s *Session, st any, i int, src string) (*CellInfo, any) {
	t.Helper()
	return s.AnalyzeNext(st, i, src)
}

func TestSessionSplit(t *testing.T) {
	s := NewSession(SessionOptions{})
	st := s.Begin()
	c0, st := run(t, s, st, 0, "integer :: n = 3\nreal(8) :: x(4)\nsubroutine bump()\n n = n + 1\nend subroutine\nn = n * 2\nprint *, n\n")
	if len(c0.Errors) != 0 {
		t.Fatal(c0.Errors)
	}
	if c0.Module != "__cell0" || c0.Run != "__cell0_run" {
		t.Errorf("module %q run %q", c0.Module, c0.Run)
	}
	if want := []string{"n", "x"}; !reflect.DeepEqual(c0.Defines, want) {
		t.Errorf("defines %v, want %v", c0.Defines, want)
	}
	if want := []string{"bump"}; !reflect.DeepEqual(c0.Procedures, want) {
		t.Errorf("procedures %v, want %v", c0.Procedures, want)
	}
	if !c0.Crosses("N") || c0.Crosses("bump") {
		t.Error("variables cross, procedures do not")
	}

	// cell 1 sees cell 0 through the implicit use, and reads n
	c1, st := run(t, s, st, 1, "integer :: m\nm = n + 1\ncall bump()\n")
	if len(c1.Errors) != 0 {
		t.Fatal(c1.Errors)
	}
	if want := []string{"m"}; !reflect.DeepEqual(c1.Defines, want) {
		t.Errorf("defines %v", c1.Defines)
	}
	if want := []string{"bump", "n"}; !reflect.DeepEqual(c1.Uses, want) {
		t.Errorf("uses %v, want %v", c1.Uses, want)
	}

	// a module defined by a cell is in scope for the later cells
	c2, st := run(t, s, st, 2, "module shapes\n real, parameter :: pi = 3.14159\nend module\n")
	if len(c2.Errors) != 0 || c2.Module != "" || !reflect.DeepEqual(c2.Modules, []string{"shapes"}) {
		t.Fatalf("%+v", c2)
	}
	c3, st := run(t, s, st, 3, "real :: r\nr = 2 * pi\n")
	if len(c3.Errors) != 0 {
		t.Fatal(c3.Errors)
	}
	if !reflect.DeepEqual(c3.Uses, []string{"pi"}) {
		t.Errorf("uses %v", c3.Uses)
	}

	// redefinition shadows: the new n is real, earlier cells keep theirs
	c4, st := run(t, s, st, 4, "real :: n\nn = 1.5\n")
	if len(c4.Errors) != 0 {
		t.Fatal(c4.Errors)
	}
	if got := c4.Vars["n"].Type; got.Basic != Real {
		t.Errorf("redefined n: %v", got)
	}
	if got := st.(*sessionState).modules["__cell0"].Names["n"].Type; got.Basic != Integer {
		t.Errorf("cell 0 lost its n: %v", got)
	}
	c5, _ := run(t, s, st, 5, "real :: y\ny = n / 2\n")
	if len(c5.Errors) != 0 {
		t.Fatal(c5.Errors)
	}
	if got := c5.Info.Types; len(got) == 0 {
		t.Error("no typed tree")
	}
}

func TestSessionErrorLeavesNothingBehind(t *testing.T) {
	s := NewSession(SessionOptions{})
	st := s.Begin()
	bad, st2 := run(t, s, st, 0, "integer :: a\na = 'x'\n")
	if len(bad.Errors) == 0 {
		t.Fatal("no error")
	}
	if st2 != st {
		t.Error("a cell with errors changed the state")
	}
	// the broken cell defined nothing: its a is not in scope
	c1, _ := run(t, s, st2, 1, "a = 1\n")
	if len(c1.Errors) == 0 || !strings.Contains(c1.Errors.Error(), "has no IMPLICIT type") {
		t.Errorf("errors %v", c1.Errors)
	}
	// parse errors too
	bad, st3 := run(t, s, st, 2, "do i = 1\n")
	if len(bad.Errors) == 0 || st3 != st {
		t.Errorf("parse error: %v", bad.Errors)
	}
}

func TestSessionImplicitTyping(t *testing.T) {
	src := "x = 1.5\nk = 2\n"
	off := NewSession(SessionOptions{})
	c, _ := off.AnalyzeNext(off.Begin(), 0, src)
	if len(c.Errors) == 0 {
		t.Error("implicit typing is on by default in a cell")
	}
	on := NewSession(SessionOptions{ImplicitTyping: true})
	c, _ = on.AnalyzeNext(on.Begin(), 0, src)
	if len(c.Errors) != 0 {
		t.Errorf("implicit typing option: %v", c.Errors)
	}
	// "implicit none" and top-level statements before and after units
	c, _ = off.AnalyzeNext(off.Begin(), 0, "integer :: i\ni = 1\nfunction f(a)\n real :: a, f\n f = a\nend function\ni = 2\n")
	if len(c.Errors) != 0 {
		t.Errorf("mixed cell: %v", c.Errors)
	}
}

func TestCaseInsensitive(t *testing.T) {
	s := NewSession(SessionOptions{})
	c, st := s.AnalyzeNext(s.Begin(), 0, "INTEGER :: Total\nTotal = 4\n")
	if len(c.Errors) != 0 || !reflect.DeepEqual(c.Defines, []string{"total"}) {
		t.Fatalf("%+v", c)
	}
	c, _ = s.AnalyzeNext(st, 1, "integer :: k\nk = TOTAL + 1\n")
	if len(c.Errors) != 0 || !reflect.DeepEqual(c.Uses, []string{"total"}) {
		t.Fatalf("%+v", c)
	}
}

func TestGoType(t *testing.T) {
	for _, c := range []struct {
		t    Type
		rank int
		want string
		ok   bool
	}{
		{Type{Basic: Integer, Kind: 1}, 0, "int8", true},
		{Type{Basic: Integer, Kind: 4}, 0, "int32", true},
		{Type{Basic: Integer, Kind: 8}, 0, "int64", true},
		{Type{Basic: Integer, Kind: 16}, 0, "", false},
		{Type{Basic: Real, Kind: 4}, 0, "float32", true},
		{Type{Basic: Real, Kind: 8}, 0, "float64", true},
		{Type{Basic: Real, Kind: 16}, 0, "", false},
		{Type{Basic: Complex, Kind: 4}, 0, "complex64", true},
		{Type{Basic: Complex, Kind: 8}, 0, "complex128", true},
		{Type{Basic: Logical, Kind: 4}, 0, "bool", true},
		{Type{Basic: Character, Kind: 1, Len: 5}, 0, "string", true},
		{Type{Basic: Real, Kind: 8}, 2, "*ndarray.Array", true},
		{Type{Basic: Derived}, 0, "opaque", true},
	} {
		got, ok := GoType(c.t, c.rank)
		if got != c.want || ok != c.ok {
			t.Errorf("%v rank %d: %q %v, want %q %v", c.t, c.rank, got, ok, c.want, c.ok)
		}
	}
}
