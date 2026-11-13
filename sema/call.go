package sema

import (
	"fmt"
	"slices"
	"strings"

	"github.com/go-embedded-fortran/fortran/syntax"
)

// This file is M2 part 2 (bib/fortran.md §10): argument association (F2018
// 15.5.2), explicit-interface requirements (15.4.2.2), generic resolution
// (15.5.5.2), the restrictions on generic declarations (15.4.3.4.5), defined
// operations (10.1.6) and defined assignment (10.2.1.4).
//
// Leniency as everywhere in the package: a dummy or an actual whose type is not
// known (class(*), type(*), an opaque module, an implicit typing that failed)
// is never the reason for a refusal.

// actual is an actual argument after typing.
type actual struct {
	kw  string // argument keyword, "" when positional
	e   syntax.Expr
	tv  TV
	pos syntax.Pos
}

// actuals types the actual arguments of a reference. alt is set when one is an
// alternate return specifier (F2018 15.5.2.3), which the checks leave alone.
func (c *checker) actuals(sc *Scope, as []syntax.Arg) (out []actual, alt bool) {
	if len(as) > c.lim.MaxActuals {
		c.errorf(as[0].Pos, "reference with more than %d actual arguments: refused", c.lim.MaxActuals)
		c.stop = true
		return nil, true
	}
	out = make([]actual, 0, len(as))
	for i := range as {
		a := &as[i]
		if a.Value == nil {
			if a.AltReturn != "" {
				alt = true
			}
			continue
		}
		out = append(out, actual{lower(a.Keyword), a.Value, c.expr(sc, a.Value), a.Pos})
	}
	return out, alt
}

// dummy is a dummy argument of a procedure interface (F2018 15.3.2).
type dummy struct {
	name     string
	t        Type
	rank     int // -1: assumed rank
	dims     []Dim
	fl       Flags
	intent   string
	proc     bool // a dummy procedure
	implicit bool // the type comes from the implicit rules
}

func (d *dummy) optional() bool { return d.fl&FOptional != 0 }

func (d *dummy) deferred() bool { return d.fl&(FPointer|FAllocatable) != 0 }

// assumedShape reports an assumed-shape array (F2018 8.5.8.3).
func (d *dummy) assumedShape() bool {
	if d.rank <= 0 || d.deferred() || len(d.dims) != d.rank {
		return false
	}
	for _, x := range d.dims {
		if !x.AssumedShape && !x.Deferred { // "a(:)" is a deferred dimension in the array spec
			return false
		}
	}
	return true
}

// assumedSize reports an assumed-size array (F2018 8.5.8.5).
func (d *dummy) assumedSize() bool {
	return d.rank > 0 && !d.deferred() && len(d.dims) > 0 && d.dims[len(d.dims)-1].Assumed
}

// size is the element count of an explicit-shape dummy whose bounds are all
// constant.
func (d *dummy) size() (int64, bool) {
	if d.rank <= 0 || d.deferred() || len(d.dims) != d.rank {
		return 0, false
	}
	n := int64(1)
	for _, x := range d.dims {
		e, ok := x.extent()
		if !ok {
			return 0, false
		}
		n *= e
		if n > 1<<40 {
			return 0, false
		}
	}
	return n, true
}

// extent is the number of elements of a dimension with constant bounds.
func (x Dim) extent() (int64, bool) {
	if !x.LoKnown || !x.HiKnown || x.Assumed || x.Deferred || x.Lo < -maxExtent || x.Lo > maxExtent || x.Hi < -maxExtent || x.Hi > maxExtent {
		return 0, false
	}
	return max(x.Hi-x.Lo+1, 0), true
}

// procIface is the explicit interface of a procedure (F2018 15.4.2) as far as
// the analyser knows it.
type procIface struct {
	sym       *Symbol
	ds        []dummy
	elemental bool
	fn        bool
	bind      bool
	result    *Symbol // the result variable of a function
}

// ifaceOf returns the interface of p, when its definition (or interface body)
// is in view and has no alternate return.
func (c *checker) ifaceOf(p *Symbol) (*procIface, bool) {
	if p == nil || p.Unit == nil || p.Scope == nil {
		return nil, false
	}
	ifc := &procIface{sym: p, fn: p.Unit.Kind == syntax.FunctionUnit, bind: p.Unit.HasBind}
	ifc.elemental = slices.Contains(p.Unit.Prefix, "elemental")
	if ifc.fn {
		rn := p.Unit.Ident.Name
		if p.Unit.Result != nil {
			rn = p.Unit.Result.Name
		}
		ifc.result = p.Scope.Names[rn]
	}
	for _, n := range p.Params {
		if n == "*" {
			return nil, false
		}
		s := p.Scope.Names[n]
		if s == nil || (s.Kind != SymVar && s.Kind != SymProc) {
			return nil, false
		}
		d := dummy{name: n, t: s.Type, rank: s.Rank, dims: s.Dims, fl: s.Flags, intent: s.Intent}
		if d.t.Basic == Invalid {
			d.implicit = true
			if t, ok := implicitFor(p.Scope, n); ok {
				d.t = t
			} else {
				d.t = Type{Basic: Unknown}
			}
		}
		d.proc = s.Kind == SymProc || s.Flags&FExternal != 0
		ifc.ds = append(ifc.ds, d)
	}
	return ifc, true
}

// calleeOf returns the procedure whose interface applies to a reference through
// sym and whether that interface is explicit at the reference. An external
// procedure referenced through an implicit interface is looked up among the
// file's own definitions, to apply F2018 15.4.2.2.
func (c *checker) calleeOf(sym *Symbol) (callee *Symbol, explicit bool) {
	switch {
	case sym.Unit != nil:
		return sym, true
	case sym.Flags&(FProcDecl|FDummy|FStmtFunc|FIntrinsic) != 0 || sym.Origin != nil:
		return nil, true // an interface the analyser does not see
	}
	return c.globals[sym.Name], false
}

// procSymbol is the symbol of an external procedure defined in the file.
func (c *checker) procSymbol(u *syntax.Unit, psc *Scope, host *Scope) *Symbol {
	s := &Symbol{Name: u.Ident.Name, Kind: SymProc, Pos: u.Pos, Home: host, Unit: u, Scope: psc}
	for _, p := range u.Params {
		s.Params = append(s.Params, p.Name)
	}
	if u.Kind == syntax.FunctionUnit {
		s.Flags |= FFunction
	} else {
		s.Flags |= FSub
	}
	return s
}

// ---- argument association ----

// associate matches actual arguments with dummy arguments by position and by
// keyword (F2018 15.5.2.1, 15.5.2.2). pairs[i] is the dummy of actual i.
func associate(ifc *procIface, name string, callPos syntax.Pos, acts []actual) (pairs []int, msg string, at syntax.Pos) {
	pairs = make([]int, len(acts))
	used := make([]bool, len(ifc.ds))
	pos, kw := 0, false
	for i := range acts {
		a := &acts[i]
		if a.kw == "" {
			if kw {
				return nil, "Missing keyword name in actual argument list at (1)", a.pos
			}
			if pos >= len(ifc.ds) {
				return nil, "More actual than formal arguments in procedure call at (1)", a.pos
			}
			pairs[i] = pos
			used[pos] = true
			pos++
			continue
		}
		kw = true
		j := slices.IndexFunc(ifc.ds, func(d dummy) bool { return d.name == a.kw })
		switch {
		case j < 0:
			return nil, fmt.Sprintf("Keyword argument '%s' at (1) is not in the procedure '%s'", a.kw, name), a.pos
		case used[j]:
			return nil, fmt.Sprintf("Keyword argument '%s' at (1) is already associated with another actual argument", a.kw), a.pos
		}
		pairs[i] = j
		used[j] = true
	}
	for j := range ifc.ds {
		if !used[j] && !ifc.ds[j].optional() {
			return nil, fmt.Sprintf("Missing actual argument '%s' in call to '%s' at (1)", ifc.ds[j].name, name), callPos
		}
	}
	return pairs, "", 0
}

// typeCompatible reports whether an actual of type at may be associated with a
// dummy of type dt (F2018 15.5.2.4: same type and kind; a polymorphic dummy
// also takes an extension).
func typeCompatible(dt Type, poly bool, at Type) bool {
	if dt.Lenient() || at.Lenient() || dt.Basic != at.Basic {
		return dt.Lenient() || at.Lenient()
	}
	if dt.Basic == Derived {
		if dt.Der == nil || at.Der == nil || dt.Der == at.Der || dt.Der.Name == at.Der.Name {
			return true
		}
		return poly && extends(at.Der, dt.Der)
	}
	return dt.Kind == at.Kind
}

func isDesignator(a *actual) bool {
	switch a.e.(type) {
	case *syntax.Index, *syntax.Component:
		return a.tv.Var && !a.tv.Call
	}
	return false
}

// argCompat is the consistency of an actual with a dummy that generic
// resolution and a procedure reference both need: type, kind, rank and the
// pointer and allocatable attributes (F2018 15.5.2.4 - 15.5.2.6). It returns a
// gfortran-worded message, "" when the pair is consistent.
func (c *checker) argCompat(ifc *procIface, d *dummy, a *actual) string {
	tv := a.tv
	if d.proc {
		return ""
	}
	if _, ok := a.e.(*syntax.Ident); ok && tv.Sym != nil && tv.Sym.Kind == SymProc && tv.Sym.Unit != nil {
		// a procedure with a definition in view, passed to a data dummy
		if !d.implicit && d.t.Basic != Unknown {
			return fmt.Sprintf("Expected a procedure for argument '%s' at (1)", d.name)
		}
		return ""
	}
	if _, boz := a.e.(*syntax.BozLit); boz {
		return ""
	}
	if !typeCompatible(d.t, d.fl&FPoly != 0, tv.Type) {
		return fmt.Sprintf("Type mismatch in argument '%s' at (1); passed %s to %s", d.name, tv.Type, d.t)
	}
	assumedType := d.t.Lenient() || d.t.Basic == Derived && d.t.Der != nil && intrinsicModuleTypes[d.t.Der.Name]
	if tv.Rank != RankUnknown && d.rank != -1 && !ifc.elemental && !assumedType {
		switch {
		case d.rank == 0 && tv.Rank != 0:
			return fmt.Sprintf("Rank mismatch in argument '%s' at (1) (scalar and rank-%d)", d.name, max(tv.Rank, 1))
		case d.rank > 0 && (d.assumedShape() || d.deferred()) && tv.Rank != d.rank:
			return fmt.Sprintf("Rank mismatch in argument '%s' at (1) (rank-%d and %s)", d.name, d.rank, rankName(tv.Rank))
		case d.rank > 0 && tv.Rank == 0 && !isDesignator(a) && !(tv.Type.Basic == Character && tv.Type.Kind == DefaultChar):
			return fmt.Sprintf("Rank mismatch in argument '%s' at (1) (rank-%d and scalar)", d.name, d.rank)
		}
	}
	if _, ok := a.e.(*syntax.Ident); ok && tv.Sym != nil && tv.Sym.Kind == SymVar && tv.Sym.Flags&FAssoc == 0 {
		// a pointer dummy with INTENT(IN) takes a target (F2018 15.5.2.5)
		if d.fl&FPointer != 0 && d.intent != "in" && tv.Sym.Flags&FPointer == 0 {
			return fmt.Sprintf("Actual argument for '%s' must be a pointer at (1)", d.name)
		}
		if d.fl&FAllocatable != 0 && tv.Sym.Flags&FAllocatable == 0 {
			return fmt.Sprintf("Actual argument for '%s' must be ALLOCATABLE at (1)", d.name)
		}
	}
	return ""
}

func rankName(r int) string {
	if r == 0 {
		return "scalar"
	}
	return fmt.Sprintf("rank-%d", r)
}

// callCheck checks a reference to a procedure: its argument list against the
// dummy arguments, when the interface is explicit (F2018 15.5.2), and the
// explicit-interface requirements, when it is not (15.4.2.2). callee is the
// procedure whose interface applies (nil: unknown). It returns the rank of an
// elemental reference's result, or -3 when the rank is the procedure's.
func (c *checker) callCheck(sc *Scope, pos syntax.Pos, name string, callee *Symbol, explicit bool, acts []actual, alt bool) int {
	const own = -3
	if c.stop {
		return own
	}
	if !explicit {
		for i := range acts {
			if acts[i].kw != "" {
				c.errorf(acts[i].pos, "Keyword argument requires explicit interface for procedure '%s' at (1)", name)
				return own
			}
		}
	}
	if alt {
		return own
	}
	ifc, ok := c.ifaceOf(callee)
	if !ok {
		return own
	}
	if !explicit {
		if msg := explicitRequired(ifc); msg != "" {
			c.errorf(pos, "Explicit interface required for '%s' at (1): %s", name, msg)
			return own
		}
	}
	pairs, msg, at := associate(ifc, name, pos, acts)
	if msg != "" {
		c.errorf(at, "%s", msg)
		return own
	}
	for i := range acts {
		a, d := &acts[i], &ifc.ds[pairs[i]]
		if m := c.argCompat(ifc, d, a); m != "" {
			c.errorf(a.pos, "%s", m)
			continue
		}
		if a.tv.Type.Lenient() || d.proc {
			continue
		}
		if d.intent == "out" || d.intent == "inout" {
			if d.fl&FValue == 0 {
				c.definable(sc, a.e, a.tv, "actual argument to INTENT = OUT/INOUT")
			}
		}
		c.sizeCheck(d, a)
	}
	if ifc.elemental {
		var first *actual
		for i := range acts {
			a := &acts[i]
			if a.tv.Rank <= 0 {
				continue
			}
			if first == nil {
				first = a
				continue
			}
			if a.tv.Rank != first.tv.Rank {
				c.errorf(a.pos, "Shapes for arguments of elemental procedure '%s' at (1) are not conformable: ranks %d and %d", name, first.tv.Rank, a.tv.Rank)
				break
			}
			if d, x, y, bad := conformDim(first.tv.Shape, a.tv.Shape); bad {
				c.errorf(a.pos, "Shapes for arguments of elemental procedure '%s' at (1) are not conformable: extents %d and %d on dimension %d", name, x, y, d)
				break
			}
		}
		return elementalRank(acts)
	}
	return own
}

// actualsShape is the shape of an elemental reference: that of its array
// actuals (F2018 15.8.1); conformance is checked by callCheck.
func actualsShape(acts []actual) []int64 {
	var out []int64
	rank := 0
	for i := range acts {
		if tv := acts[i].tv; tv.Rank > 0 {
			if rank == 0 {
				out, rank = tv.Shape, tv.Rank
			} else if tv.Rank == rank {
				out = joinShape(out, tv.Shape, rank, rank)
			}
		}
	}
	return out
}

// sizeCheck refuses a whole array too small for an explicit-shape dummy (F2018
// 15.5.2.11, sequence association).
func (c *checker) sizeCheck(d *dummy, a *actual) {
	if a.tv.Rank <= 0 {
		return
	}
	dn, ok := d.size()
	if !ok || d.fl&FValue != 0 || d.t.Basic == Character {
		return // a character element sequence counts characters, not elements
	}
	if s := a.tv.Sym; s != nil && s.Flags&(FPointer|FAllocatable) != 0 {
		return
	}
	n, ok := shapeSize(a.tv.Shape)
	if ok && n < dn {
		c.errorf(a.pos, "Actual argument contains too few elements for dummy argument '%s' (%d/%d) at (1)", d.name, n, dn)
	}
}

// elementalRank is the rank of an elemental reference: the rank of its array
// actuals (F2018 15.8.1), which must agree (10.1.5 conformance of ranks).
func elementalRank(acts []actual) int {
	r := 0
	for i := range acts {
		if acts[i].tv.Rank == RankUnknown {
			return RankUnknown
		}
		r = max(r, acts[i].tv.Rank)
	}
	return r
}

// explicitRequired reports why a procedure needs an explicit interface (F2018
// 15.4.2.2), "" when it does not.
func explicitRequired(ifc *procIface) string {
	for i := range ifc.ds {
		d := &ifc.ds[i]
		switch {
		case d.fl&FAllocatable != 0:
			return "ALLOCATABLE argument"
		case d.fl&FOptional != 0:
			return "OPTIONAL argument"
		case d.fl&FPointer != 0 && !d.proc:
			return "POINTER argument"
		case d.fl&FTarget != 0:
			return "TARGET argument"
		case d.fl&FValue != 0:
			return "VALUE argument"
		case d.fl&FVolatile != 0:
			return "VOLATILE argument"
		case d.assumedShape():
			return "assumed-shape argument"
		case d.rank == -1:
			return "assumed-rank argument"
		case d.fl&FPoly != 0:
			return "polymorphic argument"
		}
	}
	if ifc.result != nil {
		switch {
		case ifc.result.Rank != 0:
			return "array result"
		case ifc.result.Flags&FPointer != 0:
			return "pointer result"
		case ifc.result.Flags&FAllocatable != 0:
			return "allocatable result"
		}
	}
	if ifc.elemental {
		return "elemental procedure"
	}
	if ifc.bind {
		return "BIND attribute"
	}
	return ""
}

// ---- generic resolution (F2018 15.5.5.2) ----

// pickSpecific selects the specific procedure of g that a reference with the
// given actuals invokes. unsure is set when a specific could not be examined
// (an interface that is not in view), in which case no diagnostic is made.
func (c *checker) pickSpecific(sc *Scope, g *Symbol, name string, acts []actual, fn bool) (best *Symbol, unsure bool) {
	specs := specificsOf(g)
	if len(specs) > c.lim.MaxSpecifics {
		return nil, true // refused where the generic is checked (checkGeneric)
	}
	var cands []*procIface
	for _, s := range specs {
		if fn && s.Flags&FSub != 0 && s.Flags&FFunction == 0 || !fn && s.Flags&FFunction != 0 && s.Flags&FSub == 0 {
			continue
		}
		ifc, ok := c.ifaceOf(s)
		if !ok {
			unsure = true
			continue
		}
		pairs, msg, _ := associate(ifc, s.Name, 0, acts)
		if msg != "" {
			continue
		}
		consistent := true
		for i := range acts {
			if c.argCompat(ifc, &ifc.ds[pairs[i]], &acts[i]) != "" {
				consistent = false
				break
			}
		}
		if consistent {
			cands = append(cands, ifc)
		}
	}
	if len(cands) == 0 {
		return nil, unsure
	}
	// a nonelemental specific is preferred over an elemental one (F2018 15.5.5.2)
	for _, ifc := range cands {
		if !ifc.elemental {
			return ifc.sym, unsure
		}
	}
	return cands[0].sym, unsure
}

// specificsOf are the specifics a reference through generic g can select: its
// own and those of the generics of the same name in the enclosing scoping units,
// which a local generic extends (F2018 15.4.3.4.1, 19.5.1.4).
func specificsOf(g *Symbol) []*Symbol {
	if g.Home == nil || g.Home.Parent == nil {
		return g.Specifics
	}
	out := g.Specifics
	for sc := g.Home.Parent; sc != nil; sc = sc.Parent {
		h := sc.Names[g.Name]
		if h == nil {
			continue
		}
		if h.Kind != SymGeneric {
			break
		}
		for _, s := range h.Specifics {
			if !slices.ContainsFunc(out, func(x *Symbol) bool { return x == s || x.Unit != nil && x.Unit == s.Unit }) {
				out = append(slices.Clip(out), s)
			}
		}
	}
	return out
}

// resolveGeneric types a reference through a generic name (F2018 15.5.5.2).
func (c *checker) resolveGeneric(sc *Scope, e *syntax.Index, g *Symbol, acts []actual, alt bool) TV {
	unknown := TV{Type: Type{Basic: Unknown}, Rank: RankUnknown, Call: true, Sym: g}
	if alt || c.stop {
		return unknown
	}
	best, unsure := c.pickSpecific(sc, g, g.Name, acts, true)
	if best == nil {
		if in, ok := intrinsicFuncs[g.Name]; ok {
			// the intrinsic of the same name stays accessible (F2018 15.4.3.4.5)
			return c.intrinsicOf(sc, e, g.Name, in, acts)
		}
		if !unsure && !sc.Opaque && len(specificsOf(g)) > 0 {
			c.errorf(e.Pos, "There is no specific function for the generic '%s' at (1)", g.Name)
		}
		var t Type
		for i, s := range g.Specifics {
			if i == 0 {
				t = s.Type
			} else if s.Type != t {
				return unknown
			}
		}
		if len(g.Specifics) > 0 && !t.Lenient() {
			unknown.Type = t
		}
		return unknown
	}
	r := c.callCheck(sc, e.Pos, best.Name, best, true, acts, false)
	return c.callTV(best, r, acts)
}

// callTV is the typed result of a reference to procedure p whose elemental
// rank (or -3) is r.
func (c *checker) callTV(p *Symbol, r int, acts []actual) TV {
	t := p.Type
	if t.Basic == Invalid {
		if it, ok := implicitFor(p.Scope, p.Name); ok && p.Scope != nil {
			t = it
		} else {
			t = Type{Basic: Unknown}
		}
	}
	tv := TV{Type: t, Rank: p.Rank, Call: true, Sym: p, Shape: shapeOfDims(p.Dims, p.Rank)}
	if r != -3 {
		tv.Rank, tv.Shape = r, actualsShape(acts)
	}
	return tv
}

func (c *checker) intrinsicOf(sc *Scope, e *syntax.Index, name string, in intrinsic, acts []actual) TV {
	args := make([]argTV, len(acts))
	for i, a := range acts {
		args[i] = argTV{a.kw, a.tv}
	}
	tv, msg := c.intrinsicType(name, in, args)
	if msg != "" {
		c.errorf(e.Pos, "%s at (1)", msg)
		return tv
	}
	if in.pol == pElem {
		tv.Shape = c.elementalShape(e.Pos, name, acts)
	} else {
		c.intrinsicShape(e.Pos, name, args, &tv)
	}
	tv.IsConst = true
	for _, a := range args {
		tv.IsConst = tv.IsConst && (a.tv.constant() || a.tv.Type.Lenient())
	}
	switch name {
	case "size", "lbound", "ubound", "shape":
		// the inquiries of F2018 10.1.12 (8): constant unless the array has a
		// deferred or assumed shape
		if len(args) > 0 {
			tv.IsConst = tv.IsConst || !deferredDims(args[0].tv.Dims)
		}
	}
	if tv.Rank == 0 || tv.Rank == RankUnknown {
		if k := c.foldIntrinsic(name, args, tv.Type); k != nil {
			tv.Const = k
		} else if name == "size" {
			if k := sizeOf(args, tv.Type); k != nil {
				tv.Const = k
			}
		}
	}
	return tv
}

// elementalShape is the shape of an elemental reference: that of its array
// arguments, which shall conform (F2018 15.8.1).
func (c *checker) elementalShape(pos syntax.Pos, name string, acts []actual) []int64 {
	var out []int64
	rank := 0
	for i := range acts {
		tv := acts[i].tv
		if tv.Rank <= 0 {
			continue
		}
		if rank == 0 {
			out, rank = tv.Shape, tv.Rank
			continue
		}
		if tv.Rank != rank {
			c.errorf(acts[i].pos, "Incompatible ranks %d and %d in the arguments of intrinsic '%s' at (1)", rank, tv.Rank, name)
			return nil
		}
		if d, x, y, bad := conformDim(out, tv.Shape); bad {
			c.errorf(acts[i].pos, "Different shape for the arguments of intrinsic '%s' at (1) on dimension %d (%d and %d)", name, d, x, y)
			return nil
		}
		out = joinShape(out, tv.Shape, rank, rank)
	}
	return out
}

// sizeOf folds size(array [, dim]) when the shape of the array is known (F2018
// 16.9.196, 10.1.12).
func sizeOf(args []argTV, t Type) *Const {
	if len(args) == 0 || args[0].tv.Rank <= 0 || len(args[0].tv.Shape) == 0 {
		return nil
	}
	s := args[0].tv.Shape
	if d, ok := argAt(args, "dim", 1); ok {
		if d.Const == nil || d.Const.Type.Basic != Integer || d.Const.I < 1 || int(d.Const.I) > len(s) || s[d.Const.I-1] < 0 {
			return nil
		}
		return &Const{Type: t, I: s[d.Const.I-1]}
	}
	n, ok := shapeSize(s)
	if !ok {
		return nil
	}
	return &Const{Type: t, I: n}
}

// ---- defined operations and assignment (F2018 10.1.6, 10.2.1.4) ----

// intrinsicModuleTypes are the types of the intrinsic modules, which carry
// operators the analyser does not model.
var intrinsicModuleTypes = map[string]bool{
	"c_ptr": true, "c_funptr": true, "lock_type": true, "event_type": true, "team_type": true,
	"ieee_class_type": true, "ieee_status_type": true, "ieee_round_type": true,
}

func hasBoundOp(t Type, name string) bool {
	if t.Basic != Derived || t.Der == nil {
		return false
	}
	if intrinsicModuleTypes[t.Der.Name] {
		return true
	}
	_, ok := t.Der.FindBound(name)
	return ok
}

// definedOp resolves a reference to an operator through the generic
// "operator(op)" (F2018 10.1.6.2, 15.4.3.4.2). It returns ok = false when the
// operator is not defined for the operands: the caller reports it as it does
// for an intrinsic operator.
func (c *checker) definedOp(sc *Scope, pos syntax.Pos, op string, xs ...actual) (TV, bool) {
	unknown := TV{Type: Type{Basic: Unknown}, Rank: RankUnknown}
	name := "operator(" + op + ")"
	for i := range xs {
		if hasBoundOp(xs[i].tv.Type, name) {
			return unknown, true // a type-bound generic operator
		}
	}
	g := sc.Lookup(name)
	if g == nil {
		return unknown, sc.Opaque
	}
	if g.Kind != SymGeneric {
		return unknown, true
	}
	best, unsure := c.pickSpecific(sc, g, name, xs, true)
	if best == nil {
		return unknown, unsure
	}
	ifc, _ := c.ifaceOf(best)
	if ifc != nil && len(ifc.ds) != len(xs) {
		return unknown, true
	}
	return c.callTV(best, c.operatorRank(ifc, xs), xs), true
}

func (c *checker) operatorRank(ifc *procIface, xs []actual) int {
	if ifc != nil && ifc.elemental {
		return elementalRank(xs)
	}
	return -3
}

// definedAssignment resolves "l = r" through the generic assignment(=)
// (F2018 10.2.1.4): ok is true when a specific subroutine takes the pair, or
// when it cannot be told.
func (c *checker) definedAssignment(sc *Scope, pos syntax.Pos, l, r actual) bool {
	if hasBoundOp(l.tv.Type, "assignment(=)") || hasBoundOp(r.tv.Type, "assignment(=)") {
		return true
	}
	g := sc.Lookup("assignment(=)")
	if g == nil || g.Kind != SymGeneric {
		return g != nil || sc.Opaque
	}
	best, unsure := c.pickSpecific(sc, g, "assignment(=)", []actual{l, r}, false)
	return best != nil || unsure
}

// ---- restrictions on generic declarations (F2018 15.4.3.4.5) ----

// checkAllGenerics applies 15.4.3.4.5 to every generic of the file's scoping
// units, once all their specifics are known.
func (c *checker) checkAllGenerics() {
	for _, ui := range c.info.Units {
		if c.stop {
			return
		}
		for _, s := range ui.Scope.Order {
			if s.Kind != SymGeneric || len(s.Specifics) < 2 {
				continue
			}
			if s.Origin != nil && len(s.Origin.Specifics) == len(s.Specifics) {
				continue // use-associated unchanged: checked in its module
			}
			c.checkGeneric(s)
		}
	}
}

func (c *checker) checkGeneric(g *Symbol) {
	if len(g.Specifics) > c.lim.MaxSpecifics {
		c.errorf(g.Pos, "Generic '%s' at (1) has more than %d specific procedures: refused", g.Name, c.lim.MaxSpecifics)
		return
	}
	ifcs := make([]*procIface, 0, len(g.Specifics))
	for _, s := range g.Specifics {
		ifc, ok := c.ifaceOf(s)
		if !ok || slices.ContainsFunc(ifc.ds, func(d dummy) bool { return !d.proc && d.t.Lenient() }) {
			ifc = nil // not in view, or a type the analyser cannot compare: no verdict
		}
		ifcs = append(ifcs, ifc)
	}
	positional := strings.HasPrefix(g.Name, "operator(") || strings.HasPrefix(g.Name, "assignment(")
	if strings.HasPrefix(g.Name, "read(") || strings.HasPrefix(g.Name, "write(") {
		return
	}
	for i := range ifcs {
		for j := i + 1; j < len(ifcs); j++ {
			p, q := ifcs[i], ifcs[j]
			if p == nil || q == nil || p.sym == q.sym || p.sym.Unit == q.sym.Unit && p.sym.Unit != nil {
				continue
			}
			if p.sym.Home != g.Home || q.sym.Home != g.Home {
				continue // a pair with a use-associated specific is a warning in gfortran, an error only when referenced
			}
			if p.fn != q.fn {
				continue
			}
			var amb bool
			if positional {
				amb = len(p.ds) == len(q.ds) && !positionDistinguishable(p, q)
			} else {
				amb = !distinguishable(p, q) && !distinguishable(q, p)
			}
			if amb {
				c.errorf(q.sym.Pos, "Ambiguous interfaces '%s' and '%s' in generic interface '%s' at (1)", p.sym.Name, q.sym.Name, g.Name)
			}
		}
	}
}

// tkr reports whether a is type, kind and rank compatible with b (F2018
// 15.4.3.4.5 p2).
func tkr(a, b *dummy) bool {
	if a.t.Basic != b.t.Basic {
		return false
	}
	if a.rank != b.rank && a.rank != -1 && b.rank != -1 {
		return false
	}
	if a.t.Basic == Derived {
		if a.t.Der == nil || b.t.Der == nil || a.t.Der == b.t.Der {
			return true
		}
		return a.fl&FPoly != 0 && extends(b.t.Der, a.t.Der)
	}
	return a.t.Kind == b.t.Kind
}

// dummiesDistinct is the distinguishability of two dummy arguments (F2018
// 15.4.3.4.5 p3).
func dummiesDistinct(a, b *dummy) bool {
	switch {
	case a.proc != b.proc:
		return true
	case a.proc:
		return false
	case !tkr(a, b) && !tkr(b, a):
		return true
	case a.fl&FAllocatable != 0 && b.fl&FPointer != 0 && b.intent != "in":
		return true
	case b.fl&FAllocatable != 0 && a.fl&FPointer != 0 && a.intent != "in":
		return true
	}
	return false
}

func positionDistinguishable(p, q *procIface) bool {
	for i := range p.ds {
		if dummiesDistinct(&p.ds[i], &q.ds[i]) {
			return true
		}
	}
	return false
}

// distinguishable is condition C1514 (1), (2) and (4) of F2018 15.4.3.4.5,
// holding for p in one direction; the caller tries both. (Condition (3) is
// for passed-object dummies, which belong to type-bound generics.)
func distinguishable(p, q *procIface) bool {
	// (1) a dummy data object of p with more nonoptional TKR-compatible
	// dummies in p than dummies of q not distinguishable from it
	for i := range p.ds {
		d := &p.ds[i]
		if d.proc {
			continue
		}
		a, b := 0, 0
		for j := range p.ds {
			if x := &p.ds[j]; !x.proc && !x.optional() && tkr(x, d) {
				a++
			}
		}
		for j := range q.ds {
			if y := &q.ds[j]; !y.proc && !dummiesDistinct(y, d) {
				b++
			}
		}
		if a > b {
			return true
		}
	}
	// (2) more nonoptional dummy procedures in p than dummy procedures in q
	np, nq := 0, 0
	for i := range p.ds {
		if p.ds[i].proc && !p.ds[i].optional() {
			np++
		}
	}
	for i := range q.ds {
		if q.ds[i].proc {
			nq++
		}
	}
	if np > nq {
		return true
	}
	// (4) a nonoptional dummy that disambiguates by position, at or before
	// one that disambiguates by name
	first, last := -1, -1
	for i := range p.ds {
		d := &p.ds[i]
		if d.optional() {
			continue
		}
		if first < 0 && (i >= len(q.ds) || dummiesDistinct(d, &q.ds[i])) {
			first = i
		}
		if k := slices.IndexFunc(q.ds, func(y dummy) bool { return y.name == d.name }); k < 0 || dummiesDistinct(d, &q.ds[k]) {
			last = i
		}
	}
	return first >= 0 && last >= first
}
