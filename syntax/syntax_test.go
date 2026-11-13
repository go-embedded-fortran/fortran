package syntax

import (
	"fmt"
	"strings"
	"testing"
)

func mustParse(t *testing.T, name, src string) *File {
	t.Helper()
	f, err := Parse(name, []byte(src), nil)
	if err != nil {
		t.Fatalf("%s: %v\n%s", name, err, src)
	}
	return f
}

// sexp renders an expression fully parenthesised, to check precedence.
func sexp(e Expr) string {
	switch n := e.(type) {
	case *Binary:
		return "(" + n.Op + " " + sexp(n.X) + " " + sexp(n.Y) + ")"
	case *Unary:
		return "(" + n.Op + " " + sexp(n.X) + ")"
	case *Paren:
		return "[" + sexp(n.X) + "]"
	}
	return PrintExpr(e)
}

func TestDetectForm(t *testing.T) {
	for name, want := range map[string]Form{
		"a.f90": Free, "a.F90": Free, "a.f95": Free, "a.f03": Free, "a.f08": Free, "a.txt": Free, "a": Free,
		"a.f": Fixed, "a.F": Fixed, "a.for": Fixed, "a.FOR": Fixed, "a.f77": Fixed, "a.ftn": Fixed,
	} {
		if got := DetectForm(name); got != want {
			t.Errorf("DetectForm(%q) = %v, want %v", name, got, want)
		}
	}
	// the override wins
	f, err := Parse("x.f90", []byte("      X = 1\n      END\n"), &Options{Form: Fixed})
	if err != nil || f.Form != Fixed {
		t.Errorf("override: %v %v", f.Form, err)
	}
}

func TestPrecedence(t *testing.T) {
	// F2018 10.1.1: ** (right assoc) > * / > unary and binary + - > // >
	// relational > .not. > .and. > .or. > .eqv. .neqv. > defined binary
	for src, want := range map[string]string{
		"a + b * c ** d ** e":           "(+ a (* b (** c (** d e))))",
		"-a ** 2":                       "(- (** a 2))",
		"-a + b":                        "(+ (- a) b)",
		"a - b - c":                     "(- (- a b) c)",
		"a / b * c":                     "(* (/ a b) c)",
		"a // b // c":                   "(// (// a b) c)",
		"a + b // c":                    "(// (+ a b) c)",
		"a // b == c":                   "(== (// a b) c)",
		".not. a .and. b":               "(.and. (.not. a) b)",
		"a .or. b .and. c":              "(.or. a (.and. b c))",
		"a .and. b .eqv. c .or. d":      "(.eqv. (.and. a b) (.or. c d))",
		"a .eqv. b .neqv. c":            "(.neqv. (.eqv. a b) c)",
		"a .foo. b .or. c":              "(.foo. a (.or. b c))",
		"a .eq. b":                      "(== a b)",
		"a /= b":                        "(/= a b)",
		"a >= b + 1":                    "(>= a (+ b 1))",
		".foo. a + b":                   "(+ (.foo. a) b)",
		"a * (b + c)":                   "(* a [(+ b c)])",
		"a ** -b":                       "(** a (- b))",
		"2 * -3":                        "(* 2 (- 3))",
		"a(1:n:2, :) + b%c(2)%d":        "(+ a(1:n:2, :) b%c(2)%d)",
		"x(1)(2:3)":                     "x(1)(2:3)",
		"f(a, k = b, *10)":              "f(a, k = b, *10)",
		"[1, 2] + (/ 3, 4 /)":           "(+ [1, 2] [3, 4])",
		"(1.0, -2.0d0)":                 "(1.0, -2.0d0)",
		"1.0_dp * 2_8 + 3.e0":           "(+ (* 1.0_dp 2_8) 3.e0)",
		"z'1F' + b\"101\" + o'17'":      "(+ (+ z'1F' b'101') o'17')",
		"'it''s' // \"say \"\"hi\"\"\"": "(// 'it''s' 'say \"hi\"')",
		"1.eq.2":                        "(== 1 2)",
		".true. .and. .false.":          "(.and. .true. .false.)",
	} {
		f := mustParse(t, "p.f90", "x = "+src+"\nend\n")
		a := f.Units[0].Body[0].(*Assignment)
		if got := sexp(a.RHS); got != want {
			t.Errorf("%s:\n got  %s\n want %s", src, got, want)
		}
	}
}

func TestCaseInsensitivity(t *testing.T) {
	f := mustParse(t, "c.f90", "PROGRAM Main\n  INTEGER :: MyVar\n  MyVar = 1\n  IF (MyVar .EQ. 1) Print *, 'KeepCase'\nEND PROGRAM MAIN\n")
	u := f.Units[0]
	if u.Ident.Name != "main" || u.Ident.Orig != "Main" {
		t.Errorf("unit name %+v", u.Ident)
	}
	d := u.Body[0].(*Decl)
	if e := d.Entities[0]; e.Name.Name != "myvar" || e.Name.Orig != "MyVar" {
		t.Errorf("entity %+v", e.Name)
	}
	li := u.Body[2].(*LogicalIf)
	pr := li.Stmt.(*IOStmt)
	if s := pr.Items[0].(*StringLit).Value; s != "KeepCase" {
		t.Errorf("character context lowercased: %q", s)
	}
}

func TestLiterals(t *testing.T) {
	f := mustParse(t, "l.f90", "x = 1.0d0 + 1.0_dp + 1_i8 + .5 + 5. + 1e-3 + 2.5q0 + z'ff'\nend\n")
	var got []string
	var walk func(e Expr)
	walk = func(e Expr) {
		switch n := e.(type) {
		case *Binary:
			walk(n.X)
			walk(n.Y)
		case *RealLit:
			got = append(got, "real:"+n.Text+"/"+n.Kind)
		case *IntLit:
			got = append(got, "int:"+n.Text+"/"+n.Kind)
		case *BozLit:
			got = append(got, "boz:"+string(n.Base)+n.Digits)
		}
	}
	walk(f.Units[0].Body[0].(*Assignment).RHS)
	want := "real:1.0d0/ real:1.0/dp int:1/i8 real:.5/ real:5./ real:1e-3/ real:2.5q0/ boz:zff"
	if s := strings.Join(got, " "); s != want {
		t.Errorf("got  %s\nwant %s", s, want)
	}
}

func TestFreeFormContinuation(t *testing.T) {
	src := "program p\n  x = 1 + &\n   ! a comment line inside\n\n   & 2 &\n  & + 3 ! trailing\n  s = 'abc&\n    &def'\n  t = 'ab&\n   cd'\n  a = 1; b = 2 ;; c = 3\nend\n"
	f := mustParse(t, "p.f90", src)
	b := f.Units[0].Body
	if len(b) != 6 {
		t.Fatalf("%d statements: %s", len(b), Print(f))
	}
	if got := sexp(b[0].(*Assignment).RHS); got != "(+ (+ 1 2) 3)" {
		t.Errorf("x: %s", got)
	}
	if got := b[1].(*Assignment).RHS.(*StringLit).Value; got != "abcdef" {
		t.Errorf("s: %q", got)
	}
	// a continued literal without the leading '&' continues at column 1 (gfortran only warns)
	if got := b[2].(*Assignment).RHS.(*StringLit).Value; got != "ab   cd" {
		t.Errorf("t: %q", got)
	}
}

func TestFixedForm(t *testing.T) {
	src := "C comment\n* another\n! bang comment\n      PROGRAM P\n      INTEGER I, J\n      DO 10 I = 1, 3\n         J = I +\n     &       1\n   10 CONTINUE\n      DO 20 I = 1.10\n   20 CONTINUE\n" +
		"      REAL*8 X(3)\n      X(1) = 1.0D0 ! trailing\n      IF (J .GT. 1) GOTO 30\n      IF (J) 10, 20, 30\n   30 CALL FOO(X, 'a b')\n      END\n"
	f, err := Parse("p.f", []byte(src), nil)
	if err != nil {
		t.Fatal(err)
	}
	u := f.Units[0]
	if u.Ident.Name != "p" {
		t.Errorf("name %q", u.Ident.Name)
	}
	do1 := u.Body[1].(*Do)
	if do1.TermLabel != 10 || do1.Var.Name != "i" || len(do1.Body) != 2 {
		t.Errorf("labelled do: %+v", do1)
	}
	if got := sexp(do1.Body[0].(*Assignment).RHS); got != "(+ i 1)" {
		t.Errorf("continued statement: %s", got)
	}
	// "DO 20 I = 1.10" is an assignment to the variable DO20I: blanks are insignificant
	as, ok := u.Body[2].(*Assignment)
	if !ok || as.LHS.(*Ident).Name != "do20i" {
		t.Fatalf("DO 20 I = 1.10 must be an assignment, got %T", u.Body[2])
	}
	if d := u.Body[4].(*Decl); d.Type.Name != "real" || !d.Type.KindStar {
		t.Errorf("real*8: %+v", d.Type)
	}
	if _, ok := u.Body[7].(*ArithIf); !ok {
		t.Errorf("arithmetic IF: %T", u.Body[7])
	}
	call := u.Body[len(u.Body)-1].(*Call)
	if call.Label != 30 || call.Args[1].Value.(*StringLit).Value != "a b" {
		t.Errorf("call: %+v", call)
	}
	// columns beyond 72 are ignored
	f2, err := Parse("w.f", []byte("      X = 1"+strings.Repeat(" ", 61)+"+ GARBAGE\n      END\n"), nil)
	if err != nil {
		t.Fatalf("text after column 72 must be ignored: %v", err)
	}
	if got := sexp(f2.Units[0].Body[0].(*Assignment).RHS); got != "1" {
		t.Errorf("column 72: %s", got)
	}
}

func TestFixedFormShared(t *testing.T) {
	src := "      DO 10 I = 1, 2\n      DO 10 J = 1, 2\n      K = I + J\n   10 CONTINUE\n      END\n"
	f := mustParse(t, "s.f", src)
	outer := f.Units[0].Body[0].(*Do)
	if len(outer.Body) != 1 {
		t.Fatalf("outer body: %d", len(outer.Body))
	}
	inner := outer.Body[0].(*Do)
	if inner.TermLabel != 10 || len(inner.Body) != 2 {
		t.Errorf("shared terminal statement: %+v", inner)
	}
}

func TestErrorsAndRecovery(t *testing.T) {
	src := "program p\n  x = = 1\n  y = 2\n  z = (3\n  w = 4\nend program\nsubroutine s\n  call\nend\n"
	f, err := Parse("e.f90", []byte(src), nil)
	if err == nil {
		t.Fatal("expected errors")
	}
	el := err.(ErrorList)
	if len(el) != 3 {
		t.Fatalf("%d errors:\n%v", len(el), err)
	}
	for _, e := range el {
		if !strings.HasPrefix(e.Error(), "e.f90:") || strings.Count(strings.SplitN(e.Error(), " ", 2)[0], ":") < 3 {
			t.Errorf("not file:line:col: message: %q", e.Error())
		}
	}
	if el[0].Pos.Line != 2 || el[0].Pos.Col != 7 {
		t.Errorf("first error at %v", el[0].Pos)
	}
	// recovery: the good statements and the second unit survive
	if len(f.Units) != 2 {
		t.Fatalf("%d units", len(f.Units))
	}
	body := f.Units[0].Body
	var good int
	for _, s := range body {
		if _, ok := s.(*Assignment); ok {
			good++
		}
	}
	if good != 2 {
		t.Errorf("%d assignments survived: %s", good, Print(f))
	}
}

func TestErrorMessages(t *testing.T) {
	for src, want := range map[string]string{
		"x = 'abc\nend\n":                 "unterminated character literal",
		"x = 1 + \nend\n":                 "expected an expression",
		"if (a) then\nend\n":              "missing 'end if'",
		"do 10 i = 1, 2\nend\n":           "missing terminal statement with label 10",
		"program a\nend program b\n":      "end name 'b' does not match 'a'",
		"x = 1 ~ 2\nend\n":                "illegal character '~'",
		"integer, parameter x = 1\nend\n": "expected '::'",
		"real :: x(\nend\n":               "expected an expression",
		"x = &\n":                         "ends at the end of the file",
	} {
		_, err := Parse("m.f90", []byte(src), nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want %q, got %v", src, want, err)
		}
	}
}

func TestConstructs(t *testing.T) {
	src := `module m
  implicit none
  integer, parameter :: dp = kind(1.0d0)
  real(dp), dimension(3, 3), allocatable :: a(:, :)
  character(len=*), parameter :: s = 'x'
  type, public :: t
    integer :: n = 0
  contains
    procedure :: f => g
  end type t
  interface operator(+)
    module procedure add
  end interface
contains
  pure function g(self) result(r)
    class(t), intent(in) :: self
    integer :: r
    r = self%n
  end function g
  subroutine s1(x, *)
    real, intent(inout) :: x(:)
    outer: do i = 1, 10, 2
      inner: do while (x(i) > 0)
        if (i == 3) cycle outer
        if (i == 4) then
          exit inner
        else if (i == 5) then
          x(i) = 0
        else
          continue
        end if
      end do inner
    end do outer
    select case (i)
    case (1, 3:5)
      x = 1
    case default
      return 1
    end select
    where (x > 0) x = -x
    where (x < 0)
      x = 0
    elsewhere
      x = 1
    end where
    forall (i = 1:3, j = 1:3, i /= j) a(i, j) = 0
    allocate (x(10), stat = ierr)
    deallocate (x)
    open (unit = 10, file = 'f', status = 'old')
    read (10, *) (x(i), i = 1, 3)
    write (*, '(a, i3)') 'n = ', n
    print *, a(1:n:2, :)
    print '(i0)', 5
    stop 'done'
    error stop 1
    go to (10, 20) i
 10 continue
 20 call sub(x, y = 2)
  end subroutine s1
end module m
`
	f := mustParse(t, "m.f90", src)
	m := f.Units[0]
	if m.Kind != ModuleUnit || len(m.Contains) != 2 {
		t.Fatalf("module: %v %d", m.Kind, len(m.Contains))
	}
	roundTrip(t, "constructs.f90", []byte(src))
}

func TestStatementSeparatorsAndLabels(t *testing.T) {
	f := mustParse(t, "l.f90", "10 x = 1; 20 y = 2\n30 continue\nend\n")
	var labels []int
	for _, s := range f.Units[0].Body {
		labels = append(labels, s.StmtLabel())
	}
	if fmt.Sprint(labels) != "[10 20 30]" {
		t.Errorf("labels %v", labels)
	}
}

func TestKeywordsAsNames(t *testing.T) {
	// Fortran has no reserved words
	src := "program p\n  integer :: if, do, end, real\n  if = 1\n  do = if + 1\n  end = do\n  real = 2.0\n  print *, if, do, end, real\nend program p\n"
	f := mustParse(t, "k.f90", src)
	if _, ok := f.Units[0].Body[1].(*Assignment); !ok {
		t.Errorf("if = 1: %T", f.Units[0].Body[1])
	}
}

func TestFormatStatement(t *testing.T) {
	f := mustParse(t, "f.f", "      WRITE (6, 10) X\n   10 FORMAT (1X, 5HHELLO, F8.3, ' it''s ')\n      END\n")
	fm := f.Units[0].Body[1].(*FormatStmt)
	if fm.Label != 10 || fm.Text != "(1X, 5HHELLO, F8.3, ' it''s ')" {
		t.Errorf("format: %+v", fm)
	}
}
