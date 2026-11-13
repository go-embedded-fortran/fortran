module geometry
  implicit none
  private
  public :: pi_approx, area, scale_factor, describe, counter, bump
  real, parameter :: pi_approx = 3.14159
  real :: scale_factor = 2.0
  integer :: counter = 0
  interface area
    module procedure area_circle, area_rect
  end interface
contains
  function area_circle(r) result(a)
    real, intent(in) :: r
    real :: a
    a = pi_approx * r * r
  end function area_circle
  function area_rect(w, h) result(a)
    real, intent(in) :: w, h
    real :: a
    a = w * h
  end function area_rect
  subroutine describe(name)
    character(*), intent(in) :: name
    print '(a,a,a)', 'shape: ', trim(name), '!'
  end subroutine describe
  subroutine bump()
    counter = counter + 1
  end subroutine bump
end module geometry

program modules
  use geometry, only: area, scale_factor, describe, counter, bump, circle_const => pi_approx
  implicit none
  print *, circle_const
  print *, area(2.0)
  print *, area(2.0, 3.0)
  call describe('circle   ')
  scale_factor = scale_factor * 1.5
  print *, scale_factor
  call bump()
  call bump()
  print *, counter
end program modules
