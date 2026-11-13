module shapes
  implicit none
  type :: point
    real :: x = 0.0, y = 0.0
  end type point
  type :: segment
    type(point) :: a, b
    character(len=8) :: label = 'seg'
  end type segment
  type :: stack
    integer, allocatable :: items(:)
    integer :: top = 0
  contains
    procedure :: push => stack_push
    procedure :: size_of => stack_size
  end type stack
contains
  subroutine stack_push(self, v)
    class(stack), intent(inout) :: self
    integer, intent(in) :: v
    integer, allocatable :: tmp(:)
    if (.not. allocated(self%items)) allocate (self%items(0))
    allocate (tmp(self%top + 1))
    tmp(1:self%top) = self%items(1:self%top)
    tmp(self%top + 1) = v
    call move_alloc(tmp, self%items)
    self%top = self%top + 1
  end subroutine stack_push
  integer function stack_size(self)
    class(stack), intent(in) :: self
    stack_size = self%top
  end function stack_size
end module shapes

program derived_types
  use shapes
  implicit none
  type(point) :: p, q
  type(segment) :: s
  type(stack) :: st
  type(point) :: pts(3)
  integer :: i
  p = point(1.5, 2.5)
  q = point(y = 4.0)
  print *, p
  print *, q%x, q%y
  s = segment(p, q, 'first')
  print *, s%a%x, s%b%y, s%label
  s%b%x = s%a%x + 10.0
  print *, s%b
  do i = 1, 3
    pts(i) = point(real(i), real(i * i))
  end do
  print *, pts(2)
  print *, pts%x
  print *, pts%y
  call st%push(7)
  call st%push(8)
  call st%push(9)
  print *, st%size_of(), st%items
end program derived_types
