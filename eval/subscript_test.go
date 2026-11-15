package eval

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// The subscripts i, i-1, i+1 and a constant are read without a closure call
// (idx); a bound violation through any of them is still refused.
func TestAffineSubscripts(t *testing.T) {
	out, _ := run(t, `program p
  implicit none
  integer :: i, j
  real(8) :: a(4,3), b(4)
  do j = 1, 3
    do i = 1, 4
      a(i,j) = 10d0*j + i
    end do
  end do
  do i = 2, 3
    b(i) = a(i-1,2) + a(i+1,j-1) - a(i,3) + a(4,1)
  end do
  print *, b(2), b(3)
end program p
`)
	// i=2: a(1,2)+a(3,3)-a(2,3)+a(4,1) = 21+33-32+14 = 36; i=3: 22+34-33+14 = 37
	if !strings.Contains(out, "36.0") || !strings.Contains(out, "37.0") {
		t.Fatalf("got %q", out)
	}
	for _, c := range []struct{ name, src, want string }{
		{"low", "program p\n implicit none\n integer :: i\n real(8) :: a(3)\n a = 1d0\n do i = 1, 3\n a(i) = a(i-1)\n end do\nend program p\n", "subscript 1 of a is 0, outside 1:3"},
		{"high", "program p\n implicit none\n integer :: i\n real(8) :: a(3,2)\n a = 1d0\n do i = 1, 3\n a(i+1,1) = 2d0\n end do\nend program p\n", "subscript 1 of a is 4, outside 1:3"},
		{"second", "program p\n implicit none\n integer :: j\n real(8) :: a(3,2)\n a = 1d0\n do j = 1, 2\n a(1,j+1) = 2d0\n end do\nend program p\n", "subscript 2 of a is 3, outside 1:2"},
	} {
		p, err := compile(t, c.src)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		_, err = p.Run(io.Discard)
		var ee *Error
		if !errors.As(err, &ee) || !strings.Contains(ee.Msg, c.want) {
			t.Fatalf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}
