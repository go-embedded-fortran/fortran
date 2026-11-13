program integer_kinds
  use iso_fortran_env, only: int8, int16, int32, int64
  implicit none
  integer(int8) :: a
  integer(int16) :: b
  integer(int32) :: c
  integer(int64) :: d
  a = huge(a)
  b = huge(b)
  c = huge(c)
  d = huge(d)
  print *, a, b, c, d
  print *, -huge(a) - 1_int8, -huge(d) - 1_int64
  print *, kind(a), kind(b), kind(c), kind(d)
  print *, bit_size(a), bit_size(b), bit_size(c), bit_size(d)
  print *, selected_int_kind(2), selected_int_kind(4), selected_int_kind(9), selected_int_kind(18), selected_int_kind(19)
  print *, 7/2, -7/2, mod(7, 3), mod(-7, 3), modulo(-7, 3), 7**3
  print *, iand(12, 10), ior(12, 10), ieor(12, 10), not(0), ishft(1, 5), ishft(256, -4)
  print *, popcnt32(255_int32), leadz(1_int32), trailz(8_int32)
  print *, int(3.99), int(-3.99), nint(2.5), nint(-2.5), floor(-0.5), ceiling(-0.5)
  print *, abs(-5_int16), sign(3, -1), max(1, 9, 4), min(8, 2, 6)
contains
  integer function popcnt32(x)
    integer(int32), intent(in) :: x
    popcnt32 = popcnt(x)
  end function popcnt32
end program integer_kinds
