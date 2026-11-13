program real_kinds
  use iso_fortran_env, only: real32, real64
  implicit none
  real(real32) :: s
  real(real64) :: d
  s = 1.0_real32 / 3.0_real32
  d = 1.0_real64 / 3.0_real64
  print *, s
  print *, d
  print *, kind(s), kind(d), precision(s), precision(d), range(s), range(d)
  print *, epsilon(s), epsilon(d)
  print *, tiny(s), tiny(d)
  print *, huge(s), huge(d)
  print *, digits(s), digits(d), radix(s), minexponent(d), maxexponent(d)
  print *, sqrt(2.0_real32), sqrt(2.0_real64)
  print *, real(1, real64) / 4, dble(s)
  print *, 0.1_real32 + 0.2_real32, 0.1_real64 + 0.2_real64
  print *, 1.0e10_real64, 1.0e-10_real64, 123456789.0_real64
  print *, aint(2.7_real64), anint(2.5_real64), nint(1.5_real32)
  print *, fraction(8.0_real64), exponent(8.0_real64), scale(1.0_real64, 4), spacing(1.0_real32)
  print *, sign(2.0_real64, -0.0_real64), abs(-3.5_real32)
end program real_kinds
