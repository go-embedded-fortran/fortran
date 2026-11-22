package eval

import (
	"github.com/go-embedded-fortran/fortran/rt"
	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// item is one output item: it appends the values it stands for (an implied DO
// or an array stands for several) to the list.
type item func(*frame, []rt.Val) []rt.Val

// print compiles PRINT and WRITE onto package rt: the formatted and
// list-directed output of the Fortran runtime (F2018 clauses 12 and 13).
// The format is a character constant or '*'; the unit is '*' or an integer
// expression naming unit 6 (standard output) or 0 (standard error); the items
// are constants of any intrinsic type (the analyser folded them), integer,
// logical and real scalar expressions, real(8) arrays and implied DO loops.
func (c *compiler) print(s *syntax.IOStmt) (stmtFn, error) {
	var unit syntax.Expr
	var format, iostat syntax.Expr
	nonAdv := false
	switch s.Kind {
	case "print":
		format = s.Format
	case "write", "read":
		if s.Kind == "read" && !s.HasCtrl {
			return nil, c.errf(s, "READ from standard input is not supported")
		}
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
			case "iostat":
				iostat = a.Value
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
	internal := false
	unitFn := intFn(func(*frame) int64 { return 6 })
	switch u := unit.(type) {
	case nil, *syntax.Asterisk:
	default:
		tv, err := c.tv(u)
		if err != nil {
			return nil, err
		}
		if tv.Type.Basic == sema.Character && tv.Rank == 0 {
			internal = true
		} else if tv.Type.Basic != sema.Integer || tv.Rank != 0 {
			return nil, c.errf(u, "the unit must be *, a scalar integer expression or a character scalar (an internal file)")
		} else if unitFn, err = c.intExpr(u); err != nil {
			return nil, err
		}
	}
	if s.Kind == "read" && !internal {
		return nil, c.errf(s, "only READ from a character variable (an internal file) is supported")
	}
	if iostat != nil && !internal {
		return nil, c.errf(iostat, "iostat= is supported on internal files only")
	}
	sp := rt.Spec{NonAdvancing: nonAdv}
	switch f := format.(type) {
	case nil, *syntax.Asterisk:
	default:
		tv, err := c.tv(f)
		if err != nil {
			return nil, err
		}
		if tv.Type.Basic != sema.Character || tv.Const == nil {
			return nil, c.errf(f, "only '*' and a character-constant format are supported")
		}
		if _, err := rt.CompileFormat(tv.Const.S); err != nil {
			return nil, c.errf(f, "%v", err)
		}
		sp.Format, sp.HasFormat = tv.Const.S, true
	}
	if internal {
		return c.internalIO(s, unit, sp, iostat)
	}
	items := make([]item, len(s.Items))
	for i, it := range s.Items {
		fn, err := c.printItem(it)
		if err != nil {
			return nil, err
		}
		items[i] = fn
	}
	return func(f *frame) {
		var vals []rt.Val
		for _, fn := range items {
			vals = fn(f, vals)
		}
		n := int(unitFn(f))
		u := f.tab.Get(n)
		if u == nil {
			fail(f, "Fortran runtime error: unit %d is not connected", n)
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

// constVal is the I/O value of a constant the analyser folded.
func (c *compiler) constVal(at syntax.Node, k *sema.Const) (rt.Val, error) {
	switch k.Type.Basic {
	case sema.Integer:
		return rt.Int(k.Type.Kind, k.I), nil
	case sema.Logical:
		return rt.Bool(k.Type.Kind, k.B), nil
	case sema.Real:
		switch k.Type.Kind {
		case 4:
			return rt.Real4(float32(k.F)), nil
		case 8:
			return rt.Real8(k.F), nil
		}
	case sema.Complex:
		if k.Type.Kind == 4 || k.Type.Kind == 8 {
			return rt.Cmplx(k.Type.Kind, real(k.C), imag(k.C)), nil
		}
	case sema.Character:
		if k.Type.Kind == 1 {
			return rt.Str(k.S), nil
		}
	}
	return rt.Val{}, c.errf(at, "a constant of type %s is not supported", k.Type)
}

// printItem compiles one output item.
func (c *compiler) printItem(it syntax.Expr) (item, error) {
	if sl, ok := it.(*syntax.StringLit); ok {
		v := rt.Str(sl.Value)
		return func(_ *frame, l []rt.Val) []rt.Val { return append(l, v) }, nil
	}
	if ido, ok := it.(*syntax.ImpliedDo); ok {
		return c.impliedDo(ido)
	}
	tv, err := c.tv(it)
	if err != nil {
		return nil, err
	}
	if tv.Rank == 0 && tv.Const != nil {
		v, err := c.constVal(it, tv.Const)
		if err != nil {
			return nil, err
		}
		return func(_ *frame, l []rt.Val) []rt.Val { return append(l, v) }, nil
	}
	x, err := c.expr(it)
	if err != nil {
		return nil, err
	}
	switch x.k {
	case kInt:
		size := tv.Type.Kind
		fn := x.i
		return func(f *frame, l []rt.Val) []rt.Val { return append(l, rt.Int(size, fn(f))) }, nil
	case kLog:
		size := tv.Type.Kind
		fn := x.b
		return func(f *frame, l []rt.Val) []rt.Val { return append(l, rt.Bool(size, fn(f))) }, nil
	case kReal:
		fn := x.r
		if tv.Type.Kind == 4 {
			return func(f *frame, l []rt.Val) []rt.Val { return append(l, rt.Real4(float32(fn(f)))) }, nil
		}
		return func(f *frame, l []rt.Val) []rt.Val { return append(l, rt.Real8(fn(f))) }, nil
	case kD:
		if x.dt.hasAlloc() {
			return nil, c.errf(it, "a derived type with allocatable components needs defined input/output to be transferred (F2018 12.6.4.8.2)")
		}
		fn := x.d
		return func(f *frame, l []rt.Val) []rt.Val { return fn(f).vals(f, l) }, nil
	case kGA:
		fn, size := x.g, tv.Type.Kind
		return func(f *frame, l []rt.Val) []rt.Val { return printVals(f, l, fn(f), size) }, nil
	case kStr:
		fn := x.s
		return func(f *frame, l []rt.Val) []rt.Val { return append(l, rt.Str(fn(f))) }, nil
	case kSA:
		fn := x.g
		return func(f *frame, l []rt.Val) []rt.Val { return printStrs(f, l, fn(f)) }, nil
	case kArr:
		fn := x.a
		return func(f *frame, l []rt.Val) []rt.Val {
			data := fn(f).data // column-major: the first subscript varies fastest
			data = data[:min(len(data), max(0, f.bud.valRoom()-len(l)))]
			f.bud.valsFit(f, len(data))
			for _, v := range data {
				l = append(l, rt.Real8(v))
			}
			return l
		}, nil
	}
	return nil, c.errf(it, "this output item is not supported")
}

// implicitDoVar is the storage of an implied-do index: the analyser declared it
// in a scope of its own (F2018 19.4: its scope is the implied-do), so it gets
// a slot of its own and the variable of the enclosing scope is not touched.
func (c *compiler) implicitDoVar(d *syntax.ImpliedDo) (*sym, error) {
	ss := c.info.Uses[&d.Var]
	if ss == nil {
		return nil, c.errf(d, "the implied-do variable %s was not analysed", d.Var.Name)
	}
	if s, ok := c.syms[ss]; ok {
		return s, nil
	}
	if ss.Type.Basic != sema.Integer || (ss.Type.Kind != 1 && ss.Type.Kind != 2 && ss.Type.Kind != 4 && ss.Type.Kind != 8) {
		return nil, c.errf(d, "the implied-do variable %s must be an integer", d.Var.Name)
	}
	s := &sym{k: kInt, kd: kdOf(ss.Type), slot: c.cur.nInts, owner: c.cur}
	c.cur.nInts++
	c.syms[ss] = s
	return s, nil
}

// impliedDo compiles (items, v = lo, hi [, step]) in an output list (F2018
// 12.6.3): the loop variable is an integer variable of the program.
func (c *compiler) impliedDo(d *syntax.ImpliedDo) (item, error) {
	sy, err := c.implicitDoVar(d)
	if err != nil {
		return nil, err
	}
	lo, err := c.intExpr(d.Lo)
	if err != nil {
		return nil, err
	}
	hi, err := c.intExpr(d.Hi)
	if err != nil {
		return nil, err
	}
	step := intFn(func(*frame) int64 { return 1 })
	if d.Step != nil {
		if step, err = c.intExpr(d.Step); err != nil {
			return nil, err
		}
	}
	items := make([]item, len(d.Items))
	for i, it := range d.Items {
		if items[i], err = c.printItem(it); err != nil {
			return nil, err
		}
	}
	slot := sy.slot
	return func(f *frame, l []rt.Val) []rt.Val {
		a, b, st := lo(f), hi(f), step(f)
		if st == 0 {
			fail(f, "implied-do step is zero")
		}
		n := (b - a + st) / st
		if n > MaxArrayElems {
			fail(f, "implied-do of %d iterations is out of range", n)
		}
		v := a
		room := f.bud.valRoom()
		base, start := f.bud.mark(), len(l)
		for ; n > 0 && len(l) < room; n-- { // past the room, nothing printed would be kept
			// the temporaries of the last iteration are garbage; the values built
			// so far stay reserved
			f.bud.reset(base + int64(len(l)-start)*valBytes)
			f.ints[slot] = v
			before := len(l)
			for _, it := range items {
				l = it(f, l)
			}
			f.bud.valsFit(f, len(l)-before)
			v += st
		}
		f.ints[slot] = v
		return l
	}, nil
}
