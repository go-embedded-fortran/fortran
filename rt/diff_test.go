package rt

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/oracle"
	"github.com/go-embedded-fortran/fortran/internal/oracle/oracletest"
)

func TestMain(m *testing.M) {
	oracle.Init()
	os.Exit(m.Run())
}

// A differential case: one WRITE (or READ from an internal file) executed by
// gfortran and by rt, whose texts must be byte-identical.
type dcase struct {
	format string // "" for list-directed
	star   bool
	items  []Val
	input  string // READ cases: the internal file
	name   string
}

func fq(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func fortranLit(v Val) string {
	switch v.K {
	case Integer:
		if v.I == math.MinInt64 {
			return "(-9223372036854775807_8-1_8)"
		}
		return fmt.Sprintf("(%d_%d)", v.I, v.Size)
	case Real:
		if v.Size == 4 {
			return fmt.Sprintf("transfer(int(z'%08X',4),1.0)", math.Float32bits(float32(v.F)))
		}
		return fmt.Sprintf("transfer(int(z'%016X',8),1.0d0)", math.Float64bits(v.F))
	case Complex:
		re := fortranLit(Val{K: Real, Size: v.Size, F: v.F})
		im := fortranLit(Val{K: Real, Size: v.Size, F: v.G})
		return fmt.Sprintf("cmplx(%s,%s,%d)", re, im, v.Size)
	case Logical:
		if v.B {
			return fmt.Sprintf(".true._%d", v.Size)
		}
		return fmt.Sprintf(".false._%d", v.Size)
	}
	return "'" + strings.ReplaceAll(v.S, "'", "''") + "'"
}

func (c dcase) fortranStmt(i int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  write(*,'(a,i0)') '#',%d\n", i)
	var parts []string
	for _, v := range c.items {
		parts = append(parts, fortranLit(v))
	}
	f := "*"
	if !c.star {
		// through a variable: a format gfortran can see is checked at compile time
		fmt.Fprintf(&b, "  fv = %s\n", fq(c.format))
		f = "trim(fv)"
	}
	fmt.Fprintf(&b, "  write(*,%s,iostat=ios) %s\n", f, strings.Join(parts, ", "))
	b.WriteString("  if (ios /= 0) write(*,'(a,i0)') 'IOS=',ios\n")
	return b.String()
}

func runRT(cases []dcase) string {
	var out bytes.Buffer
	tab := NewTable(&out, &out, nil, nil)
	u := tab.Get(6)
	for i, c := range cases {
		u.Write(Spec{Format: "(a,i0)", HasFormat: true}, []Val{Str("#"), Int(4, int64(i))})
		sp := Spec{Format: c.format, HasFormat: !c.star}
		if err := u.Write(sp, c.items); err != nil {
			fmt.Fprintf(&out, "IOS=%d\n", err.Stat)
		}
	}
	return out.String()
}

func split(s string) map[string]string {
	m := map[string]string{}
	var key string
	var sb strings.Builder
	flush := func() {
		if key != "" {
			m[key] = sb.String()
		}
		sb.Reset()
	}
	for _, line := range strings.SplitAfter(s, "\n") {
		if strings.HasPrefix(line, "#") && strings.HasSuffix(line, "\n") && !strings.Contains(line[1:], " ") {
			flush()
			key = strings.TrimSpace(line)
			continue
		}
		sb.WriteString(line)
	}
	flush()
	return m
}

type diffStats struct {
	same, differ int
	known        map[string]int // documented gfortran quirks, by class
	examples     []string
}

// compareOutput runs cases through gfortran and rt in chunks.
func compareOutput(t *testing.T, cases []dcase, maxExamples int) diffStats {
	tc := oracletest.Require(t)
	var st diffStats
	seen := map[string]int{}
	const chunk = 1500
	for lo := 0; lo < len(cases); lo += chunk {
		hi := min(lo+chunk, len(cases))
		part := cases[lo:hi]
		var src strings.Builder
		src.WriteString("program d\n  implicit none\n  integer :: ios\n  character(len=512) :: fv\n")
		for i, c := range part {
			src.WriteString(c.fortranStmt(i))
		}
		src.WriteString("end program\n")
		res, err := tc.Output(context.Background(), []byte(src.String()),
			oracle.CompileOptions{Extra: []string{"-fno-range-check", "-fallow-argument-mismatch"}},
			oracle.Limits{Timeout: 120e9, MemoryBytes: -1, MaxOutput: 64 << 20})
		if err != nil {
			t.Fatalf("oracle: %v", err)
		}
		want := split(string(res.Stdout))
		got := split(runRT(part))
		for i, c := range part {
			k := fmt.Sprintf("#%d", i)
			if want[k] == got[k] {
				st.same++
				continue
			}
			if k := knownGfortranQuirk(c); k != "" {
				if st.known == nil {
					st.known = map[string]int{}
				}
				st.known[k]++
				continue
			}
			st.differ++
			key := c.format
			for _, v := range c.items {
				key += fmt.Sprintf("/%d%d", v.K, v.Size)
			}
			if seen[key] < 1 && len(st.examples) < maxExamples {
				seen[key]++
				st.examples = append(st.examples, fmt.Sprintf("%s fmt=%q items=%v\n   gfortran: %q\n   rt:       %q",
					c.name, c.format, c.items, want[k], got[k]))
			}
		}
	}
	return st
}

func hexF64(bits uint64) Val { return Real8(math.Float64frombits(bits)) }

// realValues is the value corpus for REAL(8): the signed zero, denormals,
// extremes, infinities, NaN, classic hard cases and random doubles.
func realValues(r *rand.Rand, nrand int) []float64 {
	v := []float64{0, math.Copysign(0, -1), 1, -1, 0.5, -0.5, 2.5, 3.5, 0.25, 0.125, 0.05, 0.15, 0.95, 0.995, 9.5, 99.5,
		9.999, 99.995, 0.1, 0.2, 0.3, 1.0 / 3, 2.0 / 3, 123456.789, 1e-5, 1e5, 1e10, 1e15, 1e16, 1e22, 1e23, 1e-10, 1e100, 1e-100,
		math.MaxFloat64, math.SmallestNonzeroFloat64, 2.2250738585072014e-308, 2.2250738585072009e-308, 5e-324,
		4.35, 4.45, 5.5, 0.045, 1.005, 2.675, 8.5e-3, 999999.5, 9999999.5, 12345678.9, 0.000123456, 4.9406564584124654e-324,
		9007199254740993, 1.7976931348623157e308, 123456789012345680, 0.3e-300,
		math.Inf(1), math.Inf(-1), math.NaN()}
	for i := 0; i < nrand; i++ {
		switch i % 3 {
		case 0:
			v = append(v, math.Float64frombits(r.Uint64()&^(1<<63))*[]float64{1, -1}[r.IntN(2)])
		case 1:
			v = append(v, (r.Float64()-0.5)*math.Pow(10, float64(r.IntN(12)-4)))
		default:
			v = append(v, float64(r.IntN(2000000)-1000000)/float64([]int{1, 10, 100, 1000, 8, 16}[r.IntN(6)]))
		}
	}
	return v
}

func TestDifferentialReals(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	vals := realValues(r, 40)
	var fmts []string
	for _, w := range []int{0, 1, 3, 5, 8, 10, 12, 15, 20, 30} {
		for _, d := range []int{0, 1, 2, 3, 6, 9, 17} {
			if w != 0 && d >= w+4 {
				continue
			}
			for _, k := range []string{"F", "E", "ES", "EN", "D", "G"} {
				if w == 0 && (k == "E" || k == "D" || k == "G") && d == 0 {
					continue
				}
				fmts = append(fmts, fmt.Sprintf("(%s%d.%d)", k, w, d))
			}
		}
	}
	fmts = append(fmts, "(F0)", "(E0)", "(G0)", "(E12.4E1)", "(E12.4E4)", "(E12.4E0)", "(G12.4E3)", "(G12.4E0)",
		"(ES12.4E1)", "(EN12.4E3)", "(1PE12.4)", "(2PE12.4)", "(-1PE12.4)", "(1PF12.4)", "(2PF12.4)", "(-2PF12.4)",
		"(1PD12.4)", "(1PG12.4)", "(SP,F10.3)", "(SP,ES12.4)", "(SS,F10.3)", "(RU,F6.1)", "(RD,F6.1)", "(RZ,F6.1)",
		"(RN,F6.1)", "(RC,F6.1)", "(RP,F6.1)", "(RU,ES10.2)", "(RD,ES10.2)", "(RZ,ES10.2)", "(RC,ES10.2)",
		"(RU,EN10.2)", "(RD,F4.0)", "(RU,F4.0)", "(RC,F4.0)", "(RZ,F4.0)", "(DC,F8.3)", "(DC,ES10.3)", "(F12.3,3X)",
		"(EX20.5)", "(EX0)", "(EX12.0)", "(EX20)", "(EX10.2)", "(EX20.30)", "(EX20.3E2)")
	var cases []dcase
	for _, f := range fmts {
		// RT_DIFF_ONLY=E0.17,G0.9 restricts the corpus while a quirk is chased
		if only := os.Getenv("RT_DIFF_ONLY"); only != "" && !strings.Contains(","+only+",", ","+strings.Trim(f, "()")+",") {
			continue
		}
		for _, x := range vals {
			cases = append(cases, dcase{format: f, items: []Val{Real8(x)}, name: "r8"})
		}
		// kind 4 on a smaller set
		for _, x := range vals[:60] {
			cases = append(cases, dcase{format: f, items: []Val{Real4(float32(x))}, name: "r4"})
		}
	}
	report(t, "reals", compareOutput(t, cases, 90))
}

func report(t *testing.T, name string, st diffStats) {
	t.Logf("%s: %d identical, %d differing, known gfortran quirks %v", name, st.same, st.differ, st.known)
	for _, e := range st.examples {
		t.Log(e)
	}
	if st.differ > 0 {
		t.Fail()
	}
}

func i4(v int64) Val { return Int(4, v) }

func TestDifferentialIntegersLogicalsChars(t *testing.T) {
	ints := []Val{i4(0), i4(1), i4(-1), i4(5), i4(255), i4(-255), i4(123456789), i4(math.MinInt32), i4(math.MaxInt32),
		Int(8, 0), Int(8, math.MinInt64), Int(8, math.MaxInt64), Int(8, -123456789012), Int(2, -32768), Int(2, 32767),
		Int(1, -128), Int(1, 127), Int(1, 5)}
	var fmts []string
	for _, k := range []string{"I", "B", "O", "Z", "G"} {
		for _, w := range []int{0, 1, 2, 5, 8, 12, 20, 70} {
			fmts = append(fmts, fmt.Sprintf("(%s%d)", k, w))
			for _, m := range []int{0, 1, 3, 8, 20} {
				if k == "G" {
					continue
				}
				fmts = append(fmts, fmt.Sprintf("(%s%d.%d)", k, w, m))
			}
		}
	}
	fmts = append(fmts, "(SP,I6)", "(SS,I6)", "(SP,I0)", "(SP,I6.3)", "(I)", "(F5.1)", "(E10.3)", "(A)", "(A3)", "(L2)", "(I5,I5)", "(2I5)")
	var cases []dcase
	for _, f := range fmts {
		for _, v := range ints {
			cases = append(cases, dcase{format: f, items: []Val{v}, name: "int"})
		}
	}
	lf := []string{"(L1)", "(L0)", "(L5)", "(L12)", "(G5)", "(G0)", "(I3)", "(A)", "(A2)", "(Z4)", "(B4)", "(F5.1)", "(2L3)", "(L1,L1,L1)"}
	for _, f := range lf {
		for _, sz := range []int{1, 2, 4, 8} {
			for _, b := range []bool{true, false} {
				cases = append(cases, dcase{format: f, items: []Val{Bool(sz, b)}, name: "log"})
			}
		}
	}
	strs := []string{"", "a", "abc", "hello world", "x  ", "  y", strings.Repeat("0123456789", 5), "it's", `say "hi"`}
	cf := []string{"(A)", "(A0)", "(A1)", "(A2)", "(A5)", "(A10)", "(A40)", "(G0)", "(G5)", "(G10)", "(I3)", "(F5.1)",
		"('[',A,']')", "(A,A)", "(2A3)"}
	for _, f := range cf {
		for _, s := range strs {
			cases = append(cases, dcase{format: f, items: []Val{Str(s)}, name: "chr"})
		}
	}
	report(t, "ints/logicals/chars", compareOutput(t, cases, 60))
}

func TestDifferentialControl(t *testing.T) {
	ii := func(n int) []Val {
		var v []Val
		for i := 1; i <= n; i++ {
			v = append(v, i4(int64(i*i)))
		}
		return v
	}
	rr := []Val{Real8(1.5), Real8(-2.25), Real8(300), Real4(0.1), Real8(1e10)}
	mix := []Val{i4(42), Real8(3.14159), Str("txt"), Bool(4, true), Cmplx(8, 1.5, -2.5), Cmplx(4, 0.5, 1e10)}
	type tc struct {
		f     string
		items []Val
	}
	list := []tc{
		{"(I4)", ii(6)}, {"(2I4)", ii(6)}, {"(3I4)", ii(7)}, {"(I4,I2)", ii(5)}, {"(I4,/,I4)", ii(3)}, {"(I4/I4)", ii(3)},
		{"(I4,:,I4)", ii(1)}, {"(I4,:,I4)", ii(2)}, {"(I4,:,/,I4)", ii(1)}, {"('x',I4,:,'y')", ii(1)}, {"('x',I4,:,'y')", ii(2)},
		{"(I3,(I2,I1))", ii(7)}, {"(I3,2(I2,I1))", ii(9)}, {"(2(I2,1X),I3)", ii(7)}, {"(1X,2(I2),(I3,1X))", ii(9)},
		{"(I2,(I3,(I4)))", ii(6)}, {"(2(I3,2(I2)))", ii(9)}, {"(*(I3))", ii(5)}, {"(I2,*(I3,1X))", ii(5)}, {"(*(I3,:,','))", ii(4)},
		{"('abc')", nil}, {"('abc',/,'def')", nil}, {"(3(' ab'))", nil}, {"('a',I1,'b')", ii(2)}, {"(5X,I3)", ii(1)}, {"(I3,5X)", ii(1)},
		{"(I3,T10,I3)", ii(2)}, {"(I3,T2,I3)", ii(2)}, {"(T5,I3)", ii(1)}, {"(I3,TL2,I3)", ii(2)}, {"(I3,TR4,I3)", ii(2)},
		{"(T10,'ab',T2,'cd')", nil}, {"(I3,TL10,I3)", ii(2)}, {"(A,T1,A)", []Val{Str("abcdef"), Str("XY")}}, {"(I3,/,/,I3)", ii(2)},
		{"(2/,I3)", ii(1)}, {"(I3,2/)", ii(1)}, {"(/)", nil}, {"()", nil}, {"(I3)", nil}, {"(1PE10.3)", rr}, {"(F10.3,1PF10.3,0PF10.3)", rr},
		{"(2PE10.3,-1PE10.3)", rr}, {"(F8.2,SP,F8.2,SS,F8.2,S,F8.2)", rr}, {"(F8.2,BN,F8.2,BZ,F8.2)", rr}, {"(RU,F8.1,RD,F8.1,RZ,F8.1,RN,F8.1,RC,F8.1)", rr},
		{"(DC,F8.2,DP,F8.2)", rr}, {"(I3,I3,I3)", mix}, {"(I4,F8.3,A,L2,2F8.2,2E10.2)", mix}, {"(G12.4,G12.4,G12.4,G12.4)", mix},
		{"(2(F6.1,1X))", rr}, {"(5(F6.1,:,';'))", rr}, {"(F6.1,F6.1)", []Val{Cmplx(8, 1, 2)}}, {"(F6.1)", []Val{Cmplx(8, 1, 2)}},
		{"(F6.1,'|',F6.1)", []Val{Cmplx(8, 1, 2)}}, {"(2F6.1)", []Val{Cmplx(4, 1, 2), Cmplx(4, 3, 4)}}, {"(A,I0)", []Val{Str("n="), i4(7)}},
		{"(3H abc)", nil}, {"(5HHello)", nil}, {"(I5.5,I5.0)", []Val{i4(3), i4(0)}}, {"(10X)", nil}, {"(I2,10X)", ii(1)},
		{"(I3,1X,I3,1X,I3)", ii(3)}, {"(a,i3)", []Val{Str("x"), i4(1)}}, {"( i3 , i3 )", ii(2)}, {"(I3 I3)", ii(2)}, {"(I3,,I3)", ii(2)},
		{"(I3,", ii(1)}, {"I3", ii(1)}, {"(I3))", ii(1)}, {"(I3)x", ii(1)}, {"(Q3)", ii(1)}, {"(0I3)", ii(1)}, {"(I3.)", ii(1)}, {"(I.2)", ii(1)},
		{"(1X,A)", []Val{Str("x")}}, {"(I3,$)", ii(1)}, {"('it''s')", nil}, {`("say ""x""")`, nil}, {"(4(I1,:))", ii(2)}, {"(2(I1/))", ii(3)},
		{"(I1,(/,I1))", ii(4)}, {"(I2,(/,I2),3X)", ii(3)}, {"(3(/))", nil}, {"(T3,I1,TL1,I1)", ii(2)}, {"(TL3,I1)", ii(1)},
	}
	var cases []dcase
	for _, c := range list {
		cases = append(cases, dcase{format: c.f, items: c.items, name: "ctl"})
	}
	report(t, "control", compareOutput(t, cases, 60))
}

func TestDifferentialListDirected(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	var cases []dcase
	add := func(items ...Val) { cases = append(cases, dcase{star: true, items: items, name: "ld"}) }
	for _, x := range realValues(r, 60) {
		add(Real8(x))
		add(Real4(float32(x)))
		add(Cmplx(8, x, -x))
		add(Cmplx(4, float64(float32(x)), 1))
		add(i4(1), Real8(x), Str("s"), Bool(4, true), Real4(float32(x)))
	}
	for _, v := range []int64{0, 1, -1, 99, -99, 123456789, math.MaxInt32, math.MinInt32} {
		add(i4(v))
		add(Int(8, v*1000003))
		add(Int(2, v%30000))
		add(Int(1, v%100))
	}
	add()
	add(Str(""))
	add(Str("a"), Str("b"), Str("c"))
	add(Str("a"), i4(1), Str("b"), Str("c"), Bool(4, false), Str(" d "))
	add(Str("it's"), Str(`q"q`))
	add(Bool(1, true), Bool(2, false), Bool(8, true))
	add(Cmplx(4, 0, 0), Cmplx(8, 1e-5, 1e5))
	var many []Val
	for i := 0; i < 40; i++ {
		many = append(many, i4(int64(i*37)))
	}
	add(many...)
	var mr []Val
	for i := 0; i < 15; i++ {
		mr = append(mr, Real8(float64(i)*1.37))
	}
	add(mr...)
	add(Str(strings.Repeat("long ", 40)))
	report(t, "list-directed", compareOutput(t, cases, 60))
}

// knownGfortranQuirk names the documented classes where gfortran 15.2 prints
// something the standard (and flang-rt) does not, and rt keeps the standard
// result; see doc.go. The count of each is logged, never silently dropped.
func knownGfortranQuirk(c dcase) string {
	if len(c.items) != 1 || c.items[0].K != Real {
		return ""
	}
	v := c.items[0]
	var w, d int
	body := strings.TrimSuffix(strings.TrimPrefix(c.format, "("), ")")
	i := 0
	for i < len(body) && body[i] >= 'A' && body[i] <= 'Z' {
		i++
	}
	desc := body[:i]
	if n, _ := fmt.Sscanf(body[i:], "%d.%d", &w, &d); n < 2 || desc == "" {
		return ""
	}
	switch {
	case desc == "EN" && ((v.Size == 4 && d >= 6) || (v.Size == 8 && d >= 16)):
		// EN with many digits: gfortran rounds to d+1 digits before it moves
		// the decimal point, then pads with zeros; at a carry boundary it
		// does not carry into the next engineering exponent
		return "EN-many-digits"
	}
	return ""
}
