package eval

import (
	"bytes"
	"errors"
	"testing"
)

// Arrays of the general kind: element order, bounds, sections with strides,
// elemental operations, WHERE, and the intrinsics; the expected text is what
// gfortran prints for the same program.
func TestGeneralArrays(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  integer :: a(3, 2), v(5), i, j
  integer :: c(2, 2, 2)
  real :: r(4)
  logical :: l(3)
  integer, allocatable :: w(:)
  integer, parameter :: k = 2
  do j = 1, 2
    do i = 1, 3
      a(i, j) = 10 * i + j
    end do
  end do
  print *, a
  print *, a(2, :), a(:, 2)
  v = [5, 3, 9, 1, 7]
  print *, v(5:1:-2), v(2:3) * 2 + 1, sum(v), maxval(v), minloc(v), maxloc(v)
  v(2:4) = -1
  print *, v, size(a), shape(a), size(a, 1)
  r = [1.5, 2.5, 3.5, 4.5]
  print *, r * 2, sqrt(r), r(2:3) / 2
  l = v(1:3) > 0
  print *, l, any(l), all(l), count(l)
  allocate (w(-1:1))
  w = [7, 8, 9]
  print *, w, lbound(w), ubound(w), w(-1), allocated(w)
  deallocate (w)
  print *, allocated(w)
  c = reshape([(i, i = 1, 8)], [2, 2, 2])
  print *, c(2, :, :), c(:, 1, 2)
  print *, transpose(a), matmul(a, transpose(a))
  print *, pack(v, v > 0), spread([1, 2], 1, 2), cshift(v, -1), eoshift(v, 2)
  where (v < 0) v = 0
  print *, v, dot_product(v, v)
end program p
`)
	// gfortran 15.2.0's output for the same program
	want := "          11          21          31          12          22          32\n" +
		"          21          22          12          22          32\n" +
		"           7           9           5           7          19          25           9           4           3\n" +
		"           5          -1          -1          -1           7           6           3           2           3\n" +
		"   3.00000000       5.00000000       7.00000000       9.00000000       1.22474492       1.58113885       1.87082875       2.12132025       1.25000000       1.75000000    \n" +
		" T F F T F           1\n" +
		"           7           8           9          -1           1           7 T\n" +
		" F\n" +
		"           2           4           6           8           5           6\n" +
		"          11          12          21          22          31          32         265         495         725         495         925        1355         725        1355        1985\n" +
		"           5           7           1           1           2           2           7           5          -1          -1          -1          -1          -1           7           0           0\n" +
		"           5           0           0           0           7          74\n"
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}

func TestArrayDummiesAndResults(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  integer :: v(4)
  integer, allocatable :: w(:)
  v = [1, 2, 3, 4]
  call dbl(v)
  print *, v, total(v(2:3))
  w = evens(6)
  print *, w
contains
  subroutine dbl(x)
    integer, intent(inout) :: x(:)
    x = x * 2
  end subroutine dbl
  integer function total(x)
    integer, intent(in) :: x(:)
    total = sum(x)
  end function total
  function evens(n) result(r)
    integer, intent(in) :: n
    integer, allocatable :: r(:)
    integer :: i
    allocate (r(n / 2))
    r = [(2 * i, i = 1, n / 2)]
  end function evens
end program p
`)
	if want := "           2           4           6           8          10\n           2           4           6\n"; out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func TestArrayRefusalsAndBounds(t *testing.T) {
	for name, src := range map[string]string{
		"vector subscript": "program p\n  implicit none\n  integer :: a(3), i(2)\n  print *, a(i)\nend program p\n",
		"mixed kernels":    "program p\n  implicit none\n  real(8) :: a(3)\n  integer :: b(3)\n  a = 1d0\n  b = a\nend program p\n",
		"elsewhere":        "program p\n  implicit none\n  integer :: a(3)\n  a = 1\n  where (a > 0)\n    a = 2\n  elsewhere\n    a = 3\n  end where\nend program p\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile(t, src)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("accepted (err %v)", err)
			}
		})
	}
	for name, src := range map[string]string{
		"out of bounds":   "program p\n  implicit none\n  integer :: a(3)\n  integer :: i\n  i = 4\n  print *, a(i)\nend program p\n",
		"section bounds":  "program p\n  implicit none\n  integer :: a(3)\n  integer :: i\n  i = 5\n  a(1:i) = 0\nend program p\n",
		"nonconformable":  "program p\n  implicit none\n  integer :: a(3), b(4)\n  a = 1\n  b = 2\n  a = a + b\nend program p\n",
		"huge allocation": "program p\n  implicit none\n  integer, allocatable :: a(:, :, :)\n  integer :: n\n  n = 100000\n  allocate (a(n, n, n))\nend program p\n",
		"huge reshape":    "program p\n  implicit none\n  integer :: a(2)\n  integer :: n\n  n = 100000\n  print *, size(reshape(a, [n, n, n], [0]))\nend program p\n",
	} {
		t.Run(name, func(t *testing.T) {
			p, err := compile(t, src)
			if err != nil {
				return
			}
			_, err = p.Run(&bytes.Buffer{})
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("not refused (err %v)", err)
			}
		})
	}
}
