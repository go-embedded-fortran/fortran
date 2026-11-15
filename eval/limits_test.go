package eval

import (
	"context"
	"io"
	"strings"
	"testing"
)

func cellErr(t *testing.T, s *Session, src string) error {
	t.Helper()
	_, err := s.Run(context.Background(), src, io.Discard, io.Discard)
	return err
}

func memErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "memory limit exceeded")
}

// The budget counts what the session holds: two arrays of 2^25 int64 (256 MiB
// each, and each one alone under a 512 MiB budget) are refused at the second,
// before it is allocated. A check reduced to "this allocation alone is over the
// budget" lets both through, which is what these cases are for.
func TestBudgetCountsAccumulation(t *testing.T) {
	const budget = 512 << 20
	for name, src := range map[string]string{
		"two 2^25 arrays": "integer, allocatable :: a(:), b(:)\nallocate(a(2**25))\nallocate(b(2**25))\n",
		"three 2^24 arrays and one 2^25": "integer, allocatable :: a(:), b(:), c(:), d(:)\n" +
			"allocate(a(2**24))\nallocate(b(2**24))\nallocate(c(2**24))\nallocate(d(2**25))\n",
		"local arrays of one cell": "integer :: a(2**25), b(2**25)\n",
		"real(8) arrays":           "real(8), allocatable :: a(:,:), b(:,:)\nallocate(a(2**12, 2**13))\nallocate(b(2**12, 2**13))\n",
	} {
		s := NewSession(SessionOptions{MaxAlloc: budget})
		if err := cellErr(t, s, src); !memErr(err) {
			t.Errorf("%s: %v, want a memory limit error", name, err)
		}
		if err := cellErr(t, s, "integer :: k = 6\nprint *, k * 7\n"); err != nil {
			t.Errorf("%s: the session after the refusal: %v", name, err)
		}
	}
	// the one that fits is not refused
	s := NewSession(SessionOptions{MaxAlloc: budget})
	if err := cellErr(t, s, "integer, allocatable :: a(:)\nallocate(a(2**25))\na(1) = 1\n"); err != nil {
		t.Errorf("one 256 MiB array under a 512 MiB budget: %v", err)
	}
	// and it counts across cells: the first cell's array is still held
	if err := cellErr(t, s, "integer, allocatable :: b(:), c(:)\nallocate(b(2**24))\nallocate(c(2**24))\n"); !memErr(err) {
		t.Errorf("the second cell's arrays: %v", err)
	}
}

// Many small objects count as much as one big array: 100000 objects of a type
// with an array component of 8000 real(8) (64000 bytes, under any per-allocation
// threshold) are 6.4 GB; and the same with a character component of 1 MiB.
func TestBudgetCountsManySmallObjects(t *testing.T) {
	for name, src := range map[string]string{
		"array component":     "type :: t\nreal(8) :: x(8000)\nend type\ntype(t) :: a(100000)\n",
		"character component": "type :: u\ncharacter(len=1048576) :: c\nend type\ntype(u) :: a(2000)\n",
		"nested":              "type :: t\nreal(8) :: x(8000)\nend type\ntype :: w\ntype(t) :: p\nend type\ntype(w) :: a(100000)\n",
		"character variables": "character(len=33554432) :: a, b, c, d, e, f, g, h, i, j, k, l, m, n, o, p, q\n",
	} {
		s := NewSession(SessionOptions{MaxAlloc: 512 << 20})
		if err := cellErr(t, s, src); !memErr(err) {
			t.Errorf("%s: %v, want a memory limit error", name, err)
		}
		if err := cellErr(t, s, "print *, 1\n"); err != nil {
			t.Errorf("%s: the session after: %v", name, err)
		}
	}
}

// What is let go is credited: deallocate, the return of a procedure that held
// arrays, and a temporary that lives for a statement. A loop that makes and
// drops 256 MiB at a time, 40 times, stays under a 512 MiB budget.
func TestBudgetCreditsWhatIsLetGo(t *testing.T) {
	s := NewSession(SessionOptions{MaxAlloc: 512 << 20})
	var out strings.Builder
	run := func(src string) error {
		out.Reset()
		_, err := s.Run(context.Background(), src, &out, io.Discard)
		return err
	}
	if err := run("integer, allocatable :: a(:)\ninteger :: i, t\nt = 0\ndo i = 1, 40\n  allocate(a(2**25))\n  a(1) = 1\n  t = t + a(1)\n  deallocate(a)\nend do\nprint *, t\n"); err != nil || strings.TrimSpace(out.String()) != "40" {
		t.Errorf("deallocate in a loop: %q, %v", out.String(), err)
	}
	// a procedure's local arrays go back when it returns
	if err := run("module m\ncontains\nsubroutine f(s)\ninteger :: s\ninteger :: w(2**25)\nw(1) = 1\ns = s + w(1)\nend subroutine\nend module\n"); err != nil {
		t.Fatal(err)
	}
	if err := run("use m\ninteger :: i, t\nt = 0\ndo i = 1, 40\n  call f(t)\nend do\nprint *, t\n"); err != nil || strings.TrimSpace(out.String()) != "40" {
		t.Errorf("a procedure's locals in a loop: %q, %v", out.String(), err)
	}
	// an allocatable of a module, allocated by a procedure, is the module's: it
	// stays charged when the procedure returns
	if err := run("module g\ninteger, allocatable :: big(:), more(:)\ncontains\nsubroutine take()\nallocate(big(2**25))\nallocate(more(2**24))\nend subroutine\nend module\n"); err != nil {
		t.Fatal(err)
	}
	if err := run("use g\ncall take()\n"); err != nil {
		t.Fatal(err)
	}
	if err := run("integer, allocatable :: c(:)\nallocate(c(2**25))\n"); !memErr(err) {
		t.Errorf("the module's arrays were forgotten when the procedure returned: %v", err)
	}
	// an array that takes a new shape on assignment gives the old one back
	s = NewSession(SessionOptions{MaxAlloc: 512 << 20})
	if err := run("integer, allocatable :: a(:)\ninteger :: i, k\ndo i = 1, 40\n  a = [(0, k = 1, 2**24 + i)]\nend do\nprint *, size(a)\n"); err != nil || strings.TrimSpace(out.String()) != "16777256" {
		t.Errorf("reassignment to a new shape: %q, %v", out.String(), err)
	}
}

// A cell that fails part-way leaves the next cell a correct count.
func TestBudgetSurvivesAFailedCell(t *testing.T) {
	s := NewSession(SessionOptions{MaxAlloc: 512 << 20})
	for i := 0; i < 5; i++ {
		if err := cellErr(t, s, "integer, allocatable :: a(:), b(:)\nallocate(a(2**25))\nallocate(b(2**25))\n"); !memErr(err) {
			t.Fatalf("round %d: %v", i, err)
		}
	}
	// the first cell kept its array (a failed cell keeps what it had done: it is
	// held, and counted), the four others left nothing
	if err := cellErr(t, s, "integer, allocatable :: z(:)\nallocate(z(2**24))\n"); err != nil {
		t.Errorf("a cell that fits after five that did not: %v", err)
	}
}

// Without the option nothing is counted: the default is the evaluator's own
// per-array bound.
func TestBudgetZeroIsNoBound(t *testing.T) {
	s := NewSession(SessionOptions{})
	if err := cellErr(t, s, "integer, allocatable :: a(:)\nallocate(a(2**20))\nprint *, size(a)\n"); err != nil {
		t.Errorf("%v", err)
	}
}
