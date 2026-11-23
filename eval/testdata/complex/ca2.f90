! complex arrays: elemental arithmetic, comparisons, intrinsics, powers
program ca2
  implicit none
  complex(8) :: a(3), b(3), c(3)
  complex(4) :: x(2), y(2)
  real(8) :: r(3)
  real(4) :: q(2)
  integer :: k(3)
  a = [(1d0, 2d0), (-3d0, 0.5d0), (0d0, -1d0)]
  b = [(2d0, -1d0), (1d0, 1d0), (4d0, 0d0)]
  r = [0.5d0, -2d0, 3d0]
  k = [1, 2, 3]
  c = a + b
  print *, c
  c = a - b
  print *, c
  c = a * b
  print *, c
  c = a / b
  print *, c
  c = (2d0, 1d0) * a
  print *, c
  c = a * r
  print *, c
  c = r / a
  print *, c
  c = a + k
  print *, c
  c = -a
  print *, c
  c = +a
  print *, c
  c = a**2
  print *, c
  c = a**(-1)
  print *, c
  c = a**5
  print *, c
  c = a**0
  print *, c
  print *, a == b, a /= b
  print *, a == a, any(a /= a), all(a == a)
  print *, real(a), aimag(a)
  print *, conjg(a)
  print *, abs(a)
  print *, cmplx(r, r + r, 8)
  print *, cmplx(r)
  print *, dble(a)
  x = [(0.1, 0.2), (3.0, -0.7)]
  y = [(1.0, 1.0), (0.3, 0.0)]
  print *, x * y, x / y
  print *, x + 0.1d0
  q = [0.1, 0.2]
  print *, x == q, x == (0.1, 0.2)
  print *, abs(x)
  print *, (1.0, 2.0) * [real :: (3.0 + 4.0)]
  print *, (1.0, 2.0) * [real :: -(3.0 + 4.0)]
end program ca2
