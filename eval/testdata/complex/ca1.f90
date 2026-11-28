! complex arrays: declaration, constructors, elements, sections, assignment
program ca1
  implicit none
  complex(8) :: a(4), b(2, 3)
  complex(4) :: s(3)
  complex(8), parameter :: p(2) = [(1d0, 2d0), (3d0, -4d0)]
  real(8) :: r(4)
  integer :: i, j
  a = [(1d0, 0.5d0), (2d0, -1d0), (0d0, 3d0), (-4d0, 0d0)]
  print *, a
  print *, a(2), a(4)
  print *, a(2:3)
  print *, a(4:1:-2)
  a(1) = (9d0, 9d0)
  a(2:3) = a(1:2)
  print *, a
  a = 7
  print *, a
  r = [1d0, 2d0, 3d0, 4d0]
  a = r
  print *, a
  a = [(cmplx(i, -i, 8), i = 1, 4)]
  print *, a
  do j = 1, 3
    do i = 1, 2
      b(i, j) = cmplx(i, 10 * j, 8)
    end do
  end do
  print *, b
  print *, b(2, :)
  print *, b(:, 3)
  print *, size(b), shape(b), lbound(b), ubound(b)
  print *, p
  s = [(0.1d0, 0.2d0), cmplx(1d0/3d0, -2d0/3d0, 8), (1d30, -1d-30)]
  print *, s
  s = 1d0 / 3d0
  print *, s
  print '(4(f8.3,1x))', a
  print '(2es14.6)', s(1)
end program ca1
