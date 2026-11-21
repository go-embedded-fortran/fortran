package eval

import (
	"math"

	"github.com/go-embedded-fortran/fortran/rt"
	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// General arrays: integer, logical and real(4|8) arrays of rank 1 to 7 with any
// lower bounds, sections, whole-array and elemental expressions, and the array
// intrinsics. They are stored column-major (the first subscript varies
// fastest) in a slice of their own. The real(8) arrays of rank 1 and 2 that
// the gate kernels use keep their go-ndarray storage and their own code path
// (kArr); the two kinds of array are not mixed in one expression.

var powReal = math.Pow

// MaxRank is the largest rank of an array (F2018 8.5.8.1).
const MaxRank = 7

type gaFn func(*frame) *garr

type garr struct {
	et  kind // kInt, kReal or kLog: the class of the elements
	kd  int
	lb  []int64
	ext []int
	i   []int64   // kInt and kLog (0 or 1)
	r   []float64 // kReal

	// chg is what the array was charged, and own the frame it was charged to
	// (zero and nil for a temporary: see memory.go).
	chg int64
	own *frame
	// palloc marks a target ALLOCATE made through a pointer: the only one a
	// DEALLOCATE of a pointer may free (F2018 9.7.3.3).
	palloc bool
	// freed marks a target a DEALLOCATE of a pointer freed: another pointer
	// to it is told so.
	freed bool
	// aliased marks an array a pointer that outlives the array's frame was
	// associated with: when that frame returns, its charge moves to the heap
	// instead of being credited, since the pointer may still hold it
	// (pointers.go escapes, memory.go exit).
	aliased bool
}

func (g *garr) size() int {
	n := 1
	for _, e := range g.ext {
		n *= e
	}
	return n
}

func newGarr(f *frame, et kind, kd int, lb []int64, ext []int) *garr {
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
	f.bud.peek(f, garrBytes(n))
	g := &garr{et: et, kd: kd, lb: lb, ext: ext}
	if et == kReal {
		g.r = make([]float64, n)
	} else {
		g.i = make([]int64, n)
	}
	return g
}

func ones(n int) []int64 {
	l := make([]int64, n)
	for i := range l {
		l[i] = 1
	}
	return l
}

func (g *garr) clone() *garr {
	h := &garr{et: g.et, kd: g.kd, lb: append([]int64(nil), g.lb...), ext: append([]int(nil), g.ext...)}
	h.i = append([]int64(nil), g.i...)
	h.r = append([]float64(nil), g.r...)
	return h
}

// at is element n (column-major order) as an integer and as a real.
func (g *garr) at(n int) (int64, float64) {
	if g.et == kReal {
		r := g.r[n]
		return int64(r), r
	}
	i := g.i[n]
	return i, float64(i)
}

// put stores a value into element n as an assignment to the array's type does.
func (g *garr) put(n int, iv int64, rv float64) {
	switch g.et {
	case kReal:
		if g.kd == 4 {
			rv = float64(float32(rv))
		}
		g.r[n] = rv
	case kLog:
		g.i[n] = b2i(iv != 0)
	default:
		g.i[n] = narrow(g.kd, iv)
	}
}

func sameShape(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// off is the position of the element idx, bounds-checked.
func (g *garr) off(f *frame, name string, idx []int64) int {
	n, stride := 0, 1
	for d, x := range idx {
		k := x - g.lb[d]
		if uint64(k) >= uint64(g.ext[d]) {
			fail(f, "subscript %d of %s is %d, outside %d:%d", d+1, name, x, g.lb[d], g.lb[d]+int64(g.ext[d])-1)
		}
		n += int(k) * stride
		stride *= g.ext[d]
	}
	return n
}

// ---- subscripts and sections ----

type sub struct {
	scalar     intFn // a subscript
	lo, hi, st intFn // a triplet; nil parts default
	trip       bool
}

func (c *compiler) subs(n *syntax.Index, rank int) ([]sub, error) {
	if len(n.Args) != rank {
		return nil, c.errf(n, "rank %d array subscripted with %d subscripts", rank, len(n.Args))
	}
	out := make([]sub, rank)
	for i, a := range n.Args {
		if a.Keyword != "" || a.Value == nil {
			return nil, c.errf(n, "a keyword or alternate-return subscript is not supported")
		}
		if t, ok := a.Value.(*syntax.Triplet); ok {
			s := sub{trip: true}
			var err error
			for _, p := range []struct {
				e   syntax.Expr
				dst *intFn
			}{{t.Lo, &s.lo}, {t.Hi, &s.hi}, {t.Stride, &s.st}} {
				if p.e != nil {
					if *p.dst, err = c.intExpr(p.e); err != nil {
						return nil, err
					}
				}
			}
			out[i] = s
			continue
		}
		tv, err := c.tv(a.Value)
		if err != nil {
			return nil, err
		}
		if tv.Rank != 0 {
			return nil, c.errf(a.Value, "vector subscripts are not supported")
		}
		fn, err := c.intExpr(a.Value)
		if err != nil {
			return nil, err
		}
		out[i] = sub{scalar: fn}
	}
	return out, nil
}

func allScalar(ss []sub) bool {
	for _, s := range ss {
		if s.trip {
			return false
		}
	}
	return true
}

// resolve is the set of elements a section selects: their positions in g in
// the order of the section (first subscript fastest) and the extents of the
// section's dimensions (those of the triplets).
func resolve(f *frame, g *garr, name string, ss []sub) (offs []int, ext []int) {
	type dim struct {
		idx    []int64
		scalar bool
	}
	dims := make([]dim, len(ss))
	total := 1
	sect := false // a triplet: the result is a section, not one element
	for d, s := range ss {
		if !s.trip {
			dims[d] = dim{idx: []int64{s.scalar(f)}, scalar: true}
			continue
		}
		lo, hi, st := g.lb[d], g.lb[d]+int64(g.ext[d])-1, int64(1)
		if s.lo != nil {
			lo = s.lo(f)
		}
		if s.hi != nil {
			hi = s.hi(f)
		}
		if s.st != nil {
			if st = s.st(f); st == 0 {
				fail(f, "stride of a section of %s is zero", name)
			}
		}
		var n int64
		switch {
		case st > 0 && hi >= lo:
			n = (hi-lo)/st + 1
		case st < 0 && hi <= lo:
			n = (lo-hi)/(-st) + 1
		}
		if n > MaxArrayElems {
			fail(f, "section of %s is out of range", name)
		}
		// the subscripts of a triplet and, below, the offsets of the section are
		// temporaries of the statement: charged before they are allocated (an
		// element, with scalar subscripts only, allocates no list and is not
		// charged, so a loop of element assignments does not accumulate)
		f.bud.peek(f, 8*n)
		sect = true
		idx := make([]int64, 0, n)
		for k, v := int64(0), lo; k < n; k, v = k+1, v+st {
			idx = append(idx, v)
		}
		dims[d] = dim{idx: idx}
		ext = append(ext, int(n))
		total *= int(n)
		if total > MaxArrayElems {
			fail(f, "section of %s is out of range", name)
		}
	}
	if sect {
		f.bud.peek(f, 8*int64(total))
	}
	offs = make([]int, 0, total)
	if total == 0 {
		return offs, ext
	}
	cnt := make([]int, len(dims))
	idx := make([]int64, len(dims))
	for {
		for d := range dims {
			idx[d] = dims[d].idx[cnt[d]]
		}
		offs = append(offs, g.off(f, name, idx))
		d := 0
		for ; d < len(dims); d++ {
			if cnt[d]++; cnt[d] < len(dims[d].idx) {
				break
			}
			cnt[d] = 0
		}
		if d == len(dims) {
			return offs, ext
		}
	}
}

// ---- variables ----

// gref is where an array of the general kind lives: a variable's slot or an
// array component of a derived-type object.
type gref struct {
	ptr   func(*frame) **garr
	name  string
	et    kind
	kd    int
	rank  int
	alloc bool
	isPtr bool // a pointer array: what ALLOCATE makes for it is the heap's (pointers.go)
	// own is the frame the array's storage is charged to: the one that holds the
	// variable (nil: the running one). See memory.go.
	own func(*frame) *frame
}

// owner is the frame the array of r is charged to. The target ALLOCATE makes
// for a pointer outlives the pointer's frame (another pointer may hold it), so
// it is the heap's, credited only by DEALLOCATE.
func (r *gref) owner(f *frame) *frame {
	if r.isPtr {
		return f.bud.heapFrame()
	}
	if r.own != nil {
		return r.own(f)
	}
	return f
}

func (r *gref) get(f *frame) *garr {
	g := *r.ptr(f)
	if g == nil {
		fail(f, "array %s is not allocated", r.name)
	}
	return g
}

// symRef is the reference to a general array variable of this unit.
func (c *compiler) symRef(id *syntax.Ident, s *sym) (*gref, error) {
	h, err := c.hops(s, id)
	if err != nil {
		return nil, err
	}
	slot := s.slot
	return &gref{ptr: func(f *frame) **garr { return &f.at(h).gas[slot] }, own: func(f *frame) *frame { return f.at(h) }, name: id.Name, et: s.et, kd: s.kd, rank: s.rank, alloc: s.alloc, isPtr: s.ptr}, nil
}

func (c *compiler) gaVar(id *syntax.Ident, s *sym) (gaFn, error) {
	r, err := c.symRef(id, s)
	if err != nil {
		return nil, err
	}
	return r.get, nil
}

func (c *compiler) gaIdent(id *syntax.Ident, s *sym) (*ex, error) {
	r, err := c.symRef(id, s)
	if err != nil {
		return nil, err
	}
	return &ex{k: kGA, slot: -1, rank: s.rank, et: s.et, kd: s.kd, g: r.get, v: s}, nil
}

// declGA gives an array of the general kind its storage: a fixed-shape array is
// allocated when its unit starts, an allocatable one when ALLOCATE says.
func (c *compiler) declGA(ss *sema.Symbol, e syntax.Node, s *sym, k kind) error {
	if ss.Rank < 1 || ss.Rank > MaxRank || len(ss.Dims) != ss.Rank {
		return c.errf(e, "rank %d arrays are not supported", ss.Rank)
	}
	s.k, s.ga, s.et, s.rank, s.slot = kGA, true, k, ss.Rank, c.cur.nGAs
	c.cur.nGAs++
	if ss.Flags&sema.FDummy != 0 {
		// an assumed-shape or assumed-size dummy array takes the shape of its
		// actual argument (a view of it); an explicit-shape one is reshaped
		return c.explicitShapeDummy(ss, e, s)
	}
	if ss.Flags&sema.FAllocatable != 0 {
		s.alloc = true
		for _, dm := range ss.Dims {
			if !dm.Deferred {
				return c.errf(e, "an allocatable array is declared with (:,...)")
			}
		}
		return nil
	}
	if ss.Flags&sema.FPointer != 0 {
		// disassociated until a pointer assignment or an ALLOCATE (pointers.go)
		s.ptr = true
		for _, dm := range ss.Dims {
			if !dm.Deferred {
				return c.errf(e, "a pointer array is declared with (:,...)")
			}
		}
		return nil
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
	slot, kd := s.slot, s.kd
	c.cur.init = append(c.cur.init, func(f *frame) {
		f.gas[slot] = f.bud.retain(f, f, newGarr(f, k, kd, append([]int64(nil), lb...), append([]int(nil), ext...)))
	})
	return nil
}

// ---- elements and sections as expressions ----

func (c *compiler) gaIndex(n *syntax.Index, id *syntax.Ident, s *sym) (*ex, error) {
	r, err := c.symRef(id, s)
	if err != nil {
		return nil, err
	}
	return c.gaIndexRef(n, r)
}

func (c *compiler) gaIndexRef(n *syntax.Index, r *gref) (*ex, error) {
	gf := r.get
	ss, err := c.subs(n, r.rank)
	if err != nil {
		return nil, err
	}
	name, et, kd := r.name, r.et, r.kd
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
		switch et {
		case kReal:
			return &ex{k: kReal, kd: kd, slot: -1, r: func(f *frame) float64 { g, o := pos(f); return g.r[o] }}, nil
		case kLog:
			return &ex{k: kLog, slot: -1, b: func(f *frame) bool { g, o := pos(f); return g.i[o] != 0 }}, nil
		}
		return &ex{k: kInt, kd: kd, slot: -1, i: func(f *frame) int64 { g, o := pos(f); return g.i[o] }}, nil
	}
	rank := 0
	for _, s := range ss {
		if s.trip {
			rank++
		}
	}
	return &ex{k: kGA, slot: -1, rank: rank, et: et, kd: kd, g: func(f *frame) *garr {
		g := gf(f)
		offs, ext := resolve(f, g, name, ss)
		lb := ones(len(ext))
		h := newGarr(f, et, kd, lb, ext)
		for k, o := range offs {
			if et == kReal {
				h.r[k] = g.r[o]
			} else {
				h.i[k] = g.i[o]
			}
		}
		return h
	}}, nil
}

// ---- assignment ----

// val is a scalar value as an integer and a real.
type valFn func(*frame) (int64, float64)

func scalarVal(e *ex) valFn {
	switch e.k {
	case kInt:
		fn := e.i
		return func(f *frame) (int64, float64) { v := fn(f); return v, float64(v) }
	case kReal:
		fn := e.r
		return func(f *frame) (int64, float64) { v := fn(f); return int64(v), v }
	case kLog:
		fn := e.b
		return func(f *frame) (int64, float64) { return b2i(fn(f)), 0 }
	}
	return nil
}

// assignGA compiles an assignment whose target is an array variable, a
// section or an element; mask, when not nil, restricts it (WHERE).
func (c *compiler) assignGA(at syntax.Node, lhs syntax.Expr, rhs *ex, mask gaFn) (stmtFn, error) {
	ref, ix, err := c.lhsRef(at, lhs)
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
			// a section target's offsets are temporaries of the statement
			// (resolve charges them): the statement must start from none of the
			// last one's, as stmts does for a statement that makes temporaries
			c.tmps = true
		}
	}
	if rhs.k == kArr {
		return nil, c.errf(at, "an assignment between a real(8) array of the kernels' kind and another array is not supported")
	}
	name := ref.name
	var sv valFn
	var rv gaFn
	if rhs.k == kGA {
		rv = rhs.g
	} else if sv = scalarVal(rhs); sv == nil {
		return nil, c.errf(at, "this assignment is not supported")
	}
	alloc := ref.alloc
	return func(f *frame) {
		var src *garr
		var iv int64
		var rvv float64
		if rv != nil {
			src = rv(f)
		} else {
			iv, rvv = sv(f)
		}
		var dst *garr
		if whole && alloc && src != nil {
			// an allocatable array takes the shape of the value (F2018 10.2.1.3)
			if d := *ref.ptr(f); d == nil || !sameShape(d.ext, src.ext) {
				f.bud.release(f, d)
				*ref.ptr(f) = f.bud.retain(f, ref.owner(f), newGarr(f, ref.et, ref.kd, ones(len(src.ext)), append([]int(nil), src.ext...)))
			}
		}
		if dst = *ref.ptr(f); dst == nil {
			fail(f, "array %s is not allocated", name)
		}
		// offs maps element k of the target to its offset in dst; a whole array
		// needs no map (offset k), so none is built: it was one int per element,
		// at every assignment, outside the memory budget
		var offs []int
		var ext []int
		var n int
		if whole {
			n, ext = dst.size(), dst.ext
		} else {
			offs, ext = resolve(f, dst, name, ss)
			n = len(offs)
			if len(ss) > 0 && allScalar(ss) {
				ext = nil
			}
		}
		var m *garr
		if mask != nil {
			if m = mask(f); len(m.i) != n {
				fail(f, "the mask of WHERE does not conform with %s", name)
			}
		}
		if src != nil {
			if !sameShape(ext, src.ext) && !(len(ext) == 0 && len(src.i)+len(src.r) == 1) {
				fail(f, "arrays of extents %v and %v are not conformable", ext, src.ext)
			}
		}
		for k := 0; k < n; k++ {
			if m != nil && m.i[k] == 0 {
				continue
			}
			o := k
			if !whole {
				o = offs[k]
			}
			if src != nil {
				a, b := src.at(k)
				dst.put(o, a, b)
			} else {
				dst.put(o, iv, rvv)
			}
		}
	}, nil
}

// ---- allocate and deallocate ----

// lhsRef is the array an assignment target designates (a variable, a section
// or element of it, or an array component) and the subscript list, if any.
func (c *compiler) lhsRef(at syntax.Node, lhs syntax.Expr) (*gref, *syntax.Index, error) {
	var base syntax.Expr = lhs
	var ix *syntax.Index
	if i, ok := lhs.(*syntax.Index); ok {
		base, ix = i.X, i
	}
	switch b := base.(type) {
	case *syntax.Ident:
		s, _, err := c.symOf(b)
		if err != nil || !s.ga {
			return nil, nil, c.errf(at, "%s is not an array of the general kind", b.Name)
		}
		r, err := c.symRef(b, s)
		return r, ix, err
	case *syntax.Component:
		r, err := c.compRef(b)
		return r, ix, err
	}
	return nil, nil, c.errf(at, "unsupported assignment target")
}

func (c *compiler) allocateGA(a *syntax.Allocate, o syntax.Expr) (stmtFn, error) {
	ref, ix, err := c.lhsRef(o, o)
	if err != nil {
		return nil, err
	}
	if !ref.alloc && !ref.isPtr {
		return nil, c.errf(o, "%s is not allocatable", ref.name)
	}
	if a.Dealloc {
		if ref.isPtr {
			return deallocPointer(ref), nil
		}
		return func(f *frame) {
			f.bud.release(f, *ref.ptr(f))
			*ref.ptr(f) = nil
		}, nil
	}
	if ix == nil || len(ix.Args) != ref.rank {
		return nil, c.errf(o, "%s has rank %d", ref.name, ref.rank)
	}
	type bnd struct{ lo, hi intFn }
	bs := make([]bnd, ref.rank)
	for d, ar := range ix.Args {
		if ar.Keyword != "" || ar.Value == nil {
			return nil, c.errf(o, "bad extent")
		}
		var err error
		if t, ok := ar.Value.(*syntax.Triplet); ok {
			if t.Hi == nil || t.Stride != nil {
				return nil, c.errf(o, "bad bounds")
			}
			if t.Lo != nil {
				if bs[d].lo, err = c.intExpr(t.Lo); err != nil {
					return nil, err
				}
			}
			bs[d].hi, err = c.intExpr(t.Hi)
		} else {
			bs[d].hi, err = c.intExpr(ar.Value)
		}
		if err != nil {
			return nil, err
		}
	}
	et, kd, name := ref.et, ref.kd, ref.name
	return func(f *frame) {
		// a pointer may be allocated again: its old target is left behind
		// (still held by the heap until a DEALLOCATE through another pointer)
		if *ref.ptr(f) != nil && !ref.isPtr {
			fail(f, "array %s is already allocated", name)
		}
		lb := make([]int64, len(bs))
		ext := make([]int, len(bs))
		for d, b := range bs {
			lb[d] = 1
			if b.lo != nil {
				lb[d] = b.lo(f)
			}
			if n := b.hi(f) - lb[d] + 1; n > MaxArrayElems {
				fail(f, "array extent %d is out of range", n)
			} else if n > 0 {
				ext[d] = int(n)
			}
		}
		g := f.bud.retain(f, ref.owner(f), newGarr(f, et, kd, lb, ext))
		g.palloc = ref.isPtr
		*ref.ptr(f) = g
	}, nil
}

// ---- WHERE ----

func (c *compiler) where(w *syntax.Where) (stmtFn, error) {
	if len(w.Else) > 0 {
		return nil, c.errf(w, "ELSEWHERE is not supported yet")
	}
	m, err := c.expr(w.Mask)
	if err != nil {
		return nil, err
	}
	if m.k != kGA || m.et != kLog {
		return nil, c.errf(w.Mask, "the mask of WHERE is a logical array of the general kind")
	}
	body := w.Body
	if w.Single != nil {
		body = []syntax.Stmt{w.Single}
	}
	var fns []stmtFn
	for _, st := range body {
		as, ok := st.(*syntax.Assignment)
		if !ok {
			return nil, c.errf(st, "only assignments are supported in WHERE")
		}
		rhs, err := c.expr(as.RHS)
		if err != nil {
			return nil, err
		}
		fn, err := c.assignGA(as, as.LHS, rhs, m.g)
		if err != nil {
			return nil, err
		}
		fns = append(fns, fn)
	}
	return func(f *frame) {
		for _, fn := range fns {
			fn(f)
		}
	}, nil
}

// ---- expressions ----

// operand is a scalar or an array operand of an elemental operation.
type operand struct {
	sc  valFn
	arr gaFn
}

func (c *compiler) operandOf(at syntax.Node, e *ex) (operand, error) {
	switch e.k {
	case kGA:
		return operand{arr: e.g}, nil
	case kArr:
		return operand{}, c.errf(at, "an array of the kernels' kind (real(8), rank 1 or 2, declared with bounds 1) cannot be combined with another kind of array yet")
	}
	if v := scalarVal(e); v != nil {
		return operand{sc: v}, nil
	}
	return operand{}, c.errf(at, "this operand of an array operation is not supported")
}

// elemental applies op elementwise to the operands: the result has the type
// (class and kind) rt, the shape of the array operands, which must agree.
func elemental(f *frame, rt kind, kd int, ops []operand, op func(iv []int64, rv []float64) (int64, float64)) *garr {
	arrs := make([]*garr, len(ops))
	var shape *garr
	for k, o := range ops {
		if o.arr != nil {
			arrs[k] = o.arr(f)
			if shape == nil {
				shape = arrs[k]
			} else if !sameShape(shape.ext, arrs[k].ext) {
				fail(f, "arrays of extents %v and %v are not conformable", shape.ext, arrs[k].ext)
			}
		}
	}
	sciv := make([]int64, len(ops))
	scrv := make([]float64, len(ops))
	for k, o := range ops {
		if o.sc != nil {
			sciv[k], scrv[k] = o.sc(f)
		}
	}
	out := newGarr(f, rt, kd, ones(len(shape.ext)), append([]int(nil), shape.ext...))
	iv := make([]int64, len(ops))
	rv := make([]float64, len(ops))
	n := out.size()
	for e := 0; e < n; e++ {
		for k := range ops {
			if arrs[k] != nil {
				iv[k], rv[k] = arrs[k].at(e)
			} else {
				iv[k], rv[k] = sciv[k], scrv[k]
			}
		}
		a, b := op(iv, rv)
		out.put(e, a, b)
	}
	return out
}

func (c *compiler) gaBinary(n *syntax.Binary, x, y *ex, tv sema.TV) (*ex, error) {
	a, err := c.operandOf(n, x)
	if err != nil {
		return nil, err
	}
	b, err := c.operandOf(n, y)
	if err != nil {
		return nil, err
	}
	rk, ok := classOf(tv.Type)
	if !ok {
		return nil, c.errf(n, "an array operation on type %s is not supported", tv.Type)
	}
	kd := kdOf(tv.Type)
	op := n.Op
	ops := []operand{a, b}
	realOp := x.et == kReal || y.et == kReal || x.k == kReal || y.k == kReal
	var fn func(iv []int64, rv []float64) (int64, float64)
	switch op {
	case "+":
		fn = func(iv []int64, rv []float64) (int64, float64) { return iv[0] + iv[1], rv[0] + rv[1] }
	case "-":
		fn = func(iv []int64, rv []float64) (int64, float64) { return iv[0] - iv[1], rv[0] - rv[1] }
	case "*":
		fn = func(iv []int64, rv []float64) (int64, float64) { return iv[0] * iv[1], rv[0] * rv[1] }
	case "/":
		fn = func(iv []int64, rv []float64) (int64, float64) {
			if rk != kReal {
				if iv[1] == 0 {
					panic(&Error{Msg: "integer division by zero"})
				}
				return iv[0] / iv[1], 0
			}
			return 0, rv[0] / rv[1]
		}
	case "**":
		fn = func(iv []int64, rv []float64) (int64, float64) {
			if rk != kReal {
				return ipow(iv[0], iv[1]), 0
			}
			return 0, powReal(rv[0], rv[1])
		}
	case "==", "/=", "<", "<=", ">", ">=":
		fn = func(iv []int64, rv []float64) (int64, float64) {
			var cmp int
			if realOp {
				switch {
				case rv[0] < rv[1]:
					cmp = -1
				case rv[0] > rv[1]:
					cmp = 1
				case rv[0] != rv[1]: // NaN: every comparison but /= is false
					return b2i(op == "/="), 0
				}
			} else {
				switch {
				case iv[0] < iv[1]:
					cmp = -1
				case iv[0] > iv[1]:
					cmp = 1
				}
			}
			var r bool
			switch op {
			case "==":
				r = cmp == 0
			case "/=":
				r = cmp != 0
			case "<":
				r = cmp < 0
			case "<=":
				r = cmp <= 0
			case ">":
				r = cmp > 0
			default:
				r = cmp >= 0
			}
			return b2i(r), 0
		}
	case ".and.":
		fn = func(iv []int64, rv []float64) (int64, float64) { return b2i(iv[0] != 0 && iv[1] != 0), 0 }
	case ".or.":
		fn = func(iv []int64, rv []float64) (int64, float64) { return b2i(iv[0] != 0 || iv[1] != 0), 0 }
	case ".eqv.":
		fn = func(iv []int64, rv []float64) (int64, float64) { return b2i((iv[0] != 0) == (iv[1] != 0)), 0 }
	case ".neqv.":
		fn = func(iv []int64, rv []float64) (int64, float64) { return b2i((iv[0] != 0) != (iv[1] != 0)), 0 }
	default:
		return nil, c.errf(n, "operator %q on arrays is not supported", op)
	}
	return &ex{k: kGA, slot: -1, rank: tv.Rank, et: rk, kd: kd, g: func(f *frame) *garr {
		return elemental(f, rk, kd, ops, fn)
	}}, nil
}

func classOf(t sema.Type) (kind, bool) {
	switch {
	case t.Basic == sema.Integer && (t.Kind == 1 || t.Kind == 2 || t.Kind == 4 || t.Kind == 8):
		return kInt, true
	case t.Basic == sema.Logical && (t.Kind == 1 || t.Kind == 2 || t.Kind == 4 || t.Kind == 8):
		return kLog, true
	case t.Basic == sema.Real && (t.Kind == 4 || t.Kind == 8):
		return kReal, true
	}
	return 0, false
}

func (c *compiler) gaUnary(n *syntax.Unary, x *ex) (*ex, error) {
	a, err := c.operandOf(n, x)
	if err != nil {
		return nil, err
	}
	var fn func(iv []int64, rv []float64) (int64, float64)
	switch n.Op {
	case "-":
		fn = func(iv []int64, rv []float64) (int64, float64) { return -iv[0], -rv[0] }
	case "+":
		fn = func(iv []int64, rv []float64) (int64, float64) { return iv[0], rv[0] }
	case ".not.":
		fn = func(iv []int64, rv []float64) (int64, float64) { return b2i(iv[0] == 0), 0 }
	default:
		return nil, c.errf(n, "operator %q on an array is not supported", n.Op)
	}
	et, kd := x.et, x.kd
	return &ex{k: kGA, slot: -1, rank: x.rank, et: et, kd: kd, g: func(f *frame) *garr {
		return elemental(f, et, kd, []operand{a}, fn)
	}}, nil
}

// ---- constructors ----

// acc accumulates the elements of an array constructor.
type acc struct {
	et kind
	i  []int64
	r  []float64
}

func (a *acc) add(f *frame, iv int64, rv float64) {
	n := len(a.i) + len(a.r)
	if n >= MaxArrayElems {
		fail(f, "array constructor of more than %d elements", MaxArrayElems)
	}
	if c := cap(a.i) + cap(a.r); n == c {
		// the next append moves the elements to a larger backing array: that
		// array is a temporary of the statement, charged before it is made (at
		// most twice the old one, Go's growth; the old one becomes garbage, so
		// this over-counts, never under-counts)
		f.bud.peek(f, 8*int64(max(2*c, 8)))
	}
	if a.et == kReal {
		a.r = append(a.r, rv)
	} else {
		a.i = append(a.i, iv)
	}
}

type emitFn func(*frame, *acc)

func (c *compiler) arrayCons(n *syntax.ArrayCons, tv sema.TV) (*ex, error) {
	et, ok := classOf(tv.Type)
	if !ok {
		return nil, c.errf(n, "an array constructor of type %s is not supported", tv.Type)
	}
	var elems []emitFn
	for _, e := range n.Elems {
		fn, err := c.emitter(e)
		if err != nil {
			return nil, err
		}
		elems = append(elems, fn)
	}
	kd := kdOf(tv.Type)
	return &ex{k: kGA, slot: -1, rank: 1, et: et, kd: kd, g: func(f *frame) *garr {
		a := &acc{et: et}
		for _, fn := range elems {
			fn(f, a)
		}
		g := newGarr(f, et, kd, []int64{1}, []int{len(a.i) + len(a.r)})
		if et == kReal {
			for k, v := range a.r {
				g.put(k, int64(v), v)
			}
		} else {
			for k, v := range a.i {
				g.put(k, v, float64(v))
			}
		}
		return g
	}}, nil
}

func (c *compiler) emitter(e syntax.Expr) (emitFn, error) {
	if d, ok := e.(*syntax.ImpliedDo); ok {
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
		var items []emitFn
		for _, it := range d.Items {
			fn, err := c.emitter(it)
			if err != nil {
				return nil, err
			}
			items = append(items, fn)
		}
		slot := sy.slot
		return func(f *frame, a *acc) {
			l, h, st := lo(f), hi(f), step(f)
			if st == 0 {
				fail(f, "implied-do step is zero")
			}
			n := (h - l + st) / st
			if n > MaxArrayElems {
				fail(f, "implied-do of %d iterations is out of range", n)
			}
			v := l
			base := f.bud.mark()
			for ; n > 0; n-- {
				f.bud.reset(base) // the temporaries of the last iteration are garbage
				f.ints[slot] = v
				for _, it := range items {
					it(f, a)
				}
				v += st
			}
			f.ints[slot] = v
		}, nil
	}
	x, err := c.expr(e)
	if err != nil {
		return nil, err
	}
	if x.k == kGA {
		g := x.g
		return func(f *frame, a *acc) {
			s := g(f)
			for k, n := 0, s.size(); k < n; k++ {
				iv, rv := s.at(k)
				a.add(f, iv, rv)
			}
		}, nil
	}
	sv := scalarVal(x)
	if sv == nil {
		return nil, c.errf(e, "this element of an array constructor is not supported")
	}
	return func(f *frame, a *acc) { iv, rv := sv(f); a.add(f, iv, rv) }, nil
}

// printVals appends the elements of g, in array element order, to an output list.
func printVals(f *frame, l []rt.Val, g *garr, size int) []rt.Val {
	n := min(g.size(), max(0, f.bud.valRoom()-len(l))) // no more than can be printed
	f.bud.valsFit(f, n)
	for k := 0; k < n; k++ {
		switch g.et {
		case kReal:
			if g.kd == 4 {
				l = append(l, rt.Real4(float32(g.r[k])))
			} else {
				l = append(l, rt.Real8(g.r[k]))
			}
		case kLog:
			l = append(l, rt.Bool(size, g.i[k] != 0))
		default:
			l = append(l, rt.Int(size, g.i[k]))
		}
	}
	return l
}
