package eval

import (
	"strings"

	"github.com/go-embedded-fortran/fortran/rt"
	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// Derived types (F2018 7.5): scalar objects and rank-1 arrays of them, with
// scalar integer, real, logical and character components, array components
// (fixed shape or allocatable), components of derived type, default
// initialisation, structure constructors, type-bound procedures (resolved
// here: the analyser does not resolve a type-bound reference) and CLASS
// dummy arguments treated as their declared type (no extension, no dynamic
// dispatch).

// MaxDerivedDepth bounds the nesting of derived-type components.
const MaxDerivedDepth = 32

// cv is the value of one component.
type cv struct {
	i int64
	r float64
	s string
	g *garr
	d *dobj
}

type dcomp struct {
	name  string
	k     kind // kInt, kReal, kLog, kStr, kD or kGA
	kd    int
	ksz   int  // the kind parameter for output (4, 8, ...)
	et    kind // the class of the elements of an array component
	rank  int
	alloc bool
	clen  int64
	dt    *dtype
	lb    []int64 // a fixed-shape array component
	ext   []int
	def   func(*frame) cv
}

type dtype struct {
	name  string
	comps []*dcomp
	idx   map[string]int
	sd    *sema.DerivedType
}

type dobj struct {
	t *dtype
	v []cv
	// own is the frame the object's storage is charged to: the one that holds
	// the variable (a module's, for a module variable), not the one that runs a
	// statement on it; nil for a temporary (see memory.go).
	own *frame
}

func (c *compiler) typeDefs() map[string]*syntax.TypeDef {
	if c.typedefs != nil && c.tdFile == c.file {
		return c.typedefs
	}
	// a session compiles one file per cell: the definitions of the earlier
	// cells stay, a later one of the same name replaces them
	c.tdFile = c.file
	if c.typedefs == nil {
		c.typedefs = map[string]*syntax.TypeDef{}
	}
	var walk func(u *syntax.Unit)
	walk = func(u *syntax.Unit) {
		for _, s := range u.Body {
			if td, ok := s.(*syntax.TypeDef); ok {
				c.typedefs[td.Ident.Name] = td
			}
		}
		for _, cu := range u.Contains {
			walk(cu)
		}
	}
	for _, u := range c.file.Units {
		walk(u)
	}
	return c.typedefs
}

// implicitBindings refuses the bindings of a type that act without being
// named: FINAL (F2018 7.5.6), and a generic binding for defined assignment, a
// defined operator or derived-type I/O (7.5.5). The evaluator does not run
// them, and ignoring them would give wrong results silently: an object never
// finalized, an assignment or a PRINT that is the intrinsic one.
// A specific binding (PROCEDURE) is only run when named, and naming it is
// refused where it is used.
func (c *compiler) implicitBindings(td *syntax.TypeDef) error {
	for _, st := range td.Bound {
		b, ok := st.(*syntax.ProcBinding)
		if !ok {
			continue
		}
		switch b.Kind {
		case "final":
			return c.errf(b, "type %s: final procedures are not supported yet (its objects would not be finalized)", td.Ident.Name)
		case "generic":
			for _, it := range b.Items {
				n := strings.ToLower(it.Name)
				if strings.HasPrefix(n, "assignment") || strings.HasPrefix(n, "operator") || strings.HasPrefix(n, "read") || strings.HasPrefix(n, "write") {
					return c.errf(b, "type %s: a type-bound %s is not supported yet (it would not be used)", td.Ident.Name, it.Name)
				}
			}
		}
	}
	return nil
}

// dtypeOf builds the evaluator's layout of a derived type.
func (c *compiler) dtypeOf(at syntax.Node, sd *sema.DerivedType) (*dtype, error) {
	if dt, ok := c.dtypes[sd]; ok {
		if dt == nil {
			return nil, c.errf(at, "type %s contains itself", sd.Name)
		}
		return dt, nil
	}
	if c.dtypes == nil {
		c.dtypes = map[*sema.DerivedType]*dtype{}
	}
	if len(c.dtypes) > MaxDerivedDepth*MaxDerivedDepth {
		return nil, c.errf(at, "too many derived types")
	}
	c.dtypes[sd] = nil // under construction
	if sd.Parent != nil || sd.Forward {
		return nil, c.errf(at, "type %s: type extension and forward references are not supported yet", sd.Name)
	}
	td := c.typeDefs()[sd.Name]
	if td == nil {
		return nil, c.errf(at, "the definition of type %s is not in this file", sd.Name)
	}
	if len(td.Params) > 0 {
		return nil, c.errf(td, "parameterised derived types are not supported")
	}
	if err := c.implicitBindings(td); err != nil {
		return nil, err
	}
	dt := &dtype{name: sd.Name, idx: map[string]int{}, sd: sd}
	inits := map[string]syntax.Expr{}
	for _, st := range td.Body {
		if d, ok := st.(*syntax.Decl); ok {
			for i := range d.Entities {
				if d.Entities[i].Init != nil {
					inits[d.Entities[i].Name.Name] = d.Entities[i].Init
				}
			}
		}
	}
	for _, sc := range sd.Components {
		if sc.Pointer {
			return nil, c.errf(td, "pointer component %s is not supported yet", sc.Name)
		}
		dc := &dcomp{name: sc.Name, ksz: sc.Type.Kind, kd: kdOf(sc.Type), rank: sc.Rank, alloc: sc.Alloc}
		if sc.Type.Basic == sema.Derived {
			if sc.Type.Der == nil {
				return nil, c.errf(td, "component %s has an unknown type", sc.Name)
			}
			sub, err := c.dtypeOf(td, sc.Type.Der)
			if err != nil {
				return nil, err
			}
			dc.dt, dc.k = sub, kD
			if sc.Rank != 0 {
				return nil, c.errf(td, "array components of derived type are not supported yet")
			}
		} else {
			k, ok := classOf(sc.Type)
			switch {
			case ok:
				dc.k, dc.et = k, k
			case sc.Type.Basic == sema.Character && sc.Type.Kind == 1 && sc.Type.Len >= 0 && sc.Rank == 0:
				dc.k, dc.clen = kStr, sc.Type.Len
			default:
				return nil, c.errf(td, "component %s of type %s is not supported", sc.Name, sc.Type)
			}
		}
		if sc.Rank != 0 {
			if sc.Rank > MaxRank || dc.k == kStr || dc.k == kD {
				return nil, c.errf(td, "array component %s is not supported", sc.Name)
			}
			dc.et, dc.k = dc.k, kGA
			if !sc.Alloc {
				for _, dm := range sc.Dims {
					if !dm.HiKnown || dm.Deferred || dm.Assumed {
						return nil, c.errf(td, "array component %s needs constant bounds or allocatable", sc.Name)
					}
					lo := int64(1)
					if dm.LoKnown {
						lo = dm.Lo
					}
					dc.lb = append(dc.lb, lo)
					dc.ext = append(dc.ext, int(max(0, dm.Hi-lo+1)))
				}
			}
		}
		if e := inits[sc.Name]; e != nil {
			def, err := c.compDefault(dc, e)
			if err != nil {
				return nil, err
			}
			dc.def = def
		}
		dt.idx[dc.name] = len(dt.comps)
		dt.comps = append(dt.comps, dc)
	}
	c.dtypes[sd] = dt
	return dt, nil
}

// compDefault compiles the default initialisation of a scalar component.
func (c *compiler) compDefault(dc *dcomp, e syntax.Expr) (func(*frame) cv, error) {
	x, err := c.expr(e)
	if err != nil {
		return nil, err
	}
	if dc.k == kGA && !dc.alloc && x.k != kGA {
		// a scalar default fills a fixed-shape array component
		sv := scalarVal(x)
		if sv == nil {
			return nil, c.errf(e, "the default of component %s is not supported", dc.name)
		}
		return func(f *frame) cv {
			g := newGarr(f, dc.et, dc.kd, append([]int64(nil), dc.lb...), append([]int(nil), dc.ext...))
			a, b := sv(f)
			for k, n := 0, g.size(); k < n; k++ {
				g.put(k, a, b)
			}
			return cv{g: g}
		}, nil
	}
	switch {
	case dc.k == kInt && x.k == kInt:
		fn, kd := x.i, dc.kd
		return func(f *frame) cv { return cv{i: narrow(kd, fn(f))} }, nil
	case dc.k == kInt && x.k == kReal:
		fn, kd := x.r, dc.kd
		return func(f *frame) cv { return cv{i: narrow(kd, int64(fn(f)))} }, nil
	case dc.k == kReal && (x.k == kReal || x.k == kInt):
		fn, kd := x.asReal().r, dc.kd
		return func(f *frame) cv {
			v := fn(f)
			if kd == 4 {
				v = float64(float32(v))
			}
			return cv{r: v}
		}, nil
	case dc.k == kLog && x.k == kLog:
		fn := x.b
		return func(f *frame) cv { return cv{i: b2i(fn(f))} }, nil
	case dc.k == kStr && x.k == kStr:
		fn, n := x.s, dc.clen
		return func(f *frame) cv { return cv{s: fitLen(fn(f), n)} }, nil
	}
	return nil, c.errf(e, "the default of component %s is not supported", dc.name)
}

// newObj makes an object of type t. Its storage is charged to the frame own
// (a variable's), or only checked when own is nil (a temporary: the value of a
// structure constructor).
func (t *dtype) newObj(f *frame, own *frame) *dobj {
	f.bud.charge(f, own, t.objBytes()) // before anything of the object is allocated
	o := &dobj{t: t, v: make([]cv, len(t.comps)), own: own}
	for i, dc := range t.comps {
		switch dc.k {
		case kStr:
			o.v[i].s = strings.Repeat(" ", int(min(dc.clen, MaxStringLen)))
		case kD:
			o.v[i].d = dc.dt.newObj(f, own)
		case kGA:
			if !dc.alloc {
				o.v[i].g = f.bud.retain(f, own, newGarr(f, dc.et, dc.kd, append([]int64(nil), dc.lb...), append([]int(nil), dc.ext...)))
			}
		}
		if dc.def != nil {
			d := dc.def(f)
			o.v[i].i, o.v[i].r, o.v[i].s = d.i, d.r, d.s
			if dc.k != kStr {
				o.v[i].s = ""
			}
			if d.g != nil {
				f.bud.release(f, o.v[i].g)
				o.v[i].g = f.bud.retain(f, own, d.g)
			}
		}
	}
	return o
}

// becomeOut is what an INTENT(OUT) dummy argument does to the object it is
// associated with when the procedure is invoked: a component with default
// initialization is initialized (F2018 8.5.10), an allocated allocatable
// component is deallocated (9.7.3.2), and the rest becomes undefined, which
// leaves it as it was.
func (o *dobj) becomeOut(f *frame) {
	for i, dc := range o.t.comps {
		switch {
		case dc.k == kD:
			o.v[i].d.becomeOut(f)
		case dc.k == kGA && dc.alloc:
			f.bud.release(f, o.v[i].g)
			o.v[i].g = nil
		}
		if dc.def == nil {
			continue
		}
		d := dc.def(f)
		o.v[i].i, o.v[i].r = d.i, d.r
		if dc.k == kStr {
			o.v[i].s = d.s
		}
		if d.g != nil {
			f.bud.release(f, o.v[i].g)
			o.v[i].g = f.bud.retain(f, o.own, d.g)
		}
	}
}

// assign copies the value of src into o (intrinsic assignment of a derived
// type: component by component, allocatable arrays are copied).
func (o *dobj) assign(f *frame, src *dobj) {
	for i, dc := range o.t.comps {
		switch dc.k {
		case kD:
			o.v[i].d.assign(f, src.v[i].d)
		case kGA:
			f.bud.release(f, o.v[i].g)
			if src.v[i].g == nil {
				o.v[i].g = nil
			} else {
				f.bud.peek(f, garrBytes(src.v[i].g.size()))
				o.v[i].g = f.bud.retain(f, o.own, src.v[i].g.clone()) // charged to the destination's owner
			}
		default:
			o.v[i] = src.v[i]
		}
	}
}

// vals appends the output values of o (a list-directed or formatted WRITE of
// a derived-type object transfers its components in order).
func (o *dobj) vals(f *frame, l []rt.Val) []rt.Val {
	for i, dc := range o.t.comps {
		v := o.v[i]
		switch dc.k {
		case kInt:
			l = append(l, rt.Int(dc.ksz, v.i))
		case kLog:
			l = append(l, rt.Bool(dc.ksz, v.i != 0))
		case kReal:
			if dc.ksz == 4 {
				l = append(l, rt.Real4(float32(v.r)))
			} else {
				l = append(l, rt.Real8(v.r))
			}
		case kStr:
			l = append(l, rt.Str(v.s))
		case kD:
			l = v.d.vals(f, l)
		case kGA:
			if v.g != nil {
				l = printVals(f, l, v.g, dc.ksz)
			}
		}
	}
	return l
}

// ---- classes ----

// objExpr is a compiled derived-type scalar.
func (c *compiler) objIdent(id *syntax.Ident, s *sym) (*ex, error) {
	n, err := c.hops(s, id)
	if err != nil {
		return nil, err
	}
	slot := s.slot
	return &ex{k: kD, slot: -1, dt: s.dt, d: func(f *frame) *dobj { return f.hop(n).dobjs[slot] }}, nil
}

// declD gives a derived-type variable its storage.
func (c *compiler) declD(ss *sema.Symbol, e syntax.Node, s *sym, init syntax.Expr) error {
	dt, err := c.dtypeOf(e, ss.Type.Der)
	if err != nil {
		return err
	}
	if ss.Flags&(sema.FPointer|sema.FAllocatable) != 0 {
		return c.errf(e, "a pointer or allocatable derived-type variable is not supported yet")
	}
	s.dt = dt
	slot := 0
	if ss.Rank == 0 {
		s.k, slot = kD, c.cur.nDs
		c.cur.nDs++
		s.slot = slot
		if ss.Flags&sema.FDummy == 0 {
			c.cur.init = append(c.cur.init, func(f *frame) { f.dobjs[slot] = dt.newObj(f, f) })
		}
		if init != nil {
			x, err := c.expr(init)
			if err != nil {
				return err
			}
			if x.k != kD || x.dt != dt {
				return c.errf(e, "the initialisation is not a value of type %s", dt.name)
			}
			fn := x.d
			c.cur.init = append(c.cur.init, func(f *frame) { f.dobjs[slot].assign(f, fn(f)) })
		}
		c.syms[ss] = s
		return nil
	}
	if ss.Rank != 1 || ss.Flags&sema.FDummy != 0 || len(ss.Dims) != 1 {
		return c.errf(e, "only fixed-size rank-1 arrays of derived type are supported yet")
	}
	dm := ss.Dims[0]
	if !dm.HiKnown || dm.Deferred || dm.Assumed || init != nil {
		return c.errf(e, "an array of derived type needs constant bounds and no initialisation")
	}
	lo := int64(1)
	if dm.LoKnown {
		lo = dm.Lo
	}
	n := int(max(0, dm.Hi-lo+1))
	if n > MaxArrayElems/16 {
		return c.errf(e, "the array is too large")
	}
	s.k, s.slot, s.rank, s.dlb = kDA, c.cur.nDAs, 1, lo
	c.cur.nDAs++
	dslot := s.slot
	c.cur.init = append(c.cur.init, func(f *frame) {
		f.bud.charge(f, f, 8*int64(n)) // the slots, before they are allocated
		a := make([]*dobj, n)
		for i := range a {
			a[i] = dt.newObj(f, f)
		}
		f.das[dslot] = a
	})
	c.syms[ss] = s
	return nil
}

func (c *compiler) daIdent(id *syntax.Ident, s *sym) (*ex, error) {
	h, err := c.hops(s, id)
	if err != nil {
		return nil, err
	}
	slot := s.slot
	return &ex{k: kDA, slot: -1, dt: s.dt, rank: 1, v: s, da: func(f *frame) []*dobj { return f.at(h).das[slot] }}, nil
}

// daElement compiles pts(i): an element of an array of derived type, a live
// reference (assigning through it changes the array).
func (c *compiler) daElement(n *syntax.Index, id *syntax.Ident, s *sym) (*ex, error) {
	x, err := c.daIdent(id, s)
	if err != nil {
		return nil, err
	}
	if len(n.Args) != 1 || n.Args[0].Value == nil || n.Args[0].Keyword != "" {
		return nil, c.errf(n, "an element of %s takes one subscript (sections of arrays of derived type are not supported yet)", id.Name)
	}
	if _, trip := n.Args[0].Value.(*syntax.Triplet); trip {
		return nil, c.errf(n, "sections of arrays of derived type are not supported yet")
	}
	ix, err := c.intExpr(n.Args[0].Value)
	if err != nil {
		return nil, err
	}
	da, lb, name := x.da, s.dlb, id.Name
	return &ex{k: kD, slot: -1, dt: s.dt, d: func(f *frame) *dobj {
		a := da(f)
		k := ix(f) - lb
		if uint64(k) >= uint64(len(a)) {
			fail(f, "subscript 1 of %s is %d, outside %d:%d", name, k+lb, lb, lb+int64(len(a))-1)
		}
		return a[k]
	}}, nil
}

// ---- components ----

func (c *compiler) lookupComp(at syntax.Node, dt *dtype, name string) (*dcomp, int, error) {
	i, ok := dt.idx[name]
	if !ok {
		return nil, 0, c.errf(at, "type %s has no component %s", dt.name, name)
	}
	return dt.comps[i], i, nil
}

// component compiles x%name for a derived-type scalar or array x.
func (c *compiler) component(n *syntax.Component) (*ex, error) {
	x, err := c.expr(n.X)
	if err != nil {
		return nil, err
	}
	switch x.k {
	case kD:
		dc, i, err := c.lookupComp(n, x.dt, n.Name.Name)
		if err != nil {
			return nil, err
		}
		o := x.d
		switch dc.k {
		case kInt:
			return &ex{k: kInt, kd: dc.kd, slot: -1, i: func(f *frame) int64 { return o(f).v[i].i }}, nil
		case kLog:
			return &ex{k: kLog, slot: -1, b: func(f *frame) bool { return o(f).v[i].i != 0 }}, nil
		case kReal:
			return &ex{k: kReal, kd: dc.kd, slot: -1, r: func(f *frame) float64 { return o(f).v[i].r }}, nil
		case kStr:
			return &ex{k: kStr, slot: -1, s: func(f *frame) string { return o(f).v[i].s }}, nil
		case kD:
			return &ex{k: kD, slot: -1, dt: dc.dt, d: func(f *frame) *dobj { return o(f).v[i].d }}, nil
		}
		r, err := c.compRef(n)
		if err != nil {
			return nil, err
		}
		return &ex{k: kGA, slot: -1, rank: dc.rank, et: dc.et, kd: dc.kd, g: r.get}, nil
	case kDA:
		dc, i, err := c.lookupComp(n, x.dt, n.Name.Name)
		if err != nil {
			return nil, err
		}
		if dc.k == kGA || dc.k == kD || dc.k == kStr {
			return nil, c.errf(n, "%s%%%s of an array of derived type is not supported yet", "array", dc.name)
		}
		da, et, kd := x.da, dc.k, dc.kd
		return &ex{k: kGA, slot: -1, rank: 1, et: et, kd: kd, g: func(f *frame) *garr {
			a := da(f)
			g := newGarr(f, et, kd, []int64{1}, []int{len(a)})
			for k, o := range a {
				if et == kReal {
					g.r[k] = o.v[i].r
				} else {
					g.i[k] = o.v[i].i
				}
			}
			return g
		}}, nil
	}
	return nil, c.errf(n, "the component of something that is not a derived-type object")
}

// compRef is the array component x%name as a reference.
func (c *compiler) compRef(n *syntax.Component) (*gref, error) {
	x, err := c.expr(n.X)
	if err != nil {
		return nil, err
	}
	if x.k != kD {
		return nil, c.errf(n, "an array component of an array of derived type is not supported yet")
	}
	dc, i, err := c.lookupComp(n, x.dt, n.Name.Name)
	if err != nil {
		return nil, err
	}
	if dc.k != kGA {
		return nil, c.errf(n, "%s is not an array component", dc.name)
	}
	o := x.d
	return &gref{ptr: func(f *frame) **garr { return &o(f).v[i].g }, own: func(f *frame) *frame { return o(f).own }, name: dc.name, et: dc.et, kd: dc.kd, rank: dc.rank, alloc: dc.alloc}, nil
}

// assignDerived compiles an assignment whose target is a derived-type object
// or a scalar component.
func (c *compiler) assignDerived(at syntax.Node, lhs syntax.Expr, rhs *ex) (stmtFn, error) {
	if comp, ok := lhs.(*syntax.Component); ok {
		x, err := c.expr(comp.X)
		if err != nil {
			return nil, err
		}
		if x.k == kD {
			dc, i, err := c.lookupComp(comp, x.dt, comp.Name.Name)
			if err != nil {
				return nil, err
			}
			if dc.k != kD && dc.k != kGA {
				return c.assignScalarComp(at, x.d, dc, i, rhs)
			}
		}
	}
	dst, err := c.expr(lhs)
	if err != nil {
		return nil, err
	}
	if dst.k == kGA {
		return c.assignGA(at, lhs, rhs, nil)
	}
	if dst.k != kD || rhs.k != kD || dst.dt != rhs.dt {
		return nil, c.errf(at, "this assignment of a derived type is not supported")
	}
	d, s := dst.d, rhs.d
	return func(f *frame) { d(f).assign(f, s(f)) }, nil
}

func (c *compiler) assignScalarComp(at syntax.Node, o func(*frame) *dobj, dc *dcomp, i int, rhs *ex) (stmtFn, error) {
	kd, n := dc.kd, dc.clen
	switch {
	case dc.k == kInt && rhs.k == kInt:
		fn := rhs.i
		return func(f *frame) { v := fn(f); o(f).v[i].i = narrow(kd, v) }, nil
	case dc.k == kInt && rhs.k == kReal:
		fn := rhs.r
		return func(f *frame) { v := fn(f); o(f).v[i].i = narrow(kd, int64(v)) }, nil
	case dc.k == kReal && (rhs.k == kReal || rhs.k == kInt):
		fn := rhs.asReal().r
		return func(f *frame) {
			v := fn(f)
			if kd == 4 {
				v = float64(float32(v))
			}
			o(f).v[i].r = v
		}, nil
	case dc.k == kLog && rhs.k == kLog:
		fn := rhs.b
		return func(f *frame) { v := fn(f); o(f).v[i].i = b2i(v) }, nil
	case dc.k == kStr && rhs.k == kStr:
		fn := rhs.s
		return func(f *frame) { v := fn(f); o(f).v[i].s = fitLen(v, n) }, nil
	}
	return nil, c.errf(at, "this assignment to component %s is not supported", dc.name)
}

// ---- structure constructors ----

func (c *compiler) structCons(n *syntax.Index, sd *sema.DerivedType) (*ex, error) {
	dt, err := c.dtypeOf(n, sd)
	if err != nil {
		return nil, err
	}
	type arg struct {
		set func(*frame, *dobj)
	}
	given := make([]*syntax.Arg, len(dt.comps))
	pos := true
	for k := range n.Args {
		a := &n.Args[k]
		if a.Value == nil {
			return nil, c.errf(n, "an alternate return in a structure constructor")
		}
		if a.Keyword == "" {
			if !pos || k >= len(dt.comps) {
				return nil, c.errf(n, "too many values for the structure constructor of %s", dt.name)
			}
			given[k] = a
			continue
		}
		pos = false
		i, ok := dt.idx[a.Keyword]
		if !ok || given[i] != nil {
			return nil, c.errf(n, "type %s has no component %s (or it is given twice)", dt.name, a.Keyword)
		}
		given[i] = a
	}
	var sets []func(*frame, *dobj)
	for i, a := range given {
		if a == nil {
			continue
		}
		dc := dt.comps[i]
		x, err := c.expr(a.Value)
		if err != nil {
			return nil, err
		}
		i := i
		switch dc.k {
		case kD:
			if x.k != kD || x.dt != dc.dt {
				return nil, c.errf(a.Value, "component %s is of type %s", dc.name, dc.dt.name)
			}
			fn := x.d
			sets = append(sets, func(f *frame, o *dobj) { o.v[i].d.assign(f, fn(f)) })
		case kGA:
			if x.k != kGA {
				return nil, c.errf(a.Value, "component %s is an array", dc.name)
			}
			fn, et, kd := x.g, dc.et, dc.kd
			sets = append(sets, func(f *frame, o *dobj) {
				src := fn(f)
				g := newGarr(f, et, kd, ones(len(src.ext)), append([]int(nil), src.ext...))
				for k, n := 0, src.size(); k < n; k++ {
					a, b := src.at(k)
					g.put(k, a, b)
				}
				f.bud.release(f, o.v[i].g)
				o.v[i].g = f.bud.retain(f, o.own, g)
			})
		default:
			set, err := c.assignScalarComp(a.Value, func(f *frame) *dobj { return f.tmpObj }, dc, i, x)
			if err != nil {
				return nil, err
			}
			sets = append(sets, func(f *frame, o *dobj) {
				saved := f.tmpObj
				f.tmpObj = o
				set(f)
				f.tmpObj = saved
			})
		}
	}
	return &ex{k: kD, slot: -1, dt: dt, d: func(f *frame) *dobj {
		o := dt.newObj(f, nil)
		for _, s := range sets {
			s(f, o)
		}
		return o
	}}, nil
}

// ---- type-bound procedures ----

// boundProc resolves x%name to the procedure that implements it and the
// object passed as the first argument.
func (c *compiler) boundProc(at syntax.Node, comp *syntax.Component) (*procCode, error) {
	x, err := c.expr(comp.X)
	if err != nil {
		return nil, err
	}
	if x.k != kD {
		return nil, c.errf(at, "a type-bound procedure of something that is not a derived-type scalar")
	}
	impl, ok := x.dt.sd.FindBound(comp.Name.Name)
	if !ok {
		return nil, c.errf(at, "type %s has no type-bound procedure %s", x.dt.name, comp.Name.Name)
	}
	var ps *sema.Symbol
	for sc := x.dt.sd.Scope; sc != nil && ps == nil; sc = sc.Parent {
		ps = sc.Names[impl]
		if ps == nil {
			ps = sc.Procs[impl]
		}
	}
	if ps == nil || ps.Unit == nil {
		return nil, c.errf(at, "the procedure %s bound to %s was not found", impl, comp.Name.Name)
	}
	return c.callee(at, ps)
}

func (c *compiler) boundCallStmt(s *syntax.Call, comp *syntax.Component) (stmtFn, error) {
	p, err := c.boundProc(s, comp)
	if err != nil {
		return nil, err
	}
	if p.unit.Kind != syntax.SubroutineUnit {
		return nil, c.errf(s, "%s is not a subroutine", p.name)
	}
	args := append([]syntax.Arg{{Value: comp.X}}, s.Args...)
	run, err := c.invoker(s, p, args)
	if err != nil {
		return nil, err
	}
	return func(f *frame) { run(f) }, nil
}

func (c *compiler) boundFuncCall(n *syntax.Index, comp *syntax.Component) (*ex, error) {
	p, err := c.boundProc(n, comp)
	if err != nil {
		return nil, err
	}
	args := append([]syntax.Arg{{Value: comp.X}}, n.Args...)
	return c.funcCallArgs(n, p, args)
}

// moveAlloc compiles call move_alloc(from, to) (F2018 16.9.137).
func (c *compiler) moveAlloc(s *syntax.Call) (stmtFn, error) {
	if len(s.Args) != 2 || s.Args[0].Value == nil || s.Args[1].Value == nil {
		return nil, c.errf(s, "move_alloc takes two arguments")
	}
	from, _, err := c.lhsRef(s, s.Args[0].Value)
	if err != nil {
		return nil, err
	}
	to, _, err := c.lhsRef(s, s.Args[1].Value)
	if err != nil {
		return nil, err
	}
	if !from.alloc || !to.alloc || from.et != to.et {
		return nil, c.errf(s, "move_alloc needs two allocatable arrays of one type")
	}
	return func(f *frame) {
		src := *from.ptr(f)
		*to.ptr(f) = src
		*from.ptr(f) = nil
	}, nil
}

// componentIndex compiles x%name(args): an element or section of an array
// component, or a reference to a type-bound function.
func (c *compiler) componentIndex(n *syntax.Index, comp *syntax.Component) (*ex, error) {
	x, err := c.expr(comp.X)
	if err != nil {
		return nil, err
	}
	if x.k == kD {
		if dc, ok := x.dt.idx[comp.Name.Name]; ok {
			if x.dt.comps[dc].k != kGA {
				return nil, c.errf(n, "component %s cannot be subscripted yet", comp.Name.Name)
			}
			r, err := c.compRef(comp)
			if err != nil {
				return nil, err
			}
			return c.gaIndexRef(n, r)
		}
	}
	return c.boundFuncCall(n, comp)
}

// hasAlloc reports an allocatable component, at any depth.
func (t *dtype) hasAlloc() bool {
	for _, dc := range t.comps {
		if dc.alloc || dc.k == kD && dc.dt.hasAlloc() {
			return true
		}
	}
	return false
}
