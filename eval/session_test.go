package eval

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// runCell runs one cell and returns what it wrote to standard output and error.
func runCell(t *testing.T, s *Session, src string) (out, errOut string, st Stats, err error) {
	t.Helper()
	var o, e bytes.Buffer
	st, err = s.Run(context.Background(), src, &o, &e)
	return o.String(), e.String(), st, err
}

func mustRun(t *testing.T, s *Session, src string) string {
	t.Helper()
	out, _, _, err := runCell(t, s, src)
	if err != nil {
		t.Fatalf("cell %q: %v", src, err)
	}
	return out
}

// With ImplicitTyping, a variable the implicit rules type gets storage like a
// declared one: in a main program, as a local of a procedure and as a dummy
// argument (i-n integer, the rest real). Without it, or under implicit none, the
// same source is refused, and the refusal says so (Error.Refused).
func TestSessionImplicitTyping(t *testing.T) {
	src := "program p\nn = 6\nx = 0.5\nprint *, n * 7, x * 4\ncall s(n)\ncontains\nsubroutine s(k)\nm = k + 1\nprint *, m\nend subroutine\nend program\n"
	s := NewSession(SessionOptions{ImplicitTyping: true})
	out := mustRun(t, s, src)
	if f := strings.Fields(out); len(f) != 3 || f[0] != "42" || f[1] != "2.00000000" || f[2] != "7" {
		t.Fatalf("implicit typing: %q", out)
	}
	for name, s := range map[string]*Session{
		"implicit typing off": NewSession(SessionOptions{}),
		"implicit none":       NewSession(SessionOptions{ImplicitTyping: true}),
	} {
		src := src
		if name == "implicit none" {
			src = strings.Replace(src, "program p\n", "program p\nimplicit none\n", 1)
		}
		_, _, _, err := runCell(t, s, src)
		var e *Error
		if !errors.As(err, &e) || !e.Refused {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
	}
	// a failure while running is not a refusal
	_, _, _, err := runCell(t, NewSession(SessionOptions{}), "integer :: a(3), i\ni = 4\na(i) = 1\n")
	var e *Error
	if !errors.As(err, &e) || e.Refused {
		t.Errorf("a subscript out of range: %v, want a run-time failure", err)
	}
}

// A cell may be a main program without a PROGRAM statement that has internal
// procedures: statements, CONTAINS, the procedures, END (the shape of most
// gfortran.dg run tests). It runs like the same program with the statement.
func TestSessionImplicitMainWithContains(t *testing.T) {
	s := NewSession(SessionOptions{})
	out := mustRun(t, s, "integer :: n\nn = 2\ncall s(n)\nprint *, n\ncontains\nsubroutine s(k)\ninteger :: k\nk = k * 21\nend subroutine\nend\n")
	if strings.TrimSpace(out) != "42" {
		t.Fatalf("an implicit main with an internal subroutine: %q, want 42", out)
	}
}

func TestSessionVariablePersists(t *testing.T) {
	s := NewSession(SessionOptions{})
	mustRun(t, s, "integer :: n = 40\nreal(8) :: x\nx = 1.5d0\n")
	if got := mustRun(t, s, "n = n + 2\nprint *, n\n"); strings.TrimSpace(got) != "42" {
		t.Fatalf("n = %q, want 42", got)
	}
	if v, ok := s.Get("n"); !ok || v != int32(42) {
		t.Fatalf("Get(n) = %v, %v; want int32(42)", v, ok)
	}
	if v, ok := s.Get("x"); !ok || v != 1.5 {
		t.Fatalf("Get(x) = %v, %v; want 1.5", v, ok)
	}
	if _, ok := s.Get("nope"); ok {
		t.Fatal("Get of an unknown name succeeded")
	}
}

func TestSessionProcedureAndModulePersist(t *testing.T) {
	s := NewSession(SessionOptions{})
	mustRun(t, s, `module geom
  implicit none
  integer :: calls = 0
contains
  function area(r) result(a)
    real(8), intent(in) :: r
    real(8) :: a
    calls = calls + 1
    a = 3.0d0 * r * r
  end function
end module
`)
	mustRun(t, s, "subroutine twice(x)\n  integer, intent(inout) :: x\n  x = 2 * x\nend subroutine\n")
	got := mustRun(t, s, "use geom\ninteger :: k = 21\ncall twice(k)\nprint *, k, area(2.0d0), calls\n")
	want := "          42   12.000000000000000                1\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// a later cell reads the module's variable that the earlier call changed
	got = mustRun(t, s, "use geom\nprint *, calls\n")
	if strings.TrimSpace(got) != "1" {
		t.Fatalf("calls = %q, want 1", got)
	}
}

func TestSessionProgramInACell(t *testing.T) {
	s := NewSession(SessionOptions{})
	got := mustRun(t, s, "program hello\n  print *, 'hello'\nend program\n")
	if strings.TrimSpace(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionRedefinitionShadows(t *testing.T) {
	s := NewSession(SessionOptions{})
	mustRun(t, s, "integer function f()\n  f = 1\nend function\n")
	mustRun(t, s, "integer :: a\na = f()\n")
	mustRun(t, s, "integer function f()\n  f = 2\nend function\n")
	if got := mustRun(t, s, "print *, f(), a\n"); strings.Fields(got)[0] != "2" || strings.Fields(got)[1] != "1" {
		t.Fatalf("got %q, want f()=2 and a=1 (earlier values keep their binding)", got)
	}
}

func TestSessionStopEndsTheCellNotTheSession(t *testing.T) {
	s := NewSession(SessionOptions{})
	mustRun(t, s, "integer :: n = 5\n")
	out, errOut, st, err := runCell(t, s, "print *, 'before'\nstop 3\nprint *, 'after'\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "after") || !strings.Contains(out, "before") || st.Exit != 3 || !strings.Contains(errOut, "STOP 3") {
		t.Fatalf("out %q err %q exit %d", out, errOut, st.Exit)
	}
	if got := mustRun(t, s, "print *, n\n"); strings.TrimSpace(got) != "5" {
		t.Fatalf("the session lost n: %q", got)
	}
}

func TestSessionErrorsArePositionedAndLeaveNothingBehind(t *testing.T) {
	s := NewSession(SessionOptions{})
	mustRun(t, s, "integer :: ok = 1\n")
	_, _, _, err := runCell(t, s, "integer :: bad\nbad = undefined_name + 1\n")
	var ee *Error
	if !errors.As(err, &ee) || !strings.Contains(ee.Pos, "cell2.f90:2") {
		t.Fatalf("error %v, want a position in cell2.f90 line 2", err)
	}
	// a refused cell is not defined: its name is unknown to the next cell
	if _, _, _, err := runCell(t, s, "print *, bad\n"); err == nil {
		t.Fatal("a variable of a refused cell is visible")
	}
	// an evaluator refusal (not an analysis error) is positioned too and leaves the session usable
	_, _, _, err = runCell(t, s, "complex :: z\nz = (1.0, 2.0)\n")
	if err == nil {
		t.Skip("complex variables run now")
	}
	if got := mustRun(t, s, "print *, ok\n"); strings.TrimSpace(got) != "1" {
		t.Fatalf("session unusable after a refusal: %q", got)
	}
}

func TestSessionRuntimeErrorKeepsState(t *testing.T) {
	s := NewSession(SessionOptions{})
	mustRun(t, s, "real(8) :: v(3)\ninteger :: i = 7\n")
	_, _, _, err := runCell(t, s, "i = 9\nv(i) = 1.0d0\n")
	var ee *Error
	if !errors.As(err, &ee) || !strings.Contains(ee.Msg, "outside 1:3") {
		t.Fatalf("error %v", err)
	}
	if got := mustRun(t, s, "print *, i\n"); strings.TrimSpace(got) != "9" {
		t.Fatalf("i = %q, want 9: a failed cell keeps what it had done", got)
	}
}

func TestSessionStepBoundRefusesAnInfiniteLoop(t *testing.T) {
	s := NewSession(SessionOptions{MaxSteps: 1 << 20})
	for name, src := range map[string]string{
		"do forever": "do\nend do\n",
		"do while":   "logical :: t = .true.\ndo while (t)\nend do\n",
		"counted":    "integer :: i, j\ndo i = 1, 1000000000\n  j = i\nend do\n",
		"recursion":  "recursive integer function f(n) result(r)\n  integer, intent(in) :: n\n  r = n\n  if (n > 0) r = f(n-1) + f(n-1)\nend function\n",
	} {
		t.Run(name, func(t *testing.T) {
			if name == "recursion" {
				mustRun(t, s, src)
				src = "integer :: r\nr = f(60)\n"
			}
			start := time.Now()
			_, _, _, err := runCell(t, s, src)
			var ee *Error
			if !errors.As(err, &ee) || !strings.Contains(ee.Msg, "step limit") {
				t.Fatalf("error %v, want the step limit", err)
			}
			if d := time.Since(start); d > 20*time.Second {
				t.Fatalf("the refusal took %v", d)
			}
		})
	}
	// the session is intact afterwards
	if got := mustRun(t, s, "print *, 1\n"); strings.TrimSpace(got) != "1" {
		t.Fatal(got)
	}
}

func TestSessionContextEndsACell(t *testing.T) {
	s := NewSession(SessionOptions{MaxSteps: -1})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var o bytes.Buffer
	_, err := s.Run(ctx, "do\nend do\n", &o, &o)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v, want the deadline", err)
	}
}

func TestSessionOutputIsPerCell(t *testing.T) {
	s := NewSession(SessionOptions{})
	a := mustRun(t, s, "print *, 'one'\n")
	b := mustRun(t, s, "write (*, '(a)', advance='no') 'two'\n")
	// a record left open at the end of a cell ends with it
	if strings.TrimSpace(a) != "one" || b != "two\n" {
		t.Fatalf("a %q b %q", a, b)
	}
}

func TestSessionDerivedTypeAcrossCells(t *testing.T) {
	s := NewSession(SessionOptions{})
	mustRun(t, s, "type point\n  real(8) :: x, y\nend type\n")
	got := mustRun(t, s, "type(point) :: p\np%x = 3.0d0\np%y = 4.0d0\nprint *, sqrt(p%x**2 + p%y**2)\n")
	if strings.TrimSpace(got) != "5.0000000000000000" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionArraysAcrossCells(t *testing.T) {
	s := NewSession(SessionOptions{})
	mustRun(t, s, "real(8) :: v(4)\nreal(8), allocatable :: w(:)\ninteger :: k(3)\ninteger :: i\nk = [1, 2, 3]\n")
	// an initialised array has its value, in its cell and in the next ones
	mustRun(t, s, "integer :: ini(2) = [1, 2]\ninteger, parameter :: pa(2) = [30, 40]\n")
	if got := mustRun(t, s, "print *, ini + pa\n"); strings.Fields(got)[0] != "31" || strings.Fields(got)[1] != "42" {
		t.Fatalf("initialised arrays across cells: %q", got)
	}
	mustRun(t, s, "do i = 1, 4\n  v(i) = i * 1.5d0\nend do\nallocate(w(2))\nw = 2.0d0\nk(2) = 20\n")
	got := mustRun(t, s, "print *, sum(v), sum(w), k\n")
	want := "   15.000000000000000        4.0000000000000000                1          20           3\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// a procedure of a cell reaches the arrays of the session, through the host
	mustRun(t, s, "subroutine bump()\n  integer :: j\n  do j = 1, 4\n    v(j) = v(j) + 1.0d0\n  end do\n  k(1) = k(1) + 100\nend subroutine\n")
	got = mustRun(t, s, "call bump()\nprint *, sum(v), k(1)\n")
	if f := strings.Fields(got); len(f) != 2 || f[0] != "19.000000000000000" || f[1] != "101" {
		t.Fatalf("got %q", got)
	}
}

// A host-associated array (an internal procedure reading and writing the array
// of its program) runs: it was refused before sessions, whose variables are a
// module's, needed it.
func TestHostAssociatedArray(t *testing.T) {
	src := "program p\n  implicit none\n  real(8) :: a(3)\n  a = 0d0\n  call s()\n  print *, a(1) + a(3)\ncontains\n  subroutine s()\n    a(1) = 1d0\n    a(3) = 2d0\n  end subroutine s\nend program p\n"
	prog, err := compile(t, src)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := prog.Run(&out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "3.0000000000000000" {
		t.Fatalf("got %q", got)
	}
}

// A cell that runs again (the kernel re-runs a stale cell in the live session)
// defines a new version: the cells after it see the newest, and a name defined
// twice is not ambiguous.
func TestSessionRerunShadows(t *testing.T) {
	s := NewSession(SessionOptions{})
	mustRun(t, s, "integer :: n = 1\n")
	mustRun(t, s, "integer :: n = 2\n")
	if got := mustRun(t, s, "print *, n\n"); strings.TrimSpace(got) != "2" {
		t.Fatalf("n = %q, want 2", got)
	}
	mustRun(t, s, "module m\n  integer :: v = 10\nend module\n")
	mustRun(t, s, "module m\n  integer :: v = 20\nend module\n")
	if got := mustRun(t, s, "use m\nprint *, v\n"); strings.TrimSpace(got) != "20" {
		t.Fatalf("v = %q, want 20", got)
	}
}
