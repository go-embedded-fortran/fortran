package eval

import (
	"bytes"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/syntax"
)

// runIO compiles and runs src, returning standard output, standard error and
// the exit status.
func runIO(t *testing.T, src string) (out, errOut string, exit int) {
	t.Helper()
	p, err := compile(t, src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var o, e bytes.Buffer
	st, err := p.RunIO(&o, &e)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return o.String(), e.String(), st.Exit
}

// STOP and ERROR STOP: the text and the exit status are libgfortran's (checked
// against gfortran 15.2 for the forms without a golden).
func TestStop(t *testing.T) {
	for _, c := range []struct {
		name, stmt, stderr string
		exit               int
	}{
		{"bare", "stop", "", 0},
		{"code", "stop 3", "STOP 3\n", 3},
		{"message", "stop 'finished'", "STOP finished\n", 0},
		{"error bare", "error stop", "ERROR STOP \n", 1},
		{"error code", "error stop 7", "ERROR STOP 7\n", 7},
		{"error message", "error stop 'bad thing'", "ERROR STOP bad thing\n", 1},
		{"runtime code", "error stop n + 1", "ERROR STOP 301\n", 45}, // the status is the code modulo 256
		{"quiet", "error stop 5, quiet = .true.", "", 5},
		{"not quiet", "error stop 5, quiet = .false.", "ERROR STOP 5\n", 5},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, errOut, exit := runIO(t, "program p\n  implicit none\n  integer :: n\n  n = 300\n  print *, 'a'\n  "+c.stmt+"\n  print *, 'unreachable'\nend program p\n")
			if out != " a\n" || errOut != c.stderr || exit != c.exit {
				t.Errorf("got %q %q %d, want \" a\\n\" %q %d", out, errOut, exit, c.stderr, c.exit)
			}
		})
	}
}

// A STOP inside nested constructs unwinds all of them.
func TestStopInsideLoops(t *testing.T) {
	out, errOut, exit := runIO(t, `program p
  implicit none
  integer :: i, j
  do i = 1, 3
    do j = 1, 3
      if (i * j == 4) stop 9
      print *, i, j
    end do
  end do
end program p
`)
	if want := "           1           1\n           1           2\n           1           3\n           2           1\n"; out != want || errOut != "STOP 9\n" || exit != 9 {
		t.Errorf("got %q %q %d", out, errOut, exit)
	}
}

func TestControlFlow(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  integer :: i, j, n
  n = 0
  outer: do i = 1, 5
    do j = 1, 5
      if (j == 3) cycle
      if (j == 4) cycle outer
      if (i == 4) exit outer
      n = n + 10 * i + j
    end do
    n = n + 1000
  end do outer
  print *, n, i, j
  n = 0
  do while (n < 5)
    n = n + 1
    if (mod(n, 2) == 0) cycle
    print *, 'odd', n
  end do
  do
    n = n - 1
    if (n < 2) exit
  end do
  print *, n
  do i = 10, 1, -3
    n = n + i
  end do
  print *, n, i
  do i = 1, 0
    n = -1
  end do
  print *, n, i
end program p
`)
	// i = 1..3 contribute (10i+1)+(10i+2) = 20i+3: 23+43+63 = 129; exit at i = 4 (j = 1)
	want := "         129           4           1\n odd           1\n odd           3\n odd           5\n           1\n          23          -2\n          23           1\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func TestSelectCase(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  integer :: i
  do i = 0, 9
    select case (i)
    case (:1)
      print *, i, 'low'
    case (2, 3)
      print *, i, 'two-three'
    case (4:6)
      print *, i, 'mid'
    case default
      print *, i, 'high'
    end select
  end do
end program p
`)
	var want strings.Builder
	for i := 0; i < 10; i++ {
		w := "high"
		switch {
		case i <= 1:
			w = "low"
		case i <= 3:
			w = "two-three"
		case i <= 6:
			w = "mid"
		}
		want.WriteString("           " + strconv.Itoa(i) + " " + w + "\n")
	}
	if out != want.String() {
		t.Errorf("got %q\nwant %q", out, want.String())
	}
}

func TestIfConstruct(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  integer :: i
  do i = 1, 4
    if (i == 1) then
      print *, 'one'
    else if (i == 2) then
      print *, 'two'
    else if (i == 3 .or. i == 4 .and. .not. (i == 4)) then
      print *, 'three'
    else
      print *, 'other'
    end if
  end do
end program p
`)
	if want := " one\n two\n three\n other\n"; out != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestLogicals(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  logical :: a, b
  logical, parameter :: t = .true.
  integer :: i
  a = .true.
  b = .false.
  print *, a .and. b, a .or. b, .not. a, a .eqv. b, a .neqv. b, t
  print '(l1,l2)', a, b
  i = 3
  a = i > 2
  b = i <= 2
  print *, a, b, i == 3, i /= 3, 2.5d0 < i, i >= 3.0
end program p
`)
	if want := " F T F F T T\nT F\n T F T F T T\n"; out != want {
		t.Errorf("got %q want %q", out, want)
	}
}

// real(4) is held in a float64 and rounded after every operation; the result
// is the one a float32 computation gives.
func TestReal4MatchesFloat32(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  real :: x, y, z
  real(8) :: d
  x = 0.1
  y = 3.0
  z = x * y + x / y
  d = z
  print '(es24.16)', d
  d = sqrt(x) + abs(-y)
  print '(es24.16)', d
  z = real(7, 4) / 3
  d = z
  print '(es24.16)', d
end program p
`)
	var x, y float32 = 0.1, 3.0
	z := x*y + x/y
	sq := float32(math.Sqrt(float64(x))) // sqrt and abs of real(4) are real(4), and so is their sum
	want := []float64{float64(z), float64(sq + 3), float64(float32(7.0) / 3)}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %q", out)
	}
	for i, l := range lines {
		g, err := strconv.ParseFloat(strings.TrimSpace(l), 64)
		if err != nil || g != want[i] {
			t.Errorf("line %d: got %q, want %v", i, l, want[i])
		}
	}
}

// integer(1) and integer(2) arithmetic wraps; a store into integer(4) wraps.
func TestIntegerKindsWrap(t *testing.T) {
	out, _, _ := runIO(t, `program p
  use iso_fortran_env, only: int8, int16, int32, int64
  implicit none
  integer(int8) :: a
  integer(int16) :: b
  integer(int32) :: c
  integer(int64) :: d
  a = 100
  a = a + a
  b = 32767
  b = b + 1
  c = 2147483647
  c = c + 1
  d = 2147483647
  d = d + 1
  print *, a, b, c, d
  print *, 7 / 2, -7 / 2, mod(7, 3), mod(-7, 3), modulo(-7, 3), 7 ** 3, 2 ** (-1), nint(2.5), nint(-2.5), floor(-0.5), ceiling(-0.5), int(-3.99)
  print *, sign(3, -1), max(1, 9, 4), min(8, 2, 6)
end program p
`)
	want := "  -56 -32768 -2147483648           2147483648\n" +
		"           3          -3           1          -1           2         343           0           3          -3          -1           0          -3\n" +
		"          -3           9           2\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func TestInquiriesOfVariables(t *testing.T) {
	out, _, _ := runIO(t, `program p
  implicit none
  real :: s
  real(8) :: d
  s = 1.0
  d = 1.0d0
  print *, tiny(s), tiny(d), huge(s), huge(d), epsilon(s), epsilon(d)
  print *, fraction(8.0d0), exponent(8.0d0), scale(1.0d0, 4), spacing(1.0), spacing(d)
end program p
`)
	want := "   1.17549435E-38   2.2250738585072014E-308   3.40282347E+38   1.7976931348623157E+308   1.19209290E-07   2.2204460492503131E-016\n" +
		"  0.50000000000000000                4   16.000000000000000        1.19209290E-07   2.2204460492503131E-016\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

// Refusals are positioned errors, never a wrong run.
func TestPart1Refusals(t *testing.T) {
	for name, src := range map[string]string{
		"exit outside a loop": "program p\n  implicit none\n  exit\nend program p\n",
		"do concurrent":       "program p\n  implicit none\n  integer :: i\n  do concurrent (i = 1:3)\n  end do\nend program p\n",
		"select on real":      "program p\n  implicit none\n  real :: x\n  x = 1.0\n  select case (x)\n  end select\nend program p\n",
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

// Resource bounds are refusals, decided before anything is allocated.
func TestBombs(t *testing.T) {
	for name, src := range map[string]string{
		"array":               "program p\n  implicit none\n  real(8), allocatable :: a(:)\n  integer :: n\n  n = 2000000000\n  allocate (a(n))\nend program p\n",
		"matrix":              "program p\n  implicit none\n  real(8), allocatable :: a(:, :)\n  integer :: n\n  n = 100000\n  allocate (a(n, n))\nend program p\n",
		"repeat":              "program p\n  implicit none\n  integer :: n\n  n = 2000000000\n  print *, repeat('ab', n)\nend program p\n",
		"implied do":          "program p\n  implicit none\n  integer :: i\n  print *, (i, i = 1, 3000000000_8)\nend program p\n",
		"huge constant array": "program p\n  implicit none\n  real(8) :: a(268435457)\nend program p\n",
	} {
		t.Run(name, func(t *testing.T) {
			p, err := compile(t, src)
			if err != nil {
				return // refused at compile time
			}
			_, err = p.Run(&bytes.Buffer{})
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("not refused (err %v)", err)
			}
		})
	}
}

// FuzzCompileRun compiles and, for a program without a loop (a loop may not
// end), runs whatever the parser accepts: nothing may panic.
func FuzzCompileRun(f *testing.F) {
	for _, s := range []string{
		"program p\n  print *, 1\n  stop 2\nend program p\n",
		"program p\n  implicit none\n  integer :: i\n  if (i > 0) error stop 'x'\n  print '(i3)', (i, i = 1, 3)\nend program p\n",
		"program p\n  implicit none\n  real :: x\n  logical :: l\n  x = 1.5\n  l = x > 1 .and. .not. l\n  print *, x, l, repeat('a', 3)\nend program p\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1<<12 {
			return
		}
		file, err := syntax.Parse("f.f90", []byte(src), nil)
		if err != nil || file == nil {
			return
		}
		p, err := Compile(file, nil)
		if err != nil {
			return
		}
		if strings.Contains(strings.ToLower(src), "do") {
			return
		}
		var o, e bytes.Buffer
		p.RunIO(&o, &e)
	})
}
