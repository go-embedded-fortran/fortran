! complex arrays: module and host association, an element as an actual
! argument (copied out), a zero-sized array
module cam
  implicit none
  complex(8) :: g(3)
end module cam

program ca3
  use cam
  implicit none
  complex(8) :: h(2), z(0)
  g = [(1d0, 1d0), (2d0, 2d0), (3d0, 3d0)]
  h = (5d0, -5d0)
  call twice(g(2))
  print *, g
  call bump()
  print *, h
  call fill()
  print *, g
  print *, size(z), any(z /= z)
  print *, z
contains
  subroutine twice(w)
    complex(8), intent(inout) :: w
    w = 2 * w
  end subroutine twice
  subroutine bump()
    h(1) = h(1) + (1d0, 0d0)
    h(2:2) = conjg(h(2:2))
  end subroutine bump
end program ca3

subroutine fill()
  use cam
  implicit none
  g(3) = (-1d0, 0d0)
end subroutine fill
