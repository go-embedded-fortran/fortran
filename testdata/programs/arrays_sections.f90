program arrays_sections
  implicit none
  integer :: v(10), i, m(3, 4)
  integer, allocatable :: w(:)
  do i = 1, 10
    v(i) = i * i
  end do
  print *, v
  print *, v(3:6)
  print *, v(::3)
  print *, v(10:1:-2)
  print *, v(:4), v(8:)
  print *, size(v), lbound(v), ubound(v), sum(v), product(v(1:5)), maxval(v), minval(v), maxloc(v), minloc(v)
  v(2:4) = 0
  print *, v
  v = v + 1
  print *, v
  where (v > 50) v = -1
  print *, v
  m = reshape([(i, i = 1, 12)], [3, 4])
  print *, m(2, :)
  print *, m(:, 2)
  print *, m(2:3, 2:3)
  print *, shape(m), size(m), size(m, 1), size(m, 2)
  allocate (w(0:4))
  w = [10, 20, 30, 40, 50]
  print *, w, lbound(w), ubound(w)
  print *, any(w > 40), all(w > 5), count(w > 15)
  print *, cshift(w, 1)
  print *, eoshift(w, -2)
  print *, dot_product(w, w)
  deallocate (w)
  print *, allocated(w)
end program arrays_sections
