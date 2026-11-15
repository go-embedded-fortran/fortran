module m
  implicit none
  type :: inner
    integer(8) :: k = 7
    logical :: on = .true.
  end type inner
  type :: rec
    integer :: id = 0
    real(8) :: w = 1.5d0
    character(len=6) :: tag = 'none'
    type(inner) :: in
    real :: fixed(3) = 0.0
    integer, allocatable :: items(:)
  contains
    procedure :: total => rec_total
    procedure :: grow => rec_grow
  end type rec
contains
  integer function rec_total(self)
    class(rec), intent(in) :: self
    rec_total = self%id
    if (allocated(self%items)) rec_total = rec_total + sum(self%items)
  end function rec_total
  subroutine rec_grow(self, n)
    class(rec), intent(inout) :: self
    integer, intent(in) :: n
    integer :: i
    if (allocated(self%items)) deallocate (self%items)
    allocate (self%items(n))
    self%items = [(i * 10, i = 1, n)]
  end subroutine rec_grow
end module m

program p
  use m
  implicit none
  type(rec) :: a, b
  type(rec) :: recs(2)
  integer :: i
  print *, a%id, a%w, a%tag, a%in%k, a%in%on, a%fixed, allocated(a%items)
  a = rec(5, tag = 'abcdefgh', in = inner(3_8, .false.))
  print *, a%id, a%w, a%tag, a%in
  call a%grow(3)
  print *, a%items, a%total()
  b = a
  b%items(2) = 0
  b%fixed(2:3) = [1.0, 2.0]
  b%in%k = b%in%k + 1
  print *, a%items, b%items, b%fixed, a%in%k, b%in%k
  do i = 1, 2
    recs(i) = rec(i, real(i, 8) * 2, 'r')
    recs(i)%fixed(1) = real(i)
  end do
  print *, recs%id, recs%w, recs(2)%tag, recs(2)%total()
end program p
