package eval

import "testing"

// TestProgramCallsTheExternalsOfItsCell pins that the PROGRAM of a cell calls
// the external subroutines and functions written in the same cell, before it
// or after it: sema puts them in the cell's module, which a PROGRAM does not
// use, so the call resolved to nothing and was refused as "only call
// cpu_time(t) is supported" (trim_1, zero_sized_4 of gfortran.dg). The
// expected line is gfortran 15.2.0's output for the source as a file.
func TestProgramCallsTheExternalsOfItsCell(t *testing.T) {
	const src = `subroutine before(n)
integer, intent(inout) :: n
n = n + 1
end subroutine
program p
integer :: k, twice
k = 1
call before(k)
call after(k)
print *, k, twice(k)
end program
subroutine after(n)
integer, intent(inout) :: n
n = n * 10
end subroutine
integer function twice(n)
integer, intent(in) :: n
twice = 2 * n
end function
`
	if out, err := runPtr(t, 100<<20, src); err != nil || out != "20 40" {
		t.Errorf("program calling the externals of its cell: %q, %v; want \"20 40\"", out, err)
	}
}
