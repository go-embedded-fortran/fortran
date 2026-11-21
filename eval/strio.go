package eval

import (
	"strings"

	"github.com/go-embedded-fortran/fortran/rt"
	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// substringRange compiles the bounds of s(lo:hi): the default bounds are 1 and
// the length of the string.
func (c *compiler) substringRange(n *syntax.Index) (lo, hi intFn, err error) {
	if len(n.Args) != 1 {
		return nil, nil, c.errf(n, "a character scalar is subscripted with one substring range")
	}
	t, ok := n.Args[0].Value.(*syntax.Triplet)
	if !ok || t.Stride != nil || n.Args[0].Keyword != "" {
		return nil, nil, c.errf(n, "a character scalar can only be given a substring range lo:hi")
	}
	if t.Lo != nil {
		if lo, err = c.intExpr(t.Lo); err != nil {
			return nil, nil, err
		}
	}
	if t.Hi != nil {
		if hi, err = c.intExpr(t.Hi); err != nil {
			return nil, nil, err
		}
	}
	return lo, hi, nil
}

func bounds2(f *frame, name string, lo, hi intFn, n int) (int, int) {
	l, h := int64(1), int64(n)
	if lo != nil {
		l = lo(f)
	}
	if hi != nil {
		h = hi(f)
	}
	if h < l {
		return 0, 0 // a zero-length substring
	}
	if l < 1 || h > int64(n) {
		fail(f, "substring %d:%d of %s is outside 1:%d", l, h, name, n)
	}
	return int(l - 1), int(h)
}

// substring compiles s(lo:hi) of a character variable.
func (c *compiler) substring(n *syntax.Index, id *syntax.Ident) (*ex, error) {
	base, err := c.ident(id)
	if err != nil {
		return nil, err
	}
	lo, hi, err := c.substringRange(n)
	if err != nil {
		return nil, err
	}
	name, bs := id.Name, base.s
	return &ex{k: kStr, slot: -1, s: func(f *frame) string {
		s := bs(f)
		a, b := bounds2(f, name, lo, hi, len(s))
		return s[a:b]
	}}, nil
}

// assignSubstring compiles s(lo:hi) = value: the value is truncated or padded
// to the length of the substring (F2018 10.2.1.3).
func (c *compiler) assignSubstring(at syntax.Node, l *syntax.Index, id *syntax.Ident, sy *sym, rhs *ex) (stmtFn, error) {
	if rhs.k != kStr {
		return nil, c.errf(at, "a character substring is assigned a character value")
	}
	base, err := c.ident(id)
	if err != nil {
		return nil, err
	}
	lo, hi, err := c.substringRange(l)
	if err != nil {
		return nil, err
	}
	name, bs, rs := id.Name, base.s, rhs.s
	whole := &ex{k: kStr, slot: -1, s: func(f *frame) string {
		s := bs(f)
		a, b := bounds2(f, name, lo, hi, len(s))
		return s[:a] + fitLen(rs(f), int64(b-a)) + s[b:]
	}}
	return c.storeVar(sy, whole, at)
}

// strIntrinsic2 compiles merge, scan and verify.
func (c *compiler) strIntrinsic2(n *syntax.Index, name string, args []*ex) (*ex, error) {
	switch name {
	case "merge":
		if len(args) != 3 || args[2].k != kLog || args[0].k != args[1].k {
			return nil, c.errf(n, "merge takes two values of one type and a logical mask")
		}
		// every argument is evaluated, tsource, fsource then mask, whichever is
		// selected: a function argument's side effects happen (gfortran, PR
		// fortran/107874, gfortran.dg/merge_1.f90)
		m, t, f2 := args[2].b, args[0], args[1]
		switch t.k {
		case kStr:
			a, b := t.s, f2.s
			return &ex{k: kStr, slot: -1, s: func(f *frame) string {
				va, vb := a(f), b(f)
				if m(f) {
					return va
				}
				return vb
			}}, nil
		case kInt:
			a, b := t.i, f2.i
			return &ex{k: kInt, kd: t.kd, slot: -1, i: func(f *frame) int64 {
				va, vb := a(f), b(f)
				if m(f) {
					return va
				}
				return vb
			}}, nil
		case kReal:
			a, b := t.r, f2.r
			return &ex{k: kReal, kd: t.kd, slot: -1, r: func(f *frame) float64 {
				va, vb := a(f), b(f)
				if m(f) {
					return va
				}
				return vb
			}}, nil
		case kLog:
			a, b := t.b, f2.b
			return &ex{k: kLog, slot: -1, b: func(f *frame) bool {
				va, vb := a(f), b(f)
				if m(f) {
					return va
				}
				return vb
			}}, nil
		}
		return nil, c.errf(n, "merge of this type is not supported")
	}
	// scan, verify
	if len(args) < 2 || len(args) > 3 || args[0].k != kStr || args[1].k != kStr {
		return nil, c.errf(n, "%s takes two character arguments and an optional back", name)
	}
	var back boolFn
	if len(args) == 3 {
		if args[2].k != kLog {
			return nil, c.errf(n, "%s: back= must be logical", name)
		}
		back = args[2].b
	}
	a, set := args[0].s, args[1].s
	isScan := name == "scan"
	return &ex{k: kInt, slot: -1, i: func(f *frame) int64 {
		s, st := a(f), set(f)
		in := func(ch byte) bool { return strings.IndexByte(st, ch) >= 0 }
		hit := func(ch byte) bool { return in(ch) == isScan }
		if back != nil && back(f) {
			for i := len(s) - 1; i >= 0; i-- {
				if hit(s[i]) {
					return int64(i + 1)
				}
			}
			return 0
		}
		for i := 0; i < len(s); i++ {
			if hit(s[i]) {
				return int64(i + 1)
			}
		}
		return 0
	}}, nil
}

// ---- internal files: write and read with a character variable as the unit ----

// ioItem is one item of a READ list: a scalar variable.
type ioItem struct {
	get func(*frame) rt.Val
	put func(*frame, rt.Val)
}

func (c *compiler) readItem(e syntax.Expr) (ioItem, error) {
	id, ok := e.(*syntax.Ident)
	if !ok {
		return ioItem{}, c.errf(e, "an input item is a scalar variable")
	}
	s, _, err := c.symOf(id)
	if err != nil || s.isC || s.ga || s.k == kArr {
		return ioItem{}, c.errf(e, "an input item is a scalar variable")
	}
	cur, err := c.ident(id)
	if err != nil {
		return ioItem{}, err
	}
	var read *ex
	var it ioItem
	switch s.k {
	case kInt:
		tmp := c.cur.nInts
		c.cur.nInts++
		read = &ex{k: kInt, kd: s.kd, slot: -1, i: func(f *frame) int64 { return f.ints[tmp] }}
		size, fn := s.kd, cur.i
		if size == 0 {
			size = 8
		}
		it.get = func(f *frame) rt.Val { return rt.Int(size, fn(f)) }
		it.put = func(f *frame, v rt.Val) { f.ints[tmp] = v.I }
	case kLog:
		tmp := c.cur.nInts
		c.cur.nInts++
		read = &ex{k: kLog, slot: -1, b: func(f *frame) bool { return f.ints[tmp] != 0 }}
		fn := cur.b
		it.get = func(f *frame) rt.Val { return rt.Bool(4, fn(f)) }
		it.put = func(f *frame, v rt.Val) { f.ints[tmp] = b2i(v.B) }
	case kReal:
		tmp := c.cur.nReals
		c.cur.nReals++
		read = &ex{k: kReal, kd: s.kd, slot: -1, r: func(f *frame) float64 { return f.reals[tmp] }}
		size, fn := 8, cur.r
		if s.kd == 4 {
			size = 4
		}
		it.get = func(f *frame) rt.Val { return rt.Val{K: rt.Real, Size: size, F: fn(f)} }
		it.put = func(f *frame, v rt.Val) { f.reals[tmp] = v.F }
	case kStr:
		tmp := c.cur.nStrs
		c.cur.nStrs++
		read = &ex{k: kStr, slot: -1, s: func(f *frame) string { return f.strs[tmp] }}
		fn := cur.s
		it.get = func(f *frame) rt.Val { return rt.Str(fn(f)) }
		it.put = func(f *frame, v rt.Val) { f.strs[tmp] = v.S }
	default:
		return ioItem{}, c.errf(e, "an input item of this type is not supported")
	}
	st, err := c.storeVar(s, read, id)
	if err != nil {
		return ioItem{}, err
	}
	put := it.put
	it.put = func(f *frame, v rt.Val) { put(f, v); st(f) }
	return it, nil
}

// internalIO compiles READ and WRITE whose unit is a character variable (an
// internal file of one record, F2018 12.4), with the format, list-directed or
// formatted, and an optional iostat=.
func (c *compiler) internalIO(s *syntax.IOStmt, unit syntax.Expr, sp rt.Spec, iostat syntax.Expr) (stmtFn, error) {
	var ios stmtFn
	var iosTmp int
	if iostat != nil {
		id, ok := iostat.(*syntax.Ident)
		if !ok {
			return nil, c.errf(iostat, "iostat= is an integer variable")
		}
		sy, _, err := c.symOf(id)
		if err != nil || sy.k != kInt || sy.isC {
			return nil, c.errf(iostat, "iostat= is an integer variable")
		}
		iosTmp = c.cur.nInts
		c.cur.nInts++
		tmp := iosTmp
		if ios, err = c.storeVar(sy, &ex{k: kInt, slot: -1, i: func(f *frame) int64 { return f.ints[tmp] }}, id); err != nil {
			return nil, err
		}
	}
	finish := func(f *frame, e *rt.Error) bool {
		if ios != nil {
			if e != nil {
				f.ints[iosTmp] = int64(e.Stat)
			} else {
				f.ints[iosTmp] = 0
			}
			ios(f)
			return e == nil
		}
		if e != nil {
			fail(f, "Fortran runtime error: %s", e.Msg)
		}
		return true
	}
	if s.Kind == "read" {
		x, err := c.expr(unit)
		if err != nil {
			return nil, err
		}
		if x.k != kStr {
			return nil, c.errf(unit, "the unit of an internal READ is a character expression")
		}
		var items []ioItem
		for _, e := range s.Items {
			it, err := c.readItem(e)
			if err != nil {
				return nil, err
			}
			items = append(items, it)
		}
		src := x.s
		return func(f *frame) {
			u := rt.NewInternalRead(src(f))
			vals := make([]rt.Val, len(items))
			for i, it := range items {
				vals[i] = it.get(f)
			}
			if !finish(f, u.Read(sp, vals)) {
				return
			}
			for i, it := range items {
				it.put(f, vals[i])
			}
		}, nil
	}
	// write
	id, ok := unit.(*syntax.Ident)
	if !ok {
		return nil, c.errf(unit, "the unit of an internal WRITE is a character variable")
	}
	sy, _, err := c.symOf(id)
	if err != nil || sy.k != kStr || sy.isC {
		return nil, c.errf(unit, "the unit of an internal WRITE is a character variable")
	}
	cur, err := c.ident(id)
	if err != nil {
		return nil, err
	}
	tmp := c.cur.nStrs
	c.cur.nStrs++
	st, err := c.storeVar(sy, &ex{k: kStr, slot: -1, s: func(f *frame) string { return f.strs[tmp] }}, id)
	if err != nil {
		return nil, err
	}
	items := make([]item, len(s.Items))
	for i, it := range s.Items {
		if items[i], err = c.printItem(it); err != nil {
			return nil, err
		}
	}
	curS := cur.s
	return func(f *frame) {
		var vals []rt.Val
		for _, it := range items {
			vals = it(f, vals)
		}
		u := rt.NewInternalWrite(1, len(curS(f)))
		if !finish(f, u.Write(sp, vals)) {
			return
		}
		f.strs[tmp] = u.Records()[0]
		st(f)
	}, nil
}

var _ = sema.Character
