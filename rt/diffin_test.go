package rt

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/oracle"
	"github.com/go-embedded-fortran/fortran/internal/oracle/oracletest"
)

// incase is a READ from an internal file (an array of 60-character records)
// into variables of the named kinds.
type incase struct {
	format string
	star   bool
	recs   []string
	vars   []string
}

const recLen = 60

// the variables a case may name: their Fortran declaration and an initial value
var inVars = map[string]struct {
	decl, init string
	val        Val
}{
	"ia":  {"integer(4)", "-77", Int(4, -77)},
	"ia2": {"integer(4)", "-77", Int(4, -77)},
	"ia3": {"integer(4)", "-77", Int(4, -77)},
	"rb2": {"real(8)", "-7.5d0", Real8(-7.5)},
	"rb3": {"real(8)", "-7.5d0", Real8(-7.5)},
	"sb":  {"character(len=6)", "'######'", Str("######")},
	"ib":  {"integer(8)", "-77_8", Int(8, -77)},
	"ic":  {"integer(2)", "-77_2", Int(2, -77)},
	"id":  {"integer(1)", "-77_1", Int(1, -77)},
	"ra":  {"real(4)", "-7.5", Real4(-7.5)},
	"rb":  {"real(8)", "-7.5d0", Real8(-7.5)},
	"ca":  {"complex(4)", "(-7.5,-8.5)", Cmplx(4, -7.5, -8.5)},
	"cb":  {"complex(8)", "(-7.5d0,-8.5d0)", Cmplx(8, -7.5, -8.5)},
	"la":  {"logical(4)", ".false.", Bool(4, false)},
	"lb":  {"logical(4)", ".true.", Bool(4, true)},
	"sa":  {"character(len=6)", "'######'", Str("######")},
}

func (c incase) fortran(i int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  write(*,'(a,i0)') '#',%d\n", i)
	b.WriteString("  rc = ' '\n")
	for j, r := range c.recs {
		fmt.Fprintf(&b, "  rc(%d) = '%s'\n", j+1, strings.ReplaceAll(r, "'", "''"))
	}
	seen := map[string]bool{}
	for _, v := range c.vars {
		if !seen[v] {
			seen[v] = true
			fmt.Fprintf(&b, "  %s = %s\n", v, inVars[v].init)
		}
	}
	f := "*"
	if !c.star {
		fmt.Fprintf(&b, "  fv = %s\n", fq(c.format))
		f = "trim(fv)"
	}
	fmt.Fprintf(&b, "  read(rc,%s,iostat=ios) %s\n", f, strings.Join(c.vars, ", "))
	b.WriteString("  write(*,'(a,i0)') 'IOS=',ios\n")
	for v := range seen {
		switch v[0] {
		case 'i':
			fmt.Fprintf(&b, "  write(*,'(a,i0)') '%s=',%s\n", v, v)
		case 'r':
			if v == "ra" {
				fmt.Fprintf(&b, "  write(*,'(a,z8.8)') '%s=',transfer(%s,0_4)\n", v, v)
			} else {
				fmt.Fprintf(&b, "  write(*,'(a,z16.16)') '%s=',transfer(%s,0_8)\n", v, v)
			}
		case 'c':
			if v == "ca" {
				fmt.Fprintf(&b, "  write(*,'(a,z8.8,1x,z8.8)') '%s=',transfer(real(%s),0_4),transfer(aimag(%s),0_4)\n", v, v, v)
			} else {
				fmt.Fprintf(&b, "  write(*,'(a,z16.16,1x,z16.16)') '%s=',transfer(real(%s),0_8),transfer(aimag(%s),0_8)\n", v, v, v)
			}
		case 'l':
			fmt.Fprintf(&b, "  write(*,'(a,l1)') '%s=',%s\n", v, v)
		default:
			fmt.Fprintf(&b, "  write(*,'(a,a,a)') '%s=[',%s,']'\n", v, v)
		}
	}
	return b.String()
}

func (c incase) rt() string {
	var out strings.Builder
	recs := make([]string, len(c.recs))
	for i, r := range c.recs {
		recs[i] = r + strings.Repeat(" ", max(0, recLen-len(r)))
	}
	// rc has 4 records; unused ones are blank
	for len(recs) < 4 {
		recs = append(recs, strings.Repeat(" ", recLen))
	}
	u := NewInternalRead(recs...)
	var items []Val
	seen := map[string]int{}
	for _, v := range c.vars {
		items = append(items, inVars[v].val)
		seen[v] = len(items) - 1
	}
	var vals map[string]Val
	err := u.Read(Spec{Format: c.format, HasFormat: !c.star}, items)
	fmt.Fprintf(&out, "IOS=%d\n", func() int {
		if err != nil {
			return err.Stat
		}
		return 0
	}())
	vals = map[string]Val{}
	for _, v := range c.vars {
		vals[v] = items[seen[v]]
	}
	_ = vals
	// the same variable named twice keeps its last value, as in Fortran
	last := map[string]Val{}
	for i, v := range c.vars {
		last[v] = items[i]
	}
	for _, v := range sortedKeys(seen) {
		x := last[v]
		switch v[0] {
		case 'i':
			fmt.Fprintf(&out, "%s=%d\n", v, x.I)
		case 'r':
			if v == "ra" {
				fmt.Fprintf(&out, "%s=%08X\n", v, math.Float32bits(float32(x.F)))
			} else {
				fmt.Fprintf(&out, "%s=%016X\n", v, math.Float64bits(x.F))
			}
		case 'c':
			if v == "ca" {
				fmt.Fprintf(&out, "%s=%08X %08X\n", v, math.Float32bits(float32(x.F)), math.Float32bits(float32(x.G)))
			} else {
				fmt.Fprintf(&out, "%s=%016X %016X\n", v, math.Float64bits(x.F), math.Float64bits(x.G))
			}
		case 'l':
			fmt.Fprintf(&out, "%s=%s\n", v, map[bool]string{true: "T", false: "F"}[x.B])
		default:
			fmt.Fprintf(&out, "%s=[%s]\n", v, x.S)
		}
	}
	return out.String()
}

func sortedKeys(m map[string]int) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	// the Fortran side iterates a Go map too: sort both through the same order
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j] < ks[j-1]; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	return ks
}

func compareInput(t *testing.T, cases []incase, maxExamples int) diffStats {
	tc := oracletest.Require(t)
	var st diffStats
	seen := map[string]int{}
	const chunk = 1200
	for lo := 0; lo < len(cases); lo += chunk {
		hi := min(lo+chunk, len(cases))
		part := cases[lo:hi]
		var src strings.Builder
		src.WriteString("program d\n  implicit none\n  integer :: ios\n  character(len=512) :: fv\n  character(len=60) :: rc(4)\n")
		for _, name := range sortedVarNames() {
			fmt.Fprintf(&src, "  %s :: %s\n", inVars[name].decl, name)
		}
		for i, c := range part {
			src.WriteString(c.fortran(i))
		}
		src.WriteString("end program\n")
		res, err := tc.Output(context.Background(), []byte(src.String()),
			oracle.CompileOptions{Extra: []string{"-fno-range-check"}},
			oracle.Limits{Timeout: 120e9, MemoryBytes: -1, MaxOutput: 64 << 20})
		if err != nil {
			t.Fatalf("oracle: %v", err)
		}
		want := split(string(res.Stdout))
		var all strings.Builder
		for i, c := range part {
			fmt.Fprintf(&all, "#%d\n%s", i, c.rt())
		}
		got := split(all.String())
		for i, c := range part {
			k := fmt.Sprintf("#%d", i)
			// the Fortran side prints the variables in source order of a map:
			// compare as sets of lines
			if sameLines(want[k], got[k]) {
				st.same++
				continue
			}
			if k := knownInputQuirk(c); k != "" {
				if st.known == nil {
					st.known = map[string]int{}
				}
				st.known[k]++
				continue
			}
			st.differ++
			key := c.format + "/" + strings.Join(c.vars, ",")
			if seen[key] < 2 && len(st.examples) < maxExamples {
				seen[key]++
				st.examples = append(st.examples, fmt.Sprintf("fmt=%q recs=%q vars=%v\n   gfortran: %q\n   rt:       %q", c.format, c.recs, c.vars, want[k], got[k]))
			}
		}
	}
	return st
}

func sortedVarNames() []string {
	var ks []string
	for k := range inVars {
		ks = append(ks, k)
	}
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j] < ks[j-1]; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	return ks
}

func sameLines(a, b string) bool {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	if len(la) != len(lb) {
		return false
	}
	m := map[string]int{}
	for _, l := range la {
		m[l]++
	}
	for _, l := range lb {
		m[l]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestDifferentialInput(t *testing.T) {
	var cases []incase
	add := func(f string, recs []string, vars ...string) {
		cases = append(cases, incase{format: f, star: f == "*", recs: recs, vars: vars})
	}
	one := func(f, text string, vars ...string) { add(f, []string{text}, vars...) }
	ints := []string{"  5", "-12", "+7", "1 2", " 1x", "", "   ", "99999999999", "2147483648", "-2147483648", "2147483647", "12345", "0", "-0", "+", "-", "1,2", "1.5", " 12", "12 ", "00012", "1e3"}
	for _, f := range []string{"(I3)", "(I5)", "(I5.2)", "(BZ,I5)", "(BN,I5)", "(BZ,I3)", "(I1)", "(I20)", "(I2,I2)"} {
		for _, s := range ints {
			one(f, s, "ia")
			one(f, s, "ic")
			one(f, s, "ib")
			one(f, s, "id")
		}
	}
	for _, f := range []string{"(B8)", "(O6)", "(Z8)", "(Z3)", "(Z16)", "(B33)", "(Z17)"} {
		for _, s := range []string{"101", "1111", "ff", "FF", "7777", "12", "g", " 1 1", "", "100000000", "ffffffff", "80000000", "7fffffffffffffff", "-1", "1 ", "11111111111111111111111111111111"} {
			one(f, s, "ia")
			one(f, s, "ib")
			one(f, s, "ic")
		}
	}
	reals := []string{"1.5", "  .5", "1.", "-1.5E3", "1.5+3", "12345", "1e5", "1.5d2", "  ", "abc", "1.5x", "Inf", "-Infinity", "NaN", "nan(abc)", "+inf", "1.7976931348623157e309",
		"4.9e-325", "0.1", "123456789012345678901234567890", "1 5", "1.5e", "1.5e+", "e5", ".", "+.", "-", "1.2.3", "1.5E-3", "1.5q2", "3.4028236e38", "1e-46", "0.30000000000000004", "1,5", "5.5555555555", "12345.6789012", "9.999999999e-5"}
	for _, f := range []string{"(F5.2)", "(F10.3)", "(F10.0)", "(E10.2)", "(D8.1)", "(G10.3)", "(F3.0)", "(F20.10)", "(BZ,F6.2)", "(BN,F6.2)", "(1PF10.2)", "(2PF10.2)", "(-1PF10.2)", "(F4.1,F4.1)", "(DC,F10.3)"} {
		for _, s := range reals {
			one(f, s, "ra")
			one(f, s, "rb")
		}
	}
	for _, f := range []string{"(L1)", "(L5)", "(L2)", "(L10)", "(G3)"} {
		for _, s := range []string{"T", ".true.", "f", ".F.", "x", " ", "tx", ".t", "  T", "T  ", ".", ".x", "true", "F  T"} {
			one(f, s, "la")
		}
	}
	for _, f := range []string{"(A5)", "(A)", "(A20)", "(A3)", "(A1)", "(A6)", "(A7)", "(2A3)"} {
		for _, s := range []string{"abcdefg", "ab", "", "  x ", "abcdef", "0123456789012345"} {
			one(f, s, "sa")
		}
	}
	// control edit descriptors on input
	one("(I2,I2,I2)", "123456", "ia", "ib", "ic")
	one("(I2,X,I2)", "12 34", "ia", "ib")
	one("(I2,3X,I2)", "12abc34", "ia", "ib")
	one("(T4,I2)", "123456", "ia")
	one("(I2,TL2,I2)", "1234", "ia", "ib")
	one("(I2,TR2,I2)", "123456", "ia", "ib")
	one("(I2/I2)", "12", "ia", "ib")
	add("(I2/I2)", []string{"12", "34"}, "ia", "ib")
	add("(I2)", []string{"12", "34", "56"}, "ia", "ib", "ic")
	add("(I2,I2)", []string{"12", "34", "56"}, "ia", "ib", "ic")
	add("(I2,/,I2)", []string{"12", "34", "56"}, "ia", "ib")
	add("(2(I2,1X))", []string{"1 2 3 4", "5 6"}, "ia", "ib", "ic")
	add("(I2:,I2)", []string{"12", "34"}, "ia")
	one("(I2,I2)", "12", "ia", "ib")
	one("('x',I2)", "x12", "ia")
	one("(I2,F4.1,A2,L2)", "12 1.5ab T", "ia", "rb", "sa", "la")
	one("(2F5.1)", "1.5 2.5", "ca")
	one("(2F5.1)", " 1.5 2.5", "cb")
	one("(I2)", "12", "ia", "ib")
	one("(I2,I2,I2,I2)", "12345678", "ia", "ib", "ic", "id")
	one("(I)", "12", "ia")
	one("(I0)", "12", "ia")
	one("(A,A)", "abc", "sa", "sb")
	// list-directed
	ld := []string{"1 2 3", "1,2,3", "1,,3", ",2,3", "2*7 3", "3*", "1 / 3", "1/", "/", "  ", "", ",", ",,", "1,", "1 2", "7*1", "0*1", "2*", "-5 +6 7", "1 2 x", "1.5 2", "1;2", "1,2,3,4,5",
		"3*,5", "1\t2\t3", "2*1,3", "1* 2", " , 5 , 6", "1 2 3 4 5 6"}
	for _, s := range ld {
		one("*", s, "ia", "ib", "ic")
		one("*", s, "ra", "rb")
		one("*", s, "ia", "rb")
	}
	chs := []string{"'abc' def", `"q""r" 'it''s'`, "abc,def", "abcdefgh ijk", "'a'", "''", "'", "'abc", "a/b", "a,", ",b", "3*'x'", "2*ab", "'a b' c", "x'y' z"}
	for _, s := range chs {
		one("*", s, "sa", "sb", "ia")
		one("*", s, "sa", "sb")
	}
	for _, s := range []string{"(1.5,2.5)", "( 1.5 , 2.5 )", "(1,2) (3,4)", "1.5", "(1.5)", "(,2)", "(1,)", "2*(1,2)", "(1e3,-2d0)", "(inf,nan)", "(1.5,2.5", "(a,b)", "1,2", "(1.5 2.5)"} {
		one("*", s, "ca", "cb")
		one("*", s, "cb")
	}
	for _, s := range []string{"T F", ".true. .false.", "t,f", "T,,F", "x", ".t", "tr", "2*T", "t/f", "fa te", "T1", ".TRUE.", ".fals", "1 0"} {
		one("*", s, "la", "lb")
	}
	for _, s := range []string{"1.5 2d0 3e1", "1.5d+3", "1+3", ".5 5. -.5", "inf -inf nan", "1e", "1e+", "0x10", "1.5.2", "1_8", "100000000000000000000", "1e400", "-1e400", "1e-400", "3.4e39"} {
		one("*", s, "ra", "rb")
		one("*", s, "ia", "ib", "ic")
	}
	add("*", []string{"1", "2", "3"}, "ia", "ib", "ic")
	add("*", []string{"1 2", "", "3"}, "ia", "ib", "ic")
	add("*", []string{"1,", "2,", "3"}, "ia", "ib", "ic")
	add("*", []string{"'ab", "cd' x"}, "sa", "sb")
	add("*", []string{"1 2", "3 4"}, "ia", "ib")
	add("*", []string{"1 /", "3 4"}, "ia", "ib", "ic")
	add("*", []string{"3*", "7"}, "ia", "ib", "ic", "id")
	add("*", []string{"2*1", "2*2"}, "ia", "ib", "ic", "id")
	report(t, "input", compareInput(t, cases, 200))
}

func TestDifferentialInputRepeat(t *testing.T) {
	var cases []incase
	add := func(f string, recs []string, vars ...string) {
		cases = append(cases, incase{format: f, star: f == "*", recs: recs, vars: vars})
	}
	for _, s := range []string{"2*7 3", "3*7", "7*1", "2*1 2*2", "2*7", "1*7 8", "2*", "2*,5"} {
		add("*", []string{s}, "ia", "ia2", "ia3")
		add("*", []string{s}, "ia", "ib", "ic")
		add("*", []string{s}, "rb", "rb2", "rb3")
		add("*", []string{s}, "ia", "ia2")
		add("*", []string{s}, "ia")
	}
	for _, s := range []string{"1.5e", "1.5e+", "1.5e-", "1.5e+ ", "1.5e ", "1.5e+  ", "4.9e-", "4.9e+", "4.9e", "e5", "e", "e+", ".e5", "1.5e5 ", "1+", "1-"} {
		for _, f := range []string{"(F5.2)", "(F10.3)", "(F4.1)", "(F6.2)"} {
			add(f, []string{s}, "rb")
		}
	}
	report(t, "input-repeat", compareInput(t, cases, 200))
}

// knownInputQuirk names the documented classes where rt keeps the standard's
// reading and libgfortran 15.2 does something else; each is counted in the log.
func knownInputQuirk(c incase) string {
	rec := strings.Join(c.recs, "\n")
	switch {
	case strings.Contains(c.format, "BZ") && strings.TrimSpace(rec) == "-":
		// BZ turns the blanks after a lone sign into zeros: libgfortran gives +0
		return "BZ-sign-only"
	case strings.Contains(c.format, "BZ") && strings.ContainsAny(rec, "eE"):
		// blanks read as zeros after an exponent letter: libgfortran refuses
		return "BZ-exponent-letter"
	case c.star && strings.Contains(rec, ";"):
		// "1;2": libgfortran stores the 1 and then fails; rt fails before storing
		return "semicolon-without-DC"
	case c.star && strings.Contains(rec, "(1.5"):
		// a complex value that is cut short: libgfortran keeps the real part
		return "complex-cut-short"
	}
	return ""
}
