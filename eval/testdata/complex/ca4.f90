! real(8) arrays of the kernels' kind (rank 1 or 2, bounds 1) given a
! constructor, a complex array and complex elements: each converted, a
! complex value giving its real part
program ca4
  implicit none
  real(8) :: r(4), m(2, 2)
  complex(8) :: c(4), w(2, 2)
  complex(4) :: s
  r = [1d0, 2d0, 3d0, 4d0]
  print *, r
  c = [(1d0, -1d0), (2d0, -2d0), (3d0, -3d0), (4d0, -4d0)]
  r = c
  print *, r
  r(2) = c(4)
  s = (0.1, 0.2)
  r(3) = s
  print *, r
  w(:, 1) = [(1d0, 5d0), (2d0, 6d0)]
  w(:, 2) = [(3d0, 7d0), (4d0, 8d0)]
  m = w
  print *, m
  m = reshape([10d0, 20d0, 30d0, 40d0], [2, 2])
  print *, m
  c = r * (0d0, 1d0)
  print *, c
end program ca4
