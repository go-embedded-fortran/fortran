package eval

import (
	"math"

	"github.com/go-embedded-fortran/fortran/syntax"
)

// relational compiles == /= < <= > >= on two integer or two real operands (an
// integer meets a real at the real: F2018 10.1.5.5.2). The operands are
// compared as the analyser typed the operation.
func (c *compiler) relational(n *syntax.Binary, x, y *ex) (*ex, error) {
	if x.k == kArr || y.k == kArr {
		return nil, c.errf(n, "a whole-array comparison is not supported")
	}
	if x.k == kInt && y.k == kInt {
		a, b := x.i, y.i
		var fn boolFn
		switch n.Op {
		case "==":
			fn = func(f *frame) bool { return a(f) == b(f) }
		case "/=":
			fn = func(f *frame) bool { return a(f) != b(f) }
		case "<":
			fn = func(f *frame) bool { return a(f) < b(f) }
		case "<=":
			fn = func(f *frame) bool { return a(f) <= b(f) }
		case ">":
			fn = func(f *frame) bool { return a(f) > b(f) }
		default:
			fn = func(f *frame) bool { return a(f) >= b(f) }
		}
		if y.isC {
			k := y.ci
			switch n.Op {
			case "==":
				fn = func(f *frame) bool { return a(f) == k }
			case "/=":
				fn = func(f *frame) bool { return a(f) != k }
			case "<":
				fn = func(f *frame) bool { return a(f) < k }
			case "<=":
				fn = func(f *frame) bool { return a(f) <= k }
			case ">":
				fn = func(f *frame) bool { return a(f) > k }
			default:
				fn = func(f *frame) bool { return a(f) >= k }
			}
		}
		return &ex{k: kLog, slot: -1, b: fn}, nil
	}
	if (x.k != kInt && x.k != kReal) || (y.k != kInt && y.k != kReal) {
		return nil, c.errf(n, "comparison of these operands is not supported")
	}
	a, b := x.asReal().r, y.asReal().r
	var fn boolFn
	switch n.Op {
	case "==":
		fn = func(f *frame) bool { return a(f) == b(f) }
	case "/=":
		fn = func(f *frame) bool { return a(f) != b(f) }
	case "<":
		fn = func(f *frame) bool { return a(f) < b(f) }
	case "<=":
		fn = func(f *frame) bool { return a(f) <= b(f) }
	case ">":
		fn = func(f *frame) bool { return a(f) > b(f) }
	default:
		fn = func(f *frame) bool { return a(f) >= b(f) }
	}
	return &ex{k: kLog, slot: -1, b: fn}, nil
}

// logical compiles .and. .or. .eqv. .neqv. on logical operands.
func (c *compiler) logical(n *syntax.Binary, x, y *ex) (*ex, error) {
	if x.k != kLog || y.k != kLog {
		return nil, c.errf(n, "operator %q needs logical operands", n.Op)
	}
	a, b := x.b, y.b
	var fn boolFn
	switch n.Op {
	case ".and.":
		fn = func(f *frame) bool { return a(f) && b(f) }
	case ".or.":
		fn = func(f *frame) bool { return a(f) || b(f) }
	case ".eqv.":
		fn = func(f *frame) bool { return a(f) == b(f) }
	default:
		fn = func(f *frame) bool { return a(f) != b(f) }
	}
	return &ex{k: kLog, slot: -1, b: fn}, nil
}

// narrow wraps v to the integer kind kd (0: integer(8)), as gfortran's
// integer(1|2|4) stores do.
func narrow(kd int, v int64) int64 {
	switch kd {
	case 1:
		return int64(int8(v))
	case 2:
		return int64(int16(v))
	case 4:
		return int64(int32(v))
	}
	return v
}

// ipow is x**k for integers (F2018 10.1.5.2.1: a negative exponent gives 0
// unless the base is 1 or -1), by repeated squaring with wrapping arithmetic.
func ipow(x, k int64) int64 {
	if k < 0 {
		switch x {
		case 1:
			return 1
		case -1:
			if k&1 == 0 {
				return 1
			}
			return -1
		}
		return 0
	}
	r := int64(1)
	for ; k > 0; k >>= 1 {
		if k&1 != 0 {
			r *= x
		}
		x *= x
	}
	return r
}

// roundHalfAway is NINT's rounding (halves away from zero).
func roundHalfAway(x float64) float64 { return math.Round(x) }
