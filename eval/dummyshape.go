package eval

import (
	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// explicitShapeDummy gives an explicit-shape dummy array its declared bounds.
// The actual argument is associated as a sequence of elements (sequence
// association): on entry the dummy becomes a view of the first elements of the
// actual's storage, in array element order, with the declared bounds. The
// bounds may depend on the other dummies (dependent_decls_3 of gfortran.dg:
// w(size(v,dim=1)/4, size(v,dim=2)/2)), so they are evaluated in the callee's
// frame, after the arguments are bound. A dummy whose spec is not in its type
// declaration and whose bounds are not constant is refused.
//
// An assumed-shape (:) or assumed-size (*) dummy keeps the actual's shape.
func (c *compiler) explicitShapeDummy(ss *sema.Symbol, e syntax.Node, s *sym) error {
	for _, dm := range ss.Dims {
		if dm.Deferred || dm.AssumedShape || dm.Assumed {
			return nil
		}
	}
	rank := ss.Rank
	los, his := make([]intFn, rank), make([]intFn, rank)
	for d, dm := range ss.Dims {
		switch {
		case dm.HiKnown && (dm.LoKnown || dm.Lo == 0):
			lo, hi := int64(1), dm.Hi
			if dm.LoKnown {
				lo = dm.Lo
			}
			los[d] = func(*frame) int64 { return lo }
			his[d] = func(*frame) int64 { return hi }
		case len(c.dimSyntax) == rank && c.dimSyntax[d].Hi != nil && !c.dimSyntax[d].Star:
			sd := c.dimSyntax[d]
			hi, err := c.intExpr(sd.Hi)
			if err != nil {
				return err
			}
			his[d] = hi
			los[d] = func(*frame) int64 { return 1 }
			if sd.Lo != nil {
				lo, err := c.intExpr(sd.Lo)
				if err != nil {
					return err
				}
				los[d] = lo
			}
		default:
			return c.errf(e, "dummy array %s: an explicit shape with bounds that are not constant needs them in its type declaration", ss.Name)
		}
	}
	slot, name := s.slot, ss.Name
	c.cur.init = append(c.cur.init, func(f *frame) {
		g := f.gas[slot]
		if g == nil {
			fail(f, "dummy array %s has no actual argument", name)
		}
		lb, ext := make([]int64, rank), make([]int, rank)
		n := 1
		for d := range rank {
			lb[d] = los[d](f)
			x := his[d](f) - lb[d] + 1
			if x < 0 {
				x = 0
			}
			if x > int64(MaxArrayElems) || x > 0 && n > MaxArrayElems/int(x) {
				fail(f, "dummy array %s: extent out of range (more than %d elements)", name, MaxArrayElems)
			}
			ext[d] = int(x)
			n *= ext[d]
		}
		if n > g.size() {
			fail(f, "dummy array %s has %d elements, more than the %d of its actual argument", name, n, g.size())
		}
		v := &garr{et: g.et, kd: g.kd, lb: lb, ext: ext}
		if g.r != nil {
			v.r = g.r[:n:n]
		}
		if g.i != nil {
			v.i = g.i[:n:n]
		}
		f.gas[slot] = v
	})
	return nil
}
