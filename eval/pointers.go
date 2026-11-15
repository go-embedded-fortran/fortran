package eval

import (
	"github.com/go-embedded-fortran/fortran/syntax"
)

// Array pointers (F2018 8.5.14, 10.2.2, 9.7, 16.9.16), part 1.
//
// A pointer array of integer, real or logical is a slot of frame.gas, like an
// allocatable array, that holds its target's storage (*garr) or nil when it is
// disassociated. So:
//
//   - p => a copies a's *garr into p's slot: p and a are one array, and an
//     assignment through either is seen through the other;
//   - p => null() and NULLIFY(p) set the slot to nil;
//   - ALLOCATE(p(n)) makes a new target; it outlives p's frame (another
//     pointer may hold it), so it is charged to the heap (budget.heap), which
//     only DEALLOCATE credits; a pointer allocated again leaves its old target
//     behind, as Fortran does;
//   - DEALLOCATE(p) frees only a target ALLOCATE made through a pointer
//     (F2018 9.7.3.3) and disassociates p; other pointers to it are left
//     undefined, as in Fortran;
//   - ASSOCIATED(p [, t]) compares the slots.
//
// A pointer never owns what it points at: the exit of its frame credits
// nothing for it (its frame was charged nothing for it). Not here yet, and
// refused with a positioned error: scalar, character and derived-type
// pointers, pointer components, pointer dummy arguments and results, a
// pointer to a section, bounds remapping.

// isNullCall reports the reference null() (no MOLD).
func isNullCall(e syntax.Expr) bool {
	ix, ok := e.(*syntax.Index)
	if !ok || len(ix.Args) != 0 {
		return false
	}
	id, ok := ix.X.(*syntax.Ident)
	return ok && id.Name == "null"
}

// heapFrame is the owner of the targets ALLOCATE makes for pointers (nil
// without a budget: nothing is counted then).
func (b *budget) heapFrame() *frame {
	if b == nil {
		return nil
	}
	return b.heap
}

// deallocPointer is DEALLOCATE(p) for a pointer array.
func deallocPointer(ref *gref) stmtFn {
	name := ref.name
	return func(f *frame) {
		g := *ref.ptr(f)
		if g == nil {
			fail(f, "deallocate: pointer %s is not associated", name)
		}
		if !g.palloc {
			fail(f, "deallocate: pointer %s is associated with a target that ALLOCATE did not make through a pointer", name)
		}
		f.bud.release(f, g)
		g.palloc = false
		*ref.ptr(f) = nil
	}
}

// pointerRef is the pointer array an object of a pointer statement names: a
// whole variable, no subscripts.
func (c *compiler) pointerRef(at syntax.Node, e syntax.Expr, what string) (*gref, error) {
	if _, ok := e.(*syntax.Ident); !ok {
		return nil, c.errf(at, "%s: only a pointer array variable is supported yet (not a component, a section or a bounds remapping)", what)
	}
	ref, ix, err := c.lhsRef(at, e)
	if err != nil {
		return nil, err
	}
	if ix != nil || !ref.isPtr {
		return nil, c.errf(at, "%s: %s is not a pointer array of integer, real or logical", what, ref.name)
	}
	return ref, nil
}

// pointerAssign compiles p => target and p => null().
func (c *compiler) pointerAssign(s *syntax.PointerAssign) (stmtFn, error) {
	ref, err := c.pointerRef(s, s.LHS, "pointer assignment")
	if err != nil {
		return nil, err
	}
	if isNullCall(s.RHS) {
		return func(f *frame) { *ref.ptr(f) = nil }, nil
	}
	if _, ok := s.RHS.(*syntax.Ident); !ok {
		return nil, c.errf(s, "pointer assignment: only a whole array variable is supported yet as the target (not a section or an expression)")
	}
	tref, ix, err := c.lhsRef(s, s.RHS)
	if err != nil {
		return nil, err
	}
	if ix != nil {
		return nil, c.errf(s, "pointer assignment to a section is not supported yet")
	}
	if tref.et != ref.et || tref.kd != ref.kd || tref.rank != ref.rank {
		return nil, c.errf(s, "pointer assignment: %s and %s differ in type, kind or rank", ref.name, tref.name)
	}
	return func(f *frame) { *ref.ptr(f) = *tref.ptr(f) }, nil
}

// nullify compiles NULLIFY(p, ...).
func (c *compiler) nullify(s *syntax.Nullify) (stmtFn, error) {
	var refs []*gref
	for _, o := range s.Objects {
		ref, err := c.pointerRef(s, o, "nullify")
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return func(f *frame) {
		for _, r := range refs {
			*r.ptr(f) = nil
		}
	}, nil
}

// associatedGA compiles ASSOCIATED(pointer [, target]): whether the pointer is
// associated, and with the target's storage when a target is given (a pointer
// target: with what it is associated with; a disassociated one is never).
func (c *compiler) associatedGA(n *syntax.Index) (*ex, error) {
	var pe, te syntax.Expr
	for i, a := range n.Args {
		switch {
		case a.Value == nil:
			return nil, c.errf(n, "associated: an alternate return")
		case a.Keyword == "pointer" || a.Keyword == "" && i == 0:
			pe = a.Value
		case a.Keyword == "target" || a.Keyword == "" && i == 1:
			te = a.Value
		default:
			return nil, c.errf(n, "associated: unexpected argument")
		}
	}
	if pe == nil {
		return nil, c.errf(n, "associated: the pointer argument is missing")
	}
	ref, err := c.pointerRef(n, pe, "associated")
	if err != nil {
		return nil, err
	}
	if te == nil {
		return &ex{k: kLog, slot: -1, b: func(f *frame) bool { return *ref.ptr(f) != nil }}, nil
	}
	if _, ok := te.(*syntax.Ident); !ok {
		return nil, c.errf(n, "associated: only a whole array variable is supported yet as the target")
	}
	tref, ix, err := c.lhsRef(n, te)
	if err != nil {
		return nil, err
	}
	if ix != nil {
		return nil, c.errf(n, "associated with a section is not supported yet")
	}
	return &ex{k: kLog, slot: -1, b: func(f *frame) bool {
		p := *ref.ptr(f)
		return p != nil && p == *tref.ptr(f)
	}}, nil
}
