program column_major
  implicit none
  integer :: a(2, 3), b(3, 2), i, j, c(2, 2, 2)
  a = reshape([1, 2, 3, 4, 5, 6], [2, 3])
  print *, a
  print *, a(1, :)
  print *, a(2, 3), a(1, 2)
  b = transpose(a)
  print *, b
  print *, ((a(i, j), j = 1, 3), i = 1, 2)
  print *, ((a(i, j), i = 1, 2), j = 1, 3)
  print '(3i3)', a
  print '(2i3)', ((a(i, j), j = 1, 3), i = 1, 2)
  do i = 1, 2
    print '(3i3)', a(i, :)
  end do
  c = reshape([(i, i = 1, 8)], [2, 2, 2])
  print *, c
  print *, c(:, :, 2)
  print *, c(2, :, :)
  print *, reshape(a, [3, 2])
  print *, reshape(a, [3, 2], order = [2, 1])
  print *, pack(a, a > 2)
  print *, matmul(a, b)
  print *, spread([1, 2], 2, 3)
end program column_major
