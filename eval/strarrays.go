package eval

import (
	"strings"

	"github.com/go-embedded-fortran/fortran/rt"
	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// Character arrays: arrays of character of one constant length, of rank 1 to
// 7, stored as the general arrays are (a garr whose s holds the elements, each
// exactly clen long, column-major). They are an expression kind of their own,
// kSA, not kGA: the paths written for the numeric arrays read g.i and g.r, and
// a kind they do not know is refused there instead of read wrong.

// strGarrBytes is the storage of a character array of n elements of length
// clen: a string header and its bytes for each.
func strGarrBytes(n int, clen int64) int64 { return (16+clen)*int64(n) + arrayOverhead }

// bytes is what g is charged when it is held.
func (g *garr) bytes() int64 {
	if g.et == kStr {
		return strGarrBytes(g.size(), g.clen)
	}
	return garrBytes(g.size())
}

// elemCount is the number of elements of the extents ext, refused when the
// rank or the number is out of range; a negative extent becomes 0.
func elemCount(f *frame, ext []int) int {
	if len(ext) < 1 || len(ext) > MaxRank {
		fail(f, "array of rank %d is out of range", len(ext))
	}
	n := 1
	for i, e := range ext {
		if e < 0 {
			ext[i], e = 0, 0
		}
		if e > 0 && n > MaxArrayElems/e {
			fail(f, "array extent %v is out of range (more than %d elements)", ext, MaxArrayElems)
		}
		n *= e
	}
	return n
}

// newStrGarr makes a character array of length clen, blank (an undefined
// character variable is printed blank, as gfortran's usually is).
func newStrGarr(f *frame, clen int64, lb []int64, ext []int) *garr {
	if clen < 0 || clen > MaxStringLen {
		fail(f, "character length %d is out of range", clen)
	}
	n := elemCount(f, ext)
	f.bud.peek(f, strGarrBytes(n, clen))
	g := &garr{et: kStr, clen: clen, lb: lb, ext: ext, s: make([]string, n)}
	if n > 0 {
		blank := strings.Repeat(" ", int(clen))
		for k := range g.s {
			g.s[k] = blank
		}
	}
	return g
}

// declSA declares the character array ss.
func (c *compiler) declSA(ss *sema.Symbol, e syntax.Node, init syntax.Expr) error {
	name := ss.Name
	param := ss.Kind == sema.SymParam
	_, deferred := c.lenExpr.(*syntax.Triplet)
	_, star := c.lenExpr.(*syntax.Asterisk)
	switch {
	case ss.Rank < 1 || ss.Rank > MaxRank || len(ss.Dims) != ss.Rank:
		return c.errf(e, "rank %d arrays are not supported", ss.Rank)
	case ss.Flags&sema.FDummy != 0:
		return c.errf(e, "dummy argument %s: a character array dummy argument is not supported yet", name)
	case ss.Flags&(sema.FPointer|sema.FAllocatable) != 0:
		return c.errf(e, "%s: an allocatable or pointer character array is not supported yet", name)
	case deferred || star || ss.Type.Len < 0:
		return c.errf(e, "%s: a character array needs a constant length yet", name)
	case init != nil && !param && (c.cur.unit.Kind == syntax.SubroutineUnit || c.cur.unit.Kind == syntax.FunctionUnit):
		return c.errf(e, "%s: an initialised variable of a procedure is saved between calls, which is not supported yet", name)
	}
	lb := make([]int64, ss.Rank)
	ext := make([]int, ss.Rank)
	for d, dm := range ss.Dims {
		if !dm.HiKnown || !dm.LoKnown && dm.Lo != 0 || dm.Assumed || dm.Deferred {
			return c.errf(e, "an array needs constant bounds (automatic and assumed-shape arrays are not supported yet)")
		}
		lb[d] = dm.Lo
		if !dm.LoKnown {
			lb[d] = 1
		}
		ext[d] = int(max(0, dm.Hi-lb[d]+1))
	}
	s := &sym{k: kSA, et: kStr, clen: ss.Type.Len, rank: ss.Rank, owner: c.cur, slot: c.cur.nGAs}
	c.cur.nGAs++
	slot, clen := s.slot, s.clen
	c.cur.init = append(c.cur.init, func(f *frame) {
		f.gas[slot] = f.bud.retain(f, f, newStrGarr(f, clen, append([]int64(nil), lb...), append([]int(nil), ext...)))
	})
	c.syms[ss] = s
	if init == nil {
		return nil
	}
	v, err := c.expr(init)
	if err != nil {
		return err
	}
	// as for a numeric array (declSym): a copy of the name stands for the
	// variable while the assignment is compiled
	id := syntax.Ident{Span: syntax.Span{Pos: e.P()}, Name: name}
	c.info.Uses[&id] = ss
	fn, err := c.assignSA(e, &id, v)
	delete(c.info.Uses, &id)
	if err != nil {
		return err
	}
	c.cur.init = append(c.cur.init, fn)
	return nil
}

// saIdent is the character array s as a whole.
func (c *compiler) saIdent(id *syntax.Ident, s *sym) (*ex, error) {
	r, err := c.symRef(id, s)
	if err != nil {
		return nil, err
	}
	return &ex{k: kSA, slot: -1, rank: s.rank, et: kStr, g: r.get, v: s}, nil
}

// saIndex is an element of the character array s, or a section of it.
func (c *compiler) saIndex(n *syntax.Index, id *syntax.Ident, s *sym) (*ex, error) {
	r, err := c.symRef(id, s)
	if err != nil {
		return nil, err
	}
	ss, err := c.subs(n, r.rank)
	if err != nil {
		return nil, err
	}
	gf, name := r.get, r.name
	if allScalar(ss) {
		rank := r.rank
		fns := make([]intFn, rank)
		for i := range ss {
			fns[i] = ss[i].scalar
		}
		pos := func(f *frame) (*garr, int) {
			g := gf(f)
			var buf [MaxRank]int64
			for i, fn := range fns {
				buf[i] = fn(f)
			}
			return g, g.off(f, name, buf[:rank])
		}
		// the place is the element's, for the copy-out of an actual argument
		pl := func(f *frame) *sref { g, o := pos(f); return &sref{g: g, off: o} }
		return &ex{k: kStr, slot: -1, s: func(f *frame) string { g, o := pos(f); return g.s[o] }, pl: pl}, nil
	}
	rank := 0
	for _, s := range ss {
		if s.trip {
			rank++
		}
	}
	return &ex{k: kSA, slot: -1, rank: rank, et: kStr, g: func(f *frame) *garr {
		g := gf(f)
		offs, ext := resolve(f, g, name, ss)
		h := newStrGarr(f, g.clen, ones(len(ext)), ext)
		for k, o := range offs {
			h.s[k] = g.s[o]
		}
		return h
	}}, nil
}

// overlay is old with its leftmost characters replaced by v: what the copy-out
// of a character dummy writes back to its actual argument. A dummy shorter than
// its actual is associated with the actual's leftmost characters only (F2018
// 15.5.2.11), so the others keep their value; a dummy is never longer.
func overlay(old, v string) string {
	if len(v) >= len(old) {
		return v[:len(old)]
	}
	return v + old[len(v):]
}

// getStr and setStr read and write a character place for the copy-out of an
// actual argument: a component of a derived-type object or an element of a
// character array. A write keeps the place's length (overlay).
func (r *sref) getStr() string {
	if r.o != nil {
		return r.o.v[r.ci].s
	}
	return r.g.s[r.off]
}

func (r *sref) setStr(v string) {
	if r.o != nil {
		r.o.v[r.ci].s = overlay(r.o.v[r.ci].s, v)
		return
	}
	r.g.s[r.off] = overlay(r.g.s[r.off], v)
}

// saRef is the character array an assignment target designates, and its
// subscript list, if any.
func (c *compiler) saRef(at syntax.Node, lhs syntax.Expr) (*gref, *syntax.Index, error) {
	var base syntax.Expr = lhs
	var ix *syntax.Index
	if i, ok := lhs.(*syntax.Index); ok {
		base, ix = i.X, i
	}
	b, ok := base.(*syntax.Ident)
	if !ok {
		return nil, nil, c.errf(at, "unsupported assignment target")
	}
	s, _, err := c.symOf(b)
	if err != nil || s.k != kSA {
		return nil, nil, c.errf(at, "%s is not a character array", b.Name)
	}
	r, err := c.symRef(b, s)
	return r, ix, err
}

// assignSA compiles an assignment to a character array, a section or an
// element of it: each element takes the value cut or blank-padded to the
// array's length (F2018 10.2.1.3). An array value is computed whole before it
// is stored (a section of the target is a copy), so a(2:3) = a(1:2) is right.
func (c *compiler) assignSA(at syntax.Node, lhs syntax.Expr, rhs *ex) (stmtFn, error) {
	ref, ix, err := c.saRef(at, lhs)
	if err != nil {
		return nil, err
	}
	var ss []sub
	whole := true
	if ix != nil {
		whole = false
		if ss, err = c.subs(ix, ref.rank); err != nil {
			return nil, err
		}
		if !allScalar(ss) {
			c.tmps = true // as in assignGA: the offsets are temporaries
		}
	}
	var sv strFn
	var rv gaFn
	switch rhs.k {
	case kSA:
		rv = rhs.g
	case kStr:
		sv = rhs.s
	default:
		return nil, c.errf(at, "a character array is assigned a value that is not character")
	}
	name, get := ref.name, ref.get
	return func(f *frame) {
		var src *garr
		var str string
		if rv != nil {
			src = rv(f)
		} else {
			str = sv(f)
		}
		dst := get(f)
		var offs []int
		var ext []int
		var n int
		if whole {
			n, ext = dst.size(), dst.ext
		} else {
			offs, ext = resolve(f, dst, name, ss)
			n = len(offs)
			if allScalar(ss) {
				ext = nil
			}
		}
		if src != nil && !sameShape(ext, src.ext) {
			fail(f, "arrays of extents %v and %v are not conformable", ext, src.ext)
		}
		if src == nil {
			str = fitLen(str, dst.clen)
		}
		for k := 0; k < n; k++ {
			o := k
			if !whole {
				o = offs[k]
			}
			if src != nil {
				dst.s[o] = fitLen(src.s[k], dst.clen)
			} else {
				dst.s[o] = str
			}
		}
	}, nil
}

// strArrayCons compiles an array constructor of character values. With a
// type-spec, each value is cut or blank-padded to its length; without one, the
// values are of one length (F2018 7.8 C7110), which the analyser does not
// always know: it is then that of the values, and values of different lengths
// stop the program rather than being cut to one of them.
func (c *compiler) strArrayCons(n *syntax.ArrayCons, tv sema.TV) (*ex, error) {
	tlen := tv.Type.Len
	var elems []emitFn
	for _, e := range n.Elems {
		fn, err := c.emitter(e, kStr)
		if err != nil {
			return nil, err
		}
		elems = append(elems, fn)
	}
	return &ex{k: kSA, slot: -1, rank: 1, et: kStr, g: func(f *frame) *garr {
		a := &acc{et: kStr}
		for _, fn := range elems {
			fn(f, a)
		}
		clen := tlen
		if clen < 0 {
			clen = 0
			for k, v := range a.s {
				if k == 0 {
					clen = int64(len(v))
				} else if int64(len(v)) != clen {
					fail(f, "the values of an array constructor are of lengths %d and %d: give it a type-spec", clen, len(v))
				}
			}
		}
		g := newStrGarr(f, clen, []int64{1}, []int{len(a.s)})
		for k, v := range a.s {
			g.s[k] = fitLen(v, clen)
		}
		return g
	}}, nil
}

// addStr appends a character value to a constructor's elements.
func (a *acc) addStr(f *frame, s string) {
	n := len(a.s)
	if n >= MaxArrayElems {
		fail(f, "array constructor of more than %d elements", MaxArrayElems)
	}
	if c := cap(a.s); n == c {
		// as in add: the larger backing array is charged before it is made
		f.bud.peek(f, 16*int64(max(2*c, 8)))
	}
	a.s = append(a.s, s)
}

// printStrs appends the elements of the character array g, in array element
// order, to an output list.
func printStrs(f *frame, l []rt.Val, g *garr) []rt.Val {
	n := min(g.size(), max(0, f.bud.valRoom()-len(l))) // no more than can be printed
	f.bud.valsFit(f, n)
	for k := 0; k < n; k++ {
		l = append(l, rt.Str(g.s[k]))
	}
	return l
}
