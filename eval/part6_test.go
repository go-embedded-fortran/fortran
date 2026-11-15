package eval

import (
	"errors"
	"os"
	"testing"
)

// Derived types: default initialisation, structure constructors with keywords,
// nested and array components, allocatable components, arrays of derived type,
// type-bound procedures and a deep copy on assignment; the expected text is
// gfortran 15.2.0's for the same program (testdata/derived.f90).
func TestDerivedTypes(t *testing.T) {
	src, err := os.ReadFile("testdata/derived.f90")
	if err != nil {
		t.Fatal(err)
	}
	out, _, _ := runIO(t, string(src))
	want := "           0   1.5000000000000000      none                      7 T   0.00000000       0.00000000       0.00000000     F\n" +
		"           5   1.5000000000000000      abcdef                    3 F\n" +
		"          10          20          30          65\n" +
		"          10          20          30          10           0          30   0.00000000       1.00000000       2.00000000                        3                    4\n" +
		"           1           2   2.0000000000000000        4.0000000000000000      r                2\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func TestDerivedRefusals(t *testing.T) {
	for name, src := range map[string]string{
		"print allocatable component": "module m\n  type t\n    integer, allocatable :: a(:)\n  end type t\nend module m\nprogram p\n  use m\n  implicit none\n  type(t) :: x\n  print *, x\nend program p\n",
		"extension":                   "module m\n  type t\n    integer :: a\n  end type t\n  type, extends(t) :: u\n    integer :: b\n  end type u\nend module m\nprogram p\n  use m\n  implicit none\n  type(u) :: x\n  x%b = 1\nend program p\n",
		"pointer component":           "module m\n  type t\n    integer, pointer :: a\n  end type t\nend module m\nprogram p\n  use m\n  implicit none\n  type(t) :: x\nend program p\n",
		"array of arrays":             "module m\n  type t\n    integer :: a\n  end type t\nend module m\nprogram p\n  use m\n  implicit none\n  type(t) :: x(2, 2)\nend program p\n",
		"wrong constructor":           "module m\n  type t\n    integer :: a\n  end type t\nend module m\nprogram p\n  use m\n  implicit none\n  type(t) :: x\n  x = t(1, 2)\nend program p\n",
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
