module m
  complex(8) :: g = (1d0, -2d0)
contains
  subroutine twice(z)
    complex(8), intent(inout) :: z
    z = z * 2
  end subroutine
  complex(8) function rot(z)
    complex(8), intent(in) :: z
    rot = z * (0d0, 1d0)
  end function
  subroutine host()
    g = g + (0.5d0, 0.5d0)
    call inner()
  contains
    subroutine inner()
      print *, g, real(g), aimag(g)
    end subroutine
  end subroutine
end module
use m
complex(4) :: a, b
complex(8) :: c, d
complex(8), parameter :: p = (3d0, 4d0)
real(8) :: x
integer :: i
a = (1.5, -2.25)
b = cmplx(0.1, 0.3)
print *, a + b, a - b, a * b, a / b
print *, a ** 2, a ** 3, a ** (-1), a ** (-3), a ** 0
c = (1d0, 2d0)
d = dcmplx(0.7d0, -1.1d0)
print *, c * d, c / d, c ** 5, c ** (-2), c ** 0.5d0
print *, c + 1, c * 2.5d0, 3 - c, 1d0 / c
print *, abs(p), abs(d), abs(a), conjg(c), conjg(a)
print *, sqrt(c), sin(c), cos(c), sqrt(a), sin(a), cos(a)
x = c
i = d
print *, x, i, real(c), dble(d), aimag(d), int(c)
print *, c == (1d0, 2d0), c /= p, a == b, c == 1
call twice(c)
print *, c, rot(c)
call host()
print *, g, p
c = 2
print *, c, cmplx(1, 2), cmplx(1.5d0, 2, kind=8)
print *, -a, -c, (2.0, 3.0) * a
end
