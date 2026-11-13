package sema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/syntax"
)

func analyze(t testing.TB, name, src string, o *Options) (*Info, syntax.ErrorList) {
	t.Helper()
	f, err := syntax.Parse(name, []byte(src), nil)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return Analyze(f, o)
}

func wantOK(t *testing.T, src string) *Info {
	t.Helper()
	info, errs := analyze(t, "t.f90", src, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors:\n%v\n%s", errs, src)
	}
	return info
}

func wantErr(t *testing.T, src, sub string) {
	t.Helper()
	_, errs := analyze(t, "t.f90", src, nil)
	if len(errs) == 0 {
		t.Fatalf("no error, want %q in:\n%s", sub, src)
	}
	if !strings.Contains(errs.Error(), sub) {
		t.Fatalf("errors %v do not contain %q", errs, sub)
	}
	for _, e := range errs {
		if e.Pos.Line == 0 && e.Msg != "" && !strings.Contains(e.Msg, "internal") && !strings.Contains(e.Msg, "too many") {
			t.Fatalf("diagnostic without a position: %v", e)
		}
	}
}

func symOf(t *testing.T, info *Info, unit, name string) *Symbol {
	t.Helper()
	for _, u := range info.Units {
		if u.Unit.Ident.Name == unit || (unit == "" && u.Unit.Implicit) {
			if s := u.Scope.Names[name]; s != nil {
				return s
			}
		}
	}
	t.Fatalf("no symbol %s in %s", name, unit)
	return nil
}

func TestImplicitTyping(t *testing.T) {
	// F2018 8.7.1: a-h, o-z real; i-n integer.
	info := wantOK(t, "program p\n x = 1\n i = 2\n n2 = 3\n z = 4\nend\n")
	for name, want := range map[string]Basic{"x": Real, "i": Integer, "n2": Integer, "z": Real} {
		if got := symOf(t, info, "p", name).Type.Basic; got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	// implicit statement overrides the default mapping
	info = wantOK(t, "program p\n implicit integer (a-c)\n implicit logical (l)\n a = 1\n l = .true.\nend\n")
	if symOf(t, info, "p", "a").Type.Basic != Integer || symOf(t, info, "p", "l").Type.Basic != Logical {
		t.Error("implicit statement not applied")
	}
}

func TestImplicitNone(t *testing.T) {
	wantErr(t, "program p\n implicit none\n x = 1\nend\n", "Symbol 'x' at (1) has no IMPLICIT type")
	wantOK(t, "program p\n implicit none\n real :: x\n x = 1\nend\n")
	wantErr(t, "program p\n implicit none\n integer :: n\n y = f(n)\nend\n", "has no IMPLICIT type")
	// the session default (bib §7) applies to every unit
	_, errs := analyze(t, "t.f90", "program p\n x = 1\nend\n", &Options{ImplicitNone: true})
	if len(errs) == 0 {
		t.Error("Options.ImplicitNone ignored")
	}
}

func TestTypeAndKinds(t *testing.T) {
	info := wantOK(t, `program p
  integer, parameter :: dp = selected_real_kind(15, 307)
  integer, parameter :: k = kind(1.0d0)
  real(dp) :: x
  real(8) :: y
  double precision :: z
  integer(kind=8) :: big
  complex(k) :: c
  character(len=5) :: s
  logical :: l
  real*8 :: old
  x = 1.0_dp; y = x; z = y; big = 2_8; c = (1.0, 2.0); s = 'abc'; l = x < y; old = z
end
`)
	check := func(n string, b Basic, k int) {
		t.Helper()
		got := symOf(t, info, "p", n).Type
		if got.Basic != b || got.Kind != k {
			t.Errorf("%s: %v, want %v(%d)", n, got, b, k)
		}
	}
	check("dp", Integer, 4)
	check("x", Real, 8)
	check("y", Real, 8)
	check("z", Real, 8)
	check("big", Integer, 8)
	check("c", Complex, 8)
	check("old", Real, 8)
	if s := symOf(t, info, "p", "s").Type; s.Len != 5 {
		t.Errorf("len = %d", s.Len)
	}
	wantErr(t, "program p\n real(3) :: x\nend\n", "Kind 3 not supported for type real")
	wantErr(t, "program p\n integer :: n\n real(n) :: x\nend\n", "kind expression at (1) must be a scalar integer constant expression")
}

func TestParameterFolding(t *testing.T) {
	info := wantOK(t, `program p
  integer, parameter :: a = 3, b = a * 4 + 1, c = b ** 2, d = mod(c, 7)
  real, parameter :: r = 1.5, s = r * 2, t = real(a) / 2
  logical, parameter :: l = a < b .and. .not. (c == 0)
  character(len=*), parameter :: msg = 'ab' // 'cd'
  integer, parameter :: len_msg = len(msg), big = huge(1)
  complex, parameter :: z = (1.0, 2.0) * 2
end
`)
	ci := func(n string, want int64) {
		t.Helper()
		s := symOf(t, info, "p", n)
		if s.Const == nil || s.Const.I != want {
			t.Errorf("%s = %+v, want %d", n, s.Const, want)
		}
	}
	ci("a", 3)
	ci("b", 13)
	ci("c", 169)
	ci("d", 1)
	ci("len_msg", 4)
	ci("big", 2147483647)
	if s := symOf(t, info, "p", "s"); s.Const == nil || s.Const.F != 3 {
		t.Errorf("s = %+v", s.Const)
	}
	if s := symOf(t, info, "p", "t"); s.Const == nil || s.Const.F != 1.5 {
		t.Errorf("t = %+v", s.Const)
	}
	if s := symOf(t, info, "p", "l"); s.Const == nil || !s.Const.B {
		t.Errorf("l = %+v", s.Const)
	}
	if s := symOf(t, info, "p", "msg"); s.Const == nil || s.Const.S != "abcd" || s.Type.Len != 4 {
		t.Errorf("msg = %+v %v", s.Const, s.Type)
	}
	if s := symOf(t, info, "p", "z"); s.Const == nil || s.Const.C != complex(2, 4) {
		t.Errorf("z = %+v", s.Const)
	}
	wantErr(t, "program p\n integer :: n\n integer, parameter :: a = n + 1\nend\n", "not a constant expression")
	wantErr(t, "program p\n integer, parameter :: a = 1 / 0\nend\n", "division by zero")
	wantErr(t, "program p\n integer, parameter :: a = huge(1) + 1\nend\n", "overflow")
	wantErr(t, "program p\n integer, parameter :: a = 2 ** 70\nend\n", "overflow")
	wantErr(t, "program p\n integer, parameter :: a\nend\n", "has no value")
}

func TestNegative(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"type mismatch", "program p\n integer :: i\n character(len=3) :: s\n i = s\nend\n", "Cannot convert"},
		{"type mismatch 2", "program p\n integer :: i\n i = 'abc'\nend\n", "Cannot convert"},
		{"logical cond", "program p\n integer :: i\n i = 1\n if (i) i = 2\nend\n", "requires a scalar LOGICAL"},
		{"undeclared", "program p\n implicit none\n i = 1\nend\n", "has no IMPLICIT type"},
		{"rank mismatch", "program p\n real :: a(3,3)\n a(1) = 1.0\nend\n", "Rank mismatch in array reference"},
		{"rank assign", "program p\n real :: a(3), s\n s = a\nend\n", "Incompatible ranks"},
		{"intent in", "subroutine s(x)\n integer, intent(in) :: x\n x = 1\nend\n", "INTENT(IN)"},
		{"named constant", "program p\n integer, parameter :: n = 1\n n = 2\nend\n", "Named constant 'n'"},
		{"do var", "program p\n integer :: i\n do i = 1, 3\n  i = 5\n end do\nend\n", "cannot be redefined inside loop"},
		{"numeric op", "program p\n logical :: l\n integer :: i\n i = 1 + l\nend\n", "Operands of binary numeric operator"},
		{"compare", "program p\n character(len=1) :: c\n logical :: l\n l = c < 1\nend\n", "Operands of comparison operator"},
		{"duplicate type", "program p\n integer :: i\n real :: i\nend\n", "already has basic type"},
		{"bad member", "program p\n type t\n  integer :: a\n end type\n type(t) :: x\n x%b = 1\nend\n", "is not a member"},
		{"subroutine as function", "subroutine s()\nend\nprogram p\n implicit none\n integer :: i\n call q\n contains\n subroutine q()\n  i = r(1)\n end subroutine\n subroutine r(k)\n  integer :: k\n end subroutine\nend\n", "is a subroutine"},
		{"unknown type", "program p\n type(nope) :: x\nend\n", "being used before it is defined"},
		{"stop code", "program p\n stop 1.5\nend\n", "STOP code"},
		{"array cons", "program p\n integer :: a(2)\n a = [1, 2.0]\nend\n", "array constructor"},
		{"only missing", "module m\n integer :: a\nend\nprogram p\n use m, only: b\nend\n", "not found in module 'm'"},
		{"private", "module m\n private\n integer :: a\nend\nprogram p\n use m\n a = 1\nend\nprogram q\n implicit none\n use m\n a = 1\nend\n", "has no IMPLICIT type"},
	} {
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.src, c.want) })
	}
}

func TestPositive(t *testing.T) {
	for _, c := range []struct{ name, src string }{
		{"function result", "integer function f(x)\n integer :: x\n f = x + 1\nend\nprogram p\n integer :: f\n print *, f(1)\nend\n"},
		{"recursive", "recursive function fact(n) result(r)\n integer :: n, r\n if (n <= 1) then\n r = 1\n else\n r = n * fact(n - 1)\n end if\nend\n"},
		{"stmt function", "program p\n f(x) = x * 2.0\n y = f(3.0)\nend\n"},
		{"array ops", "program p\n real :: a(3), b(3)\n a = 1.0\n b = a + 2.0 * a\n b(1:2) = a(2:3)\n print *, sum(b), size(a), maxval(a)\nend\n"},
		{"module", "module m\n implicit none\n integer :: n = 3\n contains\n integer function twice(i)\n  integer :: i\n  twice = 2 * i\n end function\nend module\nprogram p\n use m\n implicit none\n integer :: j\n j = twice(n)\nend\n"},
		{"rename", "module m\n integer :: a\nend\nprogram p\n use m, only: b => a\n b = 1\nend\n"},
		{"contains", "program p\n implicit none\n integer :: k\n k = sq(3)\n contains\n integer function sq(i)\n  integer :: i\n  sq = i * i\n end function\nend\n"},
		{"derived", "program p\n implicit none\n type pt\n  real :: x, y\n end type\n type(pt) :: a\n a%x = 1.0\n a%y = a%x * 2\nend\n"},
		{"select", "program p\n implicit none\n integer :: i\n i = 2\n select case (i)\n case (1)\n  print *, 'one'\n case (2:3)\n  print *, 'two'\n case default\n  print *, 'many'\n end select\nend\n"},
		{"do concurrent", "program p\n implicit none\n integer :: a(10), i\n do concurrent (i = 1:10)\n a(i) = i\n end do\nend\n"},
		{"implied do", "program p\n implicit none\n integer :: i, a(5)\n a = [(i * i, i = 1, 5)]\n print *, (a(i), i = 1, 5)\nend\n"},
		{"io", "program p\n implicit none\n integer :: ios\n character(len=20) :: s\n write (*, '(a)', iostat=ios) 'x'\n read (s, *) ios\nend\n"},
		{"iso_fortran_env", "program p\n use iso_fortran_env, only: int64, real64, output_unit\n integer(int64) :: i\n real(real64) :: x\n i = 1_int64\n x = 1.0_real64\n write (output_unit, *) i, x\nend\n"},
		{"character", "program p\n implicit none\n character(len=10) :: s\n s = 'ab' // 'cd'\n s(1:2) = 'xy'\n print *, len_trim(s), s(3:4)\nend\n"},
		{"generic", "module m\n interface sw\n  module procedure sws\n end interface\n contains\n subroutine sws(a)\n  real :: a\n end subroutine\nend\nprogram p\n use m\n real :: x\n call sw(x)\nend\n"},
		{"block data and common", "block data\n common /c/ a, b\n data a /1.0/\nend\nprogram p\n common /c/ a, b\n print *, a\nend\n"},
		{"external", "program p\n external g\n real :: x, h\n external h\n call g(x)\n x = h(1.0)\nend\n"},
	} {
		t.Run(c.name, func(t *testing.T) { wantOK(t, c.src) })
	}
}

func TestExpressionTypes(t *testing.T) {
	info := wantOK(t, `program p
  integer :: i
  real :: r
  real(8) :: d
  complex :: z
  i = 1
  r = i + 1.0
  d = r * 2.0d0
  z = d + z
  r = i ** 2
  r = d ** 2
  r = real(z)
end
`)
	got := map[string]string{}
	for e, tv := range info.Types {
		if b, ok := e.(*syntax.Binary); ok {
			got[b.Op+tv.Type.String()] = ""
		}
	}
	for _, w := range []string{"+real(4)", "*real(8)", "+complex(8)", "**integer(4)", "**real(8)"} {
		if _, ok := got[w]; !ok {
			t.Errorf("no binary %s among %v", w, got)
		}
	}
}

func TestTestdataPrograms(t *testing.T) {
	files, _ := filepath.Glob("../testdata/programs/*.f90")
	if len(files) == 0 {
		t.Fatal("no programs")
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		_, errs := analyze(t, filepath.Base(f), string(src), nil)
		if len(errs) != 0 {
			t.Errorf("%s: %v", f, errs)
		}
	}
}

func TestBounds(t *testing.T) {
	// symbol bound
	var b strings.Builder
	b.WriteString("program p\n")
	for i := 0; i < 50; i++ {
		b.WriteString(" integer :: v" + strings.Repeat("a", 1) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "\n")
	}
	b.WriteString("end\n")
	_, errs := analyze(t, "t.f90", b.String(), &Options{Limits: Limits{MaxSymbols: 10}})
	if len(errs) == 0 || !strings.Contains(errs.Error(), "too many symbols") {
		t.Errorf("symbol bound: %v", errs)
	}
	// character constant growth: each step doubles
	b.Reset()
	b.WriteString("program p\n character(len=*), parameter :: s0 = 'abcdefgh'\n")
	for i := 1; i <= 30; i++ {
		b.WriteString(" character(len=*), parameter :: s" + itoa(i) + " = s" + itoa(i-1) + " // s" + itoa(i-1) + "\n")
	}
	b.WriteString("end\n")
	_, errs = analyze(t, "t.f90", b.String(), &Options{Limits: Limits{MaxConstChars: 1 << 12}})
	if len(errs) == 0 || !strings.Contains(errs.Error(), "longer than the analyser's bound") {
		t.Errorf("constant bound: %v", errs)
	}
	// error bound
	b.Reset()
	b.WriteString("program p\n implicit none\n")
	for i := 0; i < 500; i++ {
		b.WriteString(" x" + itoa(i) + " = 1\n")
	}
	b.WriteString("end\n")
	_, errs = analyze(t, "t.f90", b.String(), nil)
	if len(errs) > DefaultLimits().MaxErrors+1 {
		t.Errorf("%d errors reported", len(errs))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var d []byte
	for ; i > 0; i /= 10 {
		d = append([]byte{byte('0' + i%10)}, d...)
	}
	return string(d)
}
