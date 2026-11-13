! K2 of bib/fortran.md 4.2: 2-D five-point Jacobi stencil, column-major order
! (j outer, i inner), 100 sweeps, 1024 x 1024. Inputs from a fixed formula.
! Line 1: checksum. Line 2: "time <seconds>" (cpu_time around the sweeps).
program k2
  implicit none
  integer, parameter :: n = 1024, sweeps = 100
  real(8), allocatable :: u(:,:), v(:,:)
  real(8) :: t0, t1
  integer :: i, j, s
  allocate (u(n,n), v(n,n))
  do j = 1, n
    do i = 1, n
      u(i,j) = real(mod(i*7 + j*13, 17), 8)/17d0
    end do
  end do
  v = u
  call cpu_time(t0)
  do s = 1, sweeps
    do j = 2, n - 1
      do i = 2, n - 1
        v(i,j) = 0.25d0*(u(i-1,j) + u(i+1,j) + u(i,j-1) + u(i,j+1))
      end do
    end do
    do j = 2, n - 1
      do i = 2, n - 1
        u(i,j) = v(i,j)
      end do
    end do
  end do
  call cpu_time(t1)
  print '(es24.16)', sum(u)
  print '(a,f0.6)', 'time ', t1 - t0
end program k2
