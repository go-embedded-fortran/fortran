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

// A derived-type copy is charged to the owner of its destination: a module's
// array, copied into by a procedure, stays charged when the procedure returns,
// and the counter ends at what the module holds. 1000 copies of a 32 MiB array
// into a module's objects are refused once the budget is passed, not counted
// as zero.
func TestBudgetChargesTheOwnerOfACopy(t *testing.T) {
	s := NewSession(SessionOptions{MaxAlloc: 512 << 20})
	if err := cellErr(t, s, "module m\ntype :: t\nreal(8), allocatable :: x(:)\nend type\ntype(t) :: keep(1000)\ncontains\nsubroutine put(i)\ninteger :: i\ntype(t) :: tmp\nallocate(tmp%x(2**22))\nkeep(i) = tmp\nend subroutine\nend module\n"); err != nil {
		t.Fatal(err)
	}
	err := cellErr(t, s, "use m\ninteger :: i\ndo i = 1, 1000\n  call put(i)\nend do\n")
	if !memErr(err) {
		t.Fatalf("1000 x 32 MiB held by a module: %v, want a memory limit error", err)
	}
	// what was copied before the refusal is the module's, and counted
	if got := held(s.mods); got < 256<<20 {
		t.Errorf("the module holds %d bytes after the copies, want at least 256 MiB", got)
	}
}

// A credit that gives back more than was charged is an error of the evaluator,
// reported, never clamped to zero.
func TestBudgetUnderflowIsReported(t *testing.T) {
	b := newBudget(nil, 0)
	b.mem = 100
	own := &frame{mem: 5}
	defer func() {
		e, ok := recover().(*Error)
		if !ok || !strings.Contains(e.Msg, "below zero") {
			t.Errorf("recovered %v, want the underflow error", e)
		}
	}()
	b.credit(&frame{}, own, 10)
	t.Error("a credit of more than the frame holds went through")
}

// The temporaries of one statement are alive together, so they are summed:
// (a+1)+((a+2)+((a+3)+...)) holds one temporary for each level, and a
// statement of 16 levels over a 32 MiB array passes 512 MiB though each
// temporary alone fits. The same statement over a small array is fine, and so
// is the next statement: the reservation ends with the statement.
func TestBudgetSumsTheTemporariesOfAStatement(t *testing.T) {
	expr := func(levels int) string {
		e := "(a+" + "99" + ")"
		for i := levels; i > 0; i-- {
			e = "((a+" + strings.Repeat("1", i) + ")+" + e + ")"
		}
		return e
	}
	s := NewSession(SessionOptions{MaxAlloc: 512 << 20})
	if err := cellErr(t, s, "integer :: a(2**22), b(2**22)\na = 1\n"); err != nil {
		t.Fatal(err)
	}
	if err := cellErr(t, s, "b = "+expr(16)+"\n"); !memErr(err) {
		t.Errorf("16 live temporaries of 32 MiB: %v", err)
	}
	// the budget is back: a statement of two temporaries runs, and runs again
	for i := 0; i < 3; i++ {
		if err := cellErr(t, s, "b = "+expr(2)+"\n"); err != nil {
			t.Errorf("a statement of two temporaries, round %d: %v", i, err)
		}
	}
}

// Character results are temporaries too: a chain of // over 16 MiB variables, a
// REPEAT of 64 MiB beside another, and ADJUSTL.
func TestBudgetCountsCharacterTemporaries(t *testing.T) {
	s := NewSession(SessionOptions{MaxAlloc: 128 << 20})
	for name, src := range map[string]string{
		"concatenations": "character(len=2**22) :: s, r\nr = " + strings.Repeat("s//", 40) + "s\n",
		"two repeats":    "character(len=1) :: c\nc = 'x'\nprint *, len(repeat(c, 2**25)//repeat(c, 2**25)//repeat(c, 2**25)//repeat(c, 2**25))\n",
		"adjustl chain":  "character(len=2**24) :: s, r\nr = " + strings.Repeat("adjustl(", 9) + "s" + strings.Repeat(")", 9) + "\n",
	} {
		if err := cellErr(t, s, src); !memErr(err) {
			t.Errorf("%s: %v, want a memory limit error", name, err)
		}
	}
	if err := cellErr(t, s, "character(len=1) :: c\nc = 'x'\nprint *, len(repeat(c, 2**24))\n"); err != nil {
		t.Errorf("one 16 MiB REPEAT: %v", err)
	}
}

// An output statement does not build the text of an array it could not print:
// 4 million values would cost 192 MiB of values in a 100 MiB budget, but the cap
// of 1000 bytes needs 501 of them, and the statement runs.
func TestPrintStopsAtTheOutputCap(t *testing.T) {
	s := NewSession(SessionOptions{MaxAlloc: 100 << 20, MaxOutput: 1000})
	var out strings.Builder
	_, err := s.Run(context.Background(), "integer :: a(2**22), i\na = 7\nprint *, a\nprint *, (i, i = 1, 100000000)\n", &out, io.Discard)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if out.Len() == 0 || out.Len() > 20000 {
		t.Errorf("the output is %d bytes", out.Len())
	}
	// without the cap the same statement needs the values, and the budget says no
	s = NewSession(SessionOptions{MaxAlloc: 100 << 20})
	if err := cellErr(t, s, "integer :: a(2**22)\na = 7\nprint *, a\n"); !memErr(err) {
		t.Errorf("an uncapped print of 4 million values in 100 MiB: %v", err)
	}
}

// A loop condition that makes an array temporary is evaluated at every
// iteration of one statement: each evaluation reserves its temporary afresh,
// not on top of the last one's. 1000 iterations of an 8 MB temporary under a
// 100 MiB budget run to completion. The same for the items of an implied-do in
// an array constructor and in an output list.
func TestBudgetResetsTheTemporariesOfARepeatedExpression(t *testing.T) {
	s := NewSession(SessionOptions{MaxAlloc: 100 << 20})
	var out strings.Builder
	run := func(src string) error {
		out.Reset()
		_, err := s.Run(context.Background(), src, &out, io.Discard)
		return err
	}
	if err := run("real(8) :: a(1000000)\ninteger :: i\na = 1\ni = 0\ndo while (sum(a + 1.0d0) > 0 .and. i < 1000)\n  i = i + 1\nend do\nprint *, i\n"); err != nil || strings.TrimSpace(out.String()) != "1000" {
		t.Errorf("do while: %q, %v", out.String(), err)
	}
	if err := run("integer :: g(1000000), n\ng = 1\nn = 0\ndo while (sum(g + 1) > 0 .and. n < 1000)\n  n = n + 1\nend do\nprint *, n\n"); err != nil || strings.TrimSpace(out.String()) != "1000" {
		t.Errorf("do while over a general array: %q, %v", out.String(), err)
	}
	if err := run("integer :: b(1000000), c(1000), j\nb = 1\nc = [(sum(b + 1), j = 1, 1000)]\nprint *, c(1000)\n"); err != nil || strings.TrimSpace(out.String()) == "" {
		t.Errorf("implied-do in a constructor: %q, %v", out.String(), err)
	}
	if err := run("real(8) :: d(1000000)\ninteger :: k\nd = 1\nprint *, (sum(d + 1.0d0), k = 1, 200)\n"); err != nil {
		t.Errorf("implied-do in an output list: %v", err)
	}
	// the reservation is still a bound: one statement that holds more at once is refused
	if err := run("real(8) :: e(4000000)\ne = 1\ne = (e+1.0d0)+((e+2.0d0)+((e+3.0d0)+((e+4.0d0)+((e+5.0d0)+((e+6.0d0)+((e+7.0d0)+(e+8.0d0)))))))\n"); !memErr(err) {
		t.Errorf("eight live 32 MB temporaries in 100 MiB: %v", err)
	}
}
