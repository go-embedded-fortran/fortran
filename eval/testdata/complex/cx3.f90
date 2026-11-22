complex(8) :: z
complex(4) :: a
z = cmplx(z'3FF0000000000000', 0, 8)
print *, z
z = cmplx(1.5d0, z'4000000000000000', kind=8)
print *, z
a = cmplx(z'40490FDB', z'BF800000')
print *, a
z = dcmplx(z'400921FB54442D18')
print *, z
end
