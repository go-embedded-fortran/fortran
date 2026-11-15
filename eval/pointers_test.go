package eval

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func runPtr(t *testing.T, maxAlloc int64, src string) (string, error) {
	t.Helper()
	var out strings.Builder
	_, err := NewSession(SessionOptions{MaxAlloc: maxAlloc}).Run(context.Background(), src, &out, io.Discard)
	return strings.Join(strings.Fields(out.String()), " "), err
}

func TestArrayPointers(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"alias: a write through p is seen in a",
			"integer, target :: a(3)\ninteger, pointer :: p(:)\na = [1, 2, 3]\np => a\np(2) = 20\nprint *, a, associated(p), associated(p, a)\n",
			"1 20 3 T T"},
		{"real(8) target and pointer share the general representation",
			"real(8), target :: x(4)\nreal(8), pointer :: px(:)\npx => x\npx = 2.5d0\nprint *, sum(x)\n",
			"10.000000000000000"},
		{"allocate through a pointer, then deallocate",
			"integer, pointer :: q(:)\nallocate(q(4))\nq = 7\nprint *, sum(q), associated(q)\ndeallocate(q)\nprint *, associated(q)\n",
			"28 T F"},
		{"=> null() and nullify",
			"integer, target :: a(2)\ninteger, pointer :: r(:) => null()\nprint *, associated(r)\nr => a\nprint *, associated(r)\nnullify(r)\nprint *, associated(r)\n",
			"F T F"},
		{"a pointer to what a pointer points at",
			"integer, target :: a(2)\ninteger, pointer :: p(:), q(:)\np => a\nq => p\nq(1) = 5\nprint *, a(1), associated(q, a), associated(q, p)\n",
			"5 T T"},
		{"a disassociated pointer as the target",
			"integer, pointer :: p(:), q(:)\nnullify(p)\nq => p\nprint *, associated(q)\n",
			"F"},
	} {
		got, err := runPtr(t, 0, tc.src)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestArrayPointerFailures(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"deallocate of a target ALLOCATE did not make",
			"integer, target :: a(2)\ninteger, pointer :: p(:)\np => a\ndeallocate(p)\n", "did not make through a pointer"},
		{"deallocate of a disassociated pointer",
			"integer, pointer :: p(:)\nnullify(p)\ndeallocate(p)\n", "is not associated"},
		{"a reference through a disassociated pointer",
			"integer, pointer :: p(:)\nnullify(p)\nprint *, p(1)\n", "not allocated"},
	} {
		_, err := runPtr(t, 0, tc.src)
		var e *Error
		if !errors.As(err, &e) || e.Refused || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want a run-time failure saying %q", tc.name, err, tc.want)
		}
	}
	for _, tc := range []struct{ name, src string }{
		{"a scalar pointer", "integer, pointer :: s\n"},
		{"a pointer to a section", "integer, target :: a(4)\ninteger, pointer :: p(:)\np => a(1:2)\n"},
		{"a pointer initialised to a target", "integer, target :: a(4)\ninteger, pointer :: p(:) => a\n"},
	} {
		_, err := runPtr(t, 0, tc.src)
		var e *Error
		if !errors.As(err, &e) || !e.Refused {
			t.Errorf("%s: %v, want a refusal", tc.name, err)
		}
	}
}

// A target ALLOCATE makes through a pointer is the heap's: allocating the
// pointer again leaves the old one held (Fortran leaks it), so the budget sees
// them pile up; DEALLOCATE gives each back, so a loop of both does not.
func TestPointerTargetsAreChargedToTheHeap(t *testing.T) {
	const leak = "module m\ninteger, pointer :: g(:)\ncontains\nsubroutine grow()\nallocate(g(2**22))\nend subroutine\nend module\nprogram p\nuse m\ninteger :: i\ndo i = 1, 10\ncall grow()\nend do\nend program\n"
	if _, err := runPtr(t, 100<<20, leak); !memErr(err) {
		t.Errorf("ten 32 MB targets left behind under 100 MiB: %v", err)
	}
	const cycle = "integer, pointer :: p(:)\ninteger :: i\ndo i = 1, 50\nallocate(p(2**22))\np = i\ndeallocate(p)\nend do\nprint *, i\n"
	if out, err := runPtr(t, 100<<20, cycle); err != nil || out != "51" {
		t.Errorf("50 allocate/deallocate of 32 MB under 100 MiB: %q, %v", out, err)
	}
	// the target outlives the procedure that allocated it: the frame's exit
	// credits nothing for it, and the counter does not go below zero later
	const outlive = "module m2\ninteger, pointer :: g(:)\ncontains\nsubroutine mk()\nallocate(g(3))\ng = 4\nend subroutine\nend module\nprogram p\nuse m2\ncall mk()\nprint *, sum(g)\ndeallocate(g)\nprint *, associated(g)\nend program\n"
	if out, err := runPtr(t, 100<<20, outlive); err != nil || out != "12 F" {
		t.Errorf("a target that outlives its procedure: %q, %v", out, err)
	}
	// the witness that the owner is the heap and not the pointer's frame: a
	// LOCAL pointer's target escapes to a module pointer, so the procedure's
	// return must not credit it. With 32 MB still held, 96 MB more does not fit
	// in 100 MiB; without the escape (the control) it does.
	const escape = "module m3\ninteger, pointer :: g(:)\ncontains\nsubroutine mk()\ninteger, pointer :: p(:)\nallocate(p(2**22))\np = 1\ng => p\nend subroutine\nend module\nprogram q\nuse m3\ninteger, allocatable :: a(:)\ncall mk()\nallocate(a(12582912))\nend program\n"
	if _, err := runPtr(t, 100<<20, escape); !memErr(err) {
		t.Errorf("96 MB after a 32 MB target escaped its procedure, under 100 MiB: %v (the escaped target is no longer counted)", err)
	}
	control := strings.Replace(escape, "call mk()\n", "", 1)
	if _, err := runPtr(t, 100<<20, control); err != nil {
		t.Errorf("the control, 96 MB alone under 100 MiB: %v", err)
	}
}
