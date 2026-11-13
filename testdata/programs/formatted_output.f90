program formatted_output
  implicit none
  integer :: i
  real :: x
  real(8) :: y
  x = 3.14159274
  y = 2.718281828459045d0
  print '(i5)', 42
  print '(i0)', -123456
  print '(i6.4)', 77
  print '(a,i3,a)', '[', 5, ']'
  print '(f8.3)', x
  print '(f0.2)', 0.5
  print '(e12.4)', y
  print '(es12.4)', y
  print '(en12.3)', 12345.678d0
  print '(g12.5)', y
  print '(l2)', .true.
  print '(a10,"|",a)', 'right', 'tail'
  print '(2(i3,1x))', 1, 2
  print '(3i4)', (i*i, i = 1, 6)
  print '(a,/,a)', 'line one', 'line two'
  print '(5x,a)', 'indented'
  print '(f10.4,tr2,f6.1)', -1.0d0/3.0d0, 2.0
  print '(1p,e12.4)', 12345.6789d0
  print '(z8.8,1x,o4,1x,b8)', 255, 8, 5
  print '(a)', repeat('=', 20)
  print '(f6.2)', 123456.0
end program formatted_output
