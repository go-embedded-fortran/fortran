package sema

import (
	"strings"
	"testing"
)

// callNegatives are programs that F2018 15.4 - 15.5 refuse; the same list is
// cross-checked against gfortran in oracle_test.go (callNegatives is exported to
// the external test package through export_test.go). want is the part of the
// diagnostic the analyser must give.
var callNegatives = []struct{ name, src, want string }{
	{"ambiguous generic", `module m
 interface g
  module procedure a, b
 end interface
contains
 subroutine a(x)
  integer :: x
 end subroutine
 subroutine b(y)
  integer :: y
 end subroutine
end module
`, "Ambiguous interfaces"},
	{"ambiguous generic, kind", `module m
 interface g
  module procedure a, b
 end interface
contains
 subroutine a(x, y)
  real :: x
  integer :: y
 end subroutine
 subroutine b(x, y)
  real :: x
  integer :: y
 end subroutine
end module
`, "Ambiguous interfaces"},
	{"ambiguous generic, optional", `module m
 interface g
  module procedure a, b
 end interface
contains
 subroutine a(x)
  integer :: x
 end subroutine
 subroutine b(x, y)
  integer :: x
  real, optional :: y
 end subroutine
end module
`, "Ambiguous interfaces"},
	{"ambiguous operator", `module m
 interface operator(+)
  module procedure a, b
 end interface
 type t
  integer :: i
 end type
contains
 function a(x, y)
  type(t), intent(in) :: x, y
  type(t) :: a
  a%i = 0
 end function
 function b(p, q)
  type(t), intent(in) :: p, q
  type(t) :: b
  b%i = 1
 end function
end module
`, "Ambiguous interfaces"},
	{"no specific subroutine", `module m
 interface g
  module procedure a
 end interface
contains
 subroutine a(x)
  integer :: x
 end subroutine
end module
program p
 use m
 call g(1.0)
end
`, "no specific subroutine"},
	{"no specific function", `module m
 interface g
  module procedure a
 end interface
contains
 function a(x)
  integer :: x, a
  a = x
 end function
end module
program p
 use m
 real :: r
 r = g(1.0)
end
`, "no specific function"},
	{"argument type", `module m
contains
 subroutine a(x)
  integer :: x
 end subroutine
end module
program p
 use m
 call a(1.0)
end
`, "Type mismatch in argument"},
	{"argument kind", `module m
contains
 subroutine a(x)
  real(8) :: x
 end subroutine
end module
program p
 use m
 real :: r
 call a(r)
end
`, "Type mismatch in argument"},
	{"scalar to assumed-shape", `module m
contains
 subroutine a(x)
  real :: x(:)
 end subroutine
end module
program p
 use m
 real :: r
 call a(r)
end
`, "Rank mismatch in argument"},
	{"array to scalar", `module m
contains
 subroutine a(x)
  real :: x
 end subroutine
end module
program p
 use m
 real :: r(3)
 call a(r)
end
`, "Rank mismatch in argument"},
	{"rank to assumed-shape", `module m
contains
 subroutine a(x)
  real :: x(:)
 end subroutine
end module
program p
 use m
 real :: r(3, 3)
 call a(r)
end
`, "Rank mismatch in argument"},
	{"too many arguments", `module m
contains
 subroutine a(x)
  real :: x
 end subroutine
end module
program p
 use m
 call a(1.0, 2.0)
end
`, "More actual than formal"},
	{"missing argument", `module m
contains
 subroutine a(x, y)
  real :: x, y
 end subroutine
end module
program p
 use m
 call a(1.0)
end
`, "Missing actual argument"},
	{"unknown keyword", `module m
contains
 subroutine a(x)
  real :: x
 end subroutine
end module
program p
 use m
 call a(z=1.0)
end
`, "not in the procedure"},
	{"keyword twice", `module m
contains
 subroutine a(x, y)
  real :: x, y
 end subroutine
end module
program p
 use m
 call a(1.0, x=2.0)
end
`, "already associated"},
	{"intent out constant", `module m
contains
 subroutine a(x)
  real, intent(out) :: x
  x = 1.0
 end subroutine
end module
program p
 use m
 call a(1.0)
end
`, "variable definition context"},
	{"intent inout expression", `module m
contains
 subroutine a(x)
  real, intent(inout) :: x
  x = 1.0
 end subroutine
end module
program p
 use m
 real :: r
 call a(r + 1.0)
end
`, "variable definition context"},
	{"intent out through intent in", `module m
contains
 subroutine a(x)
  real, intent(out) :: x
  x = 1.0
 end subroutine
 subroutine b(y)
  real, intent(in) :: y
  call a(y)
 end subroutine
end module
`, "INTENT(IN)"},
	{"function called", `module m
contains
 function f(x)
  real :: x, f
  f = x
 end function
end module
program p
 use m
 call f(1.0)
end
`, "is a function"},
	{"subroutine in expression", `module m
contains
 subroutine s(x)
  real :: x
 end subroutine
end module
program p
 use m
 real :: r
 r = s(1.0)
end
`, "subroutine"},
	{"explicit interface: assumed-shape", `subroutine s(x)
 real :: x(:)
end subroutine
program p
 real :: a(3)
 call s(a)
end
`, "Explicit interface required"},
	{"explicit interface: optional", `subroutine s(x)
 real, optional :: x
end subroutine
program p
 real :: a
 call s(a)
end
`, "Explicit interface required"},
	{"explicit interface: array result", `function f(x)
 real :: x, f(3)
 f = x
end function
program p
 real :: f(3)
 real :: a
 external f
 a = 1.0
 f = f(a)
end
`, "Explicit interface required"},
	{"explicit interface: keyword", `program p
 external s
 call s(x=1.0)
end
`, "explicit interface"},
	{"too few elements", `module m
contains
 subroutine a(x)
  real :: x(10)
 end subroutine
end module
program p
 use m
 real :: r(3)
 call a(r)
end
`, "too few elements"},
	{"pointer actual", `module m
contains
 subroutine a(x)
  real, pointer :: x(:)
 end subroutine
end module
program p
 use m
 real :: r(3)
 call a(r)
end
`, "must be a pointer"},
	{"allocatable actual", `module m
contains
 subroutine a(x)
  real, allocatable :: x(:)
 end subroutine
end module
program p
 use m
 real :: r(3)
 call a(r)
end
`, "ALLOCATABLE"},
	{"derived type", `module m
 type t
  integer :: i
 end type
 type u
  integer :: i
 end type
contains
 subroutine a(x)
  type(t) :: x
 end subroutine
end module
program p
 use m
 type(u) :: v
 call a(v)
end
`, "Type mismatch in argument"},
	{"defined operator, no match", `module m
 type t
  integer :: i
 end type
 interface operator(+)
  module procedure add
 end interface
contains
 function add(a, b)
  type(t), intent(in) :: a, b
  type(t) :: add
  add%i = a%i + b%i
 end function
end module
program p
 use m
 type(t) :: x
 x = x + 1
end
`, "Operands of"},
	{"no defined operator", `program p
 type t
  integer :: i
 end type
 type(t) :: x, y
 y = x * x
end
`, "Operands of"},
	{"defined assignment, no match", `module m
 type t
  integer :: i
 end type
 interface assignment(=)
  module procedure assign
 end interface
contains
 subroutine assign(a, b)
  type(t), intent(out) :: a
  integer, intent(in) :: b
  a%i = b
 end subroutine
end module
program p
 use m
 type(t) :: x
 x = 1.0
end
`, "Cannot convert"},
	{"elemental ranks", `module m
contains
 elemental function f(a, b)
  real, intent(in) :: a, b
  real :: f
  f = a + b
 end function
end module
program p
 use m
 real :: x(3), y(4, 4), z(3)
 z = f(x, y)
end
`, "not conformable"},
}

// callPositives are programs both gfortran and the analyser accept.
var callPositives = []struct{ name, src string }{
	{"generic by type", `module m
 interface g
  module procedure a, b
 end interface
contains
 subroutine a(x)
  integer :: x
 end subroutine
 subroutine b(y)
  real :: y
 end subroutine
end module
program p
 use m
 call g(1)
 call g(1.0)
end
`},
	{"generic by rank", `module m
 interface g
  module procedure a, b
 end interface
contains
 function a(x)
  real :: x, a
  a = x
 end function
 function b(x)
  real :: x(:), b(size(x))
  b = x
 end function
end module
program p
 use m
 real :: s, v(3)
 s = g(s)
 v = g(v)
end
`},
	{"generic by keyword", `module m
 interface g
  module procedure a, b
 end interface
contains
 subroutine a(x, y)
  integer :: x
  real :: y
 end subroutine
 subroutine b(x, z)
  integer :: x
  character :: z
 end subroutine
end module
program p
 use m
 call g(1, y=2.0)
 call g(1, z='a')
end
`},
	{"generic with the specific's name", `module m
 interface one
  module procedure one, two
 end interface
contains
 function one()
  real :: one
  one = 0.0
 end function
 function two(x)
  real :: x, two
  two = x
 end function
end module
program p
 use m
 real :: r
 r = one()
 r = one(2.0)
end
`},
	{"generics of two modules", `module m1
 interface g
  module procedure a
 end interface
contains
 subroutine a(x)
  integer :: x
 end subroutine
end module
module m2
 interface g
  module procedure b
 end interface
contains
 subroutine b(x)
  real :: x
 end subroutine
end module
program p
 use m1
 use m2
 call g(1)
 call g(1.0)
end
`},
	{"keywords and optional", `module m
contains
 subroutine a(x, y, z)
  real :: x
  real, optional :: y, z
 end subroutine
end module
program p
 use m
 call a(1.0)
 call a(1.0, z=2.0)
 call a(z=2.0, x=1.0)
end
`},
	{"assumed-shape, explicit-shape, assumed-size", `module m
contains
 subroutine a(x, y, z)
  real :: x(:), y(3), z(*)
 end subroutine
end module
program p
 use m
 real :: r(3), q(3, 3)
 call a(r, r, r)
 call a(q(:, 1), q, q(1, 1))
end
`},
	{"pointer dummy takes a target when intent in", `program p
 real, target :: x
 call s(x)
contains
 subroutine s(q)
  real, pointer, intent(in) :: q
 end subroutine
end
`},
	{"elemental", `module m
contains
 elemental function f(a, b)
  real, intent(in) :: a, b
  real :: f
  f = a + b
 end function
end module
program p
 use m
 real :: x(3), z(3)
 z = f(x, 2.0)
 z = f(1.0, 2.0)
end
`},
	{"defined operator and assignment", `module m
 type t
  integer :: i
 end type
 interface operator(+)
  module procedure add
 end interface
 interface assignment(=)
  module procedure assign
 end interface
contains
 function add(a, b)
  type(t), intent(in) :: a, b
  type(t) :: add
  add%i = a%i + b%i
 end function
 subroutine assign(a, b)
  type(t), intent(out) :: a
  integer, intent(in) :: b
  a%i = b
 end subroutine
end module
program p
 use m, only: t, operator(+), assignment(=)
 type(t) :: x, y
 x = 1
 y = x + x
end
`},
	{"intrinsic stays accessible through its generic name", `module m
 interface sin
  module procedure s
 end interface
contains
 function s(x)
  character :: x, s
  s = x
 end function
end module
program p
 use m
 real :: r
 r = sin(1.0)
end
`},
	{"external called with its own interface", `subroutine s(x)
 real :: x
end subroutine
program p
 real :: a
 call s(a)
end
`},
	{"intent out actual is a variable", `module m
contains
 subroutine a(x)
  real, intent(out) :: x
  x = 1.0
 end subroutine
end module
program p
 use m
 real :: r(3)
 call a(r(2))
end
`},
}

func TestCallNegatives(t *testing.T) {
	for _, c := range callNegatives {
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.src, c.want) })
	}
}

func TestCallPositives(t *testing.T) {
	for _, c := range callPositives {
		t.Run(c.name, func(t *testing.T) { wantOK(t, c.src) })
	}
}

// TestCallResolvesTheSpecific: the typed tree names the specific procedure a
// generic reference invokes.
func TestCallResolvesTheSpecific(t *testing.T) {
	info := wantOK(t, callPositives[0].src)
	n := 0
	for call, sym := range info.Calls {
		_ = call
		if sym.Kind != SymProc || sym.Unit == nil {
			t.Errorf("call resolved to %v", sym)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("%d resolved calls, want 2", n)
	}
	got := map[string]bool{}
	for _, sym := range info.Calls {
		got[sym.Name] = true
	}
	if !got["a"] || !got["b"] {
		t.Errorf("specifics %v, want a and b", got)
	}
}

// Bounds are refusals: a generic with more specifics than the bound, a
// reference with more actual arguments than the bound.
func TestBombSpecifics(t *testing.T) {
	var b strings.Builder
	b.WriteString("module m\n interface g\n  module procedure ")
	const n = 600
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("s" + itoa(i))
	}
	b.WriteString("\n end interface\ncontains\n")
	for i := 0; i < n; i++ {
		b.WriteString(" subroutine s" + itoa(i) + "(x)\n  real(" + "4" + ") :: x(" + itoa(i+1) + ")\n end subroutine\n")
	}
	b.WriteString("end module\n")
	_, errs := analyze(t, "t.f90", b.String(), &Options{Limits: Limits{MaxSpecifics: 100}})
	if len(errs) == 0 || !strings.Contains(errs.Error(), "specific procedures: refused") {
		t.Fatalf("no refusal: %v", errs)
	}
}

func TestBombActuals(t *testing.T) {
	var b strings.Builder
	b.WriteString("subroutine s()\nend\nprogram p\n call s(")
	for i := 0; i < 300; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("1")
	}
	b.WriteString(")\nend\n")
	_, errs := analyze(t, "t.f90", b.String(), &Options{Limits: Limits{MaxActuals: 100}})
	if len(errs) == 0 || !strings.Contains(errs.Error(), "actual arguments: refused") {
		t.Fatalf("no refusal: %v", errs)
	}
}

// TestBombShapeOverflow: extents whose products overflow int64 are not known
// extents (shape.go maxExtent); the analyser does not wrap, panic or refuse a
// valid program for them.
func TestBombShapeOverflow(t *testing.T) {
	src := `program p
 implicit none
 integer :: i, j, k, l
 integer, parameter :: big = 2000000000
 integer :: a(big, big, big)
 integer :: s(3)
 s = [(((i, i = 1, big), j = 1, big), k = 1, big)]
 s = a(1:big:3, 1, 1)
 s = a(-9000000000000000000:9000000000000000000, 1, 1)
end
`
	_, errs := analyze(t, "t.f90", src, nil)
	for _, e := range errs {
		if e.Msg == "" || strings.Contains(e.Msg, "internal error") {
			t.Fatalf("bad diagnostic %v", e)
		}
	}
}
