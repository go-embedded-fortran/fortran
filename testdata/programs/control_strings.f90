program control_strings
  implicit none
  character(len=20) :: s, t
  character(len=:), allocatable :: u
  integer :: i, n, ios
  real :: r
  s = 'Fortran'
  print *, len(s), len_trim(s), trim(s) // '!'
  print '(a)', adjustl('   padded')
  print '(a)', adjustr(s)
  print *, index('banana', 'an'), index('banana', 'an', back = .true.), scan('hello', 'lo'), verify('aabc', 'ab')
  print *, to_upper_ascii('mixed Case')
  write (t, '(i0,a,f5.2)') 42, ' units ', 2.5
  print *, trim(t)
  read (t, *) n
  print *, n
  t = '7 8.5'
  read (t, *) i, r
  print *, i, r
  t = 'xyz'
  read (t, *, iostat = ios) n
  print *, ios /= 0
  n = 0
  do while (n < 5)
    n = n + 1
    if (mod(n, 2) == 0) cycle
    print *, 'odd', n
  end do
  do i = 1, 100
    if (i * i > 50) exit
  end do
  print *, i
  select case (i)
  case (1:5)
    print *, 'low'
  case (6, 7)
    print *, 'mid'
  case default
    print *, 'high'
  end select
  u = 'allocatable'
  u = u // ' string'
  print *, len(u), u
  print *, ichar('A'), achar(98), char(67)
  print *, merge('yes', 'no ', i > 3)
contains
  function to_upper_ascii(x) result(y)
    character(*), intent(in) :: x
    character(len(x)) :: y
    integer :: k
    y = x
    do k = 1, len(x)
      if (y(k:k) >= 'a' .and. y(k:k) <= 'z') y(k:k) = achar(iachar(y(k:k)) - 32)
    end do
  end function to_upper_ascii
end program control_strings
