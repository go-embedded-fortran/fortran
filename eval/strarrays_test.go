package eval

import (
	"context"
	"io"
	"strings"
	"testing"
)

// runExact runs src and returns its output as printed, blanks included: the
// padding of character elements is what these tests are about.
func runExact(t *testing.T, src string) (string, error) {
	t.Helper()
	var out strings.Builder
	_, err := NewSession(SessionOptions{MaxAlloc: 100 << 20}).Run(context.Background(), src, &out, io.Discard)
	return out.String(), err
}

// TestCharacterArrays pins character arrays of a constant length: a module
// array with an initialisation read from a procedure, whole, element and
// section assignments (cut and padded to the length), an overlapping section
// assignment, rank 2 through RESHAPE, constructors with and without a
// type-spec and with an implied DO, a named constant, comparisons of
// elements, and a reversed section. The expected text is gfortran 15.2.0's
// output for the same source, byte for byte.
func TestCharacterArrays(t *testing.T) {
	const src = `module m
  character(len=3) :: names(3) = ['abc', 'de ', 'f  ']
contains
  subroutine show()
    integer :: i
    do i = 1, 3
      print *, '[', trim(names(i)), ']', len(names(i))
    end do
  end subroutine
end module
use m
character(len=4) :: a(4), b(2, 2)
character(len=2), parameter :: p(2) = ['xy', 'zw']
integer :: i
call show()
a = 'q'
print *, a
a(2) = 'longer'
a(3:4) = ['12', '34']
print *, a
a(2:4) = a(1:3)
print *, a
b = reshape([character(len=4) :: 'a', 'bb', 'ccc', 'dddd'], [2, 2])
print *, b(2, 1), b(1, 2), b(:, 2)
print *, [character(len=1) :: 'xyz', 'w'], p
names(2:3) = [(p(i), i = 2, 1, -1)]
call show()
if (a(1) == 'q   ' .and. a(2) /= 'x') print *, 'ok'
print *, a(4:1:-2), len(a(1)), size([p, p]), len(a), len(['xyz', 'abc'])
end
`
	// the trailing blanks are the padding of the last element: written out, so
	// that no editor trims them
	const want = " [abc]           3\n" +
		" [de]           3\n" +
		" [f]           3\n" +
		" q   q   q   q   \n" +
		" q   long12  34  \n" +
		" q   q   long12  \n" +
		" bb  ccc ccc dddd\n" +
		" xwxyzw\n" +
		" [abc]           3\n" +
		" [zw]           3\n" +
		" [xy]           3\n" +
		" ok\n" +
		" 12  q              4           4           4           3\n"
	if out, err := runExact(t, src); err != nil || out != want {
		t.Errorf("character arrays:\n%s%v\nwant\n%s", out, err, want)
	}
}

// TestCharacterCopyOut pins the copy-out of character actual arguments: an
// element of a character array gets the dummy's value back, at the element
// its subscript designated when the call started, and a dummy shorter than its
// actual (an element or a scalar variable) writes back its own leftmost
// characters only, the others keeping theirs (F2018 15.5.2.11). The expected
// text is gfortran 15.2.0's output for the same sources.
func TestCharacterCopyOut(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`character(len=3) :: a(2)
integer :: k
a = ['ab ', 'cd ']
k = 2
call sub(a(k), k)
call sub2(a(1))
print *, a, k
contains
subroutine sub(x, j)
  character(len=*), intent(out) :: x
  integer :: j
  j = 1
  x = 'qqqqq'
end subroutine
subroutine sub2(x)
  character(len=2), intent(inout) :: x
  x = x(2:2) // 'Z'
end subroutine
end
`, " bZ qqq           1\n"},
		{`character(len=3) :: a(2), s
a = ['abc', 'def']
s = 'ghi'
call sub2(a(1))
call sub2(s)
print *, a, s
contains
subroutine sub2(x)
  character(len=2), intent(inout) :: x
  x = x(2:2) // 'Z'
end subroutine
end
`, " bZcdefhZi\n"},
	} {
		if out, err := runExact(t, tc.src); err != nil || out != tc.want {
			t.Errorf("%s: %q, %v; want %q", tc.src, out, err, tc.want)
		}
	}
}

// TestCharacterArrayRefusals pins what character arrays refuse yet, at
// compile time and at run time.
func TestCharacterArrayRefusals(t *testing.T) {
	for _, tc := range []struct{ src, msg string }{
		{"call s(['ab'])\ncontains\nsubroutine s(x)\ncharacter(len=*) :: x(:)\nprint *, x\nend subroutine\nend\n", "a character array dummy argument is not supported yet"},
		{"character(len=2), allocatable :: a(:)\nend\n", "an allocatable or pointer character array is not supported yet"},
		{"character(len=:), allocatable :: a(:)\nend\n", "an allocatable or pointer character array is not supported yet"},
		{"call s(2)\ncontains\nsubroutine s(n)\ninteger :: n\ncharacter(len=n) :: a(2)\na = 'x'\nprint *, a\nend subroutine\nend\n", "a character array needs a constant length yet"},
		{"call s()\ncontains\nsubroutine s()\ncharacter(len=2) :: a(2) = 'x'\nprint *, a\nend subroutine\nend\n", "is saved between calls"},
		{"call s(2)\ncontains\nsubroutine s(n)\ninteger :: n\ncharacter(len=2) :: a(n)\na = 'x'\nprint *, a\nend subroutine\nend\n", "an array needs constant bounds"},
		{"character(len=2) :: a(2)\na = 'x'\nprint *, trim(a)\nend\n", "trim needs a character argument"},
		{"print *, f()\ncontains\nfunction f()\ncharacter(len=2) :: f(2)\nf = 'k'\nend function\nend\n", "this character array expression is not supported yet"},
		{"character(len=2) :: a(2)\na = 'x'\nprint *, reshape([1, 2], [2]), reshape(a, [2], pad=[1])\nend\n", "argument pad must be a character array"},
		{"character(len=2) :: a(2)\nprint *, reshape([1, 2], [2], pad=a)\nend\n", "argument pad must be an array"},
	} {
		if _, err := runExact(t, tc.src); err == nil || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%q: %v; want a refusal containing %q", tc.src, err, tc.msg)
		}
	}
}

// TestCharacterConstructorLengths pins a constructor without a type-spec
// whose values are of different lengths at run time: the program stops
// instead of cutting them to one length.
func TestCharacterConstructorLengths(t *testing.T) {
	const src = "character(len=3) :: s, a(2)\ns = 'ab'\na = [trim(s), 'xyz']\nprint *, a\nend\n"
	if _, err := runExact(t, src); err == nil || !strings.Contains(err.Error(), "lengths 2 and 3") {
		t.Errorf("constructor of lengths 2 and 3: %v; want a failure naming them", err)
	}
}
