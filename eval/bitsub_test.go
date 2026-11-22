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

// TestMvbitsRefusals pins what mvbits refuses at compile time.
func TestMvbitsRefusals(t *testing.T) {
	for _, tc := range []struct{ src, msg string }{
		{"integer :: a(2)\na = 0\ncall mvbits(1, 0, 1, a, 0)\nend\n", "must be a scalar integer"},
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
