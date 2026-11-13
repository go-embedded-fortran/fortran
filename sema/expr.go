package sema

import (
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-embedded-fortran/fortran/syntax"
)

var unknownTV = TV{Type: Type{Basic: Unknown}, Rank: RankUnknown}

// expr types an expression (F2018 clause 10) and records the annotation.
func (c *checker) expr(sc *Scope, e syntax.Expr) TV {
	if e == nil || c.stop {
		return unknownTV
	}
	c.depth++
	defer func() { c.depth-- }()
	if c.depth > c.lim.MaxDepth {
		c.errorf(e.P(), "expression nested deeper than the analyser's bound")
		c.stop = true
		return unknownTV
	}
	tv := c.expr1(sc, e)
	c.info.Types[e] = tv
	return tv
}

func (c *checker) expr1(sc *Scope, e syntax.Expr) TV {
	switch e := e.(type) {
	case *syntax.IntLit:
		return c.intLit(sc, e)
	case *syntax.RealLit:
		return c.realLit(sc, e)
	case *syntax.LogicalLit:
		k := c.litKind(sc, e.Kind, e.Pos, Logical)
		return TV{Type: Type{Basic: Logical, Kind: k}, Const: &Const{Type: Type{Basic: Logical, Kind: k}, B: e.Value}}
	case *syntax.StringLit:
		k := c.litKind(sc, e.Kind, e.Pos, Character)
		t := Type{Basic: Character, Kind: k, Len: int64(utf8.RuneCountInString(e.Value))}
		return TV{Type: t, Const: &Const{Type: t, S: e.Value}}
	case *syntax.BozLit:
		return TV{Type: Default(Integer)}
	case *syntax.ComplexLit:
		re, im := c.expr(sc, e.Re), c.expr(sc, e.Im)
		if re.Type.Lenient() || im.Type.Lenient() {
			return TV{Type: Type{Basic: Complex, Kind: DefaultReal}}
		}
		for _, p := range []TV{re, im} {
			if p.Type.Basic != Integer && p.Type.Basic != Real {
				c.errorf(e.Pos, "Complex constant at (1) has a %s part", p.Type)
				return TV{Type: Type{Basic: Complex, Kind: DefaultReal}}
			}
		}
		k := DefaultReal
		for _, p := range []TV{re, im} {
			if p.Type.Basic == Real && p.Type.Kind > k {
				k = p.Type.Kind
			}
		}
		t := Type{Basic: Complex, Kind: k}
		tv := TV{Type: t}
		if re.Const != nil && im.Const != nil {
			tv.Const = &Const{Type: t, C: complex(roundKind(re.Const.float(), k), roundKind(im.Const.float(), k))}
		}
		return tv
	case *syntax.Ident:
		return c.identExpr(sc, e)
	case *syntax.Paren:
		x := c.expr(sc, e.X)
		x.Var, x.Sym = false, nil
		return x
	case *syntax.Unary:
		return c.unary(sc, e)
	case *syntax.Binary:
		return c.binary(sc, e)
	case *syntax.Index:
		return c.index(sc, e)
	case *syntax.Component:
		return c.component(sc, e)
	case *syntax.Triplet:
		for _, p := range []syntax.Expr{e.Lo, e.Hi, e.Stride} {
			if p == nil {
				continue
			}
			tv := c.expr(sc, p)
			if !tv.Type.Lenient() && tv.Type.Basic != Integer {
				c.errorf(p.P(), "Array section bound at (1) must be INTEGER, it is %s", tv.Type)
			}
		}
		return TV{Type: Default(Integer), Rank: 1}
	case *syntax.Asterisk:
		return unknownTV
	case *syntax.ArrayCons:
		return c.arrayCons(sc, e)
	case *syntax.ImpliedDo:
		return c.impliedDo(sc, e)
	}
	return unknownTV
}

func (c *checker) litKind(sc *Scope, kind string, pos syntax.Pos, b Basic) int {
	if kind == "" {
		return Default(b).Kind
	}
	if n, err := strconv.Atoi(kind); err == nil {
		if !validKind(b, n) {
			c.errorf(pos, "Kind %d not supported for type %s at (1)", n, b)
			return Default(b).Kind
		}
		return n
	}
	if s := sc.Lookup(strings.ToLower(kind)); s != nil && s.Kind == SymParam && s.Const != nil && s.Const.Type.Basic == Integer {
		if !validKind(b, int(s.Const.I)) {
			c.errorf(pos, "Kind %d not supported for type %s at (1)", s.Const.I, b)
			return Default(b).Kind
		}
		return int(s.Const.I)
	} else if s == nil && !sc.Opaque {
		c.errorf(pos, "Symbol '%s' at (1) has no IMPLICIT type", kind)
	}
	return Default(b).Kind
}

func (c *checker) intLit(sc *Scope, e *syntax.IntLit) TV {
	k := c.litKind(sc, e.Kind, e.Pos, Integer)
	t := Type{Basic: Integer, Kind: k}
	v, ok := new(big.Int).SetString(strings.ReplaceAll(e.Text, "_", ""), 10)
	if !ok {
		return TV{Type: t}
	}
	if !v.IsInt64() {
		if e.Kind == "" {
			c.errorf(e.Pos, "Integer too big for its kind at (1)")
		}
		return TV{Type: t}
	}
	n := v.Int64()
	if e.Kind == "" && !fitsInt(n, k) {
		// gfortran refuses a literal above huge(0); the oracle's rule is
		// kept as a wider kind, never a silent wrap.
		t.Kind = 8
	} else if e.Kind != "" && !fitsInt(n, k) && k != 16 {
		c.errorf(e.Pos, "Integer too big for its kind at (1)")
		return TV{Type: t}
	}
	return TV{Type: t, Const: &Const{Type: t, I: n}}
}

func (c *checker) realLit(sc *Scope, e *syntax.RealLit) TV {
	k := DefaultReal
	txt := strings.ReplaceAll(e.Text, "_", "")
	if i := strings.IndexAny(txt, "dq"); i >= 0 {
		if txt[i] == 'd' {
			k = 8
		} else {
			k = 16
		}
		txt = txt[:i] + "e" + txt[i+1:]
	}
	if e.Kind != "" {
		k = c.litKind(sc, e.Kind, e.Pos, Real)
	}
	t := Type{Basic: Real, Kind: k}
	f, err := strconv.ParseFloat(txt, 64)
	if err != nil && f == 0 {
		return TV{Type: t}
	}
	return TV{Type: t, Const: &Const{Type: t, F: roundKind(f, k)}}
}

// identExpr resolves a name in an expression (F2018 19.5).
func (c *checker) identExpr(sc *Scope, id *syntax.Ident) TV {
	if strings.HasPrefix(id.Name, "%") { // %loc, %val, %ref
		return unknownTV
	}
	sym := sc.Lookup(id.Name)
	if sym == nil {
		if sc.Opaque {
			return unknownTV
		}
		sym = c.implicitVar(sc, id.Name, id.Pos)
	}
	c.info.Uses[id] = sym
	return c.symTV(sc, sym, id.Pos)
}

// implicitVar creates the variable an undeclared name denotes (F2018
// 8.7.1), in the nearest scoping unit.
func (c *checker) implicitVar(sc *Scope, name string, pos syntax.Pos) *Symbol {
	us := sc.unit()
	s := c.newSym(us, name, SymVar, pos)
	c.typeOf(s, pos)
	return s
}

func (c *checker) symTV(sc *Scope, sym *Symbol, pos syntax.Pos) TV {
	switch sym.Kind {
	case SymVar:
		t := sym.Type
		if t.Basic == Invalid {
			if sym.Flags&(FExternal|FFunction|FSub) != 0 {
				return TV{Type: Type{Basic: Unknown}, Rank: RankUnknown, Sym: sym}
			}
			t = c.typeOf(sym, pos)
		}
		return TV{Type: t, Rank: sym.Rank, Sym: sym, Var: true}
	case SymParam:
		t := sym.Type
		if t.Basic == Invalid {
			t = c.typeOf(sym, pos)
		}
		return TV{Type: t, Rank: sym.Rank, Const: sym.Const, Sym: sym}
	case SymProc:
		// a procedure name as an actual argument or a procedure pointer target
		return TV{Type: Type{Basic: Unknown}, Rank: RankUnknown, Sym: sym}
	case SymGeneric:
		return TV{Type: Type{Basic: Unknown}, Rank: RankUnknown, Sym: sym}
	}
	c.errorf(pos, "Symbol '%s' at (1) is not a variable or a constant", sym.Name)
	return unknownTV
}

func combineRank(a, b int) int {
	switch {
	case a < 0 || b < 0:
		return RankUnknown
	case a > b:
		return a
	}
	return b
}

func (c *checker) unary(sc *Scope, e *syntax.Unary) TV {
	x := c.expr(sc, e.X)
	tv := TV{Rank: x.Rank}
	if x.Type.Lenient() {
		tv.Type = x.Type
		tv.Type.Basic = Unknown
		return tv
	}
	switch e.Op {
	case "+", "-":
		if !x.Type.Numeric() {
			c.errorf(e.Pos, "Operand of unary numeric operator '%s' at (1) is %s", e.Op, x.Type)
			tv.Type = Type{Basic: Unknown}
			return tv
		}
		tv.Type = x.Type
	case ".not.":
		if x.Type.Basic != Logical && x.Type.Basic != Integer {
			c.errorf(e.Pos, "Operand of .not. operator at (1) is %s", x.Type)
			tv.Type = Type{Basic: Unknown}
			return tv
		}
		tv.Type = x.Type
	default: // a defined unary operator: generic resolution decides
		tv.Type = Type{Basic: Unknown}
		return tv
	}
	if x.Const != nil {
		r, msg := c.foldUnary(e.Op, x.Const)
		if msg != "" {
			c.errorf(e.Pos, "%s at (1)", msg)
		} else if r != nil {
			tv.Const = r
		}
	}
	return tv
}

func (c *checker) binary(sc *Scope, e *syntax.Binary) TV {
	x, y := c.expr(sc, e.X), c.expr(sc, e.Y)
	tv := TV{Rank: combineRank(x.Rank, y.Rank)}
	if x.Rank > 0 && y.Rank > 0 && x.Rank != y.Rank {
		c.errorf(e.Pos, "Shapes for operands at (1) and (2) are not conformable: ranks %d and %d", x.Rank, y.Rank)
	}
	unknown := func() TV { tv.Type = Type{Basic: Unknown}; return tv }
	if x.Type.Lenient() || y.Type.Lenient() {
		return unknown()
	}
	if x.Type.Basic == Derived || y.Type.Basic == Derived {
		return unknown() // a defined operator: generic resolution decides
	}
	bad := func(what string) TV {
		c.errorf(e.Pos, "Operands of %s '%s' at (1) are %s/%s", what, e.Op, x.Type, y.Type)
		return unknown()
	}
	switch e.Op {
	case "+", "-", "*", "/", "**":
		if !x.Type.Numeric() || !y.Type.Numeric() {
			return bad("binary numeric operator")
		}
		tv.Type = Promote(x.Type, y.Type)
		if e.Op == "**" && y.Type.Basic < x.Type.Basic {
			tv.Type = x.Type // F2018 Table 10.2: a real or complex base keeps its kind
		}
	case "//":
		if x.Type.Basic != Character || y.Type.Basic != Character {
			return bad("concatenation operator")
		}
		tv.Type = Type{Basic: Character, Kind: x.Type.Kind, Len: -1}
		if x.Type.Len >= 0 && y.Type.Len >= 0 {
			tv.Type.Len = x.Type.Len + y.Type.Len
		}
	case "==", "/=", "<", "<=", ">", ">=":
		switch {
		case x.Type.Numeric() && y.Type.Numeric():
			if (x.Type.Basic == Complex || y.Type.Basic == Complex) && e.Op != "==" && e.Op != "/=" {
				return bad("comparison operator")
			}
		case x.Type.Basic == Character && y.Type.Basic == Character:
		case x.Type.Basic == Logical && y.Type.Basic == Logical && (e.Op == "==" || e.Op == "/="):
			// F2018 requires .eqv.; gfortran accepts it as an extension
		default:
			return bad("comparison operator")
		}
		tv.Type = Default(Logical)
	case ".and.", ".or.", ".eqv.", ".neqv.":
		switch {
		case x.Type.Basic == Logical && y.Type.Basic == Logical:
			tv.Type = Type{Basic: Logical, Kind: max(x.Type.Kind, y.Type.Kind)}
		case x.Type.Basic == Integer && y.Type.Basic == Integer:
			tv.Type = Type{Basic: Integer, Kind: max(x.Type.Kind, y.Type.Kind)} // bitwise, a GNU extension
		default:
			return bad("logical operator")
		}
	default:
		return unknown() // ".foo.": defined binary operator
	}
	if x.Const != nil && y.Const != nil && tv.Rank == 0 {
		r, msg := c.foldBinary(e.Op, x.Const, y.Const)
		if msg != "" {
			c.errorf(e.Pos, "%s at (1)", msg)
		} else if r != nil {
			tv.Const = r
			if r.Type.Basic == tv.Type.Basic && tv.Type.Basic != Character {
				tv.Type = r.Type
			}
		}
	}
	return tv
}

// args types the actual arguments or subscripts of a reference.
func (c *checker) args(sc *Scope, as []syntax.Arg) []argTV {
	out := make([]argTV, 0, len(as))
	for i := range as {
		a := &as[i]
		if a.Value == nil {
			continue
		}
		out = append(out, argTV{a.Keyword, c.expr(sc, a.Value)})
	}
	return out
}

// subscripts checks the subscripts of an array reference (F2018 9.5.3) and
// returns the rank of the result: each section triplet or vector subscript
// adds one dimension.
func (c *checker) subscripts(sc *Scope, e *syntax.Index, base TV, name string) int {
	rank := 0
	n := 0
	for i := range e.Args {
		a := &e.Args[i]
		if a.Value == nil {
			continue
		}
		n++
		tv := c.expr(sc, a.Value)
		if _, tr := a.Value.(*syntax.Triplet); tr {
			rank++
			continue
		}
		if !tv.Type.Lenient() && tv.Type.Basic != Integer {
			c.errorf(a.Value.P(), "Array index at (1) must be of INTEGER type, found %s", tv.Type)
		}
		if tv.Rank > 0 {
			rank++
		} else if tv.Rank < 0 {
			rank = RankUnknown
		}
	}
	if base.Rank > 0 && n != base.Rank {
		c.errorf(e.Pos, "Rank mismatch in array reference at (1) (%d/%d)", n, base.Rank)
	}
	return rank
}

// index types X(args): an array element or section, a substring, a
// function reference or a structure constructor (F2018 9.5, 9.4.1, 15.5,
// 7.5.10); the parser cannot tell, the symbol does.
func (c *checker) index(sc *Scope, e *syntax.Index) TV {
	if id, ok := e.X.(*syntax.Ident); ok {
		return c.indexName(sc, e, id)
	}
	base := c.expr(sc, e.X)
	return c.indexValue(sc, e, base)
}

// indexValue is X(args) when X is a value: an element or section of an array,
// or a substring.
func (c *checker) indexValue(sc *Scope, e *syntax.Index, base TV) TV {
	switch {
	case base.Call && isComponent(e.X): // a%method(args): a type-bound procedure reference
		c.args(sc, e.Args)
		return base
	case base.Type.Lenient() && base.Rank == RankUnknown:
		c.args(sc, e.Args)
		return unknownTV
	case base.Rank != 0 && base.Rank != RankUnknown:
		r := c.subscripts(sc, e, base, "")
		tv := base
		tv.Rank, tv.Const, tv.Call = r, nil, false
		return tv
	case base.Type.Basic == Character:
		for i := range e.Args {
			if v := e.Args[i].Value; v != nil {
				if _, tr := v.(*syntax.Triplet); !tr {
					c.errorf(v.P(), "Substring at (1) needs a ':' range")
				}
				c.expr(sc, v)
			}
		}
		t := base.Type
		t.Len = -1
		return TV{Type: t, Var: base.Var, Sym: base.Sym}
	case base.Type.Lenient():
		c.args(sc, e.Args)
		return TV{Type: Type{Basic: Unknown}, Rank: base.Rank}
	}
	c.errorf(e.Pos, "Unclassifiable expression at (1): %s is neither an array nor a character value", base.Type)
	c.args(sc, e.Args)
	return unknownTV
}

func (c *checker) indexName(sc *Scope, e *syntax.Index, id *syntax.Ident) TV {
	name := id.Name
	if strings.HasPrefix(name, "%") {
		c.args(sc, e.Args)
		return unknownTV
	}
	sym := sc.Lookup(name)
	if sym != nil && sym.Kind == SymProc && sym.Flags&FIntrinsic != 0 && sym.Unit == nil {
		sym = nil // "intrinsic :: sin": the intrinsic
	}
	if sym == nil {
		if in, ok := intrinsicFuncs[name]; ok {
			return c.intrinsicCall(sc, e, name, in)
		}
		if sc.Opaque {
			c.args(sc, e.Args)
			return unknownTV
		}
		// an undeclared name followed by '(': an external function with an
		// implicit type (F2018 15.4.3.6, 8.7), an error under "implicit none"
		us := sc.unit()
		s := c.newSym(us, name, SymProc, id.Pos)
		s.Flags |= FFunction | FExternal
		t, ok := implicitFor(us, name)
		if !ok {
			c.errorf(id.Pos, "Function '%s' at (1) has no IMPLICIT type", name)
			t = Type{Basic: Unknown}
		}
		s.Type = t
		c.info.Uses[id] = s
		c.args(sc, e.Args)
		return TV{Type: t, Call: true, Sym: s}
	}
	c.info.Uses[id] = sym
	switch sym.Kind {
	case SymVar:
		switch {
		case sym.Rank != 0:
			t := c.typeOf(sym, id.Pos)
			base := TV{Type: t, Rank: sym.Rank, Sym: sym, Var: true}
			r := c.subscripts(sc, e, base, name)
			return TV{Type: t, Rank: r, Sym: sym, Var: true}
		case sym.Type.Basic == Character && allTriplets(e.Args):
			for i := range e.Args {
				if v := e.Args[i].Value; v != nil {
					c.expr(sc, v)
				}
			}
			t := sym.Type
			t.Len = -1
			return TV{Type: t, Sym: sym, Var: true}
		case sym.Flags&FResult != 0 && sym.Flags&FPointer == 0:
			c.args(sc, e.Args)
			return TV{Type: c.typeOf(sym, id.Pos), Call: true, Sym: sym}
		case sym.Type.Basic == Invalid && sym.Flags&(FPointer|FAllocatable) == 0:
			// an untyped dummy or local called like a function: a function
			// with an implicit type, possibly a dummy procedure
			sym.Kind = SymProc
			sym.Flags |= FFunction
			t := c.typeOf(sym, id.Pos)
			c.args(sc, e.Args)
			return TV{Type: t, Call: true, Sym: sym}
		case sym.Flags&FPointer != 0 && sym.Type.Basic == Unknown, sym.Type.Lenient():
			c.args(sc, e.Args)
			return unknownTV
		case sym.Type.Basic == Character:
			t := sym.Type
			for i := range e.Args {
				if v := e.Args[i].Value; v != nil {
					c.expr(sc, v)
				}
			}
			t.Len = -1
			return TV{Type: t, Sym: sym, Var: true}
		}
		// a scalar of a declared type referenced with arguments is an external
		// function of that result type ("integer :: f" in the caller)
		sym.Kind = SymProc
		sym.Flags |= FFunction | FExternal
		c.args(sc, e.Args)
		return TV{Type: sym.Type, Call: true, Sym: sym}
	case SymParam:
		if sym.Rank != 0 {
			base := TV{Type: sym.Type, Rank: sym.Rank, Sym: sym}
			r := c.subscripts(sc, e, base, name)
			return TV{Type: sym.Type, Rank: r, Sym: sym}
		}
		if sym.Type.Basic == Character {
			for i := range e.Args {
				if v := e.Args[i].Value; v != nil {
					c.expr(sc, v)
				}
			}
			t := sym.Type
			t.Len = -1
			return TV{Type: t, Sym: sym}
		}
		c.errorf(id.Pos, "Named constant '%s' at (1) is not an array and not a function", name)
		c.args(sc, e.Args)
		return TV{Type: sym.Type}
	case SymProc:
		t := sym.Type
		if t.Basic == Invalid {
			t = c.typeOf(sym, id.Pos)
		}
		if sym.Flags&FSub != 0 && sym.Flags&FFunction == 0 {
			c.errorf(id.Pos, "'%s' at (1) is a subroutine, it cannot be used in an expression", name)
		}
		c.args(sc, e.Args)
		return TV{Type: t, Rank: sym.Rank, Call: true, Sym: sym}
	case SymGeneric:
		c.args(sc, e.Args) // resolution by the generic's specifics: see generic.go
		return c.resolveGeneric(sc, e, sym)
	case SymType:
		c.args(sc, e.Args)
		return TV{Type: Type{Basic: Derived, Der: sym.Der}, Sym: sym}
	}
	c.errorf(id.Pos, "'%s' at (1) cannot be referenced with arguments", name)
	c.args(sc, e.Args)
	return unknownTV
}

func isComponent(e syntax.Expr) bool { _, ok := e.(*syntax.Component); return ok }

func allTriplets(as []syntax.Arg) bool {
	n := 0
	for i := range as {
		if as[i].Value == nil {
			continue
		}
		if _, ok := as[i].Value.(*syntax.Triplet); !ok {
			return false
		}
		n++
	}
	return n == 1
}

func (c *checker) intrinsicCall(sc *Scope, e *syntax.Index, name string, in intrinsic) TV {
	args := c.args(sc, e.Args)
	tv, msg := c.intrinsicType(name, in, args)
	if msg != "" {
		c.errorf(e.Pos, "%s at (1)", msg)
		return tv
	}
	if tv.Rank == 0 || tv.Rank == RankUnknown {
		if k := c.foldIntrinsic(name, args, tv.Type); k != nil {
			tv.Const = k
		}
	}
	return tv
}

func (c *checker) component(sc *Scope, e *syntax.Component) TV {
	base := c.expr(sc, e.X)
	name := e.Name.Name
	t := base.Type
	switch {
	case t.Lenient():
		return TV{Type: Type{Basic: Unknown}, Rank: base.Rank}
	case t.Basic == Derived && t.Der != nil:
		if comp := t.Der.Find(name); comp != nil {
			r := comp.Rank
			if base.Rank != 0 && comp.Rank > 0 && base.Rank > 0 {
				c.errorf(e.Pos, "Two or more part references with nonzero rank must not be specified at (1)")
			}
			if base.Rank != 0 {
				r = base.Rank
			}
			return TV{Type: comp.Type, Rank: r, Var: base.Var}
		}
		if target, ok := t.Der.FindBound(name); ok {
			if ps := t.Der.Scope.Lookup(target); ps != nil && ps.Kind == SymProc {
				return TV{Type: ps.Type, Call: true, Rank: ps.Rank, Sym: ps}
			}
			return unknownTV
		}
		c.errorf(e.Pos, "'%s' at (1) is not a member of the '%s' structure", name, t.Der.Name)
		return unknownTV
	case t.Basic == Complex && (name == "re" || name == "im"):
		return TV{Type: Type{Basic: Real, Kind: t.Kind}, Rank: base.Rank, Var: base.Var}
	case t.Basic == Character && name == "len":
		return TV{Type: Default(Integer), Rank: base.Rank}
	case t.Basic == Derived:
		return unknownTV
	}
	c.errorf(e.Pos, "Unexpected '%%' for nonderived-type variable of type %s at (1)", t)
	return unknownTV
}

func (c *checker) arrayCons(sc *Scope, e *syntax.ArrayCons) TV {
	var t Type
	if e.Type != nil {
		t = c.typeSpec(sc, e.Type)
	}
	have := e.Type != nil
	for _, el := range e.Elems {
		tv := c.expr(sc, el)
		if tv.Type.Lenient() {
			continue
		}
		if !have {
			t, have = tv.Type, true
			continue
		}
		if e.Type != nil {
			continue
		}
		if tv.Type.Basic != t.Basic || (tv.Type.Basic != Character && tv.Type.Basic != Derived && tv.Type.Kind != t.Kind) {
			c.errorf(el.P(), "Element in %s array constructor at (1) is %s", t, tv.Type)
		}
	}
	if !have {
		t = Type{Basic: Unknown}
	}
	if t.Basic == Character && e.Type == nil {
		t.Len = -1
	}
	return TV{Type: t, Rank: 1}
}

func (c *checker) impliedDo(sc *Scope, e *syntax.ImpliedDo) TV {
	csc := c.constructScope(sc)
	c.doVar(csc, sc, &e.Var)
	for _, p := range []syntax.Expr{e.Lo, e.Hi, e.Step} {
		if p == nil {
			continue
		}
		tv := c.expr(sc, p)
		if !tv.Type.Lenient() && !tv.Type.Numeric() {
			c.errorf(p.P(), "Implied-do control expression at (1) must be numeric, it is %s", tv.Type)
		}
	}
	t := Type{Basic: Unknown}
	have := false
	for _, it := range e.Items {
		tv := c.expr(csc, it)
		if !have && !tv.Type.Lenient() {
			t, have = tv.Type, true
		}
	}
	return TV{Type: t, Rank: 1}
}

func (c *checker) constructScope(sc *Scope) *Scope {
	s := newScope(sc, nil)
	s.Construct = true
	s.Kind = sc.Kind
	return s
}

// doVar declares the index of an implied-do, FORALL or DO CONCURRENT in the
// construct scope: its type is that of the variable of that name visible in
// the enclosing scope, else the implicit type (F2018 19.4).
func (c *checker) doVar(csc, outer *Scope, id *syntax.Ident) {
	s := c.newSym(csc, id.Name, SymVar, id.Pos)
	s.Flags |= FAssoc
	if o := outer.Lookup(id.Name); o != nil && o.Kind == SymVar {
		s.Type = c.typeOf(o, id.Pos)
	} else if t, ok := implicitFor(outer, id.Name); ok {
		s.Type = t
	} else if outer.Opaque {
		s.Type = Type{Basic: Unknown}
	} else {
		c.errorf(id.Pos, "Symbol '%s' at (1) has no IMPLICIT type", id.Name)
		s.Type = Type{Basic: Unknown}
	}
	if !s.Type.Lenient() && s.Type.Basic != Integer {
		c.errorf(id.Pos, "Index variable '%s' at (1) must be INTEGER, it is %s", id.Name, s.Type)
	}
	c.info.Uses[id] = s
}
