package eval

import (
	"strings"

	"github.com/go-embedded-fortran/fortran/syntax"
)

// fitLen is s as an assignment gives it to a character variable of length n
// (F2018 10.2.1.3: truncated on the right, or padded with blanks).
func fitLen(s string, n int64) string {
	switch l := int64(len(s)); {
	case l == n:
		return s
	case l > n:
		return s[:n]
	}
	return s + strings.Repeat(" ", int(n-int64(len(s))))
}

// fit is x as an assignment gives it to the character variable s of frame f:
// its length is declared, or that of the actual argument (character(*)), or
// computed when the unit started, or deferred (the variable takes the length
// of the value).
func (s *sym) fit(f *frame, x string) string {
	switch {
	case s.clen >= 0:
		return fitLen(x, s.clen)
	case s.clen == -2:
		return x
	case s.cdyn:
		return fitLen(x, f.ints[s.clslot])
	}
	return fitLen(x, int64(len(f.strs[s.slot])))
}

// strBinary compiles // and the relational operators on character operands (a
// shorter operand is padded with blanks for the comparison: F2018 10.1.5.5.1).
func (c *compiler) strBinary(n *syntax.Binary, x, y *ex) (*ex, error) {
	if x.k != kStr || y.k != kStr {
		return nil, c.errf(n, "operator %q does not take a character and a non-character operand", n.Op)
	}
	a, b := x.s, y.s
	switch n.Op {
	case "//":
		return &ex{k: kStr, slot: -1, s: func(f *frame) string {
			p, q := a(f), b(f)
			if len(p)+len(q) > MaxStringLen {
				fail(f, "//: the result is longer than %d characters", MaxStringLen)
			}
			f.bud.peek(f, int64(len(p)+len(q))) // the result is a temporary of the statement
			return p + q
		}}, nil
	case "==", "/=", "<", "<=", ">", ">=":
		var test func(int) bool
		switch n.Op {
		case "==":
			test = func(c int) bool { return c == 0 }
		case "/=":
			test = func(c int) bool { return c != 0 }
		case "<":
			test = func(c int) bool { return c < 0 }
		case "<=":
			test = func(c int) bool { return c <= 0 }
		case ">":
			test = func(c int) bool { return c > 0 }
		default:
			test = func(c int) bool { return c >= 0 }
		}
		return &ex{k: kLog, slot: -1, b: func(f *frame) bool { return test(cmpBlank(a(f), b(f))) }}, nil
	}
	return nil, c.errf(n, "operator %q is not defined on characters", n.Op)
}

// cmpBlank compares as the processor's collating sequence (ASCII) does, the
// shorter operand extended with blanks.
func cmpBlank(p, q string) int {
	n := min(len(p), len(q))
	if c := strings.Compare(p[:n], q[:n]); c != 0 {
		return c
	}
	for _, ch := range []byte(p[n:]) {
		if ch != ' ' {
			if ch < ' ' {
				return -1
			}
			return 1
		}
	}
	for _, ch := range []byte(q[n:]) {
		if ch != ' ' {
			if ch < ' ' {
				return 1
			}
			return -1
		}
	}
	return 0
}

// strIntrinsic compiles the character intrinsics trim, len, len_trim, adjustl,
// adjustr, index, ichar, iachar, achar and char (default kind).
func (c *compiler) strIntrinsic(n *syntax.Index, name string, args []*ex) (*ex, error) {
	need := 1
	switch name {
	case "index":
		if len(args) != 2 && len(args) != 3 {
			return nil, c.errf(n, "index takes two or three arguments")
		}
		need = len(args)
	default:
		if len(args) != 1 {
			return nil, c.errf(n, "%s takes one argument", name)
		}
	}
	_ = need
	switch name {
	case "achar", "char":
		if args[0].k != kInt {
			return nil, c.errf(n, "%s needs an integer", name)
		}
		a := args[0].i
		return &ex{k: kStr, slot: -1, s: func(f *frame) string {
			v := a(f)
			if v < 0 || v > 255 {
				fail(f, "%s: %d is not a character code", name, v)
			}
			return string([]byte{byte(v)})
		}}, nil
	}
	if args[0].k != kStr {
		return nil, c.errf(n, "%s needs a character argument", name)
	}
	a := args[0].s
	switch name {
	case "trim":
		return &ex{k: kStr, slot: -1, s: func(f *frame) string { return strings.TrimRight(a(f), " ") }}, nil
	case "len":
		return &ex{k: kInt, slot: -1, i: func(f *frame) int64 { return int64(len(a(f))) }}, nil
	case "len_trim":
		return &ex{k: kInt, slot: -1, i: func(f *frame) int64 { return int64(len(strings.TrimRight(a(f), " "))) }}, nil
	case "adjustl":
		return &ex{k: kStr, slot: -1, s: func(f *frame) string {
			s := a(f)
			f.bud.peek(f, int64(len(s)))
			t := strings.TrimLeft(s, " ")
			return t + strings.Repeat(" ", len(s)-len(t))
		}}, nil
	case "adjustr":
		return &ex{k: kStr, slot: -1, s: func(f *frame) string {
			s := a(f)
			f.bud.peek(f, int64(len(s)))
			t := strings.TrimRight(s, " ")
			return strings.Repeat(" ", len(s)-len(t)) + t
		}}, nil
	case "ichar", "iachar":
		return &ex{k: kInt, slot: -1, i: func(f *frame) int64 {
			s := a(f)
			if len(s) != 1 {
				fail(f, "%s of a string of length %d", name, len(s))
			}
			return int64(s[0])
		}}, nil
	case "index":
		if args[1].k != kStr {
			return nil, c.errf(n, "index needs character arguments")
		}
		sub := args[1].s
		var back boolFn
		if len(args) == 3 {
			if args[2].k != kLog {
				return nil, c.errf(n, "index: back= must be logical")
			}
			back = args[2].b
		}
		return &ex{k: kInt, slot: -1, i: func(f *frame) int64 {
			s, t := a(f), sub(f)
			if back != nil && back(f) {
				return int64(strings.LastIndex(s, t) + 1)
			}
			return int64(strings.Index(s, t) + 1)
		}}, nil
	}
	return nil, c.errf(n, "%s is not supported", name)
}
