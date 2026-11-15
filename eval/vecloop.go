package eval

import (
	"math"
	"sync/atomic"

	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// Strip kernels. A counted DO whose whole body is one assignment to an element
// of a real(8) array of rank 1 or 2, with a right-hand side built from + - * /,
// real constants, real scalars and elements of real(8) arrays whose subscripts
// are the DO variable (plus or minus a constant) in at most one position and
// invariant elsewhere, is run a strip of vecChunk iterations at a time instead of
// one closure tree walk per iteration. The iterations are independent (a read of
// the written array is the very element written, nothing else), so the result is
// the one the sequential loop gives, operation for operation; every operation is
// a separate pass over its strip, so no fused multiply-add changes a rounding.
//
// The kernel is an optimisation of a loop that is still compiled the ordinary
// way: it refuses (run returns false before anything is written) when an array
// is unallocated, when a subscript of any iteration would leave its extent (the
// ordinary loop then stops at the iteration and with the message it always
// did), or when two arrays share their storage.

const (
	vecChunk  = 256 // iterations per strip
	vecMin    = 8   // fewer iterations run the ordinary loop
	vecLeaves = 12
	vecNodes  = 40
)

// disableVec turns the kernels off (the tests compare both paths); vecHits counts
// the kernel runs.
var (
	disableVec bool
	vecHits    int64
)

// vsub is one subscript of a kernel's array element: the DO variable plus off,
// or an invariant integer expression.
type vsub struct {
	dep bool
	off int64
	fn  intFn
	src syntax.Expr
	// a dependent subscript may also add (osign 1) or subtract (osign -1) an
	// invariant integer expression: DO variable + off + osign*ofn(f).
	ofn   intFn
	osign int64
	osrc  syntax.Expr
}

type vleaf struct {
	sv   *sym
	slot int
	h    int
	rank int
	sub  [2]vsub
	dim  int  // the 1-based position of the DO variable; 0: an invariant element
	same bool // the variable the kernel writes, at the element it writes
}

// vnode is one operation of the right-hand side, in evaluation order.
type vnode struct {
	// op: 'e' element, 'k' scalar, 'v' the DO variable (plus ioff) as a real,
	// 'n' negation, '+' '-' '*' '/', 'q' x**val for a constant integer, 'p'
	// x**y, 'm' min, 'M' max, 's' sqrt, 'a' abs, 'g' g(x).
	op   byte
	a, b int
	leaf int
	ioff int64
	g    func(float64) float64
	val  float64
	fn   realFn // 'k': read once at the start (nil: val)
	buf  int
}

type vecKernel struct {
	leaves []vleaf // array form: leaves[0] is the target
	nodes  []vnode
	root   int
	nbuf   int
	red    *vacc // reduction form: s = s + E
}

// vacc is the accumulator of a reduction: the real(8) scalar s of "s = s + E" or
// "s = s - E", read once and written back once.
type vacc struct {
	slot, h int
	neg     bool
}

// vop is the value of a node over a strip: a scalar or a slice.
type vop struct {
	s  float64
	v  []float64
	sc bool
}

type vstate struct {
	a      *array
	base   int
	stride int
}

type vecBuild struct {
	c      *compiler
	loop   *sym
	acc    *sym // the accumulator of a reduction: no operand may read it
	leaves []vleaf
	nodes  []vnode
	nbuf   int
}

// vecLoop compiles the kernel of d, or returns nil when d is not of the shape.
func (c *compiler) vecLoop(d *syntax.Do, loop *sym) *vecKernel {
	if disableVec || len(d.Body) != 1 {
		return nil
	}
	as, ok := d.Body[0].(*syntax.Assignment)
	if !ok {
		return nil
	}
	if id, ok := as.LHS.(*syntax.Ident); ok {
		return c.vecReduce(as, id, loop)
	}
	l, ok := as.LHS.(*syntax.Index)
	if !ok {
		return nil
	}
	b := &vecBuild{c: c, loop: loop}
	t, ok := b.leaf(l)
	if !ok || t.dim == 0 {
		return nil
	}
	b.leaves = append(b.leaves, *t)
	root, ok := b.real(as.RHS)
	if !ok {
		return nil
	}
	return &vecKernel{leaves: b.leaves, nodes: b.nodes, root: root, nbuf: b.nbuf}
}

// vecReduce compiles "s = s + E", "s = E + s" and "s = s - E" for a real(8)
// scalar s that E does not read. The additions run in iteration order, so the sum
// is the ordinary loop's, bit for bit.
func (c *compiler) vecReduce(as *syntax.Assignment, id *syntax.Ident, loop *sym) *vecKernel {
	s, _, err := c.symOf(id)
	if err != nil || s.k != kReal || s.kd != 0 || s.isC || s.ga || s == loop {
		return nil
	}
	bin, ok := as.RHS.(*syntax.Binary)
	if !ok || (bin.Op != "+" && bin.Op != "-") {
		return nil
	}
	isS := func(x syntax.Expr) bool {
		i, ok := x.(*syntax.Ident)
		if !ok {
			return false
		}
		t, _, err := c.symOf(i)
		return err == nil && t == s
	}
	var e syntax.Expr
	switch {
	case isS(bin.X):
		e = bin.Y
	case bin.Op == "+" && isS(bin.Y):
		e = bin.X
	default:
		return nil
	}
	if tv, err := c.tv(as.RHS); err != nil || tv.Type.Basic != sema.Real || kdOf(tv.Type) != 0 {
		return nil
	}
	h, err := c.hops(s, id)
	if err != nil {
		return nil
	}
	b := &vecBuild{c: c, loop: loop, acc: s}
	root, ok := b.real(e)
	if !ok {
		return nil
	}
	return &vecKernel{leaves: b.leaves, nodes: b.nodes, root: root, nbuf: b.nbuf,
		red: &vacc{slot: s.slot, h: h, neg: bin.Op == "-"}}
}

func (b *vecBuild) node(n vnode) int {
	if len(b.nodes) >= vecNodes {
		return -1
	}
	n.buf = -1
	if n.op != 'k' { // 'e' needs a gather buffer when the stride is not 1
		n.buf = b.nbuf
		b.nbuf++
	}
	b.nodes = append(b.nodes, n)
	return len(b.nodes) - 1
}

// leaf classifies an array element reference.
func (b *vecBuild) leaf(n *syntax.Index) (*vleaf, bool) {
	c := b.c
	id, ok := n.X.(*syntax.Ident)
	if !ok {
		return nil, false
	}
	s, _, err := c.symOf(id)
	if err != nil || s.ga || s.k != kArr || s.isC || (s.rank != 1 && s.rank != 2) || len(n.Args) != s.rank {
		return nil, false
	}
	h, err := c.hops(s, n)
	if err != nil {
		return nil, false
	}
	lf := &vleaf{sv: s, slot: s.slot, h: h, rank: s.rank}
	for i, a := range n.Args {
		if a.Keyword != "" || a.Value == nil {
			return nil, false
		}
		if _, sec := a.Value.(*syntax.Triplet); sec {
			return nil, false
		}
		e, err := c.expr(a.Value)
		if err != nil || e.k != kInt {
			return nil, false
		}
		off, osrc, osign, ofn, vok := int64(0), syntax.Expr(nil), int64(0), intFn(nil), false
		if !(e.aff && e.avs == b.loop.slot) {
			off, osrc, osign, ofn, vok = b.depVar(a.Value)
		}
		switch {
		case vok:
			if lf.dim != 0 {
				return nil, false
			}
			lf.dim = i + 1
			lf.sub[i] = vsub{dep: true, off: off, ofn: ofn, osign: osign, osrc: osrc}
		case e.aff && e.avs == b.loop.slot:
			if lf.dim != 0 {
				return nil, false
			}
			lf.dim = i + 1
			lf.sub[i] = vsub{dep: true, off: e.avo}
		case b.pureInv(a.Value):
			lf.sub[i] = vsub{fn: e.i, src: a.Value}
		default:
			return nil, false
		}
	}
	if b.acc == nil && len(b.leaves) > 0 { // not the target: it may only be the target's own element
		t := &b.leaves[0]
		if lf.sv == t.sv && lf.h == t.h {
			if lf.dim == 0 || lf.dim != t.dim || !b.sameSubs(lf, t) {
				return nil, false
			}
			lf.same = true
		}
	}
	return lf, true
}

// depVar recognises DO variable (plus a constant) plus or minus an invariant
// integer expression: i+m, m+i, i-m, (i-1)+m. The invariant part is evaluated
// once per loop.
func (b *vecBuild) depVar(n syntax.Expr) (off int64, osrc syntax.Expr, osign int64, ofn intFn, ok bool) {
	for {
		p, isP := n.(*syntax.Paren)
		if !isP {
			break
		}
		n = p.X
	}
	bin, isB := n.(*syntax.Binary)
	if !isB || (bin.Op != "+" && bin.Op != "-") {
		return 0, nil, 0, nil, false
	}
	dep := func(x syntax.Expr) (int64, bool) {
		e, err := b.c.expr(x)
		if err != nil || e.k != kInt || !e.aff || e.avs != b.loop.slot {
			return 0, false
		}
		return e.avo, true
	}
	inv := func(x syntax.Expr) (intFn, bool) {
		if !b.pureInv(x) {
			return nil, false
		}
		e, err := b.c.expr(x)
		if err != nil || e.k != kInt {
			return nil, false
		}
		return e.i, true
	}
	sign := int64(1)
	if bin.Op == "-" {
		sign = -1
	}
	if o, ok := dep(bin.X); ok {
		if fn, ok := inv(bin.Y); ok {
			return o, bin.Y, sign, fn, true
		}
	}
	if bin.Op == "+" {
		if o, ok := dep(bin.Y); ok {
			if fn, ok := inv(bin.X); ok {
				return o, bin.X, 1, fn, true
			}
		}
	}
	return 0, nil, 0, nil, false
}

func (b *vecBuild) sameSubs(x, y *vleaf) bool {
	for i := 0; i < x.rank; i++ {
		p, q := x.sub[i], y.sub[i]
		if p.dep != q.dep {
			return false
		}
		if p.dep {
			if p.off != q.off || p.osign != q.osign || (p.osign != 0 && !b.sameExpr(p.osrc, q.osrc)) {
				return false
			}
		} else if !b.sameExpr(p.src, q.src) {
			return false
		}
	}
	return true
}

func (b *vecBuild) sameExpr(x, y syntax.Expr) bool {
	switch x := x.(type) {
	case *syntax.Ident:
		y, ok := y.(*syntax.Ident)
		if !ok {
			return false
		}
		sx, _, e1 := b.c.symOf(x)
		sy, _, e2 := b.c.symOf(y)
		return e1 == nil && e2 == nil && sx == sy
	case *syntax.IntLit:
		y, ok := y.(*syntax.IntLit)
		return ok && x.Text == y.Text && x.Kind == y.Kind
	case *syntax.Paren:
		y, ok := y.(*syntax.Paren)
		return ok && b.sameExpr(x.X, y.X)
	case *syntax.Unary:
		y, ok := y.(*syntax.Unary)
		return ok && x.Op == y.Op && b.sameExpr(x.X, y.X)
	case *syntax.Binary:
		y, ok := y.(*syntax.Binary)
		return ok && x.Op == y.Op && b.sameExpr(x.X, y.X) && b.sameExpr(x.Y, y.Y)
	}
	return false
}

// pureInv reports whether n is an integer expression of literals and scalar
// integer variables other than the DO variable, combined by + - *: its value is
// the same on every iteration and evaluating it has no effect.
func (b *vecBuild) pureInv(n syntax.Expr) bool {
	switch n := n.(type) {
	case *syntax.IntLit:
		return true
	case *syntax.Ident:
		s, _, err := b.c.symOf(n)
		return err == nil && s != b.loop && (s.k == kInt || (s.isC && s.k == kInt))
	case *syntax.Paren:
		return b.pureInv(n.X)
	case *syntax.Unary:
		return (n.Op == "+" || n.Op == "-") && b.pureInv(n.X)
	case *syntax.Binary:
		return (n.Op == "+" || n.Op == "-" || n.Op == "*") && b.pureInv(n.X) && b.pureInv(n.Y)
	}
	return false
}

// real adds the nodes of the real(8) scalar expression n and returns its index.
func (b *vecBuild) real(n syntax.Expr) (int, bool) {
	c := b.c
	tv, err := c.tv(n)
	if err != nil || tv.Rank != 0 {
		return 0, false
	}
	if tv.Type.Basic == sema.Integer {
		return b.intAsReal(n)
	}
	if tv.Type.Basic != sema.Real || kdOf(tv.Type) != 0 {
		return 0, false
	}
	if tv.Const != nil {
		e, err := c.expr(n)
		if err != nil || !e.isC || e.k != kReal {
			return 0, false
		}
		i := b.node(vnode{op: 'k', val: e.cr})
		return i, i >= 0
	}
	switch n := n.(type) {
	case *syntax.Paren:
		return b.real(n.X)
	case *syntax.Unary:
		switch n.Op {
		case "+":
			return b.real(n.X)
		case "-":
			x, ok := b.real(n.X)
			if !ok {
				return 0, false
			}
			i := b.node(vnode{op: 'n', a: x})
			return i, i >= 0
		}
	case *syntax.Binary:
		switch n.Op {
		case "+", "-", "*", "/":
			x, ok := b.real(n.X)
			if !ok {
				return 0, false
			}
			y, ok := b.real(n.Y)
			if !ok {
				return 0, false
			}
			i := b.node(vnode{op: n.Op[0], a: x, b: y})
			return i, i >= 0
		case "**":
			ye, err := c.expr(n.Y)
			if err != nil {
				return 0, false
			}
			x, ok := b.real(n.X)
			if !ok {
				return 0, false
			}
			if ye.k == kInt && ye.isC { // as powInt: x, x*x or Pow(x, k)
				if ye.ci == 1 {
					return x, true
				}
				i := b.node(vnode{op: 'q', a: x, val: float64(ye.ci)})
				return i, i >= 0
			}
			y, ok := b.real(n.Y)
			if !ok {
				return 0, false
			}
			i := b.node(vnode{op: 'p', a: x, b: y})
			return i, i >= 0
		}
	case *syntax.Ident:
		if s, _, err := c.symOf(n); b.acc != nil && err == nil && s == b.acc {
			return 0, false
		}
		e, err := c.expr(n)
		if err != nil || e.k != kReal || e.kd != 0 || e.rank != 0 || e.r == nil {
			return 0, false
		}
		i := b.node(vnode{op: 'k', fn: e.r})
		return i, i >= 0
	case *syntax.Index:
		if id, ok := n.X.(*syntax.Ident); ok && c.info.Uses[id] == nil {
			return b.intrinsic(n, id.Name)
		}
		lf, ok := b.leaf(n)
		if !ok || len(b.leaves) >= vecLeaves {
			return 0, false
		}
		b.leaves = append(b.leaves, *lf)
		i := b.node(vnode{op: 'e', leaf: len(b.leaves) - 1})
		return i, i >= 0
	}
	return 0, false
}

// intAsReal adds the integer expression n converted to real(8): a constant, the
// DO variable plus a constant, or an invariant expression.
func (b *vecBuild) intAsReal(n syntax.Expr) (int, bool) {
	e, err := b.c.expr(n)
	if err != nil || e.k != kInt {
		return 0, false
	}
	var i int
	switch {
	case e.isC:
		i = b.node(vnode{op: 'k', val: float64(e.ci)})
	case e.aff && e.avs == b.loop.slot:
		i = b.node(vnode{op: 'v', ioff: e.avo})
	case b.pureInv(n):
		fi := e.i
		i = b.node(vnode{op: 'k', fn: func(f *frame) float64 { return float64(fi(f)) }})
	default:
		return 0, false
	}
	return i, i >= 0
}

// intrinsic adds the nodes of a call of an elemental real intrinsic whose
// evaluation is the one eval.intrinsicWith compiles.
func (b *vecBuild) intrinsic(n *syntax.Index, name string) (int, bool) {
	for _, a := range n.Args {
		if a.Keyword != "" || a.Value == nil {
			return 0, false
		}
		if _, sec := a.Value.(*syntax.Triplet); sec {
			return 0, false
		}
	}
	args := make([]int, 0, len(n.Args))
	arg := func(k int) bool {
		x, ok := b.real(n.Args[k].Value)
		args = append(args, x)
		return ok
	}
	var nd vnode
	switch name {
	case "real", "dble":
		if len(n.Args) < 1 || len(n.Args) > 2 || !arg(0) {
			return 0, false
		}
		return args[0], true // the analyser typed the result real(8)
	case "sqrt", "abs":
		if len(n.Args) != 1 || !arg(0) {
			return 0, false
		}
		nd = vnode{op: 's', a: args[0]}
		if name == "abs" {
			nd.op = 'a'
		}
	case "aint", "anint", "exp", "log", "log10", "sin", "cos", "tan", "atan":
		if len(n.Args) != 1 || !arg(0) {
			return 0, false
		}
		g := map[string]func(float64) float64{
			"aint": math.Trunc, "anint": roundHalfAway, "exp": math.Exp, "log": math.Log,
			"log10": math.Log10, "sin": math.Sin, "cos": math.Cos, "tan": math.Tan, "atan": math.Atan,
		}[name]
		nd = vnode{op: 'g', a: args[0], g: g}
	case "min", "max":
		if len(n.Args) < 2 || !arg(0) {
			return 0, false
		}
		op := byte('m')
		if name == "max" {
			op = 'M'
		}
		acc := args[0]
		for k := 1; k < len(n.Args); k++ {
			if !arg(k) {
				return 0, false
			}
			acc = b.node(vnode{op: op, a: acc, b: args[k]})
			if acc < 0 {
				return 0, false
			}
		}
		return acc, true
	default:
		return 0, false
	}
	i := b.node(nd)
	return i, i >= 0
}

// run executes iterations l .. l+n-1, or returns false having changed nothing.
// bud is charged n steps once the run is certain.
func (k *vecKernel) run(f *frame, l, n int64, bud *budget) bool {
	var st [vecLeaves]vstate
	for i := range k.leaves {
		lf := &k.leaves[i]
		a := f.at(lf.h).arrs[lf.slot]
		if a == nil {
			return false
		}
		ext := [2]int{a.n1, a.n2}
		var at [2]int64
		for d := 0; d < lf.rank; d++ {
			s := &lf.sub[d]
			if s.dep {
				lo := l + s.off - 1
				if s.ofn != nil {
					v := s.ofn(f)
					if v > 1<<40 || v < -(1<<40) {
						return false
					}
					lo += s.osign * v
				}
				if lo < 0 || lo+n-1 >= int64(ext[d]) {
					return false
				}
				at[d] = lo
			} else {
				v := s.fn(f) - 1
				if uint64(v) >= uint64(ext[d]) {
					return false
				}
				at[d] = v
			}
		}
		base := at[0]
		if lf.rank == 2 {
			base += at[1] * int64(a.n1)
		}
		stride := 1
		if lf.dim == 2 {
			stride = a.n1
		}
		st[i] = vstate{a: a, base: int(base), stride: stride}
		if k.red == nil && i > 0 && !lf.same {
			t := st[0].a
			if a == t || (len(a.data) > 0 && len(t.data) > 0 && &a.data[0] == &t.data[0]) {
				return false
			}
		}
	}
	bud.spend(n)
	atomic.AddInt64(&vecHits, 1)
	need := k.nbuf * vecChunk
	if len(f.vs) < need {
		f.vs = make([]float64, need)
	}
	var kv [vecNodes]float64
	for i := range k.nodes {
		if nd := &k.nodes[i]; nd.op == 'k' {
			if nd.fn != nil {
				kv[i] = nd.fn(f)
			} else {
				kv[i] = nd.val
			}
		}
	}
	var res [vecNodes]vop
	root := k.root
	tg := &st[0]
	var acc float64
	if k.red != nil {
		acc = f.at(k.red.h).reals[k.red.slot]
	}
	for off := 0; off < int(n); off += vecChunk {
		m := vecChunk
		if r := int(n) - off; r < m {
			m = r
		}
		for i := range k.nodes {
			nd := &k.nodes[i]
			switch nd.op {
			case 'k':
				res[i] = vop{s: kv[i], sc: true}
			case 'e':
				s := &st[nd.leaf]
				if k.leaves[nd.leaf].dim == 0 {
					res[i] = vop{s: s.a.data[s.base], sc: true}
				} else if s.stride == 1 {
					res[i] = vop{v: s.a.data[s.base+off : s.base+off+m]}
				} else {
					b := f.vs[nd.buf*vecChunk : nd.buf*vecChunk+m]
					p := s.base + off*s.stride
					for t := range b {
						b[t] = s.a.data[p]
						p += s.stride
					}
					res[i] = vop{v: b}
				}
			case 'n':
				x := res[nd.a]
				if x.sc {
					res[i] = vop{s: -x.s, sc: true}
					continue
				}
				b := f.vs[nd.buf*vecChunk : nd.buf*vecChunk+m]
				for t, v := range x.v {
					b[t] = -v
				}
				res[i] = vop{v: b}
			case 'v':
				b := f.vs[nd.buf*vecChunk : nd.buf*vecChunk+m]
				v := l + nd.ioff + int64(off)
				for t := range b {
					b[t] = float64(v + int64(t))
				}
				res[i] = vop{v: b}
			case 's', 'a', 'g', 'q':
				x := res[nd.a]
				if x.sc {
					res[i] = vop{s: vun(nd, x.s), sc: true}
					continue
				}
				b := f.vs[nd.buf*vecChunk : nd.buf*vecChunk+m]
				xv := x.v[:len(b)]
				switch nd.op {
				case 's':
					for t := range b {
						b[t] = math.Sqrt(xv[t])
					}
				case 'a':
					for t := range b {
						b[t] = math.Abs(xv[t])
					}
				case 'g':
					g := nd.g
					for t := range b {
						b[t] = g(xv[t])
					}
				default:
					if nd.val == 2 {
						for t := range b {
							b[t] = xv[t] * xv[t]
						}
					} else {
						k := nd.val
						for t := range b {
							b[t] = math.Pow(xv[t], k)
						}
					}
				}
				res[i] = vop{v: b}
			case 'm', 'M', 'p':
				res[i] = vgen(nd.op, f.vs[nd.buf*vecChunk:nd.buf*vecChunk+m], res[nd.a], res[nd.b])
			default:
				res[i] = vbin(nd.op, f.vs[nd.buf*vecChunk:nd.buf*vecChunk+m], res[nd.a], res[nd.b])
			}
		}
		r := res[root]
		if k.red != nil {
			switch {
			case r.sc && k.red.neg:
				for t := 0; t < m; t++ {
					acc -= r.s
				}
			case r.sc:
				for t := 0; t < m; t++ {
					acc += r.s
				}
			case k.red.neg:
				for _, v := range r.v {
					acc -= v
				}
			default:
				for _, v := range r.v {
					acc += v
				}
			}
			continue
		}
		if tg.stride == 1 {
			d := tg.a.data[tg.base+off : tg.base+off+m]
			if r.sc {
				for t := range d {
					d[t] = r.s
				}
			} else {
				copy(d, r.v)
			}
		} else {
			p := tg.base + off*tg.stride
			for t := 0; t < m; t++ {
				if r.sc {
					tg.a.data[p] = r.s
				} else {
					tg.a.data[p] = r.v[t]
				}
				p += tg.stride
			}
		}
	}
	if k.red != nil {
		f.at(k.red.h).reals[k.red.slot] = acc
	}
	return true
}

// vbin is x op y over a strip, written into d unless both are scalars.
func vbin(op byte, d []float64, x, y vop) vop {
	if x.sc && y.sc {
		switch op {
		case '+':
			return vop{s: x.s + y.s, sc: true}
		case '-':
			return vop{s: x.s - y.s, sc: true}
		case '*':
			return vop{s: x.s * y.s, sc: true}
		}
		return vop{s: x.s / y.s, sc: true}
	}
	switch {
	case y.sc:
		k, xv := y.s, x.v[:len(d)]
		switch op {
		case '+':
			for t := range d {
				d[t] = xv[t] + k
			}
		case '-':
			for t := range d {
				d[t] = xv[t] - k
			}
		case '*':
			for t := range d {
				d[t] = xv[t] * k
			}
		default:
			for t := range d {
				d[t] = xv[t] / k
			}
		}
	case x.sc:
		k, yv := x.s, y.v[:len(d)]
		switch op {
		case '+':
			for t := range d {
				d[t] = k + yv[t]
			}
		case '-':
			for t := range d {
				d[t] = k - yv[t]
			}
		case '*':
			for t := range d {
				d[t] = k * yv[t]
			}
		default:
			for t := range d {
				d[t] = k / yv[t]
			}
		}
	default:
		xv, yv := x.v[:len(d)], y.v[:len(d)]
		switch op {
		case '+':
			for t := range d {
				d[t] = xv[t] + yv[t]
			}
		case '-':
			for t := range d {
				d[t] = xv[t] - yv[t]
			}
		case '*':
			for t := range d {
				d[t] = xv[t] * yv[t]
			}
		default:
			for t := range d {
				d[t] = xv[t] / yv[t]
			}
		}
	}
	return vop{v: d}
}

// vun is the unary node nd applied to the scalar x.
func vun(nd *vnode, x float64) float64 {
	switch nd.op {
	case 's':
		return math.Sqrt(x)
	case 'a':
		return math.Abs(x)
	case 'g':
		return nd.g(x)
	}
	if nd.val == 2 {
		return x * x
	}
	return math.Pow(x, nd.val)
}

// vscalar2 is min, max or x**y of two scalars, as eval compiles them: max keeps
// the first operand unless the next is greater, min unless it is smaller.
func vscalar2(op byte, x, y float64) float64 {
	switch op {
	case 'm':
		if y < x {
			return y
		}
		return x
	case 'M':
		if y > x {
			return y
		}
		return x
	}
	return math.Pow(x, y)
}

// vgen is min, max or x**y over a strip.
func vgen(op byte, d []float64, x, y vop) vop {
	if x.sc && y.sc {
		return vop{s: vscalar2(op, x.s, y.s), sc: true}
	}
	switch {
	case y.sc:
		xv := x.v[:len(d)]
		for t := range d {
			d[t] = vscalar2(op, xv[t], y.s)
		}
	case x.sc:
		yv := y.v[:len(d)]
		for t := range d {
			d[t] = vscalar2(op, x.s, yv[t])
		}
	default:
		xv, yv := x.v[:len(d)], y.v[:len(d)]
		for t := range d {
			d[t] = vscalar2(op, xv[t], yv[t])
		}
	}
	return vop{v: d}
}
