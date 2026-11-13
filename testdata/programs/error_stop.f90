program error_stop
  implicit none
  integer :: n
  n = 7
  print *, 'before error stop'
  if (n > 5) error stop 7
  print *, 'never printed'
end program error_stop
