package eval

import (
	"slices"

	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// bitSize is BIT_SIZE of an integer of kind kd (0 is the default kind 8 of an
// ex; see ex.kd).
func bitSize(kd int) int64 {
	switch kd {
	case 1:
		return 8
	case 2:
		return 16
	case 4:
		return 32
	}
	return 64
}

// mvbits compiles CALL MVBITS(FROM, FROMPOS, LEN, TO, TOPOS) on integers
// (F2018 16.9.137): LEN bits of FROM from FROMPOS on are copied into
// TO from TOPOS on, and the other bits of TO are kept. TO is a variable; it is
// read and written at one place, fixed before FROM and the positions are read
// (they may be TO itself: the value read is the one before the call).
func (c *compiler) mvbits(s *syntax.Call) (stmtFn, error) {
	formals := []string{"from", "frompos", "len", "to", "topos"}
	got := map[string]syntax.Expr{}
	pos := true
	for i, a := range s.Args {
		name := a.Keyword
		if name != "" {
			pos = false
		} else if pos && i < len(formals) {
			name = formals[i]
		}
		if !slices.Contains(formals, name) || a.Value == nil || got[name] != nil {
			return nil, c.errf(s, "mvbits takes the arguments from, frompos, len, to and topos")
		}
		got[name] = a.Value
	}
	vals := make([]*ex, len(formals))
	var arrs []int // the arguments that are arrays
	for i, name := range formals {
		a := got[name]
		if a == nil {
			return nil, c.errf(s, "mvbits: argument %s is missing", name)
		}
		v, err := c.expr(a)
		if err == nil && v.k == kGA && v.et == kInt {
			arrs = append(arrs, i)
		} else if err == nil && v.k != kInt {
			err = c.errf(a, "mvbits: argument %s must be an integer", name)
		}
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	// TO is a variable (a name, an element, a section or a component): (j),
	// j+1 and a function reference are values
	_, isName := got["to"].(*syntax.Ident)
	if !isName && !c.isDesignator(got["to"]) {
		return nil, c.errf(got["to"], "mvbits: to must be a variable")
	}
	// nor is a named constant, or a part of one: sema refuses it as the target
	// of an assignment, not as an argument of an intrinsic subroutine
	if id := designatorBase(got["to"]); id != nil {
		if ss := c.info.Uses[id]; ss != nil && ss.Kind == sema.SymParam {
			return nil, c.errf(got["to"], "mvbits: to must be a variable, and %s is a named constant", id.Name)
		}
	}
	if len(arrs) > 0 {
		return c.mvbitsElemental(s, got["to"], vals, arrs)
	}
	from, fpos, n, to, tpos := vals[0].i, vals[1].i, vals[2].i, vals[3], vals[4].i
	// a scalar TO has a place; a named constant has none
	if to.pl == nil {
		return nil, c.errf(got["to"], "mvbits: to must be a variable (a pointer is not supported yet)")
	}
	// FROM and TO are of one kind (gfortran refuses otherwise), and TO, a
	// variable, gives it: the kind of an expression such as n+1 is not kept
	kd, pl := to.kd, to.pl
	return func(f *frame) {
		r := pl(f)
		old := r.getInt()
		r.setInt(movedBits(f, kd, from(f), fpos(f), n(f), tpos(f), old))
	}, nil
}

// designatorBase is the name a designator starts from: q of q, q(1), q(1:2)
// and q%c(1); nil when there is none.
func designatorBase(x syntax.Expr) *syntax.Ident {
	for {
		switch d := x.(type) {
		case *syntax.Ident:
			return d
		case *syntax.Index:
			x = d.X
		case *syntax.Component:
			x = d.X
		default:
			return nil
		}
	}
}

// movedBits is old, a TO of kind kd, with LEN bits of src from FROMPOS on
// moved in at TOPOS.
func movedBits(f *frame, kd int, src, fp, ln, tp, old int64) int64 {
	w := bitSize(kd)
	if fp < 0 || ln < 0 || tp < 0 || fp+ln > w || tp+ln > w {
		fail(f, "mvbits: frompos=%d len=%d topos=%d do not fit in %d bits", fp, ln, tp, w)
	}
	// LEN 0 makes m 0 (a Go shift by 64 is 0): TO is kept
	m := ^uint64(0) >> uint(64-ln)
	bits := (uint64(src) >> uint(fp)) & m
	return narrow(kd, int64(uint64(old)&^(m<<uint(tp))|bits<<uint(tp)))
}

// mvbitsElemental compiles the elemental form, where the arguments at arrs are
// arrays: TO is one too (an INTENT(INOUT) argument of an elemental subroutine
// is an array when any argument is, F2018 15.8.3), and the scalars apply to
// every element. All the new elements are computed from the old TO before any
// is stored, as in the array assignment TO = ..., so FROM may be TO.
func (c *compiler) mvbitsElemental(s *syntax.Call, toX syntax.Expr, vals []*ex, arrs []int) (stmtFn, error) {
	if vals[3].k != kGA {
		return nil, c.errf(toX, "mvbits: to must be an array when another argument is")
	}
	kd := vals[3].kd
	// each array argument is read, element by element, from a slot of its own
	sc := make([]func(*frame) int64, len(vals))
	for i, v := range vals {
		sc[i] = v.i
	}
	gs := make([]gaFn, len(arrs))
	slots := make([]int, len(arrs))
	for k, i := range arrs {
		slot := c.cur.nInts
		c.cur.nInts++
		gs[k], slots[k] = vals[i].g, slot
		sc[i] = func(f *frame) int64 { return f.ints[slot] }
	}
	from, fpos, n, old, tpos := sc[0], sc[1], sc[2], sc[3], sc[4]
	rhs := &ex{k: kGA, slot: -1, rank: vals[3].rank, et: kInt, kd: kd, g: func(f *frame) *garr {
		as := make([]*garr, len(gs))
		for k, g := range gs {
			as[k] = g(f)
			if k > 0 && !sameShape(as[0].ext, as[k].ext) {
				fail(f, "mvbits: arrays of extents %v and %v are not conformable", as[0].ext, as[k].ext)
			}
		}
		out := newGarr(f, kInt, kd, ones(len(as[0].ext)), append([]int(nil), as[0].ext...))
		for e, cnt := 0, out.size(); e < cnt; e++ {
			for k, a := range as {
				f.ints[slots[k]], _ = a.at(e)
			}
			out.put(e, movedBits(f, kd, from(f), fpos(f), n(f), tpos(f), old(f)), 0)
		}
		return out
	}}
	return c.assignGA(s, toX, rhs, nil)
}
