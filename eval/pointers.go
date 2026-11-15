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
// refused with a positioned error: character and derived-type pointers,
// pointer components, pointer dummy arguments and results, a pointer to a
// section, bounds remapping, a pointer as a DO variable. Scalar pointers are
// below.

// Scalar pointers (part 2). A scalar pointer of integer, real or logical keeps
// its target in frame.ptrs[pslot]: an sref, which names a scalar variable
// (slot s of frame f) or an element of a general array (offset off of g), or
// nil when the pointer is disassociated. Reads and assignments go through it
// (ptrRead, ptrStore); the pointer's own scalar slot is -1, so a path that does
// not know pointers fails instead of reading another variable. ALLOCATE(p)
// makes a one-slot frame of its own, charged to the heap.

// sref is the target of a scalar pointer.
type sref struct {
	f      *frame // a scalar variable: slot s of frame f (f.reals when rl)
	s      int
	rl     bool
	g      *garr // or an element: offset off of g
	off    int
	palloc bool  // ALLOCATE made it through a pointer (the only target DEALLOCATE may free)
	freed  bool  // DEALLOCATE freed it, through this pointer or another one
	chg    int64 // what ALLOCATE charged the heap for it
}

// Which targets outlive their frame. A pointer held in a frame that outlives
// the frame of its target keeps the target after that frame returns (Go keeps
// it; Fortran calls such a pointer undefined, and nothing here reads it as
// anything but what it was). The live frames are a stack, so the pointer's
// frame outlives the target's when it is less deep: a module's frame, the
// main program's, a caller's, a host's. A function's result goes to its
// caller, so a result pointer counts one level up. A pointer in the target's
// own frame, or deeper, dies first and keeps nothing.
//
//   - an array target that escapes is marked aliased when it is associated,
//     and when its frame returns its charge moves to the heap (memory.go
//     exit) instead of being credited: it stays counted;
//   - a scalar target that escapes is noted in its frame (escaped), and when
//     the frame returns each such target moves into a box of its own holding
//     its value, so the pointer keeps 8 bytes and never the frame (with its
//     arrays). A box is not charged: the boxes alive are at most the scalar
//     pointers alive, and each of those is in a frame that is.

// escapes reports that a pointer held in holder (a function's result when
// result) outlives own, the frame its target lives in.
func escapes(holder *frame, result bool, own *frame) bool {
	if holder == nil || own == nil {
		return false
	}
	d := holder.depth
	if result {
		d--
	}
	return d < own.depth
}

// escapeArray notes that g is held by a pointer that outlives g's frame.
func escapeArray(f *frame, g *garr) {
	if g != nil && !g.aliased {
		g.aliased = true
		if f.bud != nil {
			f.bud.aliased = true
		}
	}
}

// escapeScalar notes that r, a scalar variable of its frame, is held by a
// pointer that outlives that frame, and returns the one sref that stands for
// that variable from now on: the frame keeps one per variable, so that a loop
// of associations grows nothing.
func escapeScalar(r *sref) *sref {
	t := r.f
	for _, x := range t.escaped {
		if x.s == r.s && x.rl == r.rl {
			return x
		}
	}
	// at most one per scalar variable of the frame: bounded by its declarations
	t.escaped = append(t.escaped, r)
	return r
}

// boxEscaped moves the scalar targets of f that outlive it into boxes of their
// own, as f returns.
func boxEscaped(f *frame) {
	for _, r := range f.escaped {
		if r.rl {
			r.f = &frame{reals: []float64{f.reals[r.s]}}
		} else {
			r.f = &frame{ints: []int64{f.ints[r.s]}}
		}
		r.s = 0
	}
	f.escaped = nil
}

// escapeTarget applies the rules above to r, the target a pointer held in
// holder is being associated with, and returns what the pointer must hold.
func escapeTarget(f, holder *frame, result bool, r *sref) *sref {
	switch {
	case r == nil:
	case r.g != nil:
		if escapes(holder, result, r.g.own) {
			escapeArray(f, r.g)
		}
	case escapes(holder, result, r.f):
		return escapeScalar(r)
	}
	return r
}

// same reports two pointers associated with one target.
func (r *sref) same(o *sref) bool {
	return r != nil && o != nil && r.f == o.f && r.s == o.s && r.g == o.g && r.off == o.off
}

func (r *sref) getInt() int64 {
	if r.g != nil {
		return r.g.i[r.off]
	}
	return r.f.ints[r.s]
}

func (r *sref) getReal() float64 {
	if r.g != nil {
		return r.g.r[r.off]
	}
	return r.f.reals[r.s]
}

func (r *sref) setInt(v int64) {
	if r.g != nil {
		r.g.i[r.off] = v
		return
	}
	r.f.ints[r.s] = v
}

func (r *sref) setReal(v float64) {
	if r.g != nil {
		r.g.r[r.off] = v
		return
	}
	r.f.reals[r.s] = v
}

// scalarPtr is the slot of the scalar pointer an expression names, or ok false
// when it does not name one.
func (c *compiler) scalarPtr(at syntax.Node, e syntax.Expr) (acc func(*frame) **sref, s *sym, ok bool, err error) {
	id, isID := e.(*syntax.Ident)
	if !isID {
		return nil, nil, false, nil
	}
	s, _, err = c.symOf(id)
	if err != nil || !s.ptr || s.ga {
		return nil, s, false, nil
	}
	acc, err = c.ptrSlot(s, at)
	return acc, s, err == nil, err
}

// ptrSlot is the slot of the scalar pointer s, in the frame that holds it.
func (c *compiler) ptrSlot(s *sym, at syntax.Node) (func(*frame) **sref, error) {
	h, err := c.hops(s, at)
	if err != nil {
		return nil, err
	}
	ps := s.pslot
	return func(f *frame) **sref { return &f.at(h).ptrs[ps] }, nil
}

// assoc is the target of an associated scalar pointer, or a failure.
func assoc(f *frame, acc func(*frame) **sref, name string) *sref {
	r := *acc(f)
	if r == nil {
		fail(f, "pointer %s is not associated", name)
	}
	return r
}

// ptrRead is the value of a scalar pointer: its target's.
func (c *compiler) ptrRead(n *syntax.Ident, s *sym) (*ex, error) {
	acc, _, ok, err := c.scalarPtr(n, n)
	if err != nil || !ok {
		return nil, c.errf(n, "internal error: %s is not a scalar pointer", n.Name)
	}
	name := n.Name
	switch s.k {
	case kInt:
		return &ex{k: kInt, kd: s.kd, slot: -1, i: func(f *frame) int64 { return assoc(f, acc, name).getInt() }}, nil
	case kLog:
		return &ex{k: kLog, slot: -1, b: func(f *frame) bool { return assoc(f, acc, name).getInt() != 0 }}, nil
	case kReal:
		return &ex{k: kReal, kd: s.kd, slot: -1, r: func(f *frame) float64 { return assoc(f, acc, name).getReal() }}, nil
	}
	return nil, c.errf(n, "a pointer of this type is not supported yet")
}

// ptrStore is "p = value" for a scalar pointer: an assignment to its target,
// with the conversions of an assignment to the variable itself (store).
func (c *compiler) ptrStore(s *sym, v *ex, at syntax.Node) (stmtFn, error) {
	name := "the pointer"
	if a, ok := at.(*syntax.Assignment); ok {
		if x, ok := a.LHS.(*syntax.Ident); ok {
			name = x.Name
		}
	}
	acc, err := c.ptrSlot(s, at)
	if err != nil {
		return nil, err
	}
	kd := s.kd
	switch {
	case s.k == kInt && v.k == kInt:
		fn := v.i
		return func(f *frame) { x := fn(f); assoc(f, acc, name).setInt(narrow(kd, x)) }, nil
	case s.k == kInt && v.k == kReal:
		fn := v.r
		return func(f *frame) { x := fn(f); assoc(f, acc, name).setInt(narrow(kd, int64(x))) }, nil
	case s.k == kLog && v.k == kLog:
		fn := v.b
		return func(f *frame) { x := fn(f); assoc(f, acc, name).setInt(b2i(x)) }, nil
	case s.k == kReal && (v.k == kReal || v.k == kInt):
		fn := v.asReal().r
		return func(f *frame) {
			x := fn(f)
			if kd == 4 {
				x = float64(float32(x))
			}
			assoc(f, acc, name).setReal(x)
		}, nil
	}
	return nil, c.errf(at, "this assignment through a pointer is not supported")
}

// scalarTarget compiles what a scalar pointer of sym p may be associated with:
// a scalar variable of its type and kind, another scalar pointer (its target),
// or an element of a general array.
func (c *compiler) scalarTarget(at syntax.Node, p *sym, e syntax.Expr) (func(*frame) *sref, error) {
	if acc, t, ok, err := c.scalarPtr(at, e); err != nil {
		return nil, err
	} else if ok {
		if t.k != p.k || t.kd != p.kd {
			return nil, c.errf(at, "pointer association between different types or kinds")
		}
		return func(f *frame) *sref { return *acc(f) }, nil
	}
	switch x := e.(type) {
	case *syntax.Ident:
		t, _, err := c.symOf(x)
		if err != nil {
			return nil, err
		}
		if t.isC || t.ga || t.slot < 0 || (t.k != kInt && t.k != kReal && t.k != kLog) || t.k != p.k || t.kd != p.kd {
			return nil, c.errf(at, "pointer association with %s: only a scalar variable of the pointer's type and kind is supported yet", x.Name)
		}
		h, err := c.hops(t, at)
		if err != nil {
			return nil, err
		}
		slot, rl := t.slot, t.k == kReal
		return func(f *frame) *sref { return &sref{f: f.at(h), s: slot, rl: rl} }, nil
	case *syntax.Index:
		if c.isFuncRef(x) {
			// p => f(): the target of f's pointer result
			v, err := c.expr(x)
			if err != nil {
				return nil, err
			}
			if v.ps == nil || v.k != p.k || v.kd != p.kd {
				return nil, c.errf(at, "pointer association with a function that does not return a pointer of the pointer's type and kind")
			}
			return v.ps, nil
		}
		ref, ix, err := c.lhsRef(at, x)
		if err != nil {
			return nil, err
		}
		if ix == nil || ref.et != p.k || ref.kd != p.kd {
			return nil, c.errf(at, "pointer association with an array element of another type or kind")
		}
		ss, err := c.subs(ix, ref.rank)
		if err != nil {
			return nil, err
		}
		if !allScalar(ss) {
			return nil, c.errf(at, "a scalar pointer is associated with an element, not a section")
		}
		name := ref.name
		return func(f *frame) *sref {
			g := ref.get(f)
			offs, _ := resolve(f, g, name, ss)
			// the pointer holds the whole array: whether that outlives the
			// array's frame is decided by the association (escapeTarget)
			return &sref{g: g, off: offs[0]}
		}, nil
	}
	return nil, c.errf(at, "pointer association with this target is not supported yet")
}

// scalarPtrAssign is p => target and p => null() for a scalar pointer.
func (c *compiler) scalarPtrAssign(s *syntax.PointerAssign, acc func(*frame) **sref, p *sym) (stmtFn, error) {
	if isNullCall(s.RHS) {
		return func(f *frame) { *acc(f) = nil }, nil
	}
	tgt, err := c.scalarTarget(s, p, s.RHS)
	if err != nil {
		return nil, err
	}
	holder, err := c.holderOf(p, s)
	if err != nil {
		return nil, err
	}
	result := c.isResult(p)
	return func(f *frame) { *acc(f) = escapeTarget(f, holder(f), result, tgt(f)) }, nil
}

// holderOf is the frame that holds the variable s.
func (c *compiler) holderOf(s *sym, at syntax.Node) (func(*frame) *frame, error) {
	h, err := c.hops(s, at)
	if err != nil {
		return nil, err
	}
	return func(f *frame) *frame { return f.at(h) }, nil
}

// isResult reports that s is the result variable of the function being
// compiled: what it holds goes to the caller.
func (c *compiler) isResult(s *sym) bool {
	return c.cur != nil && c.cur.result == s
}

// allocScalarPtr is ALLOCATE(p) for a scalar pointer: a one-slot frame of its
// own, charged to the heap; and DEALLOCATE(p).
func allocScalarPtr(acc func(*frame) **sref, k kind, name string, dealloc bool) stmtFn {
	const cost = 8 + objectOverhead
	if dealloc {
		return func(f *frame) {
			r := *acc(f)
			if r == nil {
				fail(f, "deallocate: pointer %s is not associated", name)
			}
			if r.freed {
				fail(f, "deallocate: the target of pointer %s was already deallocated", name)
			}
			if !r.palloc {
				fail(f, "deallocate: pointer %s is associated with a target that ALLOCATE did not make through a pointer", name)
			}
			if h := f.bud.heapFrame(); h != nil && r.chg > 0 {
				f.bud.credit(f, h, r.chg)
			}
			r.palloc, r.freed, r.chg = false, true, 0
			*acc(f) = nil
		}
	}
	return func(f *frame) {
		h := f.bud.heapFrame()
		var chg int64
		if h != nil {
			f.bud.charge(f, h, cost)
			chg = cost
		}
		t := &frame{}
		if k == kReal {
			t.reals = []float64{0}
		} else {
			t.ints = []int64{0}
		}
		*acc(f) = &sref{f: t, palloc: true, chg: chg}
	}
}

// isFuncRef reports a reference f(...) whose name is not a variable of the
// program: a function reference (an array element's name is a variable).
func (c *compiler) isFuncRef(e syntax.Expr) bool {
	ix, ok := e.(*syntax.Index)
	if !ok {
		return false
	}
	id, ok := ix.X.(*syntax.Ident)
	if !ok {
		return false
	}
	_, _, err := c.symOf(id)
	return err != nil
}

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
		if g.freed {
			fail(f, "deallocate: the target of pointer %s was already deallocated", name)
		}
		if !g.palloc {
			fail(f, "deallocate: pointer %s is associated with a target that ALLOCATE did not make through a pointer", name)
		}
		f.bud.release(f, g)
		g.palloc, g.freed = false, true
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
	if acc, p, ok, err := c.scalarPtr(s, s.LHS); err != nil {
		return nil, err
	} else if ok {
		return c.scalarPtrAssign(s, acc, p)
	}
	ref, err := c.pointerRef(s, s.LHS, "pointer assignment")
	if err != nil {
		return nil, err
	}
	if isNullCall(s.RHS) {
		return func(f *frame) { *ref.ptr(f) = nil }, nil
	}
	ps, _, err := c.symOf(s.LHS.(*syntax.Ident))
	if err != nil {
		return nil, err
	}
	result := c.isResult(ps)
	// assign associates the pointer with g, which stays counted after its
	// frame returns when the pointer outlives that frame (escapes)
	assign := func(f *frame, g *garr) {
		if g != nil && escapes(ref.own(f), result, g.own) {
			escapeArray(f, g)
		}
		*ref.ptr(f) = g
	}
	if c.isFuncRef(s.RHS) {
		// p => f(): the target of f's pointer result
		x, err := c.expr(s.RHS)
		if err != nil {
			return nil, err
		}
		if x.pg == nil {
			return nil, c.errf(s, "pointer assignment: the function does not return a pointer")
		}
		if x.et != ref.et || x.kd != ref.kd || x.rank != ref.rank {
			return nil, c.errf(s, "pointer assignment: %s and the function's result differ in type, kind or rank", ref.name)
		}
		pg := x.pg
		return func(f *frame) { assign(f, pg(f)) }, nil
	}
	if _, ok := s.RHS.(*syntax.Ident); !ok {
		return nil, c.errf(s, "pointer assignment: only a whole array variable or a function's pointer result is supported yet as the target (not a section or an expression)")
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
	return func(f *frame) { assign(f, *tref.ptr(f)) }, nil
}

// nullify compiles NULLIFY(p, ...).
func (c *compiler) nullify(s *syntax.Nullify) (stmtFn, error) {
	var refs []*gref
	var accs []func(*frame) **sref
	for _, o := range s.Objects {
		if acc, _, ok, err := c.scalarPtr(s, o); err != nil {
			return nil, err
		} else if ok {
			accs = append(accs, acc)
			continue
		}
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
		for _, a := range accs {
			*a(f) = nil
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
	if acc, p, ok, err := c.scalarPtr(n, pe); err != nil {
		return nil, err
	} else if ok {
		if te == nil {
			return &ex{k: kLog, slot: -1, b: func(f *frame) bool { return *acc(f) != nil }}, nil
		}
		tgt, err := c.scalarTarget(n, p, te)
		if err != nil {
			return nil, err
		}
		return &ex{k: kLog, slot: -1, b: func(f *frame) bool { return (*acc(f)).same(tgt(f)) }}, nil
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
