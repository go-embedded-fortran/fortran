package eval

import "testing"

// TestCallOfAModuleOfTheSameCell pins that the loose statements of a cell (a
// main program without PROGRAM) call a procedure of a module the same cell
// defines (#44): the call resolved to nothing and was refused. The expected
// line is gfortran 15.2.0's output for the source as a file.
func TestCallOfAModuleOfTheSameCell(t *testing.T) {
	const src = "module m\ninteger :: n = 0\ncontains\nsubroutine inc()\nn = n + 1\nend subroutine\nsubroutine s\nprint *, 'in s'\nend subroutine\nend module\nuse m\ncall s()\ncall inc()\ncall inc()\nprint *, n\nend\n"
	if out, err := runPtr(t, 100<<20, src); err != nil || out != "in s 2" {
		t.Errorf("call of a procedure of a module of the same cell: %q, %v", out, err)
	}
}
