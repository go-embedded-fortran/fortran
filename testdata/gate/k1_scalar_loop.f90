! K1 of bib/fortran.md 4.2: scalar loop, n = 10**8.
! Line 1: checksum. Line 2: "time <seconds>" (cpu_time around the kernel only).
program k1
  implicit none
  integer(8) :: i, n
  real(8) :: s, t0, t1
  n = 100000000_8
  s = 0d0
  call cpu_time(t0)
  do i = 1, n
    s = s + 1d0/real(i,8)**2
  end do
  call cpu_time(t1)
  print '(es24.16)', s
  print '(a,f0.6)', 'time ', t1 - t0
end program k1
