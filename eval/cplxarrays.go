package eval

import (
	"math/cmplx"

	"github.com/go-embedded-fortran/fortran/rt"
	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// Complex arrays: complex(4) and complex(8) arrays of rank 1 to 7 and constant
// bounds, stored as the general arrays are (a garr whose c holds the elements,
// column-major; a complex(4) element is held with both parts representable in
// single precision). They are an expression kind of their own, kCA, as the
// character arrays are (strarrays.go): the paths written for the numeric
// arrays read g.i and g.r, and a kind they do not know is refused there
// instead of read wrong.
//
// An elemental operation applies complex.go's scalar operation to each element
// (F2018 10.1.4: an elemental operation on arrays is the operation on each
// pair of corresponding elements), so the arithmetic and its error bound are
// those of the scalars: + - * / and the integer powers -1..2 are gfortran's at
// -O0, rounded to the result's kind after each operation.

// cplxGarrBytes is the storage of a complex array of n elements: two float64.
func cplxGarrBytes(n int) int64 { return 16*int64(n) + arrayOverhead }

// newCplxGarr makes a complex array of kind kd (0 or 4), zero.
func newCplxGarr(f *frame, kd int, lb []int64, ext []int) *garr {
	n := elemCount(f, ext)
	f.bud.peek(f, cplxGarrBytes(n))
	return &garr{et: kCplx, kd: kd, lb: lb, ext: ext, c: make([]complex128, n)}
}

// cat is element n of g as a complex: an integer or real element is (x, +0),
// the conversion of F2018 10.1.5.2.1.
func (g *garr) cat(n int) complex128 {
	switch g.et {
	case kCplx:
		return g.c[n]
	case kReal:
		return complex(g.r[n], 0)
	}
	return complex(float64(g.i[n]), 0)
}

// numArr is the array value e read as a general array, when e is one that a
// complex array operation takes: a complex, integer or real array. A real(8)
// array of the kernels' kind (kArr) is seen through a view that shares its
// elements; nothing writes through it.
func numArr(e *ex) gaFn {
	switch {
	case e.k == kCA || e.k == kGA && (e.et == kInt || e.et == kReal):
		return e.g
	case e.k == kArr:
		fa := e.a
		return func(f *frame) *garr {
			a := fa(f)
			ext := []int{a.n1}
			if a.rank == 2 {
				ext = []int{a.n1, a.n2}
			}
			return &garr{et: kReal, lb: ones(len(ext)), ext: ext, r: a.data}
		}
	}
	return nil
}

// declCA declares the complex array ss.
func (c *compiler) declCA(ss *sema.Symbol, e syntax.Node, init syntax.Expr) error {
	name := ss.Name
	param := ss.Kind == sema.SymParam
	switch {
	case ss.Rank < 1 || ss.Rank > MaxRank || len(ss.Dims) != ss.Rank:
		return c.errf(e, "rank %d arrays are not supported", ss.Rank)
	case ss.Flags&sema.FDummy != 0 && ss.Flags&(sema.FOptional|sema.FValue|sema.FTarget) != 0:
		return c.errf(e, "dummy argument %s: an optional, value or target complex array dummy argument is not supported yet", name)
	case ss.Flags&(sema.FPointer|sema.FAllocatable) != 0:
		return c.errf(e, "%s: an allocatable or pointer complex array is not supported yet", name)
	case init != nil && !param && (c.cur.unit.Kind == syntax.SubroutineUnit || c.cur.unit.Kind == syntax.FunctionUnit):
		return c.errf(e, "%s: an initialised variable of a procedure is saved between calls, which is not supported yet", name)
	}
	s := &sym{k: kCA, et: kCplx, kd: kdOf(ss.Type), rank: ss.Rank, owner: c.cur, slot: c.cur.nGAs}
	if ss.Flags&sema.FDummy != 0 {
		// a view of the actual's elements (bind), given its declared bounds or
		// keeping the actual's shape, as a numeric dummy array (dummyshape.go)
		c.cur.nGAs++
		c.syms[ss] = s
		return c.explicitShapeDummy(ss, e, s)
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
	c.cur.nGAs++
	slot, kd := s.slot, s.kd
	c.cur.init = append(c.cur.init, func(f *frame) {
		f.gas[slot] = f.bud.retain(f, f, newCplxGarr(f, kd, append([]int64(nil), lb...), append([]int(nil), ext...)))
	})
	c.syms[ss] = s
	if init == nil {
		return nil
	}
	v, err := c.expr(init)
	var fn stmtFn
	if err == nil {
		// as for a numeric array (declSym): a copy of the name stands for the
		// variable while the assignment is compiled
		id := syntax.Ident{Span: syntax.Span{Pos: e.P()}, Name: name}
		c.info.Uses[&id] = ss
		fn, err = c.assignCA(e, &id, s, nil, v)
		delete(c.info.Uses, &id)
	}
	if err != nil {
		return err
	}
	c.cur.init = append(c.cur.init, fn)
	return nil
}

// caIdent is the complex array s as a whole.
func (c *compiler) caIdent(id *syntax.Ident, s *sym) (*ex, error) {
	r, err := c.symRef(id, s)
	if err != nil {
		return nil, err
	}
	return &ex{k: kCA, slot: -1, rank: s.rank, et: kCplx, kd: s.kd, g: r.get, v: s}, nil
}

// caIndex is an element of the complex array s, or a section of it.
func (c *compiler) caIndex(n *syntax.Index, id *syntax.Ident, s *sym) (*ex, error) {
	r, err := c.symRef(id, s)
	var ss []sub
	if err == nil {
		ss, err = c.subs(n, s.rank)
	}
	if err != nil {
		return nil, err
	}
	gf, name, kd := r.get, r.name, s.kd
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
		return &ex{k: kCplx, kd: kd, slot: -1, z: func(f *frame) complex128 { g, o := pos(f); return g.c[o] }, pl: pl}, nil
	}
	rank := 0
	for _, s := range ss {
		if s.trip {
			rank++
		}
	}
	return &ex{k: kCA, slot: -1, rank: rank, et: kCplx, kd: kd, g: func(f *frame) *garr {
		g := gf(f)
		offs, ext := resolve(f, g, name, ss)
		h := newCplxGarr(f, kd, ones(len(ext)), ext)
		for k, o := range offs {
			h.c[k] = g.c[o]
		}
		return h
	}}, nil
}

// assignCA compiles an assignment to the complex array s named id, or to the
// section or element ix of it (nil: the whole array). The value is a complex
// array, an integer or real array converted element by element, or a numeric
// scalar given to every element (F2018 10.2.1.3, Table 10.8). An array value
// is computed whole before it is stored (a section of the target is a copy),
// so c(2:3) = c(1:2) is right.
func (c *compiler) assignCA(at syntax.Node, id *syntax.Ident, s *sym, ix *syntax.Index, rhs *ex) (stmtFn, error) {
	ref, err := c.symRef(id, s)
	var ss []sub
	if err == nil && ix != nil {
		ss, err = c.subs(ix, s.rank)
	}
	if err != nil {
		return nil, err
	}
	whole := ix == nil
	if !whole && !allScalar(ss) {
		c.tmps = true // as in assignGA: the offsets are temporaries
	}
	var sv cplxFn
	var rv gaFn
	switch {
	case numArr(rhs) != nil:
		rv = numArr(rhs)
	case rhs.k == kInt || rhs.k == kReal || rhs.k == kCplx:
		sv = rhs.asCplx().z
	default:
		return nil, c.errf(at, "a complex array is assigned a value that is not numeric")
	}
	name, get, single := ref.name, ref.get, s.kd == 4
	return func(f *frame) {
		var src *garr
		var z complex128
		if rv != nil {
			src = rv(f)
		} else {
			z = sv(f)
			if single {
				z = round4(z)
			}
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
		for k := 0; k < n; k++ {
			o := k
			if !whole {
				o = offs[k]
			}
			if src != nil {
				z = src.cat(k)
				if single {
					z = round4(z)
				}
			}
			dst.c[o] = z
		}
	}, nil
}

// cop is an operand of an elemental operation with a complex array: a scalar,
// or an array (complex, or integer or real read as complex).
type cop struct {
	sc  cplxFn
	arr gaFn
}

func (c *compiler) caOperand(at syntax.Node, e *ex) (cop, error) {
	switch {
	case numArr(e) != nil:
		return cop{arr: numArr(e)}, nil
	case e.k == kInt || e.k == kReal || e.k == kCplx:
		return cop{sc: e.asCplx().z}, nil
	}
	return cop{}, c.errf(at, "this operand of a complex array operation is not supported")
}

// cplxElemental applies op to the elements of the operands: the scalars are
// evaluated once, the arrays must have one shape, which the result has. The
// result is a complex array of kind kd (et kCplx), or a real (kReal) or
// logical (kLog) array holding the real part of what op returns.
func cplxElemental(f *frame, et kind, kd int, ops []cop, op func(v []complex128) complex128) *garr {
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
	v := make([]complex128, len(ops))
	sc := make([]complex128, len(ops))
	for k, o := range ops {
		if o.sc != nil {
			sc[k] = o.sc(f)
		}
	}
	lb, ext := ones(len(shape.ext)), append([]int(nil), shape.ext...)
	var out *garr
	if et == kCplx {
		out = newCplxGarr(f, kd, lb, ext)
	} else {
		out = newGarr(f, et, kd, lb, ext)
	}
	for e, n := 0, out.size(); e < n; e++ {
		for k := range ops {
			if arrs[k] != nil {
				v[k] = arrs[k].cat(e)
			} else {
				v[k] = sc[k]
			}
		}
		z := op(v)
		switch et {
		case kCplx:
			if kd == 4 {
				z = round4(z)
			}
			out.c[e] = z
		case kLog:
			out.i[e] = b2i(real(z) != 0)
		default:
			out.put(e, 0, real(z))
		}
	}
	return out
}

// singlePrec reports an operand that is single precision for a complex
// comparison: an integer, or of kind 4 (see cplxCompare).
func singlePrec(e *ex) bool {
	if e.k == kGA {
		return e.et == kInt || e.kd == 4
	}
	return e.k == kInt || e.kd == 4
}

// caBinary compiles a binary operation of which one operand is an array and
// either is complex; tv is the result's type.
func (c *compiler) caBinary(n *syntax.Binary, x, y *ex, tv sema.TV) (*ex, error) {
	a, err := c.caOperand(n, x)
	if err != nil {
		return nil, err
	}
	var b cop
	if n.Op != "**" {
		if b, err = c.caOperand(n, y); err != nil {
			return nil, err
		}
	}
	kd := kdOf(tv.Type)
	ops := []cop{a, b}
	var fn func(v []complex128) complex128
	et := kCplx
	switch n.Op {
	case "+":
		fn = func(v []complex128) complex128 { return cadd(kd, v[0], v[1], false) }
	case "-":
		fn = func(v []complex128) complex128 { return cadd(kd, v[0], v[1], true) }
	case "*":
		fn = func(v []complex128) complex128 { return cmul(kd, v[0], v[1]) }
	case "/":
		fn = func(v []complex128) complex128 { return cdiv(kd, v[0], v[1]) }
	case "**":
		// a constant integer exponent: inline for -1..2, as cplxBinary
		if y.k != kInt || !y.isC {
			return nil, c.errf(n, "** of a complex array is supported with a constant integer exponent only yet")
		}
		ops = ops[:1]
		switch p := y.ci; p {
		case 0:
			fn = func([]complex128) complex128 { return 1 }
		case 1:
			fn = func(v []complex128) complex128 { return v[0] }
		case 2:
			fn = func(v []complex128) complex128 { return cmul(kd, v[0], v[0]) }
		case -1:
			fn = func(v []complex128) complex128 { return cdiv(kd, 1, v[0]) }
		default:
			fn = func(v []complex128) complex128 { return cpowi(kd, v[0], p) }
		}
	case "==", "/=":
		single := singlePrec(x) && singlePrec(y)
		ne := n.Op == "/="
		et = kLog
		fn = func(v []complex128) complex128 {
			p, q := v[0], v[1]
			if single {
				p, q = round4(p), round4(q)
			}
			return complex(float64(b2i((real(p) == real(q) && imag(p) == imag(q)) != ne)), 0)
		}
	default:
		return nil, c.errf(n, "operator %q is not defined on complex operands", n.Op)
	}
	k := kCA
	if et == kLog {
		k = kGA
	}
	return &ex{k: k, slot: -1, rank: tv.Rank, et: et, kd: kd, g: func(f *frame) *garr {
		return cplxElemental(f, et, kd, ops, fn)
	}}, nil
}

// caUnary compiles - and + of a complex array.
func (c *compiler) caUnary(n *syntax.Unary, x *ex) (*ex, error) {
	switch n.Op {
	case "+":
		return x, nil
	case "-":
		ops, kd := []cop{{arr: x.g}}, x.kd
		return &ex{k: kCA, slot: -1, rank: x.rank, et: kCplx, kd: kd, g: func(f *frame) *garr {
			return cplxElemental(f, kCplx, kd, ops, func(v []complex128) complex128 { return -v[0] })
		}}, nil
	}
	return nil, c.errf(n, "operator %q on a complex array is not supported", n.Op)
}

// caIntrinsic compiles an elemental intrinsic with a complex array argument, or
// whose result is a complex array: real, aimag, conjg, abs and cmplx, applied
// to each element as complex.go applies them to a scalar.
func (c *compiler) caIntrinsic(n *syntax.Index, name string, tv sema.TV, args []*ex) (*ex, error) {
	switch name {
	case "cmplx", "dcmplx", "real", "dble", "realpart", "aimag", "dimag", "imagpart", "abs", "cabs", "cdabs", "zabs", "conjg", "dconjg":
	default:
		return nil, c.errf(n, "%s of a complex array is not supported yet", name)
	}
	rkd := kdOf(tv.Type)
	var ops []cop
	for _, a := range args {
		o, err := c.caOperand(n, a)
		if err != nil {
			return nil, err
		}
		ops = append(ops, o)
	}
	res := func(et kind, fn func(v []complex128) complex128) (*ex, error) {
		k := kCA
		if et != kCplx {
			k = kGA
		}
		return &ex{k: k, slot: -1, rank: tv.Rank, et: et, kd: rkd, g: func(f *frame) *garr {
			return cplxElemental(f, et, rkd, ops, fn)
		}}, nil
	}
	switch name {
	case "cmplx", "dcmplx":
		for _, a := range n.Args {
			if _, boz := a.Value.(*syntax.BozLit); boz {
				return nil, c.errf(n, "a BOZ argument of %s that is not folded to a constant is not supported", name)
			}
		}
		// cmplx(x [, y [, kind]]), positional: the analyser typed the result; a
		// complex x has no y (F2018 16.9.55)
		switch {
		case len(args) == 1:
			return res(kCplx, func(v []complex128) complex128 { return v[0] })
		case len(args) <= 3 && args[0].k != kCA && args[0].k != kCplx && args[1].k != kCA && args[1].k != kCplx && (len(args) == 2 || args[2].k == kInt && args[2].isC):
			ops = ops[:2]
			return res(kCplx, func(v []complex128) complex128 { return complex(real(v[0]), real(v[1])) })
		}
		return nil, c.errf(n, "%s of these arguments is not supported", name)
	}
	if len(args) != 1 || args[0].k != kCA {
		// real(c, kind) among them: the kind argument is not read yet
		return nil, c.errf(n, "%s of these arguments is not supported", name)
	}
	switch name {
	case "real", "dble", "realpart":
		return res(kReal, func(v []complex128) complex128 { return complex(real(v[0]), 0) })
	case "aimag", "dimag", "imagpart":
		return res(kReal, func(v []complex128) complex128 { return complex(imag(v[0]), 0) })
	case "abs", "cabs", "cdabs", "zabs":
		return res(kReal, func(v []complex128) complex128 { return complex(hypot(real(v[0]), imag(v[0])), 0) })
	}
	return res(kCplx, func(v []complex128) complex128 { return cmplx.Conj(v[0]) }) // conjg, dconjg
}

// cplxDot compiles DOT_PRODUCT with a complex result: sum(conjg(a)*b) (F2018
// 16.9.71), a real or integer vector read as complex, accumulated from zero in
// element order with each product and sum rounded to the result's kind kd, the
// loop gfortran expands inline (gfc_conv_intrinsic_dot_product).
func (c *compiler) cplxDot(n *syntax.Index, x, y *ex, kd int) (*ex, error) {
	a, b := numArr(x), numArr(y)
	if a == nil || b == nil {
		return nil, c.errf(n, "dot_product of these arguments is not supported")
	}
	return &ex{k: kCplx, kd: kd, slot: -1, z: func(f *frame) complex128 {
		p, q := a(f), b(f)
		if len(p.ext) != 1 || len(q.ext) != 1 || p.ext[0] != q.ext[0] {
			fail(f, "dot_product of arrays of extents %v and %v", p.ext, q.ext)
		}
		var s complex128
		for k := 0; k < p.ext[0]; k++ {
			// cmul and cadd of kind 4 read their operands in single precision
			s = cadd(kd, s, cmul(kd, cmplx.Conj(p.cat(k)), q.cat(k)), false)
		}
		return s
	}}, nil
}

// cplxArrayCons compiles an array constructor of complex values: each element
// converted to the constructor's type (F2018 7.8).
func (c *compiler) cplxArrayCons(n *syntax.ArrayCons, tv sema.TV) (*ex, error) {
	if tv.Type.Kind != 4 && tv.Type.Kind != 8 {
		return nil, c.errf(n, "an array constructor of type %s is not supported", tv.Type)
	}
	var elems []emitFn
	for _, e := range n.Elems {
		fn, err := c.emitter(e, kCplx)
		if err != nil {
			return nil, err
		}
		elems = append(elems, fn)
	}
	kd := kdOf(tv.Type)
	return &ex{k: kCA, slot: -1, rank: 1, et: kCplx, kd: kd, g: func(f *frame) *garr {
		a := &acc{et: kCplx}
		for _, fn := range elems {
			fn(f, a)
		}
		g := newCplxGarr(f, kd, []int64{1}, []int{len(a.c)})
		for k, v := range a.c {
			if kd == 4 {
				v = round4(v)
			}
			g.c[k] = v
		}
		return g
	}}, nil
}

// cplxEmitter is the emitter of the value x, the element e of a constructor
// whose elements are of the class et, when either is complex.
func (c *compiler) cplxEmitter(e syntax.Expr, x *ex, et kind) (emitFn, error) {
	if et != kCplx {
		return nil, c.errf(e, "an array constructor mixes complex and other values")
	}
	switch {
	case numArr(x) != nil:
		g := numArr(x)
		return func(f *frame, a *acc) {
			s := g(f)
			for k, n := 0, s.size(); k < n; k++ {
				a.addCplx(f, s.cat(k))
			}
		}, nil
	case x.k == kInt || x.k == kReal || x.k == kCplx:
		z := x.asCplx().z
		return func(f *frame, a *acc) { a.addCplx(f, z(f)) }, nil
	}
	return nil, c.errf(e, "this element of a complex array constructor is not supported")
}

// addCplx appends a complex value to a constructor's elements.
func (a *acc) addCplx(f *frame, z complex128) {
	n := len(a.c)
	if n >= MaxArrayElems {
		fail(f, "array constructor of more than %d elements", MaxArrayElems)
	}
	if c := cap(a.c); n == c {
		// as in add: the larger backing array is charged before it is made
		f.bud.peek(f, 16*int64(max(2*c, 8)))
	}
	a.c = append(a.c, z)
}

// printCplxs appends the elements of the complex array g, in array element
// order, to an output list.
func printCplxs(f *frame, l []rt.Val, g *garr, size int) []rt.Val {
	n := min(g.size(), max(0, f.bud.valRoom()-len(l))) // no more than can be printed
	f.bud.valsFit(f, n)
	for k := 0; k < n; k++ {
		z := g.c[k]
		l = append(l, rt.Cmplx(size, real(z), imag(z)))
	}
	return l
}
