program stderr_output
  use iso_fortran_env, only: error_unit, output_unit
  implicit none
  write (output_unit, '(a)') 'to stdout'
  write (error_unit, '(a)') 'to stderr'
  write (error_unit, *) 'list-directed to stderr', 12
  write (output_unit, '(a,i0)') 'count ', 3
  write (0, '(a)') 'unit 0 is stderr'
end program stderr_output
