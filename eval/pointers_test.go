package eval

import (
	"context"
	"errors"
	"io"
	"runtime"
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

// SAVE in a procedure was accepted and ignored: the variables of a procedure
// are made anew at each call, so a counter SAVEd across two calls printed 1
// then 1 where gfortran prints 1 then 2 (found while writing the alias tests).
// It is refused in all three forms; in the main program it changes nothing.
func TestSaveInAProcedureIsRefused(t *testing.T) {
	for _, form := range []string{"integer, save :: n\n", "integer :: n\nsave n\n", "integer :: n\nsave\n"} {
		src := "program p\ncall c()\ncall c()\ncontains\nsubroutine c()\n" + form + "n = 1\nprint *, n\nend subroutine\nend program\n"
		_, err := runPtr(t, 0, src)
		var e *Error
		if !errors.As(err, &e) || !e.Refused || !strings.Contains(err.Error(), "SAVE in a procedure") {
			t.Errorf("%q: %v, want a refusal", form, err)
		}
	}
	if out, err := runPtr(t, 0, "integer, save :: n\nn = 2\nprint *, n\n"); err != nil || out != "2" {
		t.Errorf("SAVE in a cell's main program: %q, %v", out, err)
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
	// pointer assignment allocates nothing: a million reassignments between
	// two 32 MB targets in a 100 MiB budget run, and the budget still holds the
	// two targets only (one more 32 MB array fits, a 64 MB one does not)
	const reassign = "integer, target :: a(2**22), b(2**22)\ninteger, pointer :: p(:)\ninteger :: i\na = 1\nb = 2\ndo i = 1, 1000000\np => a\np => b\nend do\nprint *, sum(p(1:2))\n"
	if out, err := runPtr(t, 100<<20, reassign); err != nil || out != "4" {
		t.Errorf("a million pointer reassignments: %q, %v", out, err)
	}
	more := strings.Replace(strings.Replace(reassign, "integer :: i\n", "integer :: i\ninteger, allocatable :: c(:)\n", 1), "print *", "allocate(c(2**23))\nprint *", 1)
	if _, err := runPtr(t, 100<<20, more); !memErr(err) {
		t.Errorf("64 MB more after the reassignments (64 MB held) under 100 MiB: %v, want a refusal", err)
	}
	// an alias of a local array that goes out of scope: DEALLOCATE through it
	// is refused (ALLOCATE did not make it through a pointer), so it is never
	// credited a second time and the counter never goes below zero (which would
	// fail the cell as an internal error)
	const alias = "module ma\ninteger, pointer :: g(:)\ncontains\nsubroutine keep()\ninteger, target :: loc(4)\nloc = 3\ng => loc\nend subroutine\nend module\nprogram q\nuse ma\ncall keep()\ndeallocate(g)\nend program\n"
	if _, err := runPtr(t, 100<<20, alias); err == nil || !strings.Contains(err.Error(), "did not make through a pointer") {
		t.Errorf("DEALLOCATE through an alias of a local array: %v, want a refusal of the deallocate", err)
	}
	// the escape a review asked about: a module pointer to a LOCAL array (not
	// SAVEd) keeps it alive after the procedure returns (Go holds it, Fortran
	// calls the pointer undefined). Its charge moves to the heap instead of
	// being credited, so it stays counted: 32 MB held after the call, and 96 MB
	// more do not fit in 100 MiB; without the pointer (the control) they do.
	const dangle = "module md\ninteger, pointer :: g(:)\ncontains\nsubroutine keep()\ninteger, target :: loc(2**22)\nloc = 3\ng => loc\nend subroutine\nend module\nprogram q\nuse md\ninteger, allocatable :: c(:)\ncall keep()\nallocate(c(12582912))\nend program\n"
	if _, err := runPtr(t, 100<<20, dangle); !memErr(err) {
		t.Errorf("96 MB after a pointer kept a 32 MB local alive, under 100 MiB: %v (the kept array is no longer counted)", err)
	}
	if _, err := runPtr(t, 100<<20, strings.Replace(dangle, "g => loc\n", "", 1)); err != nil {
		t.Errorf("the control, no pointer to the local: %v", err)
	}
}

// A pointer that dies with the frame of its target holds nothing after the
// return: a conforming procedure that points a LOCAL pointer at its own 32 MB
// array, called 20 times (640 MB in all) under 100 MiB, runs.
func TestALocalPointerToALocalTargetIsCreditedOnReturn(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"array", "integer, target :: a(2**22)\ninteger, pointer :: q(:)\nq => a\nq = k\nk = q(1)\n"},
		{"element", "integer, target :: a(2**22)\ninteger, pointer :: q\nq => a(1)\nq = k\nk = q\n"},
	} {
		src := "module ml\ncontains\nsubroutine s(k)\ninteger :: k\n" + tc.body + "end subroutine\nend module\nprogram p\nuse ml\ninteger :: i, k\ndo i = 1, 20\nk = i\ncall s(k)\nend do\nprint *, k\nend program\n"
		if out, err := runPtr(t, 100<<20, src); err != nil || out != "20" {
			t.Errorf("%s: 20 calls with a local pointer to a local 32 MB array, under 100 MiB: %q, %v", tc.name, out, err)
		}
	}
}

// A scalar pointer left on a procedure's local scalar must not keep the
// procedure's frame alive uncounted: the frame holds a 32 MB array. Three
// module pointers left on the scalar of three calls, then 96 MB more, under
// 100 MiB: either the frames are counted (refused) or they are not kept (the
// pointer holds the scalar alone), and Go's live heap says which.
func TestAScalarPointerToALocalDoesNotKeepItsFrame(t *testing.T) {
	const src = "module mf\ninteger, pointer :: p1, p2, p3\ncontains\nsubroutine grab(k)\ninteger :: k\ninteger, target :: n\ninteger :: big(2**22)\nbig(1) = k\nn = k\nselect case (k)\ncase (1)\np1 => n\ncase (2)\np2 => n\ncase (3)\np3 => n\nend select\nend subroutine\nend module\nprogram q\nuse mf\ncall grab(1)\ncall grab(2)\ncall grab(3)\nprint *, p1 + p2 + p3\nend program\n"
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	s := NewSession(SessionOptions{MaxAlloc: 100 << 20})
	var out strings.Builder
	_, err := s.Run(context.Background(), src, &out, io.Discard)
	runtime.GC()
	runtime.ReadMemStats(&after)
	if err != nil || strings.TrimSpace(out.String()) != "6" {
		t.Fatalf("the three pointers: %q, %v", out.String(), err)
	}
	// the session (and the pointers in it) is alive here; ReadMemStats counts
	// what is reachable after the collection, and three frames are 96 MB
	held := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if held > 16<<20 {
		t.Errorf("three pointers to a local scalar keep %d MB alive (the frames of the calls, with their 32 MB arrays)", held>>20)
	}
	runtime.KeepAlive(s)
}

// Every path by which a pointer can outlive the frame of its target keeps the
// target counted (32 MB kept, then 96 MB more under 100 MiB is refused), and
// its control, the same program with the pointer kept local, runs.
func TestEveryEscapeKeepsTheTargetCounted(t *testing.T) {
	const tail = "end module\nprogram q\nuse me\ninteger, allocatable :: c(:)\ncall keep()\nallocate(c(12582912))\nend program\n"
	for _, tc := range []struct{ name, escape, local string }{
		// a module scalar pointer to an element of a local array holds the array
		{"element", "module me\ninteger, pointer :: g\ncontains\nsubroutine keep()\ninteger, target :: a(2**22)\na = 1\ng => a(1)\nend subroutine\n",
			"module me\ncontains\nsubroutine keep()\ninteger, target :: a(2**22)\ninteger, pointer :: g\na = 1\ng => a(1)\nend subroutine\n"},
		// a local pointer to a local array, copied into a module pointer
		{"through a local pointer", "module me\ninteger, pointer :: g(:)\ncontains\nsubroutine keep()\ninteger, target :: a(2**22)\ninteger, pointer :: q(:)\na = 1\nq => a\ng => q\nend subroutine\n",
			"module me\ncontains\nsubroutine keep()\ninteger, target :: a(2**22)\ninteger, pointer :: q(:), g(:)\na = 1\nq => a\ng => q\nend subroutine\n"},
		// a function's pointer result to its own local array goes to the caller
		{"function result", "module me\ninteger, pointer :: g(:)\ncontains\nfunction f() result(r)\ninteger, pointer :: r(:)\ninteger, target :: a(2**22)\na = 1\nr => a\nend function\nsubroutine keep()\ng => f()\nend subroutine\n",
			"module me\ncontains\nsubroutine keep()\ninteger, target :: a(2**22)\ninteger, pointer :: r(:)\na = 1\nr => a\nend subroutine\n"},
	} {
		if _, err := runPtr(t, 100<<20, tc.escape+tail); !memErr(err) {
			t.Errorf("%s: 96 MB after a pointer kept a 32 MB local, under 100 MiB: %v, want a refusal", tc.name, err)
		}
		if _, err := runPtr(t, 100<<20, tc.local+tail); err != nil {
			t.Errorf("%s, kept local: %v", tc.name, err)
		}
	}
}

// A scalar target boxed when its frame returns keeps its last value, and two
// pointers associated with it in one call stay associated with each other.
func TestABoxedScalarKeepsItsValueAndItsAssociation(t *testing.T) {
	const src = "module mb\nreal, pointer :: p1, p2\ninteger, pointer :: i1\ncontains\nsubroutine grab()\nreal, target :: x\ninteger, target :: n\nx = 1.5\nn = 3\np1 => x\np2 => x\ni1 => n\nx = 2.5\nn = 4\nend subroutine\nend module\nprogram q\nuse mb\ncall grab()\nprint *, p1, i1, associated(p1, p2)\np2 = 7.0\nprint *, p1\nend program\n"
	out, err := runPtr(t, 100<<20, src)
	if err != nil || out != "2.50000000 4 T 7.00000000" {
		t.Errorf("boxed scalars after the return: %q, %v", out, err)
	}
}

// An INTENT(OUT) dummy of derived type gets its default initialization on
// entry (F2018 8.5.10): gfortran 15.2.0 prints 0, then 0 5.
func TestIntentOutDerivedIsDefaultInitialized(t *testing.T) {
	const src = "module m\ntype :: t\ninteger :: i = 0\ninteger :: j\nend type\ncontains\nsubroutine mk(a)\ntype(t), intent(out) :: a\nprint *, a%i\na%j = 5\nend subroutine\nend module\nprogram p\nuse m\ntype(t) :: x\nx%i = 1\nx%j = 2\ncall mk(x)\nprint *, x%i, x%j\nend program\n"
	if out, err := runPtr(t, 100<<20, src); err != nil || out != "0 0 5" {
		t.Errorf("intent(out) default initialization: %q, %v", out, err)
	}
	// an allocated allocatable component is deallocated, and a component of
	// derived type gets its own defaults: gfortran prints F 7 twice
	const nested = "module m\ntype :: u\ninteger :: k = 7\nend type\ntype :: t\ninteger, allocatable :: x(:)\ntype(u) :: s\nend type\ncontains\nsubroutine mk(a)\ntype(t), intent(out) :: a\nprint *, allocated(a%x), a%s%k\nend subroutine\nend module\nprogram p\nuse m\ntype(t) :: y\nallocate(y%x(3))\ny%s%k = 1\ncall mk(y)\nprint *, allocated(y%x), y%s%k\nend program\n"
	if out, err := runPtr(t, 100<<20, nested); err != nil || out != "F 7 F 7" {
		t.Errorf("intent(out) with an allocatable and a nested component: %q, %v", out, err)
	}
}

// TestValueDummyIsNotCopiedBack pins F2018 8.5.18: a VALUE dummy changed in
// the callee leaves the actual alone, and a result length declared from it is
// taken on entry. The expected line is gfortran 15.2.0's output.
func TestValueDummyIsNotCopiedBack(t *testing.T) {
	const src = "program p\ninteger :: n\nreal :: x\ncharacter(len=5) :: s\nn = 3\nx = 1.5\ns = 'abc'\ncall sub(n, x, s)\nprint *, n, x, s, len(f(n)), n\ncontains\nsubroutine sub(i, r, c)\ninteger, value :: i\nreal, value :: r\ncharacter(len=5), value :: c\ni = i + 1\nr = r * 2\nc = 'xyz'\nprint *, i, r, c\nend subroutine\nfunction f(k)\ninteger, value :: k\ncharacter(len=k) :: f\nk = 17\nf = ' '\nend function\nend program\n"
	if out, err := runPtr(t, 100<<20, src); err != nil || out != "4 3.00000000 xyz 3 1.50000000 abc 3 3" {
		t.Errorf("VALUE dummies: %q, %v", out, err)
	}
	// a VALUE dummy of derived type would need a copy of the object: refused
	const dt = "module m\ntype :: t\ninteger :: i\nend type\ncontains\nsubroutine s(a)\ntype(t), value :: a\na%i = 1\nend subroutine\nend module\nprogram p\nuse m\ntype(t) :: x\nx%i = 0\ncall s(x)\nprint *, x%i\nend program\n"
	if _, err := runPtr(t, 100<<20, dt); err == nil || !strings.Contains(err.Error(), "VALUE attribute") {
		t.Errorf("VALUE derived-type dummy: want a refusal, got %v", err)
	}
}

// TestAssumedLengthResultIsRefused: the length of a character(*) function
// result is the caller's; eval ran it with length 0 and printed "[] 0".
func TestAssumedLengthResultIsRefused(t *testing.T) {
	const src = "function g()\ncharacter(*) :: g\ng = \"efgh\"\nprint *, '[', g, ']', len(g)\nend function g\ncharacter(4), external :: g\nprint *, '[', g(), ']'\nend\n"
	if _, err := runPtr(t, 100<<20, src); err == nil || !strings.Contains(err.Error(), "assumed-length character function result") {
		t.Errorf("character(*) result: want a refusal, got %v", err)
	}
	// a character(*) dummy keeps the actual's length
	const dummy = "program p\ncall s('abc')\ncontains\nsubroutine s(c)\ncharacter(*) :: c\nprint *, len(c)\nend subroutine\nend program\n"
	if out, err := runPtr(t, 100<<20, dummy); err != nil || out != "3" {
		t.Errorf("character(*) dummy: %q, %v", out, err)
	}
}

// TestExplicitShapeDummyHasItsDeclaredShape: an explicit-shape dummy array is
// the actual's element sequence with the declared bounds, which may depend on
// other dummies (dependent_decls_3 of gfortran.dg). gfortran 15.2.0's output.
func TestExplicitShapeDummyHasItsDeclaredShape(t *testing.T) {
	const src = "program p\nreal :: x(8,4), w(4,2)\ninteger :: a(6)\na = [1, 2, 3, 4, 5, 6]\nw = 0\ncall sub(x, w)\nprint *, sum(w), shape(w)\ncall two(a)\ncall low(a, 5)\ncontains\nsubroutine sub(v, d)\nreal, intent(in) :: v(:,:)\nreal, intent(out), dimension(size(v,dim=1)/2, size(v,dim=2)/2) :: d\nd = 1.0\nprint *, shape(d)\nend subroutine\nsubroutine two(b)\ninteger :: b(2,3)\nprint *, b(2,3), b(1,2), shape(b)\nend subroutine\nsubroutine low(b, n)\ninteger :: n\ninteger :: b(0:n-1)\nprint *, b(0), b(n-1), lbound(b), ubound(b)\nend subroutine\nend program\n"
	if out, err := runPtr(t, 100<<20, src); err != nil || out != "4 2 8.00000000 4 2 6 3 2 3 1 5 0 4" {
		t.Errorf("explicit-shape dummies: %q, %v", out, err)
	}
	// a dummy larger than its actual is stopped, not read past the storage
	const big = "program p\ninteger :: a(3)\na = 1\ncall s(a, 4)\ncontains\nsubroutine s(b, n)\ninteger :: n\ninteger :: b(n)\nprint *, b(n)\nend subroutine\nend program\n"
	if _, err := runPtr(t, 100<<20, big); err == nil || !strings.Contains(err.Error(), "more than the 3 of its actual") {
		t.Errorf("dummy larger than its actual: want a stop, got %v", err)
	}
}

// TestBozRealIsTheBitPattern pins REAL/DBLE of a BOZ constant to its bits
// (F2018 16.3.3); the expected line is gfortran 15.2.0's output.
func TestBozRealIsTheBitPattern(t *testing.T) {
	const src = "program p\nreal :: r\ndouble precision :: d\nd = dble(z\"400921FB54411744\")\nr = real(z\"40490FDB\")\nprint *, d == 3.1415926535_8, r == 3.1415927\nprint *, real(b\"00111111100000000000000000000000\"), dble(o\"0\")\nend program\n"
	if out, err := runPtr(t, 100<<20, src); err != nil || out != "T T 1.00000000 0.0000000000000000" {
		t.Errorf("BOZ as real: %q, %v", out, err)
	}
	// INT keeps the integer value, and REAL of a non-BOZ argument converts
	const other = "program p\nprint *, int(z\"10\"), real(3)\nend program\n"
	if out, err := runPtr(t, 100<<20, other); err != nil || out != "16 3.00000000" {
		t.Errorf("INT of BOZ and REAL of an integer: %q, %v", out, err)
	}
	// INT reads the bits as an integer of its kind (gfortran 15.2.0's line)
	const ints = "program p\nprint *, int(z'FFFFFFFF', 4), int(z'FB2E', 2), int(z'FF', 1), int(z'7F', 1), int(z'FFFFFFFFFFFFFFFF', 8)\nend program\n"
	if out, err := runPtr(t, 100<<20, ints); err != nil || out != "-1 -1234 -1 127 -1" {
		t.Errorf("INT of BOZ: %q, %v", out, err)
	}
	if _, err := runPtr(t, 100<<20, "program p\nprint *, int(z'1FF', 1)\nend program\n"); err == nil || !strings.Contains(err.Error(), "wider than INTEGER(1)") {
		t.Errorf("INT of a BOZ wider than its kind: want a refusal, got %v", err)
	}
	// what is not modelled is refused, never truncated or guessed
	for src, want := range map[string]string{
		"program p\nprint *, real(z\"1FFFFFFFF\")\nend program\n":          "wider than REAL(4)",
		"program p\nprint *, dble(z\"1FFFFFFFFFFFFFFFFF\")\nend program\n": "wider than 64 bits",
		"program p\nprint *, real(z\"3F800000\", 16)\nend program\n":       "REAL(16) is not supported",
	} {
		if _, err := runPtr(t, 100<<20, src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want a refusal containing %q, got %v", src, want, err)
		}
	}
}

// A type whose bindings act implicitly (FINAL, a type-bound defined assignment
// or operator, derived-type I/O) is refused: ignoring them gives wrong results
// silently (derived_init_7.f90 counted no finalization and stopped on 3).
func TestImplicitTypeBoundProceduresAreRefused(t *testing.T) {
	for _, b := range []string{"final :: fin", "generic :: assignment(=) => fin", "generic :: operator(+) => fin", "generic :: write(formatted) => fin"} {
		src := "module m\ntype :: t\ninteger :: i = 0\ncontains\n" + b + "\nend type\ncontains\nsubroutine fin(a)\ntype(t), intent(inout) :: a\nend subroutine\nend module\nprogram p\nuse m\ntype(t) :: x\nx%i = 1\nprint *, x%i\nend program\n"
		if _, err := runPtr(t, 100<<20, src); err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Errorf("%s: %v, want a refusal", b, err)
		}
	}
}

// A second DEALLOCATE of a target, through another pointer, says so.
func TestASecondDeallocateSaysAlreadyDeallocated(t *testing.T) {
	for _, src := range []string{
		"integer, pointer :: p(:), q(:)\nallocate(p(3))\nq => p\ndeallocate(p)\ndeallocate(q)\n",
		"integer, pointer :: p, q\nallocate(p)\nq => p\ndeallocate(p)\ndeallocate(q)\n",
	} {
		if _, err := runPtr(t, 100<<20, src); err == nil || !strings.Contains(err.Error(), "already deallocated") {
			t.Errorf("%q: %v, want already deallocated", src, err)
		}
	}
}
