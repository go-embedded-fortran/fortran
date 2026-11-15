package eval

import (
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
	vecNodes  = 24
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
	op   byte // 'e' element, 'k' scalar, 'n' negation, '+' '-' '*' '/'
	a, b int
	leaf int
	val  float64
	fn   realFn // 'k': read once at the start (nil: val)
	buf  int
}

type vecKernel struct {
	leaves []vleaf // leaves[0] is the target
	nodes  []vnode
	nbuf   int
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
	if _, ok := b.real(as.RHS); !ok {
		return nil
	}
	return &vecKernel{leaves: b.leaves, nodes: b.nodes, nbuf: b.nbuf}
}

func (b *vecBuild) node(n vnode) int {
	if len(b.nodes) >= vecNodes {
		return -1
	}
	n.buf = -1
	switch n.op {
	case 'n', '+', '-', '*', '/':
		n.buf = b.nbuf
		b.nbuf++
	case 'e':
		n.buf = b.nbuf // a gather buffer when the stride is not 1
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
		switch {
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
	if len(b.leaves) > 0 { // not the target: it may only be the target's own element
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

func (b *vecBuild) sameSubs(x, y *vleaf) bool {
	for i := 0; i < x.rank; i++ {
		p, q := x.sub[i], y.sub[i]
		if p.dep != q.dep {
			return false
		}
		if p.dep {
			if p.off != q.off {
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
	if err != nil || tv.Rank != 0 || tv.Type.Basic != sema.Real || kdOf(tv.Type) != 0 {
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
		}
	case *syntax.Ident:
		e, err := c.expr(n)
		if err != nil || e.k != kReal || e.kd != 0 || e.rank != 0 || e.r == nil {
			return 0, false
		}
		i := b.node(vnode{op: 'k', fn: e.r})
		return i, i >= 0
	case *syntax.Index:
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
		if i > 0 && !lf.same {
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
	root := len(k.nodes) - 1
	tg := &st[0]
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
			default:
				res[i] = vbin(nd.op, f.vs[nd.buf*vecChunk:nd.buf*vecChunk+m], res[nd.a], res[nd.b])
			}
		}
		r := res[root]
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
