package eval

import (
	"strings"
	"testing"
)

// TestOptionalArguments pins optional dummy arguments and PRESENT (F2018
// 15.5.2.12, 16.9.152): absent and present, positional and keyword, an absent
// one passed on to another optional dummy, PRESENT of a host's dummy, copy-out
// of a present one. The expected line is gfortran 15.2.0's output.
func TestOptionalArguments(t *testing.T) {
	const src = `module mo
implicit none
contains
subroutine outer(m)
integer, optional :: m
call inner()
call pass(m)
contains
subroutine inner()
print *, 'inner', present(m)
end subroutine
end subroutine
subroutine pass(q)
integer, optional :: q
print *, 'pass', present(q)
end subroutine
end module
program p
use mo
implicit none
integer :: k
real :: r
character(5) :: s
k = 0
r = 0.
s = 'abc'
call a(1)
call a(1, 2)
call a(1, c=3.5)
call b()
call b(k)
print *, k
print *, f(), f(4), g(), g(.true.)
call c(s)
call c()
call outer()
call outer(7)
call setr(r)
print *, r
contains
subroutine a(x, y, c)
integer, intent(in) :: x
integer, intent(in), optional :: y
real, intent(in), optional :: c
if (present(y)) then
print *, x, y, present(c)
else if (present(c)) then
print *, x, c
else
print *, x, 'none'
end if
end subroutine
subroutine b(z)
integer, optional :: z
if (present(z)) z = z + 5
print *, present(z)
end subroutine
integer function f(n)
integer, optional :: n
f = -1
if (present(n)) f = n * 2
end function
logical function g(l)
logical, intent(in), optional :: l
g = .false.
if (present(l)) g = l
end function
subroutine c(t)
character(*), optional :: t
if (present(t)) then
print *, len(t), t
else
print *, 'no t'
end if
end subroutine
subroutine setr(v)
real, optional, intent(out) :: v
if (present(v)) v = 2.5
end subroutine
end program
`
	const want = "1 none 1 2 F 1 3.50000000 F T 5 -1 8 F T 5 abc no t inner F pass F inner T pass T 2.50000000"
	if out, err := runPtr(t, 100<<20, src); err != nil || out != want {
		t.Errorf("optional arguments:\n got %q, %v\nwant %q", out, err, want)
	}
}

// TestOptionalPointerActual pins that a disassociated pointer passed to an
// optional nonpointer dummy makes it absent (F2018 15.5.2.12 p1), and an
// associated one is copied in and out; gfortran 15.2.0's output.
func TestOptionalPointerActual(t *testing.T) {
	const src = "program p\nimplicit none\ninteger, pointer :: ptr => null()\ninteger, target :: t\nt = 4\ncall foo(ptr)\nptr => t\ncall foo(ptr)\nprint *, t\nnullify(ptr)\ncall foo(ptr)\nprint *, t\ncontains\nsubroutine foo(x)\ninteger, optional :: x\nprint *, present(x)\nif (present(x)) x = x + 1\nend subroutine\nend program\n"
	if out, err := runPtr(t, 100<<20, src); err != nil || out != "F T 5 F 5" {
		t.Errorf("pointer actual of an optional dummy: %q, %v", out, err)
	}
}

// TestOptionalRefusals pins what is refused with OPTIONAL, each by its cause.
func TestOptionalRefusals(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		// an optional array dummy
		{"program p\ncall s()\ncontains\nsubroutine s(a)\ninteger, optional :: a(3)\nprint *, present(a)\nend subroutine\nend program\n", "arrays that are not optional"},
		// an optional derived-type dummy
		{"program p\ntype t\ninteger :: i\nend type\ncall s()\ncontains\nsubroutine s(a)\ntype(t), optional :: a\nprint *, present(a)\nend subroutine\nend program\n", "OPTIONAL is supported on scalar"},
		// PRESENT of what is not an optional dummy, or not a name, or of two
		// arguments (the analyser lets these through)
		{"program p\ncall s(1)\ncontains\nsubroutine s(a)\ninteger :: a\nprint *, present(a)\nend subroutine\nend program\n", "a is not an optional dummy argument"},
		{"program p\ncall s()\ncontains\nsubroutine s(a)\ninteger, optional :: a\nprint *, present(a+1)\nend subroutine\nend program\n", "must be an optional dummy argument"},
		{"program p\ncall s()\ncontains\nsubroutine s(a)\ninteger, optional :: a\nprint *, present(a, a)\nend subroutine\nend program\n", "present takes one argument"},
		// an optional dummy passed on to an optional argument of an intrinsic,
		// by position or keyword: absent, size took dim as 0
		{"program p\ninteger :: a(2,3)\ncall s(a)\ncontains\nsubroutine s(x, d)\ninteger, intent(in) :: x(:,:)\ninteger, optional :: d\nprint *, size(x, d)\nend subroutine\nend program\n", "optional argument of size"},
		{"program p\ninteger :: a(2,3)\ncall s(a)\ncontains\nsubroutine s(x, d)\ninteger, intent(in) :: x(:,:)\ninteger, optional :: d\nprint *, size(x, dim=d)\nend subroutine\nend program\n", "optional argument of size"},
		{"program p\ncall s(1, 2)\ncontains\nsubroutine s(x, y, z)\ninteger :: x, y\ninteger, optional :: z\nprint *, max(x, y, z)\nend subroutine\nend program\n", "optional argument of max"},
	} {
		if _, err := runPtr(t, 100<<20, tc.src); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: want a refusal naming %q, got %v", tc.src, tc.want, err)
		}
	}
	// a required argument of an intrinsic may be an optional dummy: it is
	// present when it is referenced (gfortran 15.2.0 prints 3 7)
	const required = "program p\ncall s(-3, 7)\ncontains\nsubroutine s(x, y)\ninteger, optional :: x, y\nif (present(x)) print *, abs(x), max(x, y)\nend subroutine\nend program\n"
	if out, err := runPtr(t, 100<<20, required); err != nil || out != "3 7" {
		t.Errorf("optional dummy at a required intrinsic argument: %q, %v", out, err)
	}
}
