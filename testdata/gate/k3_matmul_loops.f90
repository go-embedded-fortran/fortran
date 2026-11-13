! K3 of bib/fortran.md 4.2: matmul as a triple loop, loop order j,k,i, 512 x 512.
! Line 1: checksum. Line 2: "time <seconds>" (cpu_time around the loops).
program k3loops
  implicit none
  integer, parameter :: n = 512
  real(8), allocatable :: a(:,:), b(:,:), c(:,:)
  real(8) :: t0, t1
  integer :: i, j, k
  allocate (a(n,n), b(n,n), c(n,n))
  do j = 1, n
    do i = 1, n
      a(i,j) = real(mod(i + 2*j, 7), 8)/7d0
      b(i,j) = real(mod(3*i + j, 5), 8)/5d0
    end do
  end do
  c = 0d0
  call cpu_time(t0)
  do j = 1, n
    do k = 1, n
      do i = 1, n
        c(i,j) = c(i,j) + a(i,k)*b(k,j)
      end do
    end do
  end do
  call cpu_time(t1)
  print '(es24.16)', sum(c)
  print '(a,f0.6)', 'time ', t1 - t0
end program k3loops
