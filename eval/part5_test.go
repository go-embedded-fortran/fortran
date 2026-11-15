package eval

import (
	"bytes"
	"errors"
	"testing"
)

// Substrings, character lengths computed from dummy arguments, deferred-length
// allocatable characters, merge, scan, verify and internal files; the expected
// text is gfortran 15.2.0's for the same program.
func TestStringsAndInternalFiles(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  character(len=12) :: s
  character(len=20) :: t
  character(len=:), allocatable :: u
  integer :: i, n, ios
  real :: r
  logical :: b
  s = 'Hello, World'
  print *, s(1:5), '|', s(8:), '|', s(:5), '|', s(6:5), '|'
  s(1:5) = 'J'
  print *, s, len(s(3:7))
  print *, scan('hello', 'lo'), scan('hello', 'lo', back = .true.), verify('aabc', 'ab'), verify('ab', 'ab')
  print *, merge('yes', 'no ', n == 0), merge(1, 2, n /= 0), merge(.true., .false., .true.)
  u = 'abc'
  u = u // 'defgh'
  print *, len(u), u
  u = u(2:4)
  print *, len(u), u
  write (t, '(i0,a,f5.2)') 42, ' units ', 2.5
  print *, trim(t), len_trim(t)
  read (t, *) n
  print *, n
  t = '7 8.5 T'
  read (t, *) i, r, b
  print *, i, r, b
  t = 'xyz'
  read (t, *, iostat = ios) n
  print *, ios /= 0, n
  write (t, '(a)', iostat = ios) 'a very long string that does not fit'
  print *, ios /= 0
  print *, up('mixed Case'), len(up('abc'))
contains
  function up(x) result(y)
    character(*), intent(in) :: x
    character(len(x)) :: y
    integer :: k
    y = x
    do k = 1, len(x)
      if (y(k:k) >= 'a' .and. y(k:k) <= 'z') y(k:k) = achar(iachar(y(k:k)) - 32)
    end do
  end function up
end program p
`)
	want := " Hello|World|Hello||\n" +
		" J    , World           5\n" +
		"           3           5           4           0\n" +
		" yes           2 T\n" +
		"           8 abcdefgh\n" +
		"           3 bcd\n" +
		" 42 units  2.50          14\n" +
		"          42\n" +
		"           7   8.50000000     T\n" +
		" T          42\n" +
		" T\n" +
		" MIXED CASE           3\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func TestPart5Refusals(t *testing.T) {
	for name, src := range map[string]string{
		"read stdin":     "program p\n  implicit none\n  integer :: n\n  read *, n\nend program p\n",
		"read unit":      "program p\n  implicit none\n  integer :: n\n  read (5, *) n\nend program p\n",
		"read array":     "program p\n  implicit none\n  integer :: n(2)\n  character(len=4) :: t\n  t = '1 2'\n  read (t, *) n\nend program p\n",
		"write literal":  "program p\n  implicit none\n  write ('abc', *) 1\nend program p\n",
		"deferred alloc": "program p\n  implicit none\n  character(len=:) :: a\nend program p\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile(t, src)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("accepted (err %v)", err)
			}
		})
	}
	// run-time refusals: a substring outside the string, a huge length
	for name, src := range map[string]string{
		"substring": "program p\n  implicit none\n  character(len=3) :: s\n  integer :: i\n  i = 5\n  s = 'abc'\n  print *, s(2:i)\nend program p\n",
		"length":    "program p\n  implicit none\n  call f(2000000000)\ncontains\n  subroutine f(n)\n    integer, intent(in) :: n\n    character(len=n) :: s\n  end subroutine f\nend program p\n",
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
