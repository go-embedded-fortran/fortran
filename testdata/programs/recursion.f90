program recursion
  implicit none
  integer :: i
  print *, factorial(10)
  print *, (fib(i), i = 0, 15)
  print *, ackermann(2, 3)
  call countdown(3)
  print *, gcd(1071, 462)
  print *, depth(5000)
contains
  recursive function factorial(n) result(r)
    integer, intent(in) :: n
    integer(8) :: r
    if (n <= 1) then
      r = 1
    else
      r = n * factorial(n - 1)
    end if
  end function factorial
  recursive function fib(n) result(r)
    integer, intent(in) :: n
    integer :: r
    if (n < 2) then
      r = n
    else
      r = fib(n - 1) + fib(n - 2)
    end if
  end function fib
  recursive function ackermann(m, n) result(r)
    integer, intent(in) :: m, n
    integer :: r
    if (m == 0) then
      r = n + 1
    else if (n == 0) then
      r = ackermann(m - 1, 1)
    else
      r = ackermann(m - 1, ackermann(m, n - 1))
    end if
  end function ackermann
  recursive subroutine countdown(n)
    integer, intent(in) :: n
    print *, 'n =', n
    if (n > 0) call countdown(n - 1)
  end subroutine countdown
  recursive integer function gcd(a, b) result(g)
    integer, intent(in) :: a, b
    if (b == 0) then
      g = a
    else
      g = gcd(b, mod(a, b))
    end if
  end function gcd
  recursive integer function depth(n) result(d)
    integer, intent(in) :: n
    if (n == 0) then
      d = 0
    else
      d = 1 + depth(n - 1)
    end if
  end function depth
end program recursion
