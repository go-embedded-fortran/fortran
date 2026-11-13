package sema

import (
	"github.com/go-embedded-fortran/fortran/syntax"
)

// This file is M2 part 3 (bib/fortran.md §10): the shape of an array
// expression (F2018 9.5.3 sections, 10.1.5 and 10.1.9 operands, 10.2.1.2
// assignment, 16.9 intrinsic results) with compile-time extents where they are
// known, and the conformance rules that use them.
//
// A shape is a slice of extents, one per dimension: an extent is >= 0 when it
// is known at compile time and unknownExtent (-1) when it is not (assumed-shape,
// deferred-shape, an automatic array, a section with a variable bound). A nil
// shape on an array means no information at all; the rank is always TV.Rank.
// Leniency as everywhere in the package: an unknown extent is never the reason
// for a refusal.

// maxExtent bounds the extents and element counts the analyser computes: a
// larger one is treated as unknown, so that no product of extents overflows.
const maxExtent int64 = 1 << 40

// unknownExtent is the extent of a dimension that is not a compile-time
// constant.
const unknownExtent int64 = -1

// shapeOfDims is the shape of an array declared with dims (F2018 8.5.8): the
// extent of an explicit-shape dimension with constant bounds, else unknown.
// It returns nil when dims does not describe rank dimensions.
func shapeOfDims(dims []Dim, rank int) []int64 {
	if rank <= 0 || len(dims) != rank {
		return nil
	}
	s := make([]int64, rank)
	for i, d := range dims {
		if e, ok := d.extent(); ok {
			s[i] = e
		} else {
			s[i] = unknownExtent
		}
	}
	return s
}

// unknownShape is a shape of rank dimensions, all unknown.
func unknownShape(rank int) []int64 {
	if rank <= 0 {
		return nil
	}
	s := make([]int64, rank)
	for i := range s {
		s[i] = unknownExtent
	}
	return s
}

// shapeKnown reports whether every extent of s is known.
func shapeKnown(s []int64) bool {
	if len(s) == 0 {
		return false
	}
	for _, e := range s {
		if e < 0 {
			return false
		}
	}
	return true
}

// shapeSize is the number of elements of a fully known shape.
func shapeSize(s []int64) (int64, bool) {
	if !shapeKnown(s) {
		return 0, false
	}
	n := int64(1)
	for _, e := range s {
		n *= e
		if n > maxExtent {
			return 0, false
		}
	}
	return n, true
}

// conformDim finds the first dimension on which two shapes of equal rank have
// known and different extents (F2018 3.51: conformable arrays have the same
// shape).
func conformDim(a, b []int64) (dim int, x, y int64, bad bool) {
	if len(a) != len(b) {
		return 0, 0, 0, false
	}
	for i := range a {
		if a[i] >= 0 && b[i] >= 0 && a[i] != b[i] {
			return i + 1, a[i], b[i], true
		}
	}
	return 0, 0, 0, false
}

// joinShape is the shape of a result whose operands have shapes a and b (F2018
// 10.1.5: a scalar operand is conformable with anything): the shape of the
// array operand, and where both are arrays the known extent of either.
func joinShape(a, b []int64, ra, rb int) []int64 {
	switch {
	case ra > 0 && rb <= 0:
		return a
	case rb > 0 && ra <= 0:
		return b
	case ra == rb && ra > 0:
		if a == nil {
			return b
		}
		if b == nil {
			return a
		}
		out := make([]int64, len(a))
		for i := range a {
			out[i] = a[i]
			if out[i] < 0 {
				out[i] = b[i]
			}
		}
		return out
	}
	return nil
}

// conformable checks the shapes of two array operands and reports a
// non-conformance (F2018 10.1.5.2, 10.2.1.2: shapes shall conform).
func (c *checker) conformable(pos syntax.Pos, a, b TV, what string) {
	if a.Rank <= 0 || b.Rank <= 0 || a.Rank != b.Rank {
		return
	}
	if d, x, y, bad := conformDim(a.Shape, b.Shape); bad {
		c.errorf(pos, "Different shape for %s at (1) on dimension %d (%d and %d)", what, d, x, y)
	}
}

// ---- sections and subscripts (F2018 9.5.3) ----

// tripletExtent is the number of elements of lo:hi:stride (F2018 9.5.3.3.2)
// when its bounds are constant; lo and hi default to the bounds of the
// dimension of the array being sectioned.
func (c *checker) tripletExtent(t *syntax.Triplet, dim *Dim) int64 {
	lo, hi, stride := int64(0), int64(0), int64(1)
	var ok bool
	if t.Lo != nil {
		if lo, ok = c.constOf(t.Lo); !ok {
			return unknownExtent
		}
	} else if dim != nil && dim.LoKnown {
		lo = dim.Lo
	} else {
		return unknownExtent
	}
	if t.Hi != nil {
		if hi, ok = c.constOf(t.Hi); !ok {
			return unknownExtent
		}
	} else if dim != nil && dim.HiKnown && !dim.Assumed && !dim.Deferred {
		hi = dim.Hi
	} else {
		return unknownExtent
	}
	if t.Stride != nil {
		if stride, ok = c.constOf(t.Stride); !ok || stride == 0 {
			return unknownExtent
		}
	}
	if lo < -maxExtent || lo > maxExtent || hi < -maxExtent || hi > maxExtent || stride < -maxExtent || stride > maxExtent {
		return unknownExtent
	}
	n := (hi - lo + stride) / stride
	return max(n, 0)
}

// constOf is the integer constant value of a typed expression.
func (c *checker) constOf(e syntax.Expr) (int64, bool) {
	tv, ok := c.info.Types[e]
	if !ok || tv.Const == nil || tv.Const.Type.Basic != Integer {
		return 0, false
	}
	return tv.Const.I, true
}

// ---- array constructors (F2018 7.8) ----

// constructorExtent adds the elements of one item of an array constructor to n;
// a negative n means the extent is not known.
func (c *checker) constructorExtent(n int64, e syntax.Expr, tv TV) int64 {
	if n < 0 {
		return n
	}
	switch x := e.(type) {
	case *syntax.ImpliedDo:
		cnt, ok := c.impliedDoCount(x)
		if !ok {
			return unknownExtent
		}
		per := int64(0)
		for _, it := range x.Items {
			per = c.constructorExtent(per, it, c.info.Types[it])
			if per < 0 {
				return unknownExtent
			}
		}
		if cnt > 0 && per > maxExtent/cnt {
			return unknownExtent // the product would overflow: not a known extent
		}
		return n + cnt*per
	}
	switch {
	case tv.Rank == 0:
		return n + 1
	case tv.Rank > 0 && shapeKnown(tv.Shape):
		sz, _ := shapeSize(tv.Shape)
		if n+sz > maxExtent {
			return unknownExtent
		}
		return n + sz
	}
	return unknownExtent
}

// impliedDoCount is the iteration count of an implied DO with constant control
// expressions (F2018 7.8.4: max((m2-m1+m3)/m3, 0)).
func (c *checker) impliedDoCount(x *syntax.ImpliedDo) (int64, bool) {
	lo, ok1 := c.constOf(x.Lo)
	hi, ok2 := c.constOf(x.Hi)
	if !ok1 || !ok2 {
		return 0, false
	}
	step := int64(1)
	if x.Step != nil {
		var ok bool
		if step, ok = c.constOf(x.Step); !ok || step == 0 {
			return 0, false
		}
	}
	return max((hi-lo+step)/step, 0), true
}

// ---- intrinsic results (F2018 16.9) ----

// dimArg returns the DIM argument of a reduction (the keyword dim=, or the
// second positional integer argument) and whether it is present.
func dimArg(args []argTV) (tv TV, present bool) {
	for i, a := range args {
		if a.kw == "dim" || (a.kw == "" && i == 1 && a.tv.Type.Basic == Integer) {
			return a.tv, true
		}
	}
	return TV{}, false
}

// argAt returns the argument called kw or at position i.
func argAt(args []argTV, kw string, i int) (TV, bool) {
	for j, a := range args {
		if a.kw == kw || (a.kw == "" && j == i) {
			return a.tv, true
		}
	}
	return TV{}, false
}

// dropDim is shape s without dimension dim (1-based), or an unknown shape of
// rank-1 when dim is not a constant.
func dropDim(s []int64, rank int, dim TV) []int64 {
	if rank <= 1 {
		return nil
	}
	if dim.Const != nil && dim.Const.Type.Basic == Integer && s != nil && dim.Const.I >= 1 && int(dim.Const.I) <= len(s) {
		d := int(dim.Const.I) - 1
		out := make([]int64, 0, len(s)-1)
		out = append(out, s[:d]...)
		return append(out, s[d+1:]...)
	}
	return unknownShape(rank - 1)
}

// intrinsicShape sets the rank and shape of a transformational intrinsic
// result and checks the conformance rules of its arguments. It is called after
// intrinsicType, whose rank for these functions is unknown.
func (c *checker) intrinsicShape(pos syntax.Pos, name string, args []argTV, tv *TV) {
	arg := func(i int) TV {
		if i < len(args) {
			return args[i].tv
		}
		return unknownTV
	}
	a0 := arg(0)
	known := func(t TV) bool { return t.Rank != RankUnknown && !t.Type.Lenient() }
	switch name {
	case "shape":
		if known(a0) && a0.Rank >= 0 {
			tv.Rank, tv.Shape = 1, []int64{int64(a0.Rank)}
		}
	case "lbound", "ubound":
		if known(a0) && a0.Rank >= 0 {
			if _, has := argAt(args, "dim", 1); has {
				tv.Rank, tv.Shape = 0, nil
			} else {
				tv.Rank, tv.Shape = 1, []int64{int64(a0.Rank)}
			}
		}
	case "size":
		tv.Rank, tv.Shape = 0, nil
	case "sum", "product", "maxval", "minval", "iall", "iany", "iparity", "norm2", "parity", "all", "any", "count":
		if !known(a0) || a0.Rank < 0 {
			return
		}
		c.maskShape(pos, name, args, a0)
		if d, has := dimArg(args); has && a0.Rank > 0 {
			tv.Rank = a0.Rank - 1
			tv.Shape = dropDim(a0.Shape, a0.Rank, d)
		} else {
			tv.Rank, tv.Shape = 0, nil
		}
	case "maxloc", "minloc":
		if !known(a0) || a0.Rank < 0 {
			return
		}
		c.maskShape(pos, name, args, a0)
		if d, has := argAt(args, "dim", 1); has && d.Type.Basic == Integer && a0.Rank > 0 {
			tv.Rank = a0.Rank - 1
			tv.Shape = dropDim(a0.Shape, a0.Rank, d)
		} else {
			tv.Rank, tv.Shape = 1, []int64{int64(a0.Rank)}
		}
	case "transpose":
		if !known(a0) {
			return
		}
		if a0.Rank != 2 {
			c.errorf(pos, "'matrix' argument of 'transpose' intrinsic at (1) must be of rank 2")
			return
		}
		tv.Rank = 2
		if len(a0.Shape) == 2 {
			tv.Shape = []int64{a0.Shape[1], a0.Shape[0]}
		}
	case "matmul":
		c.matmulShape(pos, args, tv)
	case "dot_product":
		b := arg(1)
		if known(a0) && known(b) {
			if a0.Rank != 1 || b.Rank != 1 {
				c.errorf(pos, "Arguments of 'dot_product' intrinsic at (1) must be of rank 1")
				return
			}
			c.conformable(pos, a0, b, "arguments 'vector_a' and 'vector_b' of intrinsic 'dot_product'")
		}
		tv.Rank, tv.Shape = 0, nil
	case "spread":
		if !known(a0) || a0.Rank < 0 {
			return
		}
		tv.Rank = a0.Rank + 1
		tv.Shape = unknownShape(tv.Rank)
		d, okd := argAt(args, "dim", 1)
		n, okn := argAt(args, "ncopies", 2)
		if okd && okn && d.Const != nil && d.Const.Type.Basic == Integer {
			dim := int(d.Const.I)
			if dim >= 1 && dim <= tv.Rank {
				ext := unknownExtent
				if n.Const != nil && n.Const.Type.Basic == Integer {
					ext = max(n.Const.I, 0)
				}
				if a0.Shape != nil || a0.Rank == 0 {
					s := make([]int64, 0, tv.Rank)
					s = append(s, a0.Shape[:min(dim-1, len(a0.Shape))]...)
					s = append(s, ext)
					if dim-1 < len(a0.Shape) {
						s = append(s, a0.Shape[dim-1:]...)
					}
					if len(s) == tv.Rank {
						tv.Shape = s
					}
				}
			} else {
				c.errorf(pos, "'dim' argument of 'spread' intrinsic at (1) is out of range (%d)", dim)
			}
		}
	case "reshape":
		c.reshapeShape(pos, args, tv)
	case "pack":
		tv.Rank, tv.Shape = 1, []int64{unknownExtent}
	case "cshift", "eoshift":
		if known(a0) && a0.Rank >= 0 {
			tv.Rank, tv.Shape = a0.Rank, a0.Shape
		}
	case "merge": // elemental in its three arguments (F2018 16.9.134)
		tv.Rank = 0
		for _, a := range args {
			switch {
			case a.tv.Rank == RankUnknown:
				tv.Rank, tv.Shape = RankUnknown, nil
				return
			case a.tv.Rank > 0 && tv.Rank == 0:
				tv.Rank, tv.Shape = a.tv.Rank, a.tv.Shape
			case a.tv.Rank > 0 && a.tv.Rank == tv.Rank:
				tv.Shape = joinShape(tv.Shape, a.tv.Shape, tv.Rank, tv.Rank)
			}
		}
	}
}

// matmulShape is the shape of matmul(a, b) (F2018 16.9.140): rank 2 x rank 2
// gives [n, p], rank 2 x rank 1 gives [n], rank 1 x rank 2 gives [p]; the
// inner extents shall agree.
func (c *checker) matmulShape(pos syntax.Pos, args []argTV, tv *TV) {
	if len(args) < 2 {
		return
	}
	a, b := args[0].tv, args[1].tv
	if a.Rank == RankUnknown || b.Rank == RankUnknown || a.Type.Lenient() || b.Type.Lenient() {
		return
	}
	switch {
	case a.Rank == 2 && b.Rank == 2:
		tv.Rank = 2
	case a.Rank == 2 && b.Rank == 1, a.Rank == 1 && b.Rank == 2:
		tv.Rank = 1
	default:
		c.errorf(pos, "Argument 'matrix_a' of 'matmul' intrinsic at (1) must be of rank 1 or 2, and not both of rank 1 (ranks %d and %d)", a.Rank, b.Rank)
		return
	}
	var ax, bx int64 = unknownExtent, unknownExtent
	if len(a.Shape) == a.Rank {
		ax = a.Shape[a.Rank-1] // the last extent of a
	}
	if len(b.Shape) == b.Rank {
		bx = b.Shape[0] // the first extent of b
	}
	if ax >= 0 && bx >= 0 && ax != bx {
		c.errorf(pos, "Dimension of array B incorrect in MATMUL intrinsic at (1) (%d and %d)", ax, bx)
	}
	var s []int64
	if len(a.Shape) == a.Rank && len(b.Shape) == b.Rank {
		if a.Rank == 2 {
			s = append(s, a.Shape[0])
		}
		if b.Rank == 2 {
			s = append(s, b.Shape[1])
		}
	}
	tv.Shape = s
	if s == nil {
		tv.Shape = unknownShape(tv.Rank)
	}
}

// reshapeShape is the shape of reshape(source, shape, pad, order) (F2018
// 16.9.163): its rank is the extent of the rank-1 argument SHAPE, its extents
// the values of SHAPE when they are constant. Without PAD the source must have
// enough elements.
func (c *checker) reshapeShape(pos syntax.Pos, args []argTV, tv *TV) {
	src, ok1 := argAt(args, "source", 0)
	shp, ok2 := argAt(args, "shape", 1)
	if !ok1 || !ok2 || shp.Type.Lenient() || shp.Rank == RankUnknown {
		return
	}
	if shp.Rank != 1 {
		c.errorf(pos, "'shape' argument of 'reshape' intrinsic at (1) must be an array of rank 1")
		return
	}
	if len(shp.Shape) != 1 || shp.Shape[0] < 0 {
		return
	}
	rank := int(shp.Shape[0])
	if rank < 1 || rank > 15 {
		return
	}
	tv.Rank = rank
	tv.Shape = unknownShape(rank)
	if len(shp.Values) == rank {
		for _, v := range shp.Values {
			if v < 0 {
				c.errorf(pos, "'shape' argument of 'reshape' intrinsic at (1) has negative element (%d)", v)
				return
			}
		}
		copy(tv.Shape, shp.Values)
		total, known := shapeSize(tv.Shape)
		_, hasPad := argAt(args, "pad", 2)
		if known && !hasPad && src.Rank > 0 {
			if n, ok := shapeSize(src.Shape); ok && n < total {
				c.errorf(pos, "Without padding, there are not enough elements in the intrinsic RESHAPE source at (1) to match the shape (%d < %d)", n, total)
			}
		}
	}
}

// wholeDims are the declared bounds of a whole array symbol, when they describe
// every dimension.
func wholeDims(s *Symbol) []Dim {
	if s.Rank > 0 && len(s.Dims) == s.Rank {
		return s.Dims
	}
	return nil
}

// initShape checks the shape of the initialisation of an array against the
// declared shape (F2018 8.5.8, 10.2.1.2), and completes the bounds of an
// implied-shape named constant "a(*) = [...]" (F2018 8.5.8.6) from it.
func (c *checker) initShape(pos syntax.Pos, sym *Symbol, tv TV) {
	if sym.Rank <= 0 || tv.Rank != sym.Rank || len(sym.Dims) != sym.Rank {
		return
	}
	if sym.Kind == SymParam && tv.Shape != nil && len(tv.Shape) == sym.Rank {
		for i := range sym.Dims {
			if d := &sym.Dims[i]; d.Assumed && d.LoKnown && tv.Shape[i] >= 0 {
				d.Hi, d.HiKnown, d.Assumed = d.Lo+tv.Shape[i]-1, true, false
			}
		}
	}
	c.conformable(pos, TV{Rank: sym.Rank, Shape: shapeOfDims(sym.Dims, sym.Rank)}, tv, "array assignment")
}

// deferredDims reports whether an array has a dimension that is not explicit:
// deferred or assumed-shape or assumed-size.
func deferredDims(dims []Dim) bool {
	for _, d := range dims {
		if d.Deferred || d.AssumedShape || d.Assumed {
			return true
		}
	}
	return false
}

// maskShape checks the MASK argument of a reduction against the array (F2018
// 16.9.172, 16.9.196: MASK shall be conformable with ARRAY).
func (c *checker) maskShape(pos syntax.Pos, name string, args []argTV, arr TV) {
	for i, a := range args {
		if i == 0 || !(a.kw == "mask" || a.kw == "" && a.tv.Type.Basic == Logical) || a.tv.Rank <= 0 || arr.Rank <= 0 {
			continue
		}
		if a.tv.Rank != arr.Rank {
			c.errorf(pos, "Incompatible ranks %d and %d in the 'mask' argument of intrinsic '%s' at (1)", arr.Rank, a.tv.Rank, name)
		} else {
			c.conformable(pos, arr, a.tv, "the 'array' and 'mask' arguments of intrinsic '"+name+"'")
		}
		return
	}
}
