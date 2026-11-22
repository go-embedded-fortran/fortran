package eval

import (
	"context"
	"io"
	"math"
	"math/big"
	"os"
	"strings"
	"testing"
)

// TestComplexScalars runs testdata/complex/*.f90 and compares the output with
// the .out beside it, gfortran 15.2.0's at -O0 for the same source, byte for
// byte: complex(4) and complex(8) arithmetic with integer, real and complex
// operands, Smith's division, integer powers inline (-1..2) and by the
// binary method (3, -3, 5, -2), a real power, cmplx/dcmplx, real, dble, aimag,
// int, abs, conjg, sqrt, exp, log, sin, cos, == and /=, conversions in
// assignments, a complex dummy argument copied out, a complex function
// result, a module variable read by host association, a named constant, and
// negation (-0 printed).
func TestComplexScalars(t *testing.T) {
	for _, name := range []string{"cx1"} {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile("testdata/complex/" + name + ".f90")
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile("testdata/complex/" + name + ".out")
			if err != nil {
				t.Fatal(err)
			}
			got, err := runExact(t, string(src))
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
			for i := range max(len(gl), len(wl)) {
				var g, w string
				if i < len(gl) {
					g = gl[i]
				}
				if i < len(wl) {
					w = wl[i]
				}
				if g != w {
					t.Errorf("line %d:\n got %q\nwant %q", i+1, g, w)
				}
			}
		})
	}
}

// TestComplexRefusals pins what is not supported yet: it is refused with a
// positioned error, never run wrong.
func TestComplexRefusals(t *testing.T) {
	for name, src := range map[string]string{
		"array":             "program p\n  implicit none\n  complex(8) :: c(2)\n  c = 1\nend program p\n",
		"tan of a complex":  "program p\n  implicit none\n  complex(8) :: c\n  c = (1d0, 1d0)\n  print *, tan(c)\nend program p\n",
		"ordering":          "program p\n  implicit none\n  complex(8) :: c\n  c = 1\n  print *, c < c\nend program p\n",
		"complex(16) value": "program p\n  implicit none\n  complex(16) :: c\n  c = 1\nend program p\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runExact(t, src); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// TestHypotWithinOneUlp holds hypot to its stated bound against an exact
// oracle: sqrt(x²+y²) computed with math/big at 200 bits, rounded once.
// The inputs cover the scaling edges (subnormal, huge, very different
// magnitudes) and a sweep of ordinary values.
func TestHypotWithinOneUlp(t *testing.T) {
	exact := func(x, y float64) float64 {
		bx, by := new(big.Float).SetPrec(200).SetFloat64(x), new(big.Float).SetPrec(200).SetFloat64(y)
		s := new(big.Float).SetPrec(200).Mul(bx, bx)
		s.Add(s, new(big.Float).SetPrec(200).Mul(by, by))
		r, _ := new(big.Float).SetPrec(200).Sqrt(s).Float64()
		return r
	}
	cases := [][2]float64{
		{3, 4}, {1, 1}, {0.1, 0.3}, {1e308, 1e308}, {1e-320, 3e-320},
		{5e-324, 5e-324}, {1e300, 1e-300}, {math.MaxFloat64, 1}, {0.7, -1.1},
		{1, 1e-17}, {math.SmallestNonzeroFloat64, 0},
	}
	x := 0.123456789
	for range 2000 { // a deterministic sweep of mantissas and exponents
		x = math.Mod(x*997.13+0.31, 13)
		cases = append(cases, [2]float64{math.Ldexp(x, int(x*40)-260), math.Ldexp(13-x, int(x*31)-250)})
	}
	for _, c := range cases {
		got, want := hypot(c[0], c[1]), exact(c[0], c[1])
		if d := math.Abs(got - want); d > ulp(want) {
			t.Errorf("hypot(%g, %g) = %g, exact %g: %g ulp", c[0], c[1], got, want, d/ulp(want))
		}
	}
	for _, c := range [][3]float64{{math.Inf(1), math.NaN(), math.Inf(1)}, {0, 0, 0}} {
		if got := hypot(c[0], c[1]); got != c[2] {
			t.Errorf("hypot(%g, %g) = %g, want %g", c[0], c[1], got, c[2])
		}
	}
	if !math.IsNaN(hypot(math.NaN(), 1)) {
		t.Error("hypot(NaN, 1) is not NaN")
	}
}

// TestComplexExpLogBound holds exp and log of a complex to their stated bound,
// 2 ulp per part, against values exact to 30 digits: exp(1+2i) = e·cos 2 +
// i e·sin 2 (the product formed in math/big) and log(1+2i) = ln 5 / 2 +
// i atan 2. They are math/cmplx's, one ulp from glibc's on these very values
// (gfortran printed -1.1312043837568135, 2.4717266720048188 and
// 0.80471895621705014, 1.1071487177940904), so they are not in the byte for
// byte file.
func TestComplexExpLogBound(t *testing.T) {
	dec := func(s string) *big.Float {
		f, _, err := big.ParseFloat(s, 10, 200, big.ToNearestEven)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	mul := func(a, b string) float64 {
		r, _ := new(big.Float).SetPrec(200).Mul(dec(a), dec(b)).Float64()
		return r
	}
	val := func(s string) float64 { r, _ := dec(s).Float64(); return r }
	const e, cos2, sin2 = "2.71828182845904523536028747135", "-0.41614683654714238699756822950", "0.90929742682568169539601986591"
	for _, c := range []struct {
		fn     string
		re, im float64
	}{
		{"exp", mul(e, cos2), mul(e, sin2)},
		{"log", val("0.804718956217050187300379666615"), val("1.10714871779409050301706546018")},
	} {
		t.Run(c.fn, func(t *testing.T) {
			s := NewSession(SessionOptions{})
			if _, err := s.Run(context.Background(), "complex(8) :: c, z\nc = (1d0, 2d0)\nz = "+c.fn+"(c)\n", io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			v, ok := s.Get("z")
			z, isZ := v.(complex128)
			if !ok || !isZ {
				t.Fatalf("Get(z) = %#v, %v", v, ok)
			}
			for i, p := range [][2]float64{{real(z), c.re}, {imag(z), c.im}} {
				if d := math.Abs(p[0] - p[1]); d > 2*ulp(p[1]) {
					t.Errorf("part %d: %v, exact %v: %g ulp", i, p[0], p[1], d/ulp(p[1]))
				}
			}
		})
	}
}

// TestComplexGet reads complex variables of a session back with their kinds;
// a / 3 is a complex(4) quotient, formed in single precision before b holds it.
func TestComplexGet(t *testing.T) {
	s := NewSession(SessionOptions{})
	if _, err := s.Run(context.Background(), "complex(4) :: a\ncomplex(8) :: b\ncomplex(8), parameter :: p = (1d0, -1d0)\na = (0.1, 2)\nb = a / 3\n", io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]any{"a": complex64(complex(0.1, 2)), "b": cdiv(4, complex(float64(float32(0.1)), 2), 3), "p": complex(1, -1)} {
		if v, ok := s.Get(name); !ok || v != want {
			t.Errorf("Get(%s) = %#v, %v; want %#v", name, v, ok, want)
		}
	}
}

// ulp is the spacing of the floats at |x|.
func ulp(x float64) float64 {
	x = math.Abs(x)
	return math.Nextafter(x, math.Inf(1)) - x
}
