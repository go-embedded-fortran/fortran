package eval

import (
	"bytes"
	"errors"
	"testing"
)

func TestInternalProcedures(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  integer :: n, total
  real(8) :: x
  logical :: ok
  n = 5
  total = 100
  call add_to_total(n)
  print *, total, n
  x = half(3.0d0)
  print *, x, twice(n) + 1
  call swap_test()
  ok = is_even(total)
  print *, ok, is_even(n)
  call bump(n)
  call bump(n)
  print *, n, counter()
  print *, scale3(b = 2, a = 10)
contains
  subroutine add_to_total(k)
    integer, intent(in) :: k
    total = total + k      ! host association
  end subroutine add_to_total
  function half(v) result(r)
    real(8), intent(in) :: v
    real(8) :: r
    r = v / 2
  end function half
  integer function twice(v)
    integer, intent(in) :: v
    twice = 2 * v
  end function twice
  subroutine swap_test()
    integer :: a, b
    a = 1
    b = 2
    call swap(a, b)
    print *, a, b
  end subroutine swap_test
  subroutine swap(u, v)
    integer, intent(inout) :: u, v
    integer :: tmp
    tmp = u
    u = v
    v = tmp
  end subroutine swap
  logical function is_even(v)
    integer, intent(in) :: v
    is_even = mod(v, 2) == 0
  end function is_even
  subroutine bump(k)
    integer, intent(inout) :: k
    k = k + 1
    if (k > 100) return
    k = k + 10
  end subroutine bump
  integer function counter()
    counter = total / 10
  end function counter
  integer function scale3(a, b)
    integer, intent(in) :: a, b
    scale3 = 3 * a + b
  end function scale3
end program p
`)
	want := "         105           5\n" +
		"   1.5000000000000000               11\n" +
		"           2           1\n" +
		" F F\n" +
		"          27          10\n" +
		"          32\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

// An external subroutine and function of the same file; a function whose
// result is real(4) rounds like any real(4) value.
func TestExternalProcedures(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  integer :: i
  real :: r
  integer, external :: sq
  real, external :: third
  call hello(3)
  r = third(1.0)
  print *, sq(7), r
end program p

subroutine hello(n)
  implicit none
  integer, intent(in) :: n
  integer :: i
  do i = 1, n
    if (i == 2) cycle
    print *, 'hello', i
  end do
end subroutine hello

real function third(x)
  implicit none
  real, intent(in) :: x
  third = x / 3.0
end function third

integer function sq(n)
  implicit none
  integer, intent(in) :: n
  sq = n * n
end function sq
`)
	want := " hello           1\n hello           3\n          49  0.333333343    \n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func TestRecursionAndReturnInLoops(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  print *, fact(20), isodd(7), find_first(12)
contains
  recursive integer(8) function fact(n) result(r)
    integer, intent(in) :: n
    if (n <= 1) then
      r = 1
    else
      r = n * fact(n - 1)
    end if
  end function fact
  recursive logical function isodd(n) result(r)
    integer, intent(in) :: n
    if (n == 0) then
      r = .false.
    else
      r = iseven(n - 1)
    end if
  end function isodd
  recursive logical function iseven(n) result(r)
    integer, intent(in) :: n
    if (n == 0) then
      r = .true.
    else
      r = isodd(n - 1)
    end if
  end function iseven
  integer function find_first(limit)
    integer, intent(in) :: limit
    integer :: i, j
    find_first = -1
    do i = 1, limit
      do j = 1, limit
        if (i * j == 42) then
          find_first = 10 * i + j
          return
        end if
      end do
    end do
  end function find_first
end program p
`)
	if want := "  2432902008176640000 T          67\n"; out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

// STOP inside a procedure ends the program; the output before it stays.
func TestStopInProcedure(t *testing.T) {
	out, errOut, exit := runIO(t, `program p
  implicit none
  call die(4)
  print *, 'unreachable'
contains
  subroutine die(code)
    integer, intent(in) :: code
    print *, 'dying'
    error stop code
  end subroutine die
end program p
`)
	if out != " dying\n" || errOut != "ERROR STOP 4\n" || exit != 4 {
		t.Errorf("got %q %q %d", out, errOut, exit)
	}
}

func TestPart2Refusals(t *testing.T) {
	for name, src := range map[string]string{
		"array dummy":     "program p\n  implicit none\n  integer :: i\n  call s(i)\ncontains\n  subroutine s(a)\n    real(8), intent(in) :: a(:)\n  end subroutine s\nend program p\n",
		"optional":        "program p\n  implicit none\n  call s()\ncontains\n  subroutine s(a)\n    integer, optional :: a\n  end subroutine s\nend program p\n",
		"saved local":     "program p\n  implicit none\n  call s()\ncontains\n  subroutine s()\n    integer :: k = 0\n    k = k + 1\n  end subroutine s\nend program p\n",
		"function result": "program p\n  implicit none\n  integer :: i\n  i = f()\ncontains\n  function f()\n    real(8), allocatable :: f(:)\n  end function f\nend program p\n",
		"host array":      "program p\n  implicit none\n  real(8) :: a(3)\n  call s()\ncontains\n  subroutine s()\n    a(1) = 1d0\n  end subroutine s\nend program p\n",
		"type conversion": "program p\n  implicit none\n  real(8) :: x\n  x = 1d0\n  call s(x)\ncontains\n  subroutine s(a)\n    real(8), intent(in) :: a\n  end subroutine s\nend program p\n",
	} {
		t.Run(name, func(t *testing.T) {
			f, err := compile(t, src)
			_ = f
			if name == "type conversion" {
				if err != nil {
					t.Fatalf("a matching call is accepted, got %v", err)
				}
				return
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("accepted (err %v)", err)
			}
		})
	}
}

// An unbounded recursion is refused with an error, not a crash of the host.
func TestRecursionBomb(t *testing.T) {
	p, err := compile(t, `program p
  implicit none
  print *, f(1)
contains
  recursive integer function f(n) result(r)
    integer, intent(in) :: n
    r = f(n + 1) + 1
  end function f
end program p
`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Run(&bytes.Buffer{})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("not refused (err %v)", err)
	}
}
