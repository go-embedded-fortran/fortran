package eval

import (
	"github.com/go-embedded-fortran/fortran/rt"
	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// print compiles PRINT and WRITE onto package rt: the formatted and
// list-directed output of the Fortran runtime (F2018 clauses 12 and 13).
// The format is a character literal or '*'; the units are '*' and 6 (the
// cell output) and 0 (standard error, discarded here); the items are
// character literals and integer and real(8) scalar expressions.
func (c *compiler) print(s *syntax.IOStmt) (stmtFn, error) {
	var unit syntax.Expr
	var format syntax.Expr
	nonAdv := false
	switch s.Kind {
	case "print":
		format = s.Format
	case "write":
		pos := 0
		for _, a := range s.Controls {
			switch a.Keyword {
			case "":
				switch pos {
				case 0:
					unit = a.Value
				case 1:
					format = a.Value
				default:
					return nil, c.errf(s, "too many positional controls")
				}
				pos++
			case "unit":
				unit = a.Value
			case "fmt":
				format = a.Value
			case "advance":
				lit, ok := a.Value.(*syntax.StringLit)
				if !ok {
					return nil, c.errf(a.Value, "advance= must be a character literal")
				}
				switch lit.Value {
				case "no", "NO", "No":
					nonAdv = true
				case "yes", "YES", "Yes":
				default:
					return nil, c.errf(a.Value, "advance=%q is not supported", lit.Value)
				}
			default:
				return nil, c.errf(a.Value, "the %s= specifier is not supported", a.Keyword)
			}
		}
	default:
		return nil, c.errf(s, "%s is not supported by the prototype", s.Kind)
	}
	unitNum := 6
	switch u := unit.(type) {
	case nil, *syntax.Asterisk:
	case *syntax.IntLit:
		n, err := intLit(u)
		if err != nil || (n != 6 && n != 0) {
			return nil, c.errf(u, "only units *, 6 and 0 are supported")
		}
		unitNum = n
	default:
		return nil, c.errf(unit, "only units *, 6 and 0 are supported")
	}
	sp := rt.Spec{NonAdvancing: nonAdv}
	switch f := format.(type) {
	case *syntax.Asterisk:
	case *syntax.StringLit:
		if _, err := rt.CompileFormat(f.Value); err != nil {
			return nil, c.errf(f, "%v", err)
		}
		sp.Format, sp.HasFormat = f.Value, true
	default:
		return nil, c.errf(s, "only '*' and a character-literal format are supported")
	}
	items := make([]func(*frame, *rt.Val), len(s.Items))
	for i, it := range s.Items {
		fn, err := c.printItem(it)
		if err != nil {
			return nil, err
		}
		items[i] = fn
	}
	vals := make([]rt.Val, len(items))
	return func(f *frame) {
		for i, fn := range items {
			fn(f, &vals[i])
		}
		u := f.tab.Get(unitNum)
		if u == nil {
			fail(f, "Fortran runtime error: unit %d is not connected", unitNum)
		}
		if err := u.Write(sp, vals); err != nil {
			fail(f, "Fortran runtime error: %s", err.Msg)
		}
	}, nil
}

func intLit(l *syntax.IntLit) (int, error) {
	n := 0
	for _, ch := range l.Text {
		if ch < '0' || ch > '9' {
			return 0, errBadLit
		}
		n = n*10 + int(ch-'0')
		if n > 1<<20 {
			return 0, errBadLit
		}
	}
	return n, nil
}

type litError struct{}

func (litError) Error() string { return "bad literal" }

var errBadLit error = litError{}

// printItem compiles one output item into a function that fills an rt.Val.
func (c *compiler) printItem(it syntax.Expr) (func(*frame, *rt.Val), error) {
	if sl, ok := it.(*syntax.StringLit); ok {
		v := rt.Str(sl.Value)
		return func(_ *frame, d *rt.Val) { *d = v }, nil
	}
	tv, err := c.tv(it)
	if err != nil {
		return nil, err
	}
	x, err := c.expr(it)
	if err != nil {
		return nil, err
	}
	switch x.k {
	case kInt:
		size := tv.Type.Kind
		if tv.Type.Basic != sema.Integer || size == 0 {
			size = sema.DefaultInt
		}
		fn := x.i
		return func(f *frame, d *rt.Val) { *d = rt.Int(size, fn(f)) }, nil
	case kReal:
		fn := x.r
		return func(f *frame, d *rt.Val) { *d = rt.Real8(fn(f)) }, nil
	}
	return nil, c.errf(it, "array items are not supported in an output list")
}
