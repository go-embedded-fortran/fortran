package eval

import (
	"errors"
	"testing"
)

// A module's variables live for the whole run and are shared by every unit that
// uses it; a second module uses the first; rename and only: work.
func TestModules(t *testing.T) {
	out, _, _ := runIO(t, `module counters
  implicit none
  private
  public :: n, tick, reset, limit
  integer :: n = 10
  integer, parameter :: limit = 3
  real(8) :: hidden = 2.5d0
contains
  subroutine tick()
    n = n + 1
  end subroutine tick
  subroutine reset()
    n = 0
    hidden = hidden * 2
  end subroutine reset
  real(8) function peek()
    peek = hidden
  end function peek
end module counters

module doubler
  use counters, only: n, tick
  implicit none
contains
  integer function twice_n()
    call tick()
    twice_n = 2 * n
  end function twice_n
end module doubler

program p
  use counters, only: count => n, tick, reset, limit
  use doubler
  implicit none
  integer :: i
  do i = 1, limit
    call tick()
  end do
  print *, count, twice_n(), count
  call reset()
  print *, count
  count = 7
  print *, twice_n()
end program p
`)
	want := "          13          28          14\n           0\n          16\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func TestSubstringRefused(t *testing.T) {
	_, err := compile(t, `program p
  implicit none
  character(len=5) :: s, t
  character(len=3), parameter :: pre = 'ab'
  character(len=8) :: u = 'init'
  character(len=10) :: shout
  s = 'abcdefgh'
  t = 'xy'
  print '(a,"|",a,"|",a,"|")', s, t, u
  print *, len(s), len_trim(t), trim(t) // '!', pre // '.'
  print *, s == 'abcde', t == 'xy   ', t /= 'xy', 'a' < 'b', 'a ' == 'a'
  print *, adjustl('  pad'), adjustr(t), index('banana', 'an'), index('banana', 'an', back = .true.)
  print *, ichar('A'), achar(98), char(67)
  shout = greet('bob')
  print '(a)', shout
  call shorten(s)
  print '(a)', s
  call show('hello  ')
contains
  function greet(name) result(r)
    character(len=*), intent(in) :: name
    character(len=10) :: r
    r = 'hi ' // name
  end function greet
  subroutine shorten(x)
    character(len=5), intent(inout) :: x
    x = trim(x(1:1)) ! substring is not supported: replaced below
  end subroutine shorten
  subroutine show(x)
    character(*), intent(in) :: x
    print '(i0,a)', len(x), trim(x)
  end subroutine show
end program p
`)
	if err != nil {
		t.Fatal(err) // substrings are supported since part 5
	}
}

func TestCharactersRun(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  character(len=5) :: s, t
  character(len=3), parameter :: pre = 'ab'
  character(len=8) :: u = 'init'
  character(len=10) :: shout
  s = 'abcdefgh'
  t = 'xy'
  print '(a,"|",a,"|",a,"|")', s, t, u
  print *, len(s), len_trim(t), trim(t) // '!', pre // '.'
  print *, s == 'abcde', t == 'xy   ', t /= 'xy', 'a' < 'b', 'a ' == 'a'
  print *, adjustl('  pad'), adjustr(t), index('banana', 'an'), index('banana', 'an', back = .true.)
  print *, ichar('A'), achar(98), char(67)
  shout = greet('bob')
  print '(a)', shout
  call fill(s)
  print '(a)', s
  call show('hello  ')
contains
  function greet(name) result(r)
    character(len=*), intent(in) :: name
    character(len=10) :: r
    r = 'hi ' // name
  end function greet
  subroutine fill(x)
    character(len=5), intent(inout) :: x
    x = x // 'zzz'
  end subroutine fill
  subroutine show(x)
    character(*), intent(in) :: x
    print '(i0,a)', len(x), trim(x)
  end subroutine show
end program p
`)
	want := "abcde|xy   |init    |\n" +
		"           5           2 xy!ab .\n" +
		" T T F T T\n" +
		" pad     xy           2           4\n" +
		"          65 bC\n" +
		"hi bob    \n" +
		"abcde\n" +
		"7hello\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func TestPart3Refusals(t *testing.T) {
	for name, src := range map[string]string{
		"module of another file": "program p\n  use ieee_arithmetic\n  implicit none\nend program p\n",
		"derived type":           "module m\n  type t\n    integer :: a\n  end type t\nend module m\nprogram p\n  use m\n  implicit none\nend program p\n",
		"character array":        "program p\n  implicit none\n  character(len=3) :: a(2)\nend program p\n",
		"pointer character":      "program p\n  implicit none\n  character(len=3), pointer :: a\nend program p\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile(t, src)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("accepted (err %v)", err)
			}
		})
	}
}
