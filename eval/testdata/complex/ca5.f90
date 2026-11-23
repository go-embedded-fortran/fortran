! complex arrays: dummy arguments (explicit, assumed and assumed-size shape),
! function results, reshape, transpose and dot_product
module cdum
  implicit none
contains
  subroutine scale(x, k)
    complex(8), intent(inout) :: x(:)
    real(8), intent(in) :: k
    x = x * k
  end subroutine scale
  function twice(x) result(r)
    complex(8), intent(in) :: x(3)
    complex(8) :: r(3)
    r = x + x
  end function twice
end module cdum

program ca5
  use cdum
  implicit none
  complex(8) :: a(3), m(2, 3), t(3, 2), e(0)
  complex(4) :: s(2), u(2)
  real(8) :: r(3)
  integer :: i
  a = [(1d0, 2d0), (3d0, -1d0), (-2d0, 0.5d0)]
  call scale(a, 2d0)
  print *, a
  print *, twice(a)
  call fill(m, 2, 3)
  print *, m
  call bump(a, 3)
  print *, a
  t = transpose(m)
  print *, t
  print *, shape(t)
  a = reshape(m(:, 2:3), [3], pad=[(9d0, 9d0)])
  print *, a
  m = reshape([(cmplx(i, -i, 8), i = 1, 6)], [2, 3], order=[2, 1])
  print *, m
  print *, dot_product(a, a)
  r = [1d0, -2d0, 0.5d0]
  print *, dot_product(a, r), dot_product(r, a)
  if (dot_product(e, e) /= 0) stop 1
  s = [(0.1, 0.2), (0.3, -0.7)]
  u = [(1.1, 1.0), (0.3, 0.9)]
  print *, dot_product(s, u)
  print *, sum(abs(a))
end program ca5

subroutine fill(x, n1, n2)
  implicit none
  integer, intent(in) :: n1, n2
  complex(8), intent(out) :: x(n1, n2)
  integer :: i, j
  do j = 1, n2
    do i = 1, n1
      x(i, j) = cmplx(i, j, 8)
    end do
  end do
end subroutine fill

subroutine bump(x, n)
  implicit none
  integer, intent(in) :: n
  complex(8) :: x(*)
  integer :: k
  do k = 1, n
    x(k) = x(k) * (0d0, 1d0)
  end do
end subroutine bump
