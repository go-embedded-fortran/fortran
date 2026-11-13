package sema

import (
	"fmt"
	"strings"
)

// Basic is the type category of F2018 7.4.
type Basic uint8

// The categories. Unknown is not a Fortran type: it is the type of an
// expression the analyser cannot classify yet (a defined operator before
// generic resolution, an opaque module); no check is made on it, so that the
// analyser refuses less than gfortran does rather than more.
const (
	Invalid Basic = iota // not yet determined (implicit typing is lazy)
	Integer
	Real
	Complex
	Logical
	Character
	Derived
	Unknown
)

var basicNames = [...]string{"untyped", "integer", "real", "complex", "logical", "character", "type", "unknown"}

func (b Basic) String() string { return basicNames[b] }

// Type is an intrinsic or derived type with its kind (F2018 7.4.2-7.4.4).
// Len is the character length, -1 when it is not a constant (len=*, len=n).
type Type struct {
	Basic Basic
	Kind  int
	Len   int64
	Der   *DerivedType
}

// Default kinds of gfortran 15.2.0 (the oracle): the standard's default
// integer, real and logical (F2018 7.4.3.1, 7.4.3.2, 7.4.5) share one storage
// size, double precision is twice it.
const (
	DefaultInt  = 4
	DefaultReal = 4
	DefaultChar = 1
)

// Kinds is the set of kind values of each category supported by the oracle.
func validKind(b Basic, k int) bool {
	switch b {
	case Integer, Logical:
		return k == 1 || k == 2 || k == 4 || k == 8 || k == 16
	case Real, Complex:
		return k == 4 || k == 8 || k == 10 || k == 16
	case Character:
		return k == 1 || k == 4
	}
	return true
}

// Default returns the default type of a category.
func Default(b Basic) Type {
	switch b {
	case Integer, Logical:
		return Type{Basic: b, Kind: DefaultInt}
	case Real, Complex:
		return Type{Basic: b, Kind: DefaultReal}
	case Character:
		return Type{Basic: b, Kind: DefaultChar, Len: 1}
	}
	return Type{Basic: b}
}

func (t Type) String() string {
	switch t.Basic {
	case Derived:
		if t.Der != nil {
			return "type(" + t.Der.Name + ")"
		}
		return "type(?)"
	case Character:
		if t.Len >= 0 {
			return fmt.Sprintf("character(%d,%d)", t.Len, t.Kind)
		}
		return fmt.Sprintf("character(*,%d)", t.Kind)
	case Invalid, Unknown:
		return t.Basic.String()
	}
	return fmt.Sprintf("%s(%d)", t.Basic, t.Kind)
}

// Numeric reports whether t is integer, real or complex (F2018 7.4.2).
func (t Type) Numeric() bool { return t.Basic >= Integer && t.Basic <= Complex }

// Lenient reports whether checks on t are skipped.
func (t Type) Lenient() bool { return t.Basic == Unknown || t.Basic == Invalid }

// Promote is the type of a numeric binary operation (F2018 10.1.5.2.2,
// Table 10.2): the operand of higher category decides the category, and for
// the same category the larger kind; an integer meets a real or complex
// operand at that operand's kind; real meets complex at the larger kind.
func Promote(a, b Type) Type {
	if a.Basic < b.Basic {
		a, b = b, a
	}
	r := Type{Basic: a.Basic, Kind: a.Kind}
	if a.Basic == b.Basic || (a.Basic == Complex && b.Basic == Real) {
		r.Kind = max(a.Kind, b.Kind)
	}
	return r
}

// DerivedType is a derived type definition (F2018 7.5).
type DerivedType struct {
	Name       string
	Parent     *DerivedType // extends(parent)
	Components []*Component
	// Bound maps a type-bound procedure name to the name of the procedure
	// that implements it, resolved in Scope.
	Bound    map[string]string
	Sequence bool
	// Forward marks a type named by a pointer component before its definition.
	Forward bool
	Scope   *Scope
}

// Component is a data component of a derived type.
type Component struct {
	Name    string
	Type    Type
	Rank    int
	Pointer bool
	Alloc   bool
}

// Find returns the component name of d or of an ancestor (F2018 7.5.7.2).
func (d *DerivedType) Find(name string) *Component {
	for t := d; t != nil; t = t.Parent {
		for _, c := range t.Components {
			if c.Name == name {
				return c
			}
		}
	}
	return nil
}

// FindBound returns the implementing procedure name of a type-bound
// procedure of d or an ancestor.
func (d *DerivedType) FindBound(name string) (string, bool) {
	for t := d; t != nil; t = t.Parent {
		if p, ok := t.Bound[name]; ok {
			return p, true
		}
	}
	return "", false
}

func lower(s string) string { return strings.ToLower(s) }
