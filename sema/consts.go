package sema

import (
	"fmt"
	"math"
	"math/bits"
	"math/cmplx"
	"strings"
)

// Const is the value of a constant expression (F2018 10.1.12). Arrays are not
// folded: an array-valued named constant has a type and a rank but no Const.
type Const struct {
	Type Type
	I    int64
	F    float64
	C    complex128
	B    bool
	S    string
}

// Limits bound the analyser; a bound is a refusal with an error, never a
// truncation.
type Limits struct {
	MaxSymbols    int // symbols over all scopes
	MaxConstChars int // length of a folded character constant
	MaxErrors     int
	MaxDepth      int // nesting of scopes and of expression folding
	// MaxSpecifics bounds the specific procedures of one generic: the
	// pairwise ambiguity check of F2018 15.4.3.4.5 is quadratic in it, and
	// the candidates of a generic reference are the specifics.
	MaxSpecifics int
	// MaxActuals bounds the actual arguments of one reference: argument
	// association (F2018 15.5.2) matches them against the dummy arguments.
	MaxActuals int
}

// DefaultLimits are the bounds Analyze applies when Options.Limits is zero.
func DefaultLimits() Limits {
	return Limits{MaxSymbols: 200_000, MaxConstChars: 1 << 20, MaxErrors: 100, MaxDepth: 1000, MaxSpecifics: 512, MaxActuals: 4096}
}

func intRange(kind int) (lo, hi int64, ok bool) {
	switch kind {
	case 1:
		return math.MinInt8, math.MaxInt8, true
	case 2:
		return math.MinInt16, math.MaxInt16, true
	case 4:
		return math.MinInt32, math.MaxInt32, true
	case 8:
		return math.MinInt64, math.MaxInt64, true
	}
	return 0, 0, false // kind 16 does not fit an int64: not folded
}

func fitsInt(v int64, kind int) bool {
	if kind == 16 {
		return true // 128 bits: every int64 fits; arithmetic is not folded
	}
	lo, hi, ok := intRange(kind)
	return ok && v >= lo && v <= hi
}

func (c *Const) float() float64 {
	switch c.Type.Basic {
	case Integer:
		return float64(c.I)
	case Complex:
		return real(c.C)
	}
	return c.F
}

func (c *Const) complex() complex128 {
	if c.Type.Basic == Complex {
		return c.C
	}
	return complex(c.float(), 0)
}

func roundKind(f float64, kind int) float64 {
	if kind == 4 {
		return float64(float32(f))
	}
	return f
}

// convert converts a numeric, logical or character constant to type t as the
// intrinsic assignment would (F2018 10.2.1.3); ok is false when the
// conversion does not exist or the value does not fit.
func convert(c *Const, t Type) (*Const, string) {
	r := &Const{Type: t}
	switch t.Basic {
	case Integer:
		switch c.Type.Basic {
		case Integer:
			r.I = c.I
		case Real:
			if math.IsNaN(c.F) || math.IsInf(c.F, 0) || math.Abs(c.F) >= 9.3e18 {
				return nil, "integer overflow converting a real constant"
			}
			r.I = int64(c.F)
		case Complex:
			r.I = int64(real(c.C))
		default:
			return nil, "cannot convert to integer"
		}
		if !fitsInt(r.I, t.Kind) {
			return nil, fmt.Sprintf("integer constant %d overflows kind %d", r.I, t.Kind)
		}
	case Real:
		if !c.Type.Numeric() {
			return nil, "cannot convert to real"
		}
		r.F = roundKind(c.float(), t.Kind)
	case Complex:
		if !c.Type.Numeric() {
			return nil, "cannot convert to complex"
		}
		z := c.complex()
		r.C = complex(roundKind(real(z), t.Kind), roundKind(imag(z), t.Kind))
	case Logical:
		if c.Type.Basic != Logical {
			return nil, "cannot convert to logical"
		}
		r.B = c.B
	case Character:
		if c.Type.Basic != Character {
			return nil, "cannot convert to character"
		}
		r.S = c.S
		if t.Len >= 0 && int64(len(r.S)) != t.Len {
			// assignment pads with blanks or truncates (F2018 10.2.1.3)
			if int64(len(r.S)) > t.Len {
				r.S = r.S[:t.Len]
			} else {
				r.S += strings.Repeat(" ", int(t.Len)-len(r.S))
			}
		}
	default:
		return nil, "not a constant type"
	}
	return r, ""
}

func (c *checker) foldUnary(op string, x *Const) (*Const, string) {
	switch op {
	case "+":
		if x.Type.Numeric() {
			return x, ""
		}
	case "-":
		r := *x
		switch x.Type.Basic {
		case Integer:
			if x.I == math.MinInt64 || !fitsInt(-x.I, x.Type.Kind) {
				return nil, "integer overflow in a constant expression"
			}
			r.I = -x.I
		case Real:
			r.F = -x.F
		case Complex:
			r.C = -x.C
		default:
			return nil, ""
		}
		return &r, ""
	case ".not.":
		if x.Type.Basic == Logical {
			return &Const{Type: x.Type, B: !x.B}, ""
		}
	}
	return nil, ""
}

// foldBinary folds x op y; the operand types were already checked, so a nil
// result with an empty message means "not a constant" and a message is a
// definite error (division by zero, overflow, a bound exceeded).
func (c *checker) foldBinary(op string, x, y *Const) (*Const, string) {
	switch {
	case x.Type.Numeric() && y.Type.Numeric():
		switch op {
		case "==", "/=", "<", "<=", ">", ">=":
			return foldCompare(op, x, y), ""
		case "+", "-", "*", "/", "**":
			return foldArith(op, x, y)
		}
	case x.Type.Basic == Character && y.Type.Basic == Character:
		switch op {
		case "//":
			if len(x.S)+len(y.S) > c.lim.MaxConstChars {
				return nil, "character constant longer than the analyser's bound"
			}
			return &Const{Type: Type{Basic: Character, Kind: x.Type.Kind, Len: int64(len(x.S) + len(y.S))}, S: x.S + y.S}, ""
		case "==", "/=", "<", "<=", ">", ">=":
			return foldCompare(op, x, y), ""
		}
	case x.Type.Basic == Logical && y.Type.Basic == Logical:
		k := max(x.Type.Kind, y.Type.Kind)
		r := &Const{Type: Type{Basic: Logical, Kind: k}}
		switch op {
		case ".and.":
			r.B = x.B && y.B
		case ".or.":
			r.B = x.B || y.B
		case ".eqv.", "==":
			r.B = x.B == y.B
		case ".neqv.", "/=":
			r.B = x.B != y.B
		default:
			return nil, ""
		}
		return r, ""
	}
	return nil, ""
}

func foldCompare(op string, x, y *Const) *Const {
	var cmp int
	switch {
	case x.Type.Basic == Character:
		a, b := strings.TrimRight(x.S, " "), strings.TrimRight(y.S, " ")
		cmp = strings.Compare(a, b)
	case x.Type.Basic == Complex || y.Type.Basic == Complex:
		if op != "==" && op != "/=" {
			return nil
		}
		if x.complex() == y.complex() {
			cmp = 0
		} else {
			cmp = 1
		}
	case x.Type.Basic == Integer && y.Type.Basic == Integer:
		switch {
		case x.I < y.I:
			cmp = -1
		case x.I > y.I:
			cmp = 1
		}
	default:
		a, b := x.float(), y.float()
		switch {
		case a < b:
			cmp = -1
		case a > b:
			cmp = 1
		case a != b: // NaN: every comparison but /= is false
			return &Const{Type: Default(Logical), B: op == "/="}
		}
	}
	var r bool
	switch op {
	case "==":
		r = cmp == 0
	case "/=":
		r = cmp != 0
	case "<":
		r = cmp < 0
	case "<=":
		r = cmp <= 0
	case ">":
		r = cmp > 0
	case ">=":
		r = cmp >= 0
	}
	return &Const{Type: Default(Logical), B: r}
}

func foldArith(op string, x, y *Const) (*Const, string) {
	t := Promote(x.Type, y.Type)
	switch t.Basic {
	case Integer:
		if _, _, ok := intRange(t.Kind); !ok {
			return nil, "" // kind 16: not folded
		}
		a, b := x.I, y.I
		var v int64
		switch op {
		case "+":
			v = a + b
			if (a >= 0) == (b >= 0) && (v >= 0) != (a >= 0) {
				return nil, "integer overflow in a constant expression"
			}
		case "-":
			v = a - b
			if (a >= 0) != (b >= 0) && (v >= 0) != (a >= 0) {
				return nil, "integer overflow in a constant expression"
			}
		case "*":
			if a != 0 && b != 0 {
				hi, lo := bits.Mul64(uint64(abs64(a)), uint64(abs64(b)))
				if hi != 0 || lo > math.MaxInt64 {
					return nil, "integer overflow in a constant expression"
				}
			}
			v = a * b
		case "/":
			if b == 0 {
				return nil, "division by zero in a constant expression"
			}
			if a == math.MinInt64 && b == -1 {
				return nil, "integer overflow in a constant expression"
			}
			v = a / b
		case "**":
			switch {
			case b >= 0:
				switch {
				case a == 0 && b > 0:
					v = 0
				case a == -1 && b%2 == 1:
					v = -1
				case a == 0 || a == 1 || a == -1:
					v = 1
				default:
					v = 1
					for i := int64(0); i < b; i++ { // |a| >= 2: at most 63 turns
						hi, lo := bits.Mul64(uint64(abs64(v)), uint64(abs64(a)))
						if hi != 0 || lo > math.MaxInt64 {
							return nil, "integer overflow in a constant expression"
						}
						v *= a
					}
				}
			case a == 0:
				return nil, "zero raised to a negative power in a constant expression"
			case a == 1:
				v = 1
			case a == -1:
				v = 1
				if b%2 != 0 {
					v = -1
				}
			default:
				v = 0
			}
		}
		if !fitsInt(v, t.Kind) {
			return nil, fmt.Sprintf("integer overflow in a constant expression (kind %d)", t.Kind)
		}
		return &Const{Type: t, I: v}, ""
	case Real:
		a, b := x.float(), y.float()
		var v float64
		switch op {
		case "+":
			v = a + b
		case "-":
			v = a - b
		case "*":
			v = a * b
		case "/":
			v = a / b
		case "**":
			v = math.Pow(a, b)
		}
		return &Const{Type: t, F: roundKind(v, t.Kind)}, ""
	case Complex:
		a, b := x.complex(), y.complex()
		var v complex128
		switch op {
		case "+":
			v = a + b
		case "-":
			v = a - b
		case "*":
			v = a * b
		case "/":
			v = a / b
		case "**":
			v = cmplx.Pow(a, b)
		}
		return &Const{Type: t, C: complex(roundKind(real(v), t.Kind), roundKind(imag(v), t.Kind))}, ""
	}
	return nil, ""
}

func abs64(a int64) int64 {
	if a < 0 {
		return -a
	}
	return a
}
