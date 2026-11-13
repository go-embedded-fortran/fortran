! K4 of bib/fortran.md 4.2: whole-array expression a = b*c + d, n = 10**7.
! The expression runs 10 times (b(1) changes between runs so that nothing is
! loop-invariant) so that the kernel outlasts the initialisation.
! Line 1: checksum. Line 2: "time <seconds>" (cpu_time around the runs).
program k4
  implicit none
  integer, parameter :: n = 10000000, reps = 10
  real(8), allocatable :: a(:), b(:), c(:), d(:)
  real(8) :: t0, t1
  integer :: i, r
  allocate (a(n), b(n), c(n), d(n))
  do i = 1, n
    b(i) = real(mod(i, 13), 8)/13d0
    c(i) = real(mod(i, 11), 8)/11d0
    d(i) = real(mod(i, 7), 8)/7d0
  end do
  call cpu_time(t0)
  do r = 1, reps
    b(1) = real(r, 8)
    a = b*c + d
  end do
  call cpu_time(t1)
  print '(es24.16)', sum(a)
  print '(a,f0.6)', 'time ', t1 - t0
end program k4
