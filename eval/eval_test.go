package eval

import (
	"bytes"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/go-ndarray/ndarray"

	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

func compile(t *testing.T, src string) (*Program, error) {
	t.Helper()
	f, err := syntax.Parse("t.f90", []byte(src), nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return Compile(f, nil)
}

func run(t *testing.T, src string) (string, Stats) {
	t.Helper()
	p, err := compile(t, src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var out bytes.Buffer
	st, err := p.Run(&out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return out.String(), st
}

// first reads the first output line as a float64.
func first(t *testing.T, out string) float64 {
	t.Helper()
	l, _, _ := strings.Cut(out, "\n")
	v, err := strconv.ParseFloat(strings.TrimSpace(l), 64)
	if err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	return v
}

func TestK1(t *testing.T) {
	out, _ := run(t, `program k1
  implicit none
  integer(8) :: i, n
  real(8) :: s, t0, t1
  n = 1000_8
  s = 0d0
  call cpu_time(t0)
  do i = 1, n
    s = s + 1d0/real(i,8)**2
  end do
  call cpu_time(t1)
  print '(es24.16)', s
  print '(a,f0.6)', 'time ', t1 - t0
end program k1
`)
	want := 0.0
	for i := 1; i <= 1000; i++ {
		x := float64(i)
		want += 1 / (x * x)
	}
	if got := first(t, out); got != want {
		t.Errorf("%v, want %v (the same sequence of float64 operations)", got, want)
	}
	if !strings.Contains(out, "\ntime ") {
		t.Errorf("no time line in %q", out)
	}
	if l, _, _ := strings.Cut(out, "\n"); len(l) != 24 {
		t.Errorf("es24.16 line %q is %d wide", l, len(l))
	}
}

const jacobi = `program k2
  implicit none
  integer, parameter :: n = 9, sweeps = 3
  real(8), allocatable :: u(:,:), v(:,:)
  integer :: i, j, s
  allocate (u(n,n), v(n,n))
  do j = 1, n
    do i = 1, n
      u(i,j) = real(mod(i*7 + j*13, 17), 8)/17d0
    end do
  end do
  v = u
  do s = 1, sweeps
    do j = 2, n - 1
      do i = 2, n - 1
        v(i,j) = 0.25d0*(u(i-1,j) + u(i+1,j) + u(i,j-1) + u(i,j+1))
      end do
    end do
    do j = 2, n - 1
      do i = 2, n - 1
        u(i,j) = v(i,j)
      end do
    end do
  end do
  print '(es24.16)', sum(u)
end program k2
`

func TestJacobiColumnMajor(t *testing.T) {
	out, _ := run(t, jacobi)
	const n = 9
	u := make([][]float64, n)
	v := make([][]float64, n)
	for i := range u {
		u[i], v[i] = make([]float64, n), make([]float64, n)
	}
	for j := 1; j <= n; j++ {
		for i := 1; i <= n; i++ {
			u[i-1][j-1] = float64((i*7+j*13)%17) / 17
			v[i-1][j-1] = u[i-1][j-1]
		}
	}
	for range 3 {
		for j := 1; j < n-1; j++ {
			for i := 1; i < n-1; i++ {
				v[i][j] = 0.25 * (u[i-1][j] + u[i+1][j] + u[i][j-1] + u[i][j+1])
			}
		}
		for j := 1; j < n-1; j++ {
			for i := 1; i < n-1; i++ {
				u[i][j] = v[i][j]
			}
		}
	}
	var sum float64
	for _, r := range u {
		for _, x := range r {
			sum += x
		}
	}
	if got := first(t, out); math.Abs(got-sum) > 1e-12*sum {
		t.Errorf("%v, want %v", got, sum)
	}
}

const matmulProg = `program m
  implicit none
  integer, parameter :: n = 6
  real(8), allocatable :: a(:,:), b(:,:), c(:,:), d(:,:)
  integer :: i, j, k
  allocate (a(n,n), b(n,n), c(n,n), d(n,n))
  do j = 1, n
    do i = 1, n
      a(i,j) = real(mod(i + 2*j, 7), 8)/7d0
      b(i,j) = real(mod(3*i + j, 5), 8)/5d0
    end do
  end do
  c = matmul(a, b)
  d = 0d0
  do j = 1, n
    do k = 1, n
      do i = 1, n
        d(i,j) = d(i,j) + a(i,k)*b(k,j)
      end do
    end do
  end do
  print '(es24.16)', sum(c)
  print '(es24.16)', sum(d)
  print '(es24.16)', c(2,5) - d(2,5)
  print '(es24.16)', c(5,2) - d(5,2)
end program m
`

// matmul through go-ndarray equals the triple loop, element by element: the
// transposition (c' = b' a') is right and an asymmetric element (2,5) differs
// from (5,2).
func TestMatmulEqualsLoops(t *testing.T) {
	out, st := run(t, matmulProg)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("%q", out)
	}
	s1, _ := strconv.ParseFloat(strings.TrimSpace(lines[0]), 64)
	s2, _ := strconv.ParseFloat(strings.TrimSpace(lines[1]), 64)
	if math.Abs(s1-s2) > 1e-12*s2 {
		t.Errorf("sum(matmul) %v, loops %v", s1, s2)
	}
	for _, l := range lines[2:] {
		if d, _ := strconv.ParseFloat(strings.TrimSpace(l), 64); math.Abs(d) > 1e-13 {
			t.Errorf("element differs by %v", d)
		}
	}
	if st.Copies != 36 {
		t.Errorf("copies %d, want 36 (the result of matmul into c)", st.Copies)
	}
}

func TestArrayExpression(t *testing.T) {
	out, st := run(t, `program k4
  implicit none
  integer, parameter :: n = 1000, reps = 3
  real(8), allocatable :: a(:), b(:), c(:), d(:)
  integer :: i, r
  allocate (a(n), b(n), c(n), d(n))
  do i = 1, n
    b(i) = real(mod(i, 13), 8)/13d0
    c(i) = real(mod(i, 11), 8)/11d0
    d(i) = real(mod(i, 7), 8)/7d0
  end do
  do r = 1, reps
    b(1) = real(r, 8)
    a = b*c + d
  end do
  print '(es24.16)', sum(a)
  a = 2d0*a + 1d0
  print '(es24.16)', sum(a)
  a = a - c
  print '(es24.16)', a(1000)
end program k4
`)
	var sum, last float64
	for i := 1; i <= 1000; i++ {
		b := float64(i%13) / 13
		if i == 1 {
			b = 3
		}
		x := b*float64(i%11)/11 + float64(i%7)/7
		sum += x
		if i == 1000 {
			last = 2*x + 1 - float64(i%11)/11
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	got1, _ := strconv.ParseFloat(strings.TrimSpace(lines[0]), 64)
	got2, _ := strconv.ParseFloat(strings.TrimSpace(lines[1]), 64)
	got3, _ := strconv.ParseFloat(strings.TrimSpace(lines[2]), 64)
	if math.Abs(got1-sum) > 1e-12*sum || math.Abs(got2-(2*sum+1000)) > 1e-12*sum*2 || math.Abs(got3-last) > 1e-12 {
		t.Errorf("%q, want %v %v %v", out, sum, 2*sum+1000, last)
	}
	if st.Copies != 0 {
		t.Errorf("copies %d: a = b*c + d writes through AddInto and copies nothing", st.Copies)
	}
}

// The arrays the evaluator indexes are go-ndarray's own storage: nothing is
// copied between the evaluator and go-ndarray (the claim of the package
// documentation).
func TestArrayStorageIsNdarrays(t *testing.T) {
	a := newArray(&frame{}, 2, 3, 4)
	for j := 0; j < 4; j++ {
		for i := 0; i < 3; i++ {
			a.data[i+j*3] = float64(10*(i+1) + (j + 1))
		}
	}
	// a(i,j) is the row-major element [j-1][i-1] of the (4,3) ndarray
	for j := 1; j <= 4; j++ {
		for i := 1; i <= 3; i++ {
			if got := a.nd.At(j-1, i-1); got != float64(10*i+j) {
				t.Fatalf("a(%d,%d) = %v through ndarray", i, j, got)
			}
		}
	}
	if sh := a.nd.Shape(); len(sh) != 2 || sh[0] != 4 || sh[1] != 3 {
		t.Errorf("shape %v, want the reversed (4,3)", sh)
	}
	var _ *ndarray.Array = a.nd
}

func TestBoundsAndConformance(t *testing.T) {
	for name, src := range map[string]string{
		"subscript": `program p
  implicit none
  real(8), allocatable :: a(:)
  real(8) :: x
  allocate (a(3))
  x = a(4)
end program p
`,
		"subscript 2": `program p
  implicit none
  real(8), allocatable :: a(:,:)
  allocate (a(3,2))
  a(1,3) = 1d0
end program p
`,
		"not allocated": `program p
  implicit none
  real(8), allocatable :: a(:)
  real(8) :: x
  x = a(1)
end program p
`,
		"conformance": `program p
  implicit none
  real(8), allocatable :: a(:), b(:), c(:)
  allocate (a(3), b(4), c(3))
  c = a + b
end program p
`,
		"matmul shape": `program p
  implicit none
  real(8), allocatable :: a(:,:), b(:,:), c(:,:)
  allocate (a(2,3), b(2,3), c(2,3))
  c = matmul(a, b)
end program p
`,
	} {
		t.Run(name, func(t *testing.T) {
			p, err := compile(t, src)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Run(&bytes.Buffer{})
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("got %v, want an *Error", err)
			}
		})
	}
}

func TestRefusals(t *testing.T) {
	for name, src := range map[string]string{
		"undeclared":    "program p\n  implicit none\n  x = 1\nend program p\n",
		"if":            "program p\n  implicit none\n  integer :: i\n  if (i == 1) i = 2\nend program p\n",
		"real default":  "program p\n  implicit none\n  real :: x\nend program p\n",
		"rank 3":        "program p\n  implicit none\n  real(8) :: a(2,2,2)\nend program p\n",
		"section":       "program p\n  implicit none\n  real(8) :: a(4)\n  real(8) :: x\n  x = a(1:2)\nend program p\n",
		"lower bound":   "program p\n  implicit none\n  real(8), allocatable :: a(:)\n  allocate (a(0:3))\nend program p\n",
		"unknown call":  "program p\n  implicit none\n  call foo()\nend program p\n",
		"two units":     "program p\nend program p\nsubroutine s\nend subroutine s\n",
		"real to int":   "program p\n  implicit none\n  integer :: i\n  i = 1d0\nend program p\n",
		"array minus s": "program p\n  implicit none\n  real(8), allocatable :: a(:)\n  allocate (a(2))\n  a = a - 1d0\nend program p\n",
	} {
		t.Run(name, func(t *testing.T) {
			f, perr := syntax.Parse("t.f90", []byte(src), nil)
			if perr != nil {
				t.Fatalf("parse: %v", perr)
			}
			_, err := Compile(f, nil)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("accepted (err %v)", err)
			}
		})
	}
}

func TestDoLoopSemantics(t *testing.T) {
	out, _ := run(t, `program p
  implicit none
  integer :: i, k
  real(8) :: s
  s = 0d0
  do i = 10, 1, -3
    s = s + real(i, 8)
  end do
  k = i
  print '(es24.16)', s
  print '(es24.16)', real(k, 8)
  do i = 5, 1
    s = 1d9
  end do
  print '(es24.16)', s
end program p
`)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	want := []float64{10 + 7 + 4 + 1, -2, 22}
	for i, w := range want {
		if g, _ := strconv.ParseFloat(strings.TrimSpace(lines[i]), 64); g != w {
			t.Errorf("line %d: %v, want %v", i, g, w)
		}
	}
}

func TestParseFormat(t *testing.T) {
	for _, bad := range []string{"es24.16", "(i5)", "(es24)", "(f0.x)", "(es24.16,)"} {
		if _, err := parseFormat(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	e, err := parseFormat("(a, f0.6, ES12.3)")
	if err != nil || len(e) != 3 || e[1] != (edit{'f', 0, 6}) || e[2] != (edit{'e', 12, 3}) {
		t.Errorf("%v %v", e, err)
	}
	if got := (edit{'e', 24, 16}).real(1.0 / 3); got != "  3.3333333333333331E-01" {
		t.Errorf("%q", got)
	}
}

// The evaluator takes every type, kind, rank and constant from the typed tree:
// the analyser's diagnostics reach it as refusals, and its folded constants
// are the values of the program.
func TestConsumesTheTypedTree(t *testing.T) {
	// the analyser refuses statically non-conformable arrays; the evaluator
	// used to find out when the program ran
	_, err := compile(t, "program p\n  implicit none\n  real(8) :: a(3), b(4)\n  a = a + b\nend program p\n")
	var e *Error
	if !errors.As(err, &e) || !strings.Contains(e.Msg, "not conformable") || e.Pos == "" {
		t.Fatalf("got %v, want the analyser's positioned diagnostic", err)
	}

	// constants: named constants, kind suffixes, folded expressions, an extent
	// that is a constant expression (none of it is evaluated by this package)
	out, _ := run(t, `program p
  implicit none
  integer, parameter :: n = 2*3 + 1, m = n - 1
  real(8), parameter :: third = 1d0/3d0, k = real(m, 8)*third
  real(8) :: a(n)
  integer(8) :: i
  do i = 1, 1_8*n
    a(i) = real(i, 8)*k
  end do
  print '(es24.16)', sum(a)
  print '(es24.16)', k
end program p
`)
	var want float64
	k := float64(6) * (1.0 / 3.0)
	for i := 1; i <= 7; i++ {
		want += float64(i) * k
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	g1, _ := strconv.ParseFloat(strings.TrimSpace(lines[0]), 64)
	g2, _ := strconv.ParseFloat(strings.TrimSpace(lines[1]), 64)
	if math.Abs(g1-want) > 1e-13 || g2 != k {
		t.Errorf("got %v %v, want %v %v", g1, g2, want, k)
	}

	// the typed tree of another file does not describe this one
	f1, _ := syntax.Parse("a.f90", []byte("program p\n  implicit none\n  integer :: i\n  i = 1\nend program p\n"), nil)
	f2, _ := syntax.Parse("b.f90", []byte("program p\n  implicit none\n  integer :: i\n  i = 2\nend program p\n"), nil)
	info, errs := sema.Analyze(f1, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if _, err := Compile(f2, info); err == nil {
		t.Error("compiled a file against the typed tree of another")
	}
	// and the same file against its own typed tree compiles
	if _, err := Compile(f1, info); err != nil {
		t.Errorf("own typed tree: %v", err)
	}
}

// The kind of a value is the analyser's: a default real literal is real(4) and
// is refused, whatever it looks like.
func TestKindsComeFromTheAnalyser(t *testing.T) {
	for name, src := range map[string]string{
		"real(4) literal":  "program p\n  implicit none\n  real(8) :: x\n  x = 1.5\nend program p\n",
		"real(4) result":   "program p\n  implicit none\n  real(8) :: x\n  x = real(2)\nend program p\n",
		"integer(16)":      "program p\n  implicit none\n  integer(16) :: i\nend program p\n",
		"logical":          "program p\n  implicit none\n  logical :: l\nend program p\n",
		"complex constant": "program p\n  implicit none\n  complex(8), parameter :: c = (1d0, 2d0)\nend program p\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile(t, src)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("accepted (err %v)", err)
			}
		})
	}
}
