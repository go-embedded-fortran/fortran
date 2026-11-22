complex(4) :: a
complex(8) :: c
character(len=40) :: s
integer :: ios
c = (1.25d0, -3.5d0)
a = (0.5, 2.0)
print '(2f8.3)', c
print '(a, 2es12.4, a)', '[', a, ']'
write (s, '(2f10.4)') c
print *, trim(s)
read (s, *, iostat=ios) a
print *, ios, a
s = '(7.5, -1e2)'
read (s, *) c
print *, c
read (s, *) a
print *, a
end
