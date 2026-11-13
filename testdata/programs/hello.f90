program hello
  implicit none
  print *, 'Hello, Fortran'
  print *, 42, -7, 0
  print *, 1.5, 2.25d0
  print *, .true., .false.
  print *, 'a', 'bc', 'def'
  print *, (1.0, -2.0)
  print *
  write (*, *) 'write to unit *'
end program hello
