package sema

import (
	"fmt"
	"slices"

	"github.com/go-embedded-fortran/fortran/syntax"
)

// Options configure Analyze.
type Options struct {
	Limits Limits
	// ImplicitNone makes "implicit none" the default of every scoping unit
	// (bib/fortran.md §7: the default of notebook cells).
	ImplicitNone bool
	// Modules are modules known before the file: the session modules of
	// earlier cells (bib §7), in scope for "use".
	Modules map[string]*Scope
	// Redefine is the session's redefinition rule (bib §7): a contained
	// procedure shadows a use-associated name of its host. Outside a session
	// the standard forbids it, and so does gfortran.
	Redefine bool
}

type checker struct {
	f     *syntax.File
	info  *Info
	errs  syntax.ErrorList
	lim   Limits
	opt   Options
	root  *Scope
	nsyms int
	stop  bool
	depth int
	// fwdOK allows a type name not yet defined (pointer/allocatable components).
	fwdOK bool
	// intrMods caches the intrinsic modules: a type of iso_c_binding is one
	// type however many scopes use it.
	intrMods map[string]*Scope
	// deferTyping is set while evaluating array bounds and character lengths
	// of a declaration: an untyped dummy is not given its implicit type yet.
	deferTyping bool
	// pending generic interfaces: "module procedure" names to resolve once
	// the host's contained procedures are declared.
	pending []pendingGeneric
	// globals are the external procedures defined in the file, by name: a
	// reference through an implicit interface is checked against them (F2018
	// 15.4.2.2, 15.5.2).
	globals map[string]*Symbol
	// where are the masks of the WHERE constructs being checked, innermost
	// last: the assignments of their bodies conform with them.
	where []TV
}

type pendingGeneric struct {
	host  *Scope
	gen   *Symbol
	names []string
	pos   syntax.Pos
}

// Analyze checks a parsed file against the semantic rules of F2018 and
// returns the annotated tree. The diagnostics are "file:line:col: message"; a
// non-empty list means the program is refused. It never panics, and its
// bounds (Options.Limits) are refusals.
func Analyze(f *syntax.File, o *Options) (info *Info, errs syntax.ErrorList) {
	c := &checker{f: f, info: &Info{
		Types:   map[syntax.Expr]TV{},
		Uses:    map[*syntax.Ident]*Symbol{},
		Scopes:  map[*syntax.Unit]*Scope{},
		Modules: map[string]*Scope{},
		Calls:   map[*syntax.Call]*Symbol{},
	}, globals: map[string]*Symbol{}}
	if o != nil {
		c.opt = *o
	}
	c.lim = c.opt.Limits
	d := DefaultLimits()
	if c.lim.MaxSymbols <= 0 {
		c.lim.MaxSymbols = d.MaxSymbols
	}
	if c.lim.MaxConstChars <= 0 {
		c.lim.MaxConstChars = d.MaxConstChars
	}
	if c.lim.MaxErrors <= 0 {
		c.lim.MaxErrors = d.MaxErrors
	}
	if c.lim.MaxDepth <= 0 {
		c.lim.MaxDepth = d.MaxDepth
	}
	if c.lim.MaxSpecifics <= 0 {
		c.lim.MaxSpecifics = d.MaxSpecifics
	}
	if c.lim.MaxActuals <= 0 {
		c.lim.MaxActuals = d.MaxActuals
	}
	defer func() {
		if r := recover(); r != nil { // a defect of ours must not take the caller down
			c.errs = append(c.errs, syntax.Error{Msg: fmt.Sprintf("internal error in the semantic analyser: %v", r)})
			info, errs = c.info, c.errs
		}
	}()
	c.root = newScope(nil, nil)
	c.root.ImplicitNone = c.opt.ImplicitNone
	for n, m := range c.opt.Modules {
		c.info.Modules[n] = m
	}
	scs := make([]*Scope, len(f.Units))
	for i, u := range f.Units {
		if c.stop {
			break
		}
		sc := c.declareUnit(u, c.root)
		scs[i] = sc
		switch u.Kind {
		case syntax.ModuleUnit, syntax.SubmoduleUnit:
			c.info.Modules[u.Ident.Name] = sc
		case syntax.FunctionUnit, syntax.SubroutineUnit:
			c.globals[u.Ident.Name] = c.procSymbol(u, sc, c.root)
		}
	}
	for i, u := range f.Units {
		if c.stop || scs[i] == nil {
			break
		}
		c.execUnit(u, scs[i])
	}
	c.checkAllGenerics()
	return c.info, c.errs
}

func (c *checker) errorf(pos syntax.Pos, format string, a ...any) {
	if c.stop {
		return
	}
	if len(c.errs) >= c.lim.MaxErrors {
		c.errs = append(c.errs, syntax.Error{Pos: c.f.Position(pos), Msg: "too many errors, analysis stopped"})
		c.stop = true
		return
	}
	c.errs = append(c.errs, syntax.Error{Pos: c.f.Position(pos), Msg: fmt.Sprintf(format, a...)})
}

// newSym declares name in sc; a second declaration is an error (F2018
// 8.2: an entity may be declared once) reported by the caller.
func (c *checker) newSym(sc *Scope, name string, kind SymKind, pos syntax.Pos) *Symbol {
	c.nsyms++
	if c.nsyms > c.lim.MaxSymbols {
		c.errorf(pos, "too many symbols (more than %d): refused", c.lim.MaxSymbols)
		c.stop = true
		return &Symbol{Name: name, Kind: kind, Home: sc}
	}
	s := &Symbol{Name: name, Kind: kind, Pos: pos, Home: sc}
	sc.Names[name] = s
	sc.Order = append(sc.Order, s)
	return s
}

// ---- program units ----

func (c *checker) declareUnit(u *syntax.Unit, host *Scope) *Scope {
	sc := newScope(host, u)
	if host == c.root {
		sc.Parent = c.root
	}
	c.info.Scopes[u] = sc
	c.info.Units = append(c.info.Units, &UnitInfo{u, sc})
	c.depth++
	defer func() { c.depth-- }()
	if c.depth > c.lim.MaxDepth {
		c.errorf(u.Pos, "program units nested deeper than the analyser's bound")
		c.stop = true
		return sc
	}
	for _, p := range u.Params {
		if p.Name == "*" {
			continue
		}
		if _, dup := sc.Names[p.Name]; dup {
			c.errorf(p.Pos, "duplicate dummy argument '%s'", p.Name)
			continue
		}
		s := c.newSym(sc, p.Name, SymVar, p.Pos)
		s.Flags |= FDummy
	}
	if u.Kind == syntax.FunctionUnit {
		rn := u.Ident.Name
		if u.Result != nil {
			rn = u.Result.Name
		}
		r := sc.Names[rn]
		if r == nil {
			r = c.newSym(sc, rn, SymVar, u.Ident.Pos)
		}
		r.Flags |= FResult
		if u.ResultType != nil {
			r.Type = c.typeSpec(sc, u.ResultType)
		}
	}
	c.specPass(sc, u.Body)
	for _, cu := range u.Contains {
		c.declareContained(sc, cu)
	}
	c.resolvePending(host == c.root)
	return sc
}

func (c *checker) declareContained(host *Scope, cu *syntax.Unit) {
	psc := c.declareUnit(cu, host)
	name := cu.Ident.Name
	sym := host.Names[name]
	if sym != nil && sym.Kind == SymGeneric && !(c.opt.Redefine && sym.Flags&FUsed != 0) {
		// a specific of the generic of the same name: the name keeps denoting
		// the generic, the procedure is reached through its specifics
		c.nsyms++
		sp := &Symbol{Name: name, Kind: SymProc, Pos: cu.Pos, Home: host}
		if host.Procs == nil {
			host.Procs = map[string]*Symbol{}
		}
		host.Procs[name] = sp
		sym = sp
	} else if sym == nil || sym.Kind == SymGeneric || (c.opt.Redefine && sym.Flags&FUsed != 0) {
		// in a session, a use-associated name is shadowed by the local
		// definition (the redefinition rule, bib §7)
		sym = c.newSym(host, name, SymProc, cu.Pos)
	} else if placeholder(sym) {
		sym.Kind = SymProc // "public :: f" before the definition of f
	} else if sym.Kind != SymProc || sym.Unit != nil {
		c.errorf(cu.Pos, "Symbol '%s' at (1) is already defined in this scoping unit", name)
		return
	}
	sym.Kind = SymProc
	sym.Unit, sym.Scope = cu, psc
	for _, p := range cu.Params {
		sym.Params = append(sym.Params, p.Name)
	}
	if cu.Kind == syntax.FunctionUnit {
		sym.Flags |= FFunction
		rn := name
		if cu.Result != nil {
			rn = cu.Result.Name
		}
		if r := psc.Names[rn]; r != nil {
			sym.Type = c.typeOf(r, cu.Pos)
			sym.Rank = r.Rank
			sym.Dims = r.Dims
		}
	} else {
		sym.Flags |= FSub
	}
	if host.Kind == syntax.ModuleUnit && host.DefaultPrivate && sym.Flags&FPublic == 0 {
		sym.Flags |= FPrivate
	}
}

func (c *checker) execUnit(u *syntax.Unit, sc *Scope) {
	c.execStmts(sc, u.Body)
	for _, cu := range u.Contains {
		if csc := c.info.Scopes[cu]; csc != nil {
			c.execUnit(cu, csc)
		}
	}
	c.finalize(sc)
}

// finalize gives every symbol of the scope its type: the implicit rules apply
// to a name that was never declared a type (F2018 8.7), and "implicit none"
// makes it an error (F2018 8.7.1).
func (c *checker) finalize(sc *Scope) {
	for _, s := range sc.Order {
		if s.Kind == SymVar && s.Type.Basic == Invalid && s.Flags&(FExternal|FIntrinsic|FFunction|FSub) == 0 {
			c.typeOf(s, s.Pos)
		}
	}
}

// typeOf is the type of a symbol; a symbol that has none yet gets the
// implicit type of its first letter (F2018 8.7.2), or an error under
// "implicit none".
func (c *checker) typeOf(s *Symbol, pos syntax.Pos) Type {
	if s.Type.Basic != Invalid {
		return s.Type
	}
	t, ok := implicitFor(s.Home, s.Name)
	switch {
	case ok:
		s.Type = t
		s.Flags |= FImplicit
	case s.Home != nil && s.Home.Opaque:
		s.Type = Type{Basic: Unknown}
	default:
		c.errorf(pos, "Symbol '%s' at (1) has no IMPLICIT type", s.Name)
		s.Type = Type{Basic: Unknown}
	}
	return s.Type
}

// implicitFor applies the implicit rules of sc to a name (F2018 8.7.1): the
// default mapping is a-h and o-z real, i-n integer.
func implicitFor(sc *Scope, name string) (Type, bool) {
	if sc == nil || name == "" {
		return Type{}, false
	}
	ch := name[0]
	if ch < 'a' || ch > 'z' {
		return Type{}, false
	}
	if t := sc.Implicit[ch-'a']; t != nil {
		return *t, true
	}
	if sc.ImplicitNone {
		return Type{}, false
	}
	if ch >= 'i' && ch <= 'n' {
		return Default(Integer), true
	}
	return Default(Real), true
}

// ---- specification part ----

func isSpec(s syntax.Stmt) bool {
	switch s.(type) {
	case *syntax.Decl, *syntax.AttrStmt, *syntax.ParameterStmt, *syntax.ImplicitStmt,
		*syntax.UseStmt, *syntax.CommonStmt, *syntax.EquivalenceStmt, *syntax.NamelistStmt,
		*syntax.ImportStmt, *syntax.TypeDef, *syntax.Interface, *syntax.EnumDef, *syntax.KeywordStmt, *syntax.ProcBinding:
		return true
	}
	return false
}

func (c *checker) specPass(sc *Scope, body []syntax.Stmt) {
	for _, s := range body {
		if c.stop {
			return
		}
		if isSpec(s) {
			c.specStmt(sc, s)
		}
	}
}

func (c *checker) specStmt(sc *Scope, s syntax.Stmt) {
	switch s := s.(type) {
	case *syntax.ImplicitStmt:
		if s.None {
			sc.ImplicitNone = true
			sc.Implicit = [26]*Type{}
			return
		}
		for _, sp := range s.Specs {
			t := c.typeSpec(sc, sp.Type)
			for _, r := range sp.Ranges {
				for ch := r[0]; ch <= r[1] && ch >= 'a' && ch <= 'z'; ch++ {
					tt := t
					sc.Implicit[ch-'a'] = &tt
				}
			}
		}
	case *syntax.UseStmt:
		c.use(sc, s)
	case *syntax.Decl:
		c.decl(sc, s)
	case *syntax.AttrStmt:
		c.attrStmt(sc, s)
	case *syntax.ParameterStmt:
		for i := range s.Items {
			c.parameterItem(sc, &s.Items[i])
		}
	case *syntax.CommonStmt:
		for _, b := range s.Blocks {
			for i := range b.Items {
				e := &b.Items[i]
				sym := c.declName(sc, e.Name)
				if e.HasDims {
					c.setDims(sc, sym, e.Dims)
				}
			}
		}
	case *syntax.TypeDef:
		c.typeDef(sc, s)
	case *syntax.Interface:
		c.iface(sc, s)
	case *syntax.EnumDef:
		next := int64(0)
		for _, b := range s.Body {
			en, ok := b.(*syntax.Enumerator)
			if !ok {
				continue
			}
			for i := range en.Items {
				e := &en.Items[i]
				if e.Init != nil {
					tv := c.expr(sc, e.Init)
					if tv.Const != nil && tv.Const.Type.Basic == Integer {
						next = tv.Const.I
					}
				}
				sym := c.newSym(sc, e.Name.Name, SymParam, e.Pos)
				sym.Type = Default(Integer)
				sym.Const = &Const{Type: sym.Type, I: next}
				next++
			}
		}
	case *syntax.ProcBinding:
		// procedure(iface) [, attrs] :: names: procedure declaration statement
		// (F2018 15.4.3.6), outside a derived type and an interface block
		var fl Flags
		for _, a := range s.Attrs {
			switch a.Name {
			case "pointer":
				fl |= FPointer
			case "optional":
				fl |= FOptional
			case "public":
				fl |= FPublic
			case "private":
				fl |= FPrivate
			}
		}
		for _, it := range s.Items {
			sym := sc.Names[it.Name]
			if sym == nil || sym.Flags&FUsed != 0 {
				sym = c.newSym(sc, it.Name, SymProc, s.Pos)
			}
			sym.Kind = SymProc
			sym.Flags |= fl | FExternal | FProcDecl
			if sym.Type.Basic == Invalid {
				sym.Type = Type{Basic: Unknown}
			}
		}
	case *syntax.KeywordStmt:
		if s.Word == "private" && sc.Kind == syntax.ModuleUnit {
			sc.DefaultPrivate = true
		}
	}
}

func hasAttr(as []syntax.Attr, name string) bool {
	for i := range as {
		if as[i].Name == name {
			return true
		}
	}
	return false
}

// placeholder reports whether sym was created by an attribute statement
// ("public :: f") before the entity itself was defined.
func placeholder(sym *Symbol) bool {
	return sym.Kind == SymVar && sym.Type.Basic == Invalid && sym.Unit == nil && sym.Flags&(FDummy|FResult|FUsed) == 0
}

// declName returns the symbol for name in sc, creating it as an untyped
// variable (its type may come from a later statement, or the implicit rules).
func (c *checker) declName(sc *Scope, id syntax.Ident) *Symbol {
	if s, ok := sc.Names[id.Name]; ok && s.Flags&FUsed == 0 {
		return s
	}
	return c.newSym(sc, id.Name, SymVar, id.Pos)
}

func (c *checker) attrStmt(sc *Scope, s *syntax.AttrStmt) {
	if len(s.Items) == 0 && (s.Attr == "private" || s.Attr == "public") {
		if s.Attr == "private" && sc.Kind == syntax.ModuleUnit {
			sc.DefaultPrivate = true
		}
		return
	}
	for i := range s.Items {
		e := &s.Items[i]
		if s.Attr == "private" || s.Attr == "public" {
			// "private :: operator(+)" and generic names are symbols too
			sym := sc.Names[e.Name.Name]
			if sym == nil {
				sym = c.newSym(sc, e.Name.Name, SymVar, e.Pos)
			}
			if s.Attr == "private" {
				sym.Flags = sym.Flags&^FPublic | FPrivate
			} else {
				sym.Flags = sym.Flags&^FPrivate | FPublic
			}
			continue
		}
		sym := c.declName(sc, e.Name)
		if e.HasDims {
			c.setDims(sc, sym, e.Dims)
		}
		switch s.Attr {
		case "dimension":
		case "allocatable":
			sym.Flags |= FAllocatable
		case "pointer":
			sym.Flags |= FPointer
		case "target":
			sym.Flags |= FTarget
		case "optional":
			sym.Flags |= FOptional
		case "save":
			sym.Flags |= FSave
		case "value":
			sym.Flags |= FValue
		case "volatile":
			sym.Flags |= FVolatile
		case "protected":
			sym.Flags |= FProtected
		case "external":
			sym.Flags |= FExternal
			if sym.Kind == SymVar {
				sym.Kind = SymProc
			}
		case "intrinsic":
			sym.Flags |= FIntrinsic
			if sym.Kind == SymVar {
				sym.Kind = SymProc
			}
		case "intent":
			if sym.Flags&FDummy == 0 && !sc.Opaque {
				c.errorf(e.Pos, "Symbol '%s' at (1) has INTENT but is not a dummy argument", sym.Name)
			}
			sym.Intent = s.Intent
		}
	}
}

func (c *checker) parameterItem(sc *Scope, e *syntax.Entity) {
	sym := c.declName(sc, e.Name)
	sym.Kind = SymParam
	if e.Init == nil {
		c.errorf(e.Pos, "Parameter '%s' at (1) has no value", sym.Name)
		return
	}
	t := c.typeOf(sym, e.Pos)
	tv := c.expr(sc, e.Init)
	c.setParam(sc, sym, t, tv, e.Pos)
}

// setParam gives a named constant its value: the initialisation expression
// must be a constant expression (F2018 10.1.12) convertible to the type.
func (c *checker) setParam(sc *Scope, sym *Symbol, t Type, tv TV, pos syntax.Pos) {
	c.initShape(pos, sym, tv)
	if sym.Rank == 1 && tv.Values != nil {
		sym.Values = tv.Values
	}
	if t.Lenient() || tv.Type.Lenient() || t.Basic == Derived {
		return // derived-type constants are typed but not folded
	}
	if tv.Const == nil {
		if tv.Rank != 0 || tv.IsConst {
			return // array-valued and subobject constants are typed but not folded
		}
		c.errorf(pos, "Parameter '%s' at (1) is initialised with an expression that is not a constant expression", sym.Name)
		return
	}
	if t.Basic == Character && t.Len < 0 {
		t.Len = tv.Const.Type.Len
		sym.Type.Len = t.Len
	}
	cv, msg := convert(tv.Const, t)
	if msg != "" {
		c.errorf(pos, "Parameter '%s' at (1): %s (%s to %s)", sym.Name, msg, tv.Const.Type, t)
		return
	}
	sym.Const = cv
}

// typeSpec resolves a type specifier (F2018 7.4): the kind is a constant
// expression of a kind supported by the oracle.
func (c *checker) typeSpec(sc *Scope, ts *syntax.TypeSpec) Type {
	if ts == nil {
		return Type{Basic: Unknown}
	}
	var t Type
	switch ts.Name {
	case "integer":
		t = Default(Integer)
	case "real":
		t = Default(Real)
	case "doubleprecision", "double precision":
		return Type{Basic: Real, Kind: 8}
	case "complex":
		t = Default(Complex)
	case "logical":
		t = Default(Logical)
	case "character":
		t = Default(Character)
	case "type", "class":
		if ts.Derived == "*" || ts.Derived == "" {
			return Type{Basic: Unknown}
		}
		switch ts.Derived {
		case "integer", "real", "complex", "logical", "character":
			return c.typeSpec(sc, &syntax.TypeSpec{Span: ts.Span, Name: ts.Derived, Kind: ts.Kind})
		}
		s := sc.Lookup(ts.Derived)
		if s == nil && c.fwdOK {
			// a pointer or allocatable component may name a type defined
			// later in the same specification part (F2018 7.5.4.1)
			s = c.newSym(sc, ts.Derived, SymType, ts.Pos)
			s.Der = &DerivedType{Name: ts.Derived, Bound: map[string]string{}, Scope: sc, Forward: true}
		}
		if s == nil || s.Kind != SymType {
			if sc.Opaque {
				return Type{Basic: Unknown}
			}
			c.errorf(ts.Pos, "Derived type '%s' at (1) is being used before it is defined", ts.Derived)
			return Type{Basic: Unknown}
		}
		return Type{Basic: Derived, Der: s.Der}
	default:
		return Type{Basic: Unknown}
	}
	if ts.Kind != nil {
		if k, ok := c.constInt(sc, ts.Kind, "kind"); ok {
			kind := int(k)
			if ts.KindStar && t.Basic == Complex {
				kind /= 2 // complex*16 is two real(8)
			}
			if !validKind(t.Basic, kind) {
				c.errorf(ts.Pos, "Kind %d not supported for type %s at (1)", k, t.Basic)
			} else {
				t.Kind = kind
			}
		}
	}
	if t.Basic == Character {
		t.Len = 1
		if ts.Len != nil {
			if _, star := ts.Len.(*syntax.Asterisk); star {
				t.Len = -1
			} else {
				t.Len = c.constLen(sc, ts.Len)
			}
		}
	}
	return t
}

// constLen is a character length: a constant when it folds, else -1 (a
// length given by a dummy or automatic expression).
func (c *checker) constLen(sc *Scope, e syntax.Expr) int64 {
	c.deferTyping = true
	tv := c.expr(sc, e)
	c.deferTyping = false
	if tv.Type.Basic == Integer && tv.Const != nil {
		if tv.Const.I < 0 {
			return 0
		}
		return tv.Const.I
	}
	if !tv.Type.Lenient() && tv.Type.Basic != Integer {
		c.errorf(e.P(), "Character length at (1) must be INTEGER, it is %s", tv.Type)
	}
	return -1
}

// constInt folds a scalar integer constant expression; the diagnostic names
// what the expression was for.
func (c *checker) constInt(sc *Scope, e syntax.Expr, what string) (int64, bool) {
	tv := c.expr(sc, e)
	if tv.Type.Lenient() {
		return 0, false
	}
	if tv.Type.Basic != Integer || tv.Const == nil {
		c.errorf(e.P(), "The %s expression at (1) must be a scalar integer constant expression", what)
		return 0, false
	}
	return tv.Const.I, true
}

// decl processes a type declaration statement (F2018 8.2).
func (c *checker) decl(sc *Scope, d *syntax.Decl) {
	proc := d.Type == nil || d.Type.Name == "procedure"
	base := Type{Basic: Unknown}
	if !proc {
		base = c.typeSpec(sc, d.Type)
	}
	var fl Flags
	if d.Type != nil && d.Type.Name == "class" {
		fl |= FPoly
	}
	var intent string
	var adims []syntax.Dim
	hasADims, param := false, false
	for _, a := range d.Attrs {
		switch a.Name {
		case "dimension":
			adims, hasADims = a.Dims, true
		case "parameter":
			param = true
		case "allocatable":
			fl |= FAllocatable
		case "pointer":
			fl |= FPointer
		case "target":
			fl |= FTarget
		case "optional":
			fl |= FOptional
		case "save":
			fl |= FSave
		case "external":
			fl |= FExternal
		case "intrinsic":
			fl |= FIntrinsic
		case "value":
			fl |= FValue
		case "volatile":
			fl |= FVolatile
		case "protected":
			fl |= FProtected
		case "public":
			fl |= FPublic
		case "private":
			fl |= FPrivate
		case "intent":
			intent = a.Intent
			fl |= 0
		}
	}
	for i := range d.Entities {
		e := &d.Entities[i]
		t := base
		if e.CharLen != nil && t.Basic == Character {
			t.Len = c.constLen(sc, e.CharLen)
		}
		sym := sc.Names[e.Name.Name]
		switch {
		case sym == nil || sym.Flags&FUsed != 0:
			sym = c.newSym(sc, e.Name.Name, SymVar, e.Pos)
		case sym.Kind == SymProc && sym.Unit != nil:
			c.errorf(e.Pos, "Symbol '%s' at (1) is already a procedure of this scoping unit", sym.Name)
			continue
		case proc || sym.Type.Basic == Invalid || sym.Has(FImplicit):
			sym.Flags &^= FImplicit
		case sym.Kind == SymType:
			c.errorf(e.Pos, "Symbol '%s' at (1) is already a derived type", sym.Name)
			continue
		default:
			if sym.Flags&FResult != 0 && d.Type != nil && sym.Type.Basic != Invalid {
				c.errorf(e.Pos, "Symbol '%s' at (1) already has basic type of %s", sym.Name, sym.Type)
				continue
			}
			c.errorf(e.Pos, "Symbol '%s' at (1) already has basic type of %s", sym.Name, sym.Type)
			continue
		}
		if !proc {
			sym.Type = t
		}
		sym.Flags |= fl
		if intent != "" {
			if sym.Flags&FDummy == 0 && !sc.Opaque {
				c.errorf(e.Pos, "Symbol '%s' at (1) has INTENT but is not a dummy argument", sym.Name)
			}
			sym.Intent = intent
		}
		if fl&(FExternal|FIntrinsic) != 0 {
			sym.Kind = SymProc
		}
		if proc {
			sym.Kind = SymProc // procedure(iface) :: p
			sym.Flags |= FProcDecl
		}
		switch {
		case e.HasDims:
			c.setDims(sc, sym, e.Dims)
		case hasADims:
			c.setDims(sc, sym, adims)
		}
		switch {
		case param:
			sym.Kind = SymParam
			if e.Init == nil {
				c.errorf(e.Pos, "Parameter '%s' at (1) has no value", sym.Name)
			} else {
				tv := c.expr(sc, e.Init)
				c.setParam(sc, sym, sym.Type, tv, e.Pos)
			}
		case e.Init != nil && !e.PtrInit:
			tv := c.expr(sc, e.Init)
			c.assignable(e.Pos, sym.Type, sym.Rank, tv, "initialization")
			c.initShape(e.Pos, sym, tv)
			sym.Flags |= FSave
		case e.Init != nil:
			c.expr(sc, e.Init)
		}
		if sc.Kind == syntax.ModuleUnit && sc.DefaultPrivate && sym.Flags&FPublic == 0 {
			sym.Flags |= FPrivate
		}
	}
}

// setDims records the array spec of a symbol (F2018 8.5.8): explicit-shape
// bounds are constant or, in a procedure, specification expressions.
func (c *checker) setDims(sc *Scope, sym *Symbol, dims []syntax.Dim) {
	sym.Dims, sym.Rank = c.arraySpec(sc, dims)
}

// arraySpec evaluates an array spec: the dimensions with their constant bounds
// and the rank (-1 for an assumed-rank spec, F2018 8.5.8.7).
func (c *checker) arraySpec(sc *Scope, dims []syntax.Dim) ([]Dim, int) {
	var out []Dim
	for i := range dims {
		d := &dims[i]
		if d.Rank {
			return nil, -1
		}
		var dm Dim
		switch {
		case d.Star && d.Lo == nil:
			dm.Assumed, dm.Lo, dm.LoKnown = true, 1, true
		case d.Colon && d.Lo == nil && d.Hi == nil:
			dm.Deferred = true
		default:
			if d.Lo != nil {
				if v, ok := c.boundValue(sc, d.Lo); ok {
					dm.Lo, dm.LoKnown = v, true
				}
			} else {
				dm.Lo, dm.LoKnown = 1, true
			}
			if d.Hi != nil && !d.Star {
				if v, ok := c.boundValue(sc, d.Hi); ok {
					dm.Hi, dm.HiKnown = v, true
				}
			}
			if d.Star {
				dm.Assumed = true
			}
			if d.Colon && d.Hi == nil && !d.Star {
				dm.Deferred, dm.AssumedShape = true, true
			}
		}
		out = append(out, dm)
	}
	return out, len(out)
}

func (c *checker) boundValue(sc *Scope, e syntax.Expr) (int64, bool) {
	c.deferTyping = true
	tv := c.expr(sc, e)
	c.deferTyping = false
	if !tv.Type.Lenient() && tv.Type.Basic != Integer {
		c.errorf(e.P(), "Array bound at (1) must be INTEGER, it is %s", tv.Type)
		return 0, false
	}
	if tv.Const != nil && tv.Const.Type.Basic == Integer {
		return tv.Const.I, true
	}
	return 0, false
}

// typeDef records a derived type definition (F2018 7.5.2).
func (c *checker) typeDef(sc *Scope, td *syntax.TypeDef) {
	dt := &DerivedType{Name: td.Ident.Name, Bound: map[string]string{}, Scope: sc}
	var sym *Symbol
	if old, dup := sc.Names[dt.Name]; dup && old.Flags&FUsed == 0 {
		switch {
		case old.Kind == SymType && old.Der != nil && old.Der.Forward:
			dt = old.Der // referenced by a pointer component before this definition
			dt.Forward = false
			sym = old
		case placeholder(old):
			sym = old
			sym.Kind = SymType
		default:
			c.errorf(td.Ident.Pos, "Derived type '%s' at (1) is already defined", dt.Name)
			return
		}
	} else {
		sym = c.newSym(sc, dt.Name, SymType, td.Ident.Pos)
	}
	sym.Der = dt
	dt.Bound = map[string]string{}
	if sc.Kind == syntax.ModuleUnit && sc.DefaultPrivate && sym.Flags&FPublic == 0 {
		sym.Flags |= FPrivate
	}
	for _, a := range td.Attrs {
		switch a.Name {
		case "extends":
			if len(a.Args) == 1 {
				if id, ok := a.Args[0].(*syntax.Ident); ok {
					p := sc.Lookup(id.Name)
					if p == nil || p.Kind != SymType {
						if !sc.Opaque {
							c.errorf(a.Pos, "Parent type '%s' at (1) is not a derived type", id.Name)
						}
					} else {
						dt.Parent = p.Der
					}
				}
			}
		case "private":
			sym.Flags = sym.Flags&^FPublic | FPrivate
		case "public":
			sym.Flags = sym.Flags&^FPrivate | FPublic
		}
	}
	for _, b := range td.Body {
		switch b := b.(type) {
		case *syntax.KeywordStmt:
			if b.Word == "sequence" {
				dt.Sequence = true
			}
		case *syntax.ProcBinding:
			// procedure(iface), pointer :: p: a procedure pointer component
			if hasAttr(b.Attrs, "pointer") {
				for _, it := range b.Items {
					dt.Components = append(dt.Components, &Component{Name: it.Name, Type: Type{Basic: Unknown}, Pointer: true})
				}
			}
		case *syntax.Decl:
			t := Type{Basic: Unknown}
			if b.Type != nil && b.Type.Name != "procedure" {
				c.fwdOK = hasAttr(b.Attrs, "pointer") || hasAttr(b.Attrs, "allocatable")
				t = c.typeSpec(sc, b.Type)
				c.fwdOK = false
			}
			var fl Flags
			var adims []syntax.Dim
			hasADims := false
			for _, a := range b.Attrs {
				switch a.Name {
				case "pointer":
					fl |= FPointer
				case "allocatable":
					fl |= FAllocatable
				case "dimension":
					adims, hasADims = a.Dims, true
				}
			}
			for i := range b.Entities {
				e := &b.Entities[i]
				ct := t
				if e.CharLen != nil && ct.Basic == Character {
					ct.Len = c.constLen(sc, e.CharLen)
				}
				if dt.Find(e.Name.Name) != nil {
					c.errorf(e.Pos, "Component '%s' at (1) is already declared in type '%s'", e.Name.Name, dt.Name)
					continue
				}
				comp := &Component{Name: e.Name.Name, Type: ct, Pointer: fl&FPointer != 0, Alloc: fl&FAllocatable != 0}
				dims := adims
				if e.HasDims {
					dims, hasADims = e.Dims, true
				}
				if hasADims {
					comp.Dims, comp.Rank = c.arraySpec(sc, dims)
				}
				if e.Init != nil && !e.PtrInit {
					c.expr(sc, e.Init)
				}
				dt.Components = append(dt.Components, comp)
			}
		}
	}
	for _, b := range td.Bound {
		pb, ok := b.(*syntax.ProcBinding)
		if !ok || pb.Kind != "procedure" {
			continue
		}
		for _, it := range pb.Items {
			target := it.Target
			if target == "" {
				target = it.Name
			}
			dt.Bound[it.Name] = target
		}
	}
	for _, b := range td.Bound { // generic :: g => specific, ...
		if pb, ok := b.(*syntax.ProcBinding); ok && pb.Kind == "generic" {
			for _, it := range pb.Items {
				target := it.Target
				if t, ok := dt.Bound[target]; ok {
					target = t
				}
				if target != "" {
					dt.Bound[it.Name] = target
				}
			}
		}
	}
}

// iface processes an interface block (F2018 15.4.3): its bodies declare
// explicit interfaces; a named one is a generic whose specifics are the bodies
// and the "module procedure" names.
func (c *checker) iface(sc *Scope, in *syntax.Interface) {
	var gen *Symbol
	if in.Generic != "" {
		gen = sc.Names[in.Generic]
		if gen == nil || gen.Flags&FUsed != 0 && gen.Kind != SymGeneric {
			gen = c.newSym(sc, in.Generic, SymGeneric, in.Pos)
		} else if placeholder(gen) {
			gen.Kind = SymGeneric // "public :: g" before the interface block
		} else if gen.Kind != SymGeneric {
			// a specific procedure of the same name becomes a specific of
			// the generic of that name (F2018 15.4.3.4.1)
			old := *gen
			gen.Kind, gen.Specifics = SymGeneric, []*Symbol{&old}
		}
		if sc.Kind == syntax.ModuleUnit && sc.DefaultPrivate && gen.Flags&FPublic == 0 {
			gen.Flags |= FPrivate
		}
	}
	var names []string
	for _, b := range in.Body {
		switch b := b.(type) {
		case *syntax.Unit:
			isc := c.declareUnit(b, sc)
			s := &Symbol{Name: b.Ident.Name, Kind: SymProc, Pos: b.Pos, Home: sc, Unit: b, Scope: isc}
			for _, p := range b.Params {
				s.Params = append(s.Params, p.Name)
			}
			if b.Kind == syntax.FunctionUnit {
				s.Flags |= FFunction
				rn := b.Ident.Name
				if b.Result != nil {
					rn = b.Result.Name
				}
				if r := isc.Names[rn]; r != nil {
					s.Type = c.typeOf(r, b.Pos)
					s.Rank, s.Dims = r.Rank, r.Dims
				}
			} else {
				s.Flags |= FSub
			}
			if gen != nil {
				gen.Specifics = append(gen.Specifics, s)
			} else if _, dup := sc.Names[s.Name]; !dup {
				c.nsyms++
				sc.Names[s.Name] = s
				sc.Order = append(sc.Order, s)
			} else {
				ex := sc.Names[s.Name]
				if ex.Kind == SymVar || ex.Kind == SymProc && ex.Unit == nil {
					old := *ex
					*ex = *s // "external f" before its interface body
					// the attributes given before it stay (optional :: a; the dummy flag)
					ex.Flags |= old.Flags &^ (FImplicit | FFunction | FSub | FExternal)
					ex.Intent = old.Intent
				}
			}
		case *syntax.ProcBinding:
			for _, it := range b.Items {
				names = append(names, it.Name)
			}
		}
	}
	if gen != nil && len(names) > 0 {
		c.pending = append(c.pending, pendingGeneric{sc, gen, names, in.Pos})
	}
}

// resolvePending resolves the "module procedure" names of the generics of sc
// to the procedures its contained units declared.
func (c *checker) resolvePending(final bool) {
	var keep []pendingGeneric
	for _, p := range c.pending {
		var left []string
		for _, n := range p.names {
			s := p.host.Procs[n]
			if s == nil {
				s = p.host.Lookup(n)
			}
			if s == nil || s.Kind != SymProc {
				if !final {
					left = append(left, n) // a procedure of an enclosing unit, declared later
				} else if !p.host.Opaque {
					c.errorf(p.pos, "Procedure '%s' in generic interface '%s' at (1) is neither function nor subroutine", n, p.gen.Name)
				}
				continue
			}
			p.gen.Specifics = append(p.gen.Specifics, s)
		}
		if len(left) > 0 {
			p.names = left
			keep = append(keep, p)
		}
	}
	c.pending = keep
}

// ---- use association (F2018 14.2.2) ----

var isoFortranEnv = map[string]int64{
	"int8": 1, "int16": 2, "int32": 4, "int64": 8, "real32": 4, "real64": 8, "real128": 16,
	"input_unit": 5, "output_unit": 6, "error_unit": 0, "iostat_end": -1, "iostat_eor": -2,
	"numeric_storage_size": 32, "character_storage_size": 8, "file_storage_size": 8,
	"iostat_inquire_internal_unit": -1, "stat_failed_image": 6, "stat_locked": 2,
	"stat_locked_other_image": 3, "stat_stopped_image": 4, "stat_unlocked": 5, "stat_unlocked_failed_image": 7,
}

var isoCBinding = map[string]int64{
	"c_int": 4, "c_short": 2, "c_long": 8, "c_long_long": 8, "c_signed_char": 1, "c_size_t": 8,
	"c_int8_t": 1, "c_int16_t": 2, "c_int32_t": 4, "c_int64_t": 8, "c_int_least8_t": 1,
	"c_int_least16_t": 2, "c_int_least32_t": 4, "c_int_least64_t": 8, "c_int_fast8_t": 1,
	"c_int_fast16_t": 8, "c_int_fast32_t": 8, "c_int_fast64_t": 8, "c_intmax_t": 8, "c_intptr_t": 8,
	"c_float": 4, "c_double": 8, "c_long_double": 16, "c_float_complex": 4, "c_double_complex": 8,
	"c_long_double_complex": 16, "c_bool": 1, "c_char": 1,
}

func (c *checker) intrinsicModule(sc *Scope, name string) *Scope {
	m := newScope(nil, nil)
	m.Name = name
	switch name {
	case "iso_fortran_env", "iso_c_binding":
		tab := isoFortranEnv
		if name == "iso_c_binding" {
			tab = isoCBinding
			for _, n := range []string{"c_ptr", "c_funptr"} {
				s := c.newSym(m, n, SymType, 0)
				s.Der = &DerivedType{Name: n, Bound: map[string]string{}, Scope: m}
			}
			for _, n := range []string{"c_loc", "c_funloc", "c_associated", "c_f_pointer", "c_f_procpointer", "c_sizeof"} {
				s := c.newSym(m, n, SymProc, 0)
				s.Type = Type{Basic: Unknown}
				s.Flags |= FFunction | FSub // function or subroutine: not checked
			}
			s := c.newSym(m, "c_null_char", SymParam, 0)
			s.Type = Type{Basic: Character, Kind: 1, Len: 1}
			s.Const = &Const{Type: s.Type, S: "\x00"}
			for _, n := range []string{"c_null_ptr", "c_null_funptr"} {
				s := c.newSym(m, n, SymVar, 0)
				s.Type = Type{Basic: Unknown}
			}
			for _, n := range []string{"c_new_line", "c_carriage_return", "c_horizontal_tab", "c_vertical_tab", "c_alert", "c_backspace", "c_form_feed"} {
				s := c.newSym(m, n, SymParam, 0)
				s.Type = Type{Basic: Character, Kind: 1, Len: 1}
			}
		} else {
			for _, n := range []string{"character_kinds", "integer_kinds", "logical_kinds", "real_kinds"} {
				s := c.newSym(m, n, SymParam, 0)
				s.Type, s.Rank = Default(Integer), 1
			}
			for _, n := range []string{"compiler_version", "compiler_options"} {
				s := c.newSym(m, n, SymProc, 0)
				s.Type = Type{Basic: Character, Kind: 1, Len: -1}
				s.Flags |= FFunction
			}
			for _, n := range []string{"event_type", "lock_type", "team_type"} {
				s := c.newSym(m, n, SymType, 0)
				s.Der = &DerivedType{Name: n, Bound: map[string]string{}, Scope: m}
			}
		}
		for n, v := range tab {
			s := c.newSym(m, n, SymParam, 0)
			s.Type = Default(Integer)
			s.Const = &Const{Type: s.Type, I: v}
		}
		return m
	}
	return nil
}

func (c *checker) use(sc *Scope, u *syntax.UseStmt) {
	mod := c.info.Modules[u.Module.Name]
	if mod == nil && (u.Nature == "intrinsic" || u.Nature == "") {
		if m, ok := c.intrMods[u.Module.Name]; ok {
			mod = m
		} else {
			mod = c.intrinsicModule(sc, u.Module.Name)
			if c.intrMods == nil {
				c.intrMods = map[string]*Scope{}
			}
			c.intrMods[u.Module.Name] = mod
		}
	}
	if mod == nil {
		// unknown (ieee_arithmetic, omp_lib, a module of another file): its
		// names are not known, so an unresolved name is not an error here
		sc.Opaque = true
		return
	}
	if mod.Opaque {
		sc.Opaque = true
	}
	imp := func(local string, s *Symbol) {
		cp := *s
		cp.Flags |= FUsed
		cp.Flags &^= FPrivate | FPublic
		cp.Origin = s
		if s.Origin != nil {
			cp.Origin = s.Origin
		}
		cp.Specifics = slices.Clone(s.Specifics) // a local extension must not write into the module's array
		cp.Home = sc
		if old, ok := sc.Names[local]; ok {
			if old.Flags&FUsed != 0 && old.Kind == SymGeneric && cp.Kind == SymGeneric {
				// generics of one name from two modules are one generic (F2018 15.4.3.4.1)
				for _, sp := range cp.Specifics {
					if !slices.ContainsFunc(old.Specifics, func(x *Symbol) bool { return x == sp || x.Unit != nil && x.Unit == sp.Unit }) {
						old.Specifics = append(old.Specifics, sp)
					}
				}
				return
			}
			if old.Flags&FUsed != 0 && old.Origin == cp.Origin {
				return // the same entity twice is allowed (F2018 14.2.2)
			}
			*old = cp
			return
		}
		c.nsyms++
		if c.nsyms > c.lim.MaxSymbols {
			c.errorf(u.Pos, "too many symbols (more than %d): refused", c.lim.MaxSymbols)
			c.stop = true
			return
		}
		sc.Names[local] = &cp
		sc.Order = append(sc.Order, &cp)
	}
	public := func(s *Symbol) bool {
		return s.Flags&FPrivate == 0 && !(mod.DefaultPrivate && s.Flags&FPublic == 0)
	}
	if !u.Only {
		renamed := map[string]bool{} // a renamed entity is no longer known by its own name (F2018 14.2.2)
		for _, it := range u.Items {
			if it.Op == "" && it.Remote.Name != "" && it.Local.Name != "" && it.Local.Name != it.Remote.Name {
				renamed[it.Remote.Name] = true
			}
		}
		for _, s := range mod.Order {
			if public(s) && !renamed[s.Name] {
				imp(s.Name, s)
			}
		}
	}
	for _, it := range u.Items {
		remote := it.Remote.Name
		if remote == "" {
			remote = it.Local.Name
		}
		local := it.Local.Name
		if local == "" {
			local = remote
		}
		if it.Op != "" {
			// the parser gives the generic spec whole: "operator(+)", and for
			// a rename the local and the remote spec in Local and Remote
			remote, local = it.Op, it.Op
			if it.Op == "rename" {
				local, remote = it.Local.Name, it.Remote.Name
			}
			if s, ok := mod.Names[remote]; ok {
				imp(local, s)
			}
			continue
		}
		s := mod.Names[remote]
		if s == nil || !public(s) {
			c.errorf(it.Pos, "Symbol '%s' referenced at (1) not found in module '%s'", remote, u.Module.Name)
			continue
		}
		imp(local, s)
	}
}
