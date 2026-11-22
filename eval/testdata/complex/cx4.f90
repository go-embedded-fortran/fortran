module m
contains
  subroutine show(z)
    complex(8), intent(in) :: z
    print *, z
  end subroutine
end module
use m
complex(8), parameter :: p = (3d0, 4d0)
complex(4) :: a, b
complex(8) :: w
real(8) :: r, x
call show(p)
call show(-p)
! single precision is rounded at each operation, not only on output
a = (1.0, 1.0)
b = (1e-9, 1e-9)
w = (a + b) - a
print *, w
a = (2.0, 3.0)
w = sin(a)
print *, w
w = exp(a)
print *, w
x = abs(a)
print *, x
! z**1 is z itself, not 1*z: no NaN from 0*inf
r = 1d300
r = r * r
w = cmplx(r, 0d0, 8)
print *, w ** 1, w ** 0
end
