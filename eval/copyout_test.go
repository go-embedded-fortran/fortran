package eval

import "testing"

// TestCopyOutToComponentsAndElements pins that a dummy argument that can be
// defined gives its final value back to an actual that is a scalar component
// or an array element (intent_out_13 of gfortran.dg): only a plain variable
// got it back. The element is the one of the moment the call starts — bump
// changes i, and a(1), not a(2), gets the value. The expected line is
// gfortran 15.2.0's output for the source as a file.
func TestCopyOutToComponentsAndElements(t *testing.T) {
	const src = `module m
type t
integer :: b = -1
real :: r = 0.5
character(len=4) :: s = 'abcd'
end type
integer :: i = 1
contains
subroutine seti(x)
integer, intent(out) :: x
x = 5
end subroutine
subroutine setr(x)
real, intent(inout) :: x
x = x * 4
end subroutine
subroutine sets(x)
character(len=*), intent(out) :: x
x = 'xy'
end subroutine
subroutine bump(x)
integer :: x
i = i + 1
x = x + 10
end subroutine
subroutine setl(x)
logical, intent(out) :: x
x = .true.
end subroutine
end module
use m
type(t) :: v
integer :: a(3) = (/ 1, 2, 3 /)
logical :: l(2) = .false.
call seti(v%b)
call setr(v%r)
call sets(v%s)
call bump(a(i))
call setl(l(2))
print *, v%b, v%r, v%s, '|'
print *, a, i, l
end
`
	const want = "5 2.00000000 xy | 11 2 3 2 F T"
	if out, err := runPtr(t, 100<<20, src); err != nil || out != want {
		t.Errorf("copy-out to components and elements: %q, %v; want %q", out, err, want)
	}
}

// TestNoCopyOutToAValue pins that (a(1)) is a value: touch leaves its dummy
// alone and sets a(1) through the module, and a copy-out of the dummy would
// put the old 1 back. It also runs the copy-out to elements of real arrays of
// both kinds: real(4) (the general arrays) and real(8) of rank 1 and 2 (the
// kernels' arrays, read by element). gfortran 15.2.0 prints the expected line.
func TestNoCopyOutToAValue(t *testing.T) {
	const src = `module m
integer :: a(2) = (/ 1, 2 /)
contains
subroutine touch(x)
integer :: x
a(1) = 99
end subroutine
subroutine twice(y)
real :: y
y = y * 2
end subroutine
subroutine twice8(z)
real(8), intent(inout) :: z
z = z * 2
end subroutine
end module
use m
real :: r(3)
real(8) :: d(2), e(2, 2)
r = 1.5
d = 0.25d0
e = 1d0
call touch((a(1)))
call twice(r(2))
call twice(r(2))
call twice8(d(2))
call twice8(e(1, 2))
print *, a, r
print *, d, e
end
`
	const want = "99 2 1.50000000 6.00000000 1.50000000 0.25000000000000000 0.50000000000000000 1.0000000000000000 1.0000000000000000 2.0000000000000000 1.0000000000000000"
	if out, err := runPtr(t, 100<<20, src); err != nil || out != want {
		t.Errorf("no copy-out to a value: %q, %v; want %q", out, err, want)
	}
}

// TestParenthesisedDerivedActualIsACopy pins that (e) passed to a dummy is a
// value of its own (interface_assignment_1 of gfortran.dg): it was e itself,
// so the intent(out) of the other dummy, associated with e, reset it to its
// default 2 and x1%i became 3. gfortran 15.2.0 prints 5.
func TestParenthesisedDerivedActualIsACopy(t *testing.T) {
	const src = `module tt
type data_type
integer :: i = 2
end type
contains
subroutine set(x1, x2)
type(data_type), intent(in) :: x2
type(data_type), intent(out) :: x1
x1%i = x2%i + 1
end subroutine
end module
use tt
type(data_type) :: e
e%i = 4
call set(e, (e))
print *, e%i
end
`
	if out, err := runPtr(t, 100<<20, src); err != nil || out != "5" {
		t.Errorf("parenthesised derived-type actual: %q, %v; want \"5\"", out, err)
	}
}
