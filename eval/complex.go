package eval

import (
	"math"
	"math/cmplx"

	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// Complex scalars, complex(4) and complex(8) (F2018 7.4.3.3). A variable is
// two consecutive real slots, the real part first; a complex(4) value is
// always held with both parts representable in single precision (kd 4).
//
// The arithmetic is gfortran's at -O0, the oracle's level (gcc/tree-complex.cc,
// read at gcc-mirror/gcc master): every value is VARYING, a real operand
// converted to (r, +0) included, because gfortran honours signed zeros
// (some_nonzerop), so the general formulas apply to every operation:
//
//	(a+bi)(c+di) = (ac - bd) + (ad + bc)i        expand_complex_multiplication_components
//	(a+bi)/(c+di)  Smith's method, |c| < |d| decides the branch   expand_complex_div_wide
//
// each product, sum and quotient rounded to the operation's kind (no fused
// multiply-add: -O0 contracts nothing, and the explicit conversions below keep
// Go from fusing). z**n for a constant n of -1, 0, 1 or 2 is expanded inline,
// any other integer exponent is libgfortran's binary method (pow_c4_i4.c,
// gfc_conv_cst_int_power in gcc/fortran/trans-expr.cc).
//
// Error bound: + - * / and integer powers of -1..2 are the reference's
// operations, rounded the same way. A larger integer power follows the
// reference's order of operations, but libgfortran is built at -O2, where the
// products may be contracted: within 2 ulp per multiplication of the exact
// result. abs is within 1 ulp of the exact |z| (see hypot). sqrt, exp, log,
// sin, cos and a non-integer power are Go's math/cmplx, within a few ulp of
// the C library's.

type cplxFn func(*frame) complex128

func constCplx(v complex128) *ex {
	return &ex{k: kCplx, isC: true, cz: v, slot: -1, z: func(*frame) complex128 { return v }}
}

// constCmplx is the value of a constant the analyser folded, as a complex.
func constCmplx(k *sema.Const) complex128 {
	switch k.Type.Basic {
	case sema.Complex:
		return k.C
	case sema.Integer:
		return complex(float64(k.I), 0)
	}
	return complex(k.F, 0)
}

// asCplx is an integer, real or complex value as a complex: (x, +0), the
// conversion an operation with a complex operand makes (F2018 10.1.5.2.1).
// Its kd is 4 only when both parts are single precision values.
func (e *ex) asCplx() *ex {
	switch e.k {
	case kInt, kReal:
		kd := 0
		if e.k == kReal && e.kd == 4 {
			kd = 4
		}
		r := e.asReal()
		if r.isC {
			z := constCplx(complex(r.cr, 0))
			z.kd = kd
			return z
		}
		fr := r.r
		return &ex{k: kCplx, kd: kd, slot: -1, z: func(f *frame) complex128 { return complex(fr(f), 0) }}
	}
	return e
}

// round4 is z with both parts rounded to single precision.
func round4(z complex128) complex128 {
	return complex(float64(float32(real(z))), float64(float32(imag(z))))
}

// cplxRead is the complex variable s, which lives in the frame h static links
// up (0: this frame; see hops).
func cplxRead(s *sym, h int) *ex {
	if s.isC {
		e := constCplx(s.cz)
		e.kd = s.kd
		return e
	}
	slot := s.slot
	return &ex{k: kCplx, kd: s.kd, slot: -1, z: func(f *frame) complex128 {
		g := f.at(h)
		return complex(g.reals[slot], g.reals[slot+1])
	}}
}

// cplxStore compiles "s = v" where s or v is complex, s a scalar of the frame h
// static links up: a complex value given to an integer or a real is its real
// part converted (F2018 10.2.1.3, Table 10.8), any numeric value given to a
// complex is converted to it.
func (c *compiler) cplxStore(s *sym, v *ex, h int, at syntax.Node) (stmtFn, error) {
	slot, kd := s.slot, s.kd
	switch {
	case s.k == kCplx && (v.k == kInt || v.k == kReal || v.k == kCplx):
		z := v.asCplx()
		fn, round := z.z, kd == 4 && z.kd != 4
		return func(f *frame) {
			x := fn(f)
			if round {
				x = round4(x)
			}
			g := f.at(h)
			g.reals[slot], g.reals[slot+1] = real(x), imag(x)
		}, nil
	case s.k == kInt && v.k == kCplx:
		fn := v.z
		return func(f *frame) { x := real(fn(f)); f.at(h).ints[slot] = narrow(kd, int64(x)) }, nil
	case s.k == kReal && v.k == kCplx:
		fn, round := v.z, kd == 4 && v.kd != 4
		return func(f *frame) {
			x := real(fn(f))
			if round {
				x = float64(float32(x))
			}
			f.at(h).reals[slot] = x
		}, nil
	}
	return nil, c.errf(at, "this assignment is not supported (an array into a scalar, or a type the evaluator does not convert)")
}

// cmul is (a+bi)(c+di) at kind kd, as expand_complex_multiplication_components
// computes it.
func cmul(kd int, x, y complex128) complex128 {
	if kd == 4 {
		a, b, c, d := float32(real(x)), float32(imag(x)), float32(real(y)), float32(imag(y))
		re := float32(a*c) - float32(b*d)
		im := float32(a*d) + float32(b*c)
		return complex(float64(re), float64(im))
	}
	a, b, c, d := real(x), imag(x), real(y), imag(y)
	re := float64(a*c) - float64(b*d)
	im := float64(a*d) + float64(b*c)
	return complex(re, im)
}

// cdiv is (a+bi)/(c+di) at kind kd, Smith's method as expand_complex_div_wide
// computes it.
func cdiv(kd int, x, y complex128) complex128 {
	if kd == 4 {
		a, b, c, d := float32(real(x)), float32(imag(x)), float32(real(y)), float32(imag(y))
		var re, im float32
		if abs32(c) < abs32(d) {
			ratio := c / d
			div := float32(c*ratio) + d
			re = (float32(a*ratio) + b) / div
			im = (float32(b*ratio) - a) / div
		} else {
			ratio := d / c
			div := float32(d*ratio) + c
			re = (float32(b*ratio) + a) / div
			im = (b - float32(a*ratio)) / div
		}
		return complex(float64(re), float64(im))
	}
	a, b, c, d := real(x), imag(x), real(y), imag(y)
	var re, im float64
	if math.Abs(c) < math.Abs(d) {
		ratio := c / d
		div := float64(c*ratio) + d
		re = (float64(a*ratio) + b) / div
		im = (float64(b*ratio) - a) / div
	} else {
		ratio := d / c
		div := float64(d*ratio) + c
		re = (float64(b*ratio) + a) / div
		im = (b - float64(a*ratio)) / div
	}
	return complex(re, im)
}

func abs32(x float32) float32 { return math.Float32frombits(math.Float32bits(x) &^ (1 << 31)) }

// cadd is x+y (or x-y when sub) at kind kd.
func cadd(kd int, x, y complex128, sub bool) complex128 {
	if kd == 4 {
		a, b, c, d := float32(real(x)), float32(imag(x)), float32(real(y)), float32(imag(y))
		if sub {
			return complex(float64(a-c), float64(b-d))
		}
		return complex(float64(a+c), float64(b+d))
	}
	if sub {
		return complex(real(x)-real(y), imag(x)-imag(y))
	}
	return complex(real(x)+real(y), imag(x)+imag(y))
}

// cpowi is x**n by libgfortran's binary method (pow_c4_i4.c): a negative
// exponent inverts x first.
func cpowi(kd int, x complex128, n int64) complex128 {
	pow := complex(1, 0)
	if n == 0 {
		return pow
	}
	u := uint64(n)
	if n < 0 {
		u = uint64(-n) // -MinInt64 wraps to its own magnitude as an unsigned value
		x = cdiv(kd, pow, x)
	}
	for {
		if u&1 != 0 {
			pow = cmul(kd, pow, x)
		}
		u >>= 1
		if u == 0 {
			break
		}
		x = cmul(kd, x, x)
	}
	return pow
}

// cplxBinary compiles + - * / ** with a complex operand; tv is the result's
// type (the operand of the smaller kind is converted, F2018 Table 10.2).
func (c *compiler) cplxBinary(n *syntax.Binary, x, y *ex, tv sema.TV) (*ex, error) {
	kd := kdOf(tv.Type)
	if n.Op == "**" && y.k == kInt {
		a := x.asCplx().z
		if y.isC && y.ci >= -1 && y.ci <= 2 {
			// expanded inline (gfc_conv_cst_int_power)
			switch y.ci {
			case 0:
				return &ex{k: kCplx, kd: kd, slot: -1, z: func(*frame) complex128 { return 1 }}, nil
			case 1:
				return &ex{k: kCplx, kd: kd, slot: -1, z: a}, nil
			case 2:
				return &ex{k: kCplx, kd: kd, slot: -1, z: func(f *frame) complex128 { v := a(f); return cmul(kd, v, v) }}, nil
			}
			return &ex{k: kCplx, kd: kd, slot: -1, z: func(f *frame) complex128 { return cdiv(kd, 1, a(f)) }}, nil
		}
		e := y.i
		return &ex{k: kCplx, kd: kd, slot: -1, z: func(f *frame) complex128 { return cpowi(kd, a(f), e(f)) }}, nil
	}
	a, b := x.asCplx().z, y.asCplx().z
	var fn cplxFn
	switch n.Op {
	case "+":
		fn = func(f *frame) complex128 { return cadd(kd, a(f), b(f), false) }
	case "-":
		fn = func(f *frame) complex128 { return cadd(kd, a(f), b(f), true) }
	case "*":
		fn = func(f *frame) complex128 { return cmul(kd, a(f), b(f)) }
	case "/":
		fn = func(f *frame) complex128 { return cdiv(kd, a(f), b(f)) }
	default: // ** with a real or complex exponent: cpow, exp(y log x)
		fn = func(f *frame) complex128 { return cmplx.Pow(a(f), b(f)) }
	}
	return &ex{k: kCplx, kd: kd, slot: -1, z: fn}, nil
}

// cplxCompare compiles == and /= with a complex operand: the other operand is
// converted to complex, and the values are equal when both parts are
// (expand_complex_comparison).
func (c *compiler) cplxCompare(n *syntax.Binary, x, y *ex) (*ex, error) {
	if n.Op != "==" && n.Op != "/=" {
		return nil, c.errf(n, "operator %q is not defined on complex operands", n.Op)
	}
	// operands of single precision only are compared at that precision: an
	// integer operand is converted to complex(4) then
	single := (x.k == kInt || x.kd == 4) && (y.k == kInt || y.kd == 4)
	a, b := x.asCplx().z, y.asCplx().z
	ne := n.Op == "/="
	return &ex{k: kLog, slot: -1, b: func(f *frame) bool {
		p, q := a(f), b(f)
		if single {
			p, q = round4(p), round4(q)
		}
		return (real(p) == real(q) && imag(p) == imag(q)) != ne
	}}, nil
}

// cplxNegate is -x for a complex x.
func cplxNegate(x *ex) *ex {
	if x.isC {
		e := constCplx(-x.cz)
		e.kd = x.kd
		return e
	}
	a := x.z
	return &ex{k: kCplx, kd: x.kd, slot: -1, z: func(f *frame) complex128 { return -a(f) }}
}

// hypot is sqrt(x*x + y*y) without overflow or underflow in the squares: the
// sum is formed exactly as a double-double and its square root corrected once,
// within 1 ulp of the exact value (C's cabs is held to no tighter bound).
func hypot(x, y float64) float64 {
	x, y = math.Abs(x), math.Abs(y)
	switch {
	case math.IsInf(x, 0) || math.IsInf(y, 0):
		return math.Inf(1)
	case math.IsNaN(x) || math.IsNaN(y):
		return math.NaN()
	}
	if x < y {
		x, y = y, x
	}
	if y == 0 {
		return x
	}
	_, e := math.Frexp(x)
	x, y = math.Ldexp(x, -e), math.Ldexp(y, -e) // x in [0.5, 1): the squares cannot overflow
	hx := x * x
	lx := math.FMA(x, x, -hx)
	hy := y * y
	ly := math.FMA(y, y, -hy)
	s := hx + hy
	t := hy - (s - hx) + lx + ly // hx >= hy: the rounding error of s, plus the low parts
	r := math.Sqrt(s)
	r += (math.FMA(-r, r, s) + t) / (2 * r)
	return math.Ldexp(r, e)
}

func numeric(e *ex) bool { return e.k == kInt || e.k == kReal || e.k == kCplx }

// cplxIntrinsic compiles an intrinsic whose argument is complex, or which
// makes one (cmplx); handled is false for one it does not know.
func (c *compiler) cplxIntrinsic(n *syntax.Index, name string, tv sema.TV, args []*ex) (e *ex, handled bool, err error) {
	rkd := kdOf(tv.Type)
	switch name {
	case "cmplx", "dcmplx":
		// cmplx(x [, y [, kind]]), positional (keywords are refused before):
		// the analyser typed the result, of the kind given or the default; a
		// complex x has no y (F2018 16.9.55)
		if len(args) < 1 || len(args) > 3 || !numeric(args[0]) || len(args) >= 2 && (args[0].k == kCplx || args[1].k != kInt && args[1].k != kReal) {
			return nil, true, c.errf(n, "%s of these arguments is not supported", name)
		}
		var fn cplxFn
		if len(args) == 1 {
			fn = args[0].asCplx().z
		} else {
			re, im := args[0].asReal().r, args[1].asReal().r
			fn = func(f *frame) complex128 { return complex(re(f), im(f)) }
		}
		if rkd == 4 {
			g := fn
			fn = func(f *frame) complex128 { return round4(g(f)) }
		}
		return &ex{k: kCplx, kd: rkd, slot: -1, z: fn}, true, nil
	}
	if len(args) == 0 || args[0].k != kCplx {
		return nil, false, nil
	}
	if len(args) != 1 && name != "real" && name != "int" && name != "dble" {
		return nil, true, c.errf(n, "%s of a complex argument takes one argument", name)
	}
	a := args[0].z
	akd := args[0].kd
	// a part of the argument is of the argument's kind; expr rounds it to the
	// result's when that is smaller (real(z, 4) of a complex(8) z)
	realOf := func(g realFn) (*ex, bool, error) {
		return &ex{k: kReal, kd: akd, slot: -1, r: g}, true, nil
	}
	switch name {
	case "real", "dble", "realpart":
		return realOf(func(f *frame) float64 { return real(a(f)) })
	case "aimag", "dimag", "imagpart":
		return realOf(func(f *frame) float64 { return imag(a(f)) })
	case "int":
		kd := rkd
		return &ex{k: kInt, kd: kd, slot: -1, i: func(f *frame) int64 { return narrow(kd, int64(real(a(f)))) }}, true, nil
	case "abs", "cabs", "cdabs", "zabs":
		if akd == 4 {
			return realOf(func(f *frame) float64 { z := a(f); return float64(float32(hypot(real(z), imag(z)))) })
		}
		return realOf(func(f *frame) float64 { z := a(f); return hypot(real(z), imag(z)) })
	case "conjg", "dconjg":
		return &ex{k: kCplx, kd: akd, slot: -1, z: func(f *frame) complex128 { return cmplx.Conj(a(f)) }}, true, nil
	}
	var g func(complex128) complex128
	switch name {
	case "sqrt", "csqrt", "cdsqrt", "zsqrt":
		g = cmplx.Sqrt
	case "exp", "cexp", "cdexp", "zexp":
		g = cmplx.Exp
	case "log", "clog", "cdlog", "zlog":
		g = cmplx.Log
	case "sin", "csin", "cdsin", "zsin":
		g = cmplx.Sin
	case "cos", "ccos", "cdcos", "zcos":
		g = cmplx.Cos
	default:
		return nil, true, c.errf(n, "%s of a complex argument is not supported yet", name)
	}
	if akd == 4 {
		return &ex{k: kCplx, kd: 4, slot: -1, z: func(f *frame) complex128 { return round4(g(a(f))) }}, true, nil
	}
	return &ex{k: kCplx, slot: -1, z: func(f *frame) complex128 { return g(a(f)) }}, true, nil
}
