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
		{"a character pointer", "character(len=3), pointer :: s\n"},
		{"a pointer as the do variable", "integer, target :: j\ninteger, pointer :: i\ni => j\ndo i = 1, 2\nend do\n"},
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

func TestScalarPointers(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"both ways through a variable",
			"integer, target :: x\ninteger, pointer :: p\nx = 1\np => x\np = 5\nprint *, x\nx = 7\nprint *, p, associated(p), associated(p, x)\n",
			"5 7 T T"},
		{"an element of an array",
			"integer, target :: a(3)\ninteger, pointer :: p\na = 0\np => a(2)\np = 9\nprint *, a, associated(p, a(2)), associated(p, a(1))\n",
			"0 9 0 T F"},
		{"allocate and deallocate",
			"real(8), pointer :: r\nallocate(r)\nr = 1.5d0\nprint *, r * 2\ndeallocate(r)\nprint *, associated(r)\n",
			"3.0000000000000000 F"},
		{"a pointer to what a pointer points at, logical and real(4)",
			"logical, target :: b\nlogical, pointer :: p, q\nreal, target :: s\nreal, pointer :: ps\nb = .false.\np => b\nq => p\nq = .true.\ns = 0.1\nps => s\nprint *, b, associated(q, b), ps\n",
			"T T 0.100000001"},
		{"nullify and => null()",
			"integer, target :: x\ninteger, pointer :: p => null()\nprint *, associated(p)\np => x\nnullify(p)\nprint *, associated(p)\n",
			"F F"},
		{"through a host's pointer in an internal procedure",
			"program h\ninteger, target :: x\ninteger, pointer :: p\nx = 1\np => x\ncall bump()\nprint *, x\ncontains\nsubroutine bump()\np = p + 41\nend subroutine\nend program\n",
			"42"},
	} {
		got, err := runPtr(t, 0, tc.src)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ name, src, want string }{
		{"a read through a disassociated pointer", "integer, pointer :: p\nnullify(p)\nprint *, p\n", "pointer p is not associated"},
		{"an assignment through a disassociated pointer", "integer, pointer :: p\nnullify(p)\np = 1\n", "pointer p is not associated"},
		{"deallocate of a variable's target", "integer, target :: x\ninteger, pointer :: p\np => x\ndeallocate(p)\n", "did not make through a pointer"},
	} {
		_, err := runPtr(t, 0, tc.src)
		var e *Error
		if !errors.As(err, &e) || e.Refused || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want a run-time failure saying %q", tc.name, err, tc.want)
		}
	}
	// ALLOCATE of a scalar pointer is the heap's too: left behind they pile up,
	// given back they do not
	if _, err := runPtr(t, 1<<20, "integer, pointer :: p\ninteger :: i\ndo i = 1, 100000\nallocate(p)\nend do\n"); !memErr(err) {
		t.Errorf("100000 scalar targets left behind under 1 MiB: %v", err)
	}
	if out, err := runPtr(t, 1<<20, "integer, pointer :: p\ninteger :: i\ndo i = 1, 100000\nallocate(p)\np = i\ndeallocate(p)\nend do\nprint *, i\n"); err != nil || out != "100001" {
		t.Errorf("100000 allocate/deallocate of a scalar under 1 MiB: %q, %v", out, err)
	}
}

// A function may return a pointer (F2018 15.6.2.2: a function result with the
// POINTER attribute). Four gfortran.dg tests reached such a
// result through its slot -1 before this was supported, and failed as internal
// errors: the safety net of pointers.go held.
func TestPointerResults(t *testing.T) {
	const prog = "module mr\ninteger, target :: x = 7\ncontains\n" +
		"function f()\ninteger, pointer :: f\nallocate(f)\nf = 42\nend function\n" +
		"function g() result(r)\ninteger, pointer :: r\nr => x\nend function\n" +
		"function ga()\ninteger, pointer :: ga(:)\nallocate(ga(3))\nga = [1, 2, 3]\nend function\n" +
		"function h()\ninteger, pointer :: h\nnullify(h)\nend function\n" +
		"end module\n"
	for _, tc := range []struct{ name, body, want string }{
		{"a scalar result as a value", "print *, f() + 1\n", "43"},
		{"p => f(), then the target ALLOCATE made is freed through p", "p => f()\nprint *, p, associated(p)\ndeallocate(p)\nprint *, associated(p)\n", "42 T F"},
		{"a result associated with a module variable", "p => g()\np = 9\nprint *, x, associated(p, x)\n", "9 T"},
		{"an array result, as a value and through q => ga()", "q => ga()\nprint *, sum(q), sum(ga()), associated(q)\n", "6 6 T"},
	} {
		src := prog + "program t\nuse mr\ninteger, pointer :: p, q(:)\n" + tc.body + "end program\n"
		got, err := runPtr(t, 0, src)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	// a reference to a function with a pointer result is a variable (F2018
	// 9.2): passed to an intent(out) dummy, the function runs once and the
	// dummy is copied out to its target (gfortran.dg ptr-func-4)
	if got, err := runPtr(t, 0, prog+"program t\nuse mr\ncall setv(g())\nprint *, x\ncontains\nsubroutine setv(v)\ninteger, intent(out) :: v\nv = 1\nend subroutine\nend program\n"); err != nil || got != "1" {
		t.Errorf("an intent(out) dummy associated with g(): %q, %v; want x = 1", got, err)
	}
	// a TARGET dummy would need the actual argument itself, not a copy
	// (gfortran.dg pointer_function_actual_2): refused rather than wrong
	if _, err := runPtr(t, 0, "program t\nreal, target :: x\ncall s3(x)\ncontains\nsubroutine s3(a)\nreal, target :: a\nend subroutine\nend program\n"); err == nil || !strings.Contains(err.Error(), "TARGET dummy argument") {
		t.Errorf("a TARGET dummy argument: %v, want a refusal", err)
	}
	_, err := runPtr(t, 0, prog+"program t\nuse mr\nprint *, h()\nend program\n")
	var e *Error
	if !errors.As(err, &e) || e.Refused || !strings.Contains(err.Error(), "disassociated pointer") {
		t.Errorf("the value of a disassociated result: %v, want a run-time failure", err)
	}
	// a target a function allocates for its result is the heap's: returned and
	// never freed, they pile up; freed through the pointer, they do not
	const big = "module mb\ncontains\nfunction mk()\ninteger, pointer :: mk(:)\nallocate(mk(2**22))\nmk = 1\nend function\nend module\n"
	if _, err := runPtr(t, 100<<20, big+"program t\nuse mb\ninteger, pointer :: q(:)\ninteger :: i\ndo i = 1, 10\nq => mk()\nend do\nend program\n"); !memErr(err) {
		t.Errorf("ten 32 MB results never freed under 100 MiB: %v", err)
	}
	if out, err := runPtr(t, 100<<20, big+"program t\nuse mb\ninteger, pointer :: q(:)\ninteger :: i\ndo i = 1, 10\nq => mk()\ndeallocate(q)\nend do\nprint *, i\nend program\n"); err != nil || out != "11" {
		t.Errorf("ten 32 MB results freed through the pointer under 100 MiB: %q, %v", out, err)
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
