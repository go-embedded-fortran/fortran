package eval

import (
	"strings"
	"testing"
)

// TestMvbits pins CALL MVBITS for scalar integers of the four kinds (F2018
// 16.9.137): positional and keyword arguments, an element as TO (its place is
// the one of the k of the call), all 64 bits, and FROM being TO itself. The
// expected line is gfortran 15.2.0's output for the same source.
func TestMvbits(t *testing.T) {
	const src = `integer(1) :: i1
integer(2) :: i2
integer :: i4, j, a(3), k
integer(8) :: i8
i1 = -1
call mvbits(1_1, 0, 3, i1, 0)
i2 = 0
call mvbits(-1_2, 4, 5, i2, 11)
j = 255
call mvbits(from=10, frompos=1, len=3, to=j, topos=20)
i8 = 0
call mvbits(-1_8, 0, 64, i8, 0)
a = 0
k = 2
call mvbits(7, 0, 3, a(k), 2)
i4 = 6
call mvbits(i4, 0, 2, i4, 1)
k = 0
call mvbits(k+5, 0, 3, k, 0)
print *, i1, i2, j, i4, i8, a, k
end
`
	const want = "-7 -2048 5243135 4 -1 0 28 0 5"
	if out, err := runPtr(t, 100<<20, src); err != nil || out != want {
		t.Errorf("mvbits: %q, %v; want %q", out, err, want)
	}
}

// TestMvbitsLocal pins MVBITS into a variable of the procedure's own frame
// (the cell's variables above are the cell module's), and LEN 0 at the last
// position, which keeps TO. gfortran 15.2.0 prints 49.
func TestMvbitsLocal(t *testing.T) {
	const src = `module m
contains
subroutine loc(r)
integer, intent(out) :: r
integer :: t
t = 1
call mvbits(3, 0, 2, t, 4)
call mvbits(0, 0, 0, t, 32)
r = t
end subroutine
end module
use m
integer :: k
call loc(k)
print *, k
end
`
	if out, err := runPtr(t, 100<<20, src); err != nil || out != "49" {
		t.Errorf("mvbits into a local variable: %q, %v; want \"49\"", out, err)
	}
}

// TestMvbitsOutOfRange pins that bits outside TO stop the program instead of
// being cut silently: 30+3 > 32.
func TestMvbitsOutOfRange(t *testing.T) {
	const src = "integer :: j\nj = 0\ncall mvbits(7, 0, 3, j, 30)\nprint *, j\nend\n"
	if _, err := runPtr(t, 100<<20, src); err == nil || !strings.Contains(err.Error(), "do not fit in 32 bits") {
		t.Errorf("mvbits out of range: %v; want a failure naming 32 bits", err)
	}
}

// TestMvbitsElemental pins the elemental form: arrays, sections (with a
// stride, reversed, of size zero), rank 2, FROM being TO, scalars applied to
// every element, an array TOPOS and an integer(2) TO. The expected line is
// gfortran 15.2.0's output for the same source.
func TestMvbitsElemental(t *testing.T) {
	const src = `integer :: a(4), b(4), m(2,2), z(3)
integer(2) :: h(3)
integer :: k
a = [1, 2, 3, 4]
b = 0
call mvbits(a, 0, 2, b, 1)
print *, b
call mvbits(5, 0, 3, b(1:4:2), 8)
print *, b
call mvbits(a, 0, 1, a, 4)
print *, a
m = reshape([1, 2, 3, 4], [2, 2])
call mvbits(m, 0, 3, m, 4)
print *, m
z = 0
call mvbits(1, 0, 1, z, [0, 1, 2])
print *, z
h = -1
call mvbits([0_2, 1_2, 3_2], 0, 2, h, 14)
print *, h
z = 7
k = 3
call mvbits(z(k:1), 0, 1, z(k:1), 2)
print *, z
call mvbits(a(4:1:-1), 0, 4, b, 16)
print *, b
end
`
	const want = "2 4 6 0 1282 4 1286 0 17 2 19 4 17 34 51 68 1 2 4 16383 32767 -1 7 7 7 263426 196612 132358 65536"
	if out, err := runPtr(t, 100<<20, src); err != nil || out != want {
		t.Errorf("elemental mvbits: %q, %v; want %q", out, err, want)
	}
}

// TestMvbitsNotConformable pins that arrays of different sizes stop the
// program (their sizes are known at run time only).
func TestMvbitsNotConformable(t *testing.T) {
	const src = "integer :: a(4), k\na = 1\nk = 3\ncall mvbits(a(1:k), 0, 1, a, 0)\nend\n"
	if _, err := runPtr(t, 100<<20, src); err == nil || !strings.Contains(err.Error(), "not conformable") {
		t.Errorf("mvbits on arrays of sizes 3 and 4: %v; want a failure naming them not conformable", err)
	}
}

// TestMvbitsRefusals pins what mvbits refuses at compile time.
func TestMvbitsRefusals(t *testing.T) {
	for _, tc := range []struct{ src, msg string }{
		{"real :: x(2)\ninteger :: a(2)\nx = 0\ncall mvbits(x, 0, 1, a, 0)\nend\n", "argument from must be an integer"},
		{"integer :: a(2), j\na = 0\ncall mvbits(a, 0, 1, j, 0)\nend\n", "to must be an array when another argument is"},
		{"integer :: a(2)\na = 0\ncall mvbits(1, 0, 1, (a), 0)\nend\n", "to must be a variable"},
		{"integer, target :: t\ninteger, pointer :: p\nt = 0\np => t\ncall mvbits(1, 0, 1, p, 0)\nprint *, t\nend\n", "a pointer is not supported yet"},
		{"integer, parameter :: q(2) = [1, 2]\ncall mvbits(1, 0, 1, q, 0)\nend\n", "q is a named constant"},
		{"integer, parameter :: q(2) = [1, 2]\ncall mvbits(1, 0, 1, q(1), 0)\nend\n", "q is a named constant"},
		{"integer, parameter :: q(2) = [1, 2]\ncall mvbits(1, 0, 1, q(1:2), 0)\nend\n", "q is a named constant"},
		{"integer :: j\ncall mvbits(1, 0, 1, j)\nend\n", "argument topos is missing"},
		{"integer :: j\ncall mvbits(1, 0, 1, j, 0, 2)\nend\n", "takes the arguments"},
		{"integer :: j\ncall mvbits(1, 0, 1, to=j, 0)\nend\n", "takes the arguments"},
		{"integer :: j\ncall mvbits(1, 0, 1, j, 0, to=j)\nend\n", "takes the arguments"},
		{"integer :: j\ncall mvbits(1, 0, 1, j+1, 0)\nend\n", "to must be a variable"},
		{"integer :: j\nj = 0\ncall mvbits(1, 0, 1, (j), 0)\nprint *, j\nend\n", "to must be a variable"},
		{"integer, parameter :: p = 0\ncall mvbits(1, 0, 1, p, 0)\nend\n", "to must be a variable"},
	} {
		if _, err := runPtr(t, 100<<20, tc.src); err == nil || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%q: %v; want a refusal containing %q", tc.src, err, tc.msg)
		}
	}
}

// TestMvbitsArgumentRefused pins that an argument eval cannot compile gives
// its own refusal. It is a test of its own, after TestMvbitsRefusals: without
// the check, this one crashes, and the assertions of the other must be
// printed before it does.
func TestMvbitsArgumentRefused(t *testing.T) {
	const src = "integer :: j\ncall mvbits(len(['ab', 'cd']), 0, 1, j, 0)\nend\n"
	if _, err := runPtr(t, 100<<20, src); err == nil || !strings.Contains(err.Error(), "type character(*,1) is not supported") {
		t.Errorf("mvbits with an argument eval refuses: %v", err)
	}
}
