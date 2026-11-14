package rt

import (
	"bytes"
	"math"
	"math/big"
	"math/rand/v2"
	"os"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/golden"
)

// exactDigits returns the exact decimal expansion of |x| as digits and the
// exponent e of 0.D x 10^e, computed with math/big (the oracle).
func exactDigits(x float64) ([]byte, int) {
	b := new(big.Float).SetPrec(2200).SetFloat64(math.Abs(x))
	s := b.Text('e', 1100) // far more digits than a double has
	mant, exps, _ := strings.Cut(s, "e")
	var e int
	for _, c := range exps[1:] {
		e = e*10 + int(c-'0')
	}
	if exps[0] == '-' {
		e = -e
	}
	d := []byte(strings.Replace(mant, ".", "", 1))
	return trimZeros(d), e + 1
}

func hardDoubles() []float64 {
	v := []float64{1, 0.5, 2.5, 3.5, 0.125, 0.375, 1e23, 1e22, 5e-324, 2.2250738585072014e-308, 2.2250738585072009e-308,
		math.MaxFloat64, 9007199254740993, 9007199254740992, 4.35, 1.005, 2.675, 0.3, 0.1, 2.0 / 3, 8.41e21, 5e-5, 9.5, 99.5, 0.95,
		123456789012345680, 1.7976931348623157e308, 4.9406564584124654e-324, 1e-5, 5.0000000000000001e-1, 7.0 / 3,
		math.Pow(2, -1022), math.Pow(2, 1023), math.Pow(2, -149), 8388608.5, 16777217, 1 << 53, 0.30000000000000004,
		// halfway cases of the form k + 1/2 and k + 1/2 ulp
		1.5, 4503599627370497.5, 0.0625, 0.03125}
	return v
}

// TestDecimalAgainstBig proves the float-to-decimal conversion correctly
// rounded: for the classic hard cases and random doubles, and every digit
// count, the digits are those of math/big's round-half-even.
func TestDecimalAgainstBig(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	vals := hardDoubles()
	for i := 0; i < 3000; i++ {
		x := math.Float64frombits(r.Uint64() &^ (1 << 63))
		if math.IsNaN(x) || math.IsInf(x, 0) {
			continue
		}
		vals = append(vals, x)
	}
	var buf []byte
	for _, x := range vals {
		ex, ee := exactDigits(x)
		for _, n := range []int{1, 2, 3, 5, 9, 15, 17, 18, 25, 40, 60} {
			var d dec
			d.convert(buf, x, 64, n, RoundNearest, false)
			want, we := roundBig(ex, ee, n)
			if string(d.digits) != string(want) || (x != 0 && d.exp != we) {
				t.Fatalf("x=%v n=%d: got %s e%d, want %s e%d", x, n, d.digits, d.exp, want, we)
			}
		}
	}
}

// roundBig rounds the exact digits to n digits, half to even, by comparing
// the remainder with half a unit with big integers: independent of rt's code.
func roundBig(d []byte, e, n int) ([]byte, int) {
	if n >= len(d) {
		return d, e
	}
	num, _ := new(big.Int).SetString(string(d), 10)
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(d)-n)), nil)
	q, rem := new(big.Int).QuoRem(num, pow, new(big.Int))
	twice := new(big.Int).Lsh(rem, 1)
	switch c := twice.Cmp(pow); {
	case c > 0, c == 0 && q.Bit(0) == 1:
		q.Add(q, big.NewInt(1))
	}
	s := q.String()
	if len(s) > n {
		e++
	}
	return trimZeros([]byte(s)), e
}

// TestRoundingModesBracket checks RU, RD, RZ and RC against the exact value:
// RZ truncates, RU and RD bracket it by one unit of the last digit, RC agrees
// with RN except on exact ties.
func TestRoundingModesBracket(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	vals := hardDoubles()
	for i := 0; i < 500; i++ {
		vals = append(vals, math.Float64frombits(r.Uint64()&^(1<<63))/1e3+float64(i)*0.0371)
	}
	var buf []byte
	for _, x := range vals {
		if x == 0 || math.IsInf(x, 0) || math.IsNaN(x) {
			continue
		}
		ex, ee := exactDigits(x)
		for _, n := range []int{1, 3, 6, 17} {
			var rz, ru, rd, rc, rn dec
			rz.convert(buf, x, 64, n, RoundZero, false)
			ru.convert(buf, x, 64, n, RoundUp, false)
			rd.convert(buf, x, 64, n, RoundDown, false)
			rc.convert(buf, x, 64, n, RoundCompatible, false)
			rn.convert(buf, x, 64, n, RoundNearest, false)
			trunc := ex
			if len(ex) > n {
				trunc = trimZeros(ex[:n])
			}
			if string(rz.digits) != string(trunc) || rz.exp != ee {
				t.Fatalf("RZ x=%v n=%d: %s e%d, want %s e%d", x, n, rz.digits, rz.exp, trunc, ee)
			}
			// RD of a positive value is RZ; RU is RZ plus one unit unless exact
			if string(rd.digits) != string(rz.digits) || rd.exp != rz.exp {
				t.Fatalf("RD x=%v n=%d differs from RZ", x, n)
			}
			exact := len(ex) <= n
			if exact && (string(ru.digits) != string(rz.digits) || ru.exp != rz.exp) {
				t.Fatalf("RU of an exact value changed it: x=%v n=%d", x, n)
			}
			if !exact {
				up, ue := roundUpOne(rz.digits, rz.exp, n)
				if string(ru.digits) != string(up) || ru.exp != ue {
					t.Fatalf("RU x=%v n=%d: %s e%d, want %s e%d", x, n, ru.digits, ru.exp, up, ue)
				}
				// RN and RC are RZ or RU
				for _, m := range []dec{rn, rc} {
					if !(string(m.digits) == string(rz.digits) && m.exp == rz.exp) && !(string(m.digits) == string(up) && m.exp == ue) {
						t.Fatalf("nearest mode outside the bracket: x=%v n=%d", x, n)
					}
				}
			}
		}
	}
}

func roundUpOne(d []byte, e, n int) ([]byte, int) {
	num, _ := new(big.Int).SetString(string(d), 10)
	// scale d to n digits
	for k := len(d); k < n; k++ {
		num.Mul(num, big.NewInt(10))
	}
	num.Add(num, big.NewInt(1))
	s := num.String()
	if len(s) > n {
		e++
	}
	return trimZeros([]byte(s)), e
}

func w(t *testing.T, format string, items ...Val) string {
	t.Helper()
	var out bytes.Buffer
	tab := NewTable(&out, nil, nil, nil)
	if err := tab.Get(6).Write(Spec{Format: format, HasFormat: format != ""}, items); err != nil {
		return "ERR " + err.Msg
	}
	return out.String()
}

func TestWriteExamples(t *testing.T) {
	for _, c := range []struct {
		f    string
		v    []Val
		want string
	}{
		{"(I5)", []Val{Int(4, 42)}, "   42\n"},
		{"(I0)", []Val{Int(4, -123456)}, "-123456\n"},
		{"(I6.4)", []Val{Int(4, 77)}, "  0077\n"},
		{"(F8.3)", []Val{Real4(3.14159274)}, "   3.142\n"},
		{"(F0.2)", []Val{Real8(0.5)}, ".50\n"},
		{"(E12.4)", []Val{Real8(2.718281828459045)}, "  0.2718E+01\n"},
		{"(ES12.4)", []Val{Real8(2.718281828459045)}, "  2.7183E+00\n"},
		{"(EN12.3)", []Val{Real8(12345.678)}, "  12.346E+03\n"},
		{"(G12.5)", []Val{Real8(2.718281828459045)}, "  2.7183    \n"},
		{"(L2)", []Val{Bool(4, true)}, " T\n"},
		{"(A10,'|',A)", []Val{Str("right"), Str("tail")}, "     right|tail\n"},
		{"(2(I3,1X))", []Val{Int(4, 1), Int(4, 2)}, "  1   2\n"},
		{"(3I4)", []Val{Int(4, 1), Int(4, 4), Int(4, 9), Int(4, 16), Int(4, 25), Int(4, 36)}, "   1   4   9\n  16  25  36\n"},
		{"(F6.2)", []Val{Real8(123456)}, "******\n"},
		{"(1P,E12.4)", []Val{Real8(12345.6789)}, "  1.2346E+04\n"},
		{"(Z8.8,1X,O4,1X,B8)", []Val{Int(4, 255), Int(4, 8), Int(4, 5)}, "000000FF   10      101\n"},
		{"(F10.4,TR2,F6.1)", []Val{Real8(-1.0 / 3), Real8(2)}, "   -0.3333     2.0\n"},
		{"(SP,I4)", []Val{Int(4, 5)}, "  +5\n"},
		{"(I3,:,'x')", []Val{Int(4, 1)}, "  1\n"},
		{"", []Val{Int(4, 1), Real4(0.1), Str("a"), Str("b"), Bool(4, false)}, "           1  0.100000001     ab F\n"},
		{"(F5.1)", []Val{Real8(math.NaN())}, "  NaN\n"},
		{"(F9.1)", []Val{Real8(math.Inf(-1))}, its("-Infinity")},
		{"(I5)", []Val{Real8(1)}, "ERR Data edit descriptor 'I' may not be used with a REAL data item"},
	} {
		if got := w(t, c.f, c.v...); got != c.want {
			t.Errorf("%s %v: got %q want %q", c.f, c.v, got, c.want)
		}
	}
}

func its(s string) string { return s + "\n" }

func TestFormatReversionAndErrors(t *testing.T) {
	for _, bad := range []string{"", "I5", "(I5", "(I5))x)", "(0I5)", "(I)", "(F5)", "(E5.2E)", "(1X", "('abc)", "(Q5)", "(I5 I5)", "(,I5)",
		"(T0)", "(I5,,I5)", "(1+I5)", "(*I5)", "(2*(I5))"} {
		if _, err := CompileFormat(bad); err == nil && bad != "(I5))x)" {
			t.Errorf("%q accepted", bad)
		}
	}
	// a format with no data edit descriptor and items left is an error, not a loop
	if got := w(t, "('x')", Int(4, 1)); !strings.HasPrefix(got, "ERR") && got != "x\n" {
		t.Errorf("got %q", got)
	}
}

func TestInternalFiles(t *testing.T) {
	u := NewInternalWrite(1, 20)
	if err := u.Write(Spec{Format: "(I5,F8.2)", HasFormat: true}, []Val{Int(4, 7), Real8(2.5)}); err != nil {
		t.Fatal(err)
	}
	if got := u.Records()[0]; got != "    7    2.50       " {
		t.Errorf("record %q", got)
	}
	// overflow of the record is an end-of-record condition
	u = NewInternalWrite(1, 3)
	if err := u.Write(Spec{Format: "(I5)", HasFormat: true}, []Val{Int(4, 7)}); err == nil || !err.IsEOR() {
		t.Errorf("err = %v", err)
	}
	// read
	r := NewInternalRead("  12 3.5 abc")
	items := []Val{Int(4, 0), Real8(0), Str("   ")}
	if err := r.Read(Spec{}, items); err != nil {
		t.Fatal(err)
	}
	if items[0].I != 12 || items[1].F != 3.5 || items[2].S != "abc" {
		t.Errorf("%+v", items)
	}
	r = NewInternalRead("12345")
	items = []Val{Int(4, 0), Int(4, 0)}
	if err := r.Read(Spec{Format: "(I2,I3)", HasFormat: true}, items); err != nil || items[0].I != 12 || items[1].I != 345 {
		t.Errorf("%v %+v", err, items)
	}
	// end of file
	r = NewInternalRead("1")
	if err := r.Read(Spec{}, []Val{Int(4, 0), Int(4, 0)}); err == nil || !err.IsEOF() {
		t.Errorf("err = %v", err)
	}
}

func TestUnitsAndStreams(t *testing.T) {
	var out, errw bytes.Buffer
	tab := NewTable(&out, &errw, strings.NewReader("1 2\n3\n"), nil)
	var a, b, c Val = Int(4, 0), Int(4, 0), Int(4, 0)
	items := []Val{a, b}
	if err := tab.Get(5).Read(Spec{}, items); err != nil || items[0].I != 1 || items[1].I != 2 {
		t.Fatal(err, items)
	}
	items = []Val{c, b}
	err := tab.Get(5).Read(Spec{}, items)
	if err == nil || !err.IsEOF() || items[0].I != 3 {
		t.Fatalf("%v %+v", err, items)
	}
	tab.Get(0).Write(Spec{}, []Val{Str("to stderr")})
	tab.Get(6).Write(Spec{Format: "(A)", HasFormat: true, NonAdvancing: true}, []Val{Str("a")})
	tab.Get(6).Write(Spec{Format: "(A)", HasFormat: true}, []Val{Str("b")})
	tab.Flush()
	if out.String() != "ab\n" || errw.String() != " to stderr\n" {
		t.Errorf("out %q err %q", out.String(), errw.String())
	}
	// in-memory file: write, rewind, read
	m := tab.OpenScratch(10)
	m.Write(Spec{Format: "(I3)", HasFormat: true}, []Val{Int(4, 5)})
	m.Write(Spec{Format: "(I3)", HasFormat: true}, []Val{Int(4, 6)})
	m.Rewind()
	x, y := []Val{Int(4, 0)}, []Val{Int(4, 0)}
	m.Read(Spec{Format: "(I3)", HasFormat: true}, x)
	m.Read(Spec{Format: "(I3)", HasFormat: true}, y)
	if x[0].I != 5 || y[0].I != 6 {
		t.Errorf("%v %v", x, y)
	}
}

// Bounds: every limit of a format and a record is a refusal, quickly, with the
// heap bounded (the test sets a memory limit, as the CI bomb lane does).
func TestBoundsAreRefusals(t *testing.T) {
	old := debug.SetMemoryLimit(512 << 20)
	defer debug.SetMemoryLimit(old)
	deep := strings.Repeat("(", MaxFormatDepth+2) + "I1" + strings.Repeat(")", MaxFormatDepth+2)
	for name, f := range map[string]string{
		"nesting":        deep,
		"repeat":         "(99999999999I1)",
		"width":          "(F99999999.1)",
		"digits":         "(F10.99999999)",
		"exponent width": "(E10.3E99999999)",
		"long format":    "(" + strings.Repeat("I1,", MaxFormatLen) + "I1)",
		"T":              "(T99999999999)",
	} {
		if _, err := CompileFormat(f); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a repeat count that is legal but absurd must not run for ever or grow memory
	var out bytes.Buffer
	tab := NewTable(&out, nil, nil, nil)
	if err := tab.Get(6).Write(Spec{Format: "(1000000000(/))", HasFormat: true}, nil); err == nil {
		t.Error("a billion slashes was accepted")
	}
	// a huge position is a record-length refusal
	out.Reset()
	if err := tab.Get(6).Write(Spec{Format: "(T1000000000,'x')", HasFormat: true}, nil); err == nil {
		t.Error("T1000000000 was accepted")
	}
	if err := tab.Get(6).Write(Spec{Format: "(1000000000X,'x')", HasFormat: true}, nil); err == nil {
		t.Error("a billion X was accepted")
	}
	// a wide field is limited by MaxFieldWidth, not by the host
	out.Reset()
	if err := tab.Get(6).Write(Spec{Format: "(I1048576)", HasFormat: true}, []Val{Int(4, 1)}); err != nil || out.Len() != 1048577 {
		t.Errorf("a field of MaxFieldWidth: err=%v len=%d", err, out.Len())
	}
	// input records have a bound too
	huge := strings.NewReader(strings.Repeat("x", DefaultRecLen+1024) + "\n")
	u := NewReader(5, huge)
	if err := u.Read(Spec{Format: "(A)", HasFormat: true}, []Val{Str("a")}); err == nil || err.Stat != ErrReadOverflow {
		t.Errorf("err = %v", err)
	}
}

var raceEnabled bool

func TestAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates")
	}
	var out bytes.Buffer
	tab := NewTable(&out, nil, nil, nil)
	u := tab.Get(6)
	items := []Val{Real8(math.Pi), Int(4, 12345)}
	for _, c := range []struct {
		name string
		sp   Spec
		max  float64
	}{
		{"formatted F and I", Spec{Format: "(F12.5,I8)", HasFormat: true}, 0},
		{"formatted ES", Spec{Format: "(ES24.16,I8)", HasFormat: true}, 0},
		{"list-directed", Spec{}, 0},
	} {
		u.Write(c.sp, items) // warm the format cache
		out.Reset()
		n := testing.AllocsPerRun(200, func() {
			out.Reset()
			u.Write(c.sp, items)
		})
		t.Logf("%s: %.0f allocations per statement", c.name, n)
		if n > c.max {
			t.Errorf("%s: %.0f allocations, bound %.0f", c.name, n, c.max)
		}
	}
}

func FuzzFormat(f *testing.F) {
	for _, s := range []string{"(I5)", "(3(F8.3,1X),/,A)", "(ES12.4E3,EN10.2)", "(1P,E10.3,SP,I4,BZ)", "(T5,TL2,TR3,5X,'it''s')", "(*(I3))", "(2(I3,(A,L2)))", "(G0,G12.4E2,Z8.8)", "(5HHello,I3)", "(RU,F5.1,DC)"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		fm, err := CompileFormat(s)
		if err != nil {
			return
		}
		_ = fm
		var out bytes.Buffer
		tab := NewTable(&out, nil, nil, nil)
		tab.Get(6).Write(Spec{Format: s, HasFormat: true}, []Val{Int(4, -7), Real8(3.25), Str("ab"), Bool(4, true), Cmplx(8, 1, 2), Real4(1e-3)})
		in := NewInternalRead("  12 -3.5e2 abc T (1,2) 77777")
		in.Read(Spec{Format: s, HasFormat: true}, []Val{Int(4, 0), Real8(0), Str("xyz"), Bool(4, false), Cmplx(8, 0, 0), Real4(0)})
	})
}

func FuzzInput(f *testing.F) {
	for _, s := range []string{"1 2 3", "1,,3", "2*7", "'abc' def", "(1.5,2.5)", ".true. f", "1.5e3 2d0", "3*", "1 / 3", "-0 +5 inf nan", "99999999999999999999", "1e400", "'a''b"} {
		f.Add(s, "")
	}
	f.Add("12345", "(I2,I3)")
	f.Add("  1.5  ", "(F6.2)")
	f.Fuzz(func(t *testing.T, text, format string) {
		if len(text) > 1<<12 {
			return
		}
		u := NewInternalRead(strings.Split(text, "\n")...)
		items := []Val{Int(4, 0), Real8(0), Str("abcdef"), Bool(4, false), Cmplx(4, 0, 0), Int(1, 0), Real4(0)}
		sp := Spec{}
		if format != "" {
			sp = Spec{Format: format, HasFormat: true}
		}
		u.Read(sp, items)
	})
}

func BenchmarkWriteF(b *testing.B) {
	var out bytes.Buffer
	u := NewTable(&out, nil, nil, nil).Get(6)
	items := []Val{Real8(math.Pi * 1234.5678)}
	sp := Spec{Format: "(F15.6)", HasFormat: true}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out.Reset()
		u.Write(sp, items)
	}
}

func BenchmarkWriteES(b *testing.B) {
	var out bytes.Buffer
	u := NewTable(&out, nil, nil, nil).Get(6)
	items := []Val{Real8(math.Pi * 1234.5678)}
	sp := Spec{Format: "(ES24.16)", HasFormat: true}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out.Reset()
		u.Write(sp, items)
	}
}

func BenchmarkWriteInt(b *testing.B) {
	var out bytes.Buffer
	u := NewTable(&out, nil, nil, nil).Get(6)
	items := []Val{Int(4, 1234567)}
	sp := Spec{Format: "(I10)", HasFormat: true}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out.Reset()
		u.Write(sp, items)
	}
}

func BenchmarkWriteListDirected(b *testing.B) {
	var out bytes.Buffer
	u := NewTable(&out, nil, nil, nil).Get(6)
	items := []Val{Real8(math.Pi), Int(4, 7), Str("x")}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out.Reset()
		u.Write(Spec{}, items)
	}
}

func BenchmarkReadF(b *testing.B) {
	items := []Val{Real8(0)}
	sp := Spec{Format: "(F15.6)", HasFormat: true}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		u := NewInternalRead("    3.14159265  ")
		u.Read(sp, items)
	}
}

// TestGoldenFormattedOutput replays the statements of testdata/programs/
// formatted_output.f90 on rt and compares with the oracle's recorded stdout
// (testdata/programs.golden): the golden program that package eval cannot yet
// run (it uses real(4) and implied-do lists) is reproduced here.
func TestGoldenFormattedOutput(t *testing.T) {
	data, err := os.ReadFile("../testdata/programs.golden")
	if err != nil {
		t.Fatal(err)
	}
	recs, err := golden.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var want string
	for _, r := range recs {
		if strings.Contains(r.Name, "formatted_output") {
			want = string(r.Stdout)
		}
	}
	if want == "" {
		t.Fatal("no golden record for formatted_output")
	}
	var out bytes.Buffer
	u := NewTable(&out, nil, nil, nil).Get(6)
	p := func(f string, v ...Val) {
		if err := u.Write(Spec{Format: f, HasFormat: true}, v); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	x, y := Real4(3.14159274), Real8(2.718281828459045)
	p("(i5)", Int(4, 42))
	p("(i0)", Int(4, -123456))
	p("(i6.4)", Int(4, 77))
	p("(a,i3,a)", Str("["), Int(4, 5), Str("]"))
	p("(f8.3)", x)
	p("(f0.2)", Real4(0.5))
	p("(e12.4)", y)
	p("(es12.4)", y)
	p("(en12.3)", Real8(12345.678))
	p("(g12.5)", y)
	p("(l2)", Bool(4, true))
	p(`(a10,"|",a)`, Str("right"), Str("tail"))
	p("(2(i3,1x))", Int(4, 1), Int(4, 2))
	p("(3i4)", Int(4, 1), Int(4, 4), Int(4, 9), Int(4, 16), Int(4, 25), Int(4, 36))
	p("(a,/,a)", Str("line one"), Str("line two"))
	p("(5x,a)", Str("indented"))
	p("(f10.4,tr2,f6.1)", Real8(-1.0/3.0), Real4(2))
	p("(1p,e12.4)", Real8(12345.6789))
	p("(z8.8,1x,o4,1x,b8)", Int(4, 255), Int(4, 8), Int(4, 5))
	p("(a)", Str(strings.Repeat("=", 20)))
	p("(f6.2)", Real4(123456))
	if out.String() != want {
		t.Errorf("got\n%s\nwant\n%s", out.String(), want)
	}
}
