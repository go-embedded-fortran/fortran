! An integer(8) array compared with a complex(4) one: the integer is
! converted to complex(4) (F2018 10.1.5.5.1, Table 10.2), so 2**24+1 equals
! 2**24 in single precision.
program ca6
  implicit none
  integer(8) :: i(2)
  complex(4) :: c(2)
  i = [16777217_8, 3_8]
  c = [(16777216.0, 0.0), (3.0, 0.0)]
  print *, i == c
  print *, c /= i
end program ca6
