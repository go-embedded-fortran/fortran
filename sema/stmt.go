package sema

import (
	"github.com/go-embedded-fortran/fortran/syntax"
)

// resolveGeneric types a reference through a generic name. The specific
// procedure is chosen in PR 2 (F2018 15.5.5.2); until then the result type is
// the common result type of the specifics when they agree.
func (c *checker) resolveGeneric(sc *Scope, e *syntax.Index, g *Symbol) TV {
	tv := TV{Type: Type{Basic: Unknown}, Rank: RankUnknown, Call: true, Sym: g}
	var t Type
	for i, s := range g.Specifics {
		if i == 0 {
			t = s.Type
		} else if s.Type != t {
			return tv
		}
	}
	if len(g.Specifics) > 0 && !t.Lenient() {
		tv.Type = t
	}
	return tv
}

func (c *checker) execStmts(sc *Scope, body []syntax.Stmt) {
	for _, s := range body {
		if c.stop {
			return
		}
		if isSpec(s) {
			continue
		}
		c.stmt(sc, s)
	}
}

func (c *checker) block(sc *Scope, body []syntax.Stmt) {
	for _, s := range body {
		if c.stop {
			return
		}
		if isSpec(s) {
			c.specStmt(sc, s)
		}
	}
	c.execStmts(sc, body)
}

func (c *checker) cond(sc *Scope, e syntax.Expr, what string) {
	tv := c.expr(sc, e)
	if tv.Type.Lenient() {
		return
	}
	if tv.Type.Basic != Logical || (tv.Rank != 0 && tv.Rank != RankUnknown) {
		if tv.Type.Basic == Logical || tv.Rank != 0 {
			c.errorf(e.P(), "%s at (1) requires a scalar LOGICAL expression", what)
		} else {
			c.errorf(e.P(), "%s at (1) requires a scalar LOGICAL expression, it is %s", what, tv.Type)
		}
	}
}

func (c *checker) numeric(sc *Scope, e syntax.Expr, what string) {
	if e == nil {
		return
	}
	tv := c.expr(sc, e)
	if !tv.Type.Lenient() && !tv.Type.Numeric() {
		c.errorf(e.P(), "%s at (1) must be numeric, it is %s", what, tv.Type)
	}
}

// root returns the symbol a designator denotes at its base, and whether the
// path goes through a pointer component or a subscript of a pointer.
func (c *checker) rootSym(sc *Scope, e syntax.Expr) *Symbol {
	for {
		switch x := e.(type) {
		case *syntax.Ident:
			return sc.Lookup(x.Name)
		case *syntax.Index:
			e = x.X
		case *syntax.Component:
			e = x.X
		default:
			return nil
		}
	}
}

// definable checks that e can be defined (F2018 9.4.2, 8.5.10: INTENT(IN),
// 8.5.13: named constants, 11.1.7.4.3: an active DO variable). what is the
// context, as gfortran words it.
func (c *checker) definable(sc *Scope, e syntax.Expr, tv TV, what string) {
	pos := e.P()
	if !tv.Var {
		if tv.Sym != nil && tv.Sym.Kind == SymParam {
			c.errorf(pos, "Named constant '%s' in variable definition context (%s) at (1)", tv.Sym.Name, what)
		} else if !tv.Type.Lenient() {
			c.errorf(pos, "Non-variable expression in variable definition context (%s) at (1)", what)
		}
		return
	}
	root := c.rootSym(sc, e)
	if root == nil {
		return
	}
	_, direct := e.(*syntax.Ident)
	if root.Intent == "in" && root.Flags&(FPointer) == 0 {
		c.errorf(pos, "Dummy argument '%s' with INTENT(IN) in variable definition context (%s) at (1)", root.Name, what)
	}
	if direct && root.Flags&FDoVar != 0 {
		c.errorf(pos, "Variable '%s' at (1) cannot be redefined inside loop beginning", root.Name)
	}
	if root.Flags&FProtected != 0 && root.Flags&FUsed != 0 && root.Flags&FPointer == 0 {
		c.errorf(pos, "Variable '%s' is PROTECTED and cannot appear in a variable definition context (%s) at (1)", root.Name, what)
	}
}

// assignable checks the intrinsic assignment variable = expr (F2018 10.2.1.2,
// Table 10.8): numeric to numeric, logical to logical, character to
// character, a derived type to the same type; ranks agree or the right side
// is a scalar.
func (c *checker) assignable(pos syntax.Pos, lt Type, lrank int, r TV, what string) {
	if lt.Lenient() || r.Type.Lenient() {
		return
	}
	ok := false
	switch {
	case lt.Numeric():
		ok = r.Type.Numeric() || r.Type.Basic == Logical // logical to integer: a GNU extension
	case lt.Basic == Logical:
		ok = r.Type.Basic == Logical || r.Type.Basic == Integer
	case lt.Basic == Character:
		ok = r.Type.Basic == Character
	case lt.Basic == Derived:
		ok = r.Type.Basic == Derived && (lt.Der == r.Type.Der || r.Type.Der == nil || lt.Der == nil || extends(r.Type.Der, lt.Der))
	}
	if !ok {
		c.errorf(pos, "Cannot convert %s to %s at (1) in %s", r.Type, lt, what)
		return
	}
	if lrank >= 0 && r.Rank >= 0 && lrank != r.Rank && r.Rank != 0 {
		c.errorf(pos, "Incompatible ranks %d and %d in %s at (1)", lrank, r.Rank, what)
	}
}

func extends(a, b *DerivedType) bool {
	for t := a; t != nil; t = t.Parent {
		if t == b {
			return true
		}
	}
	return false
}

func (c *checker) stmt(sc *Scope, s syntax.Stmt) {
	if c.stop {
		return
	}
	switch s := s.(type) {
	case *syntax.Assignment:
		c.assignment(sc, s)
	case *syntax.PointerAssign:
		c.expr(sc, s.LHS)
		c.expr(sc, s.RHS)
	case *syntax.If:
		c.cond(sc, s.Cond, "IF clause")
		c.execBody(sc, s.Then)
		for i := range s.ElseIfs {
			c.cond(sc, s.ElseIfs[i].Cond, "ELSE IF clause")
			c.execBody(sc, s.ElseIfs[i].Body)
		}
		c.execBody(sc, s.Else)
	case *syntax.LogicalIf:
		c.cond(sc, s.Cond, "IF clause")
		if s.Stmt != nil {
			c.stmt(sc, s.Stmt)
		}
	case *syntax.ArithIf:
		c.numeric(sc, s.Expr, "Arithmetic IF expression")
	case *syntax.Do:
		c.doStmt(sc, s)
	case *syntax.Select:
		c.selectCase(sc, s)
	case *syntax.SelectType:
		c.selectType(sc, s)
	case *syntax.SelectRank:
		tv := c.expr(sc, s.Expr)
		for i := range s.Cases {
			cs := &s.Cases[i]
			csc := c.constructScope(sc)
			if s.Assoc != "" {
				a := c.newSym(csc, s.Assoc, SymVar, cs.Pos)
				a.Type, a.Flags = tv.Type, a.Flags|FAssoc
				a.Rank = RankUnknown
				a.Rank = -1
				if cs.Value != nil {
					if v := c.expr(sc, cs.Value); v.Const != nil && v.Const.Type.Basic == Integer {
						a.Rank = int(v.Const.I)
					}
				} else if cs.Star {
					a.Rank = 1
				}
			}
			c.execBody(csc, cs.Body)
		}
	case *syntax.Where:
		c.cond1(sc, s.Mask, "WHERE mask")
		if s.Single != nil {
			c.stmt(sc, s.Single)
		}
		c.execBody(sc, s.Body)
		for i := range s.Else {
			if s.Else[i].Mask != nil {
				c.cond1(sc, s.Else[i].Mask, "ELSEWHERE mask")
			}
			c.execBody(sc, s.Else[i].Body)
		}
	case *syntax.Forall:
		csc := c.constructScope(sc)
		c.indices(csc, sc, s.Indices)
		if s.Mask != nil {
			c.cond(csc, s.Mask, "FORALL mask")
		}
		if s.Single != nil {
			c.stmt(csc, s.Single)
		}
		c.execBody(csc, s.Body)
	case *syntax.Associate:
		csc := c.constructScope(sc)
		for i := range s.Assocs {
			a := &s.Assocs[i]
			tv := c.expr(sc, a.Expr)
			sym := c.newSym(csc, a.Name.Name, SymVar, a.Pos)
			sym.Type, sym.Rank = tv.Type, tv.Rank
			sym.Flags |= FAssoc
			if !tv.Var {
				sym.Intent = "in"
			}
			if root := c.rootSym(sc, a.Expr); root != nil && tv.Var {
				sym.Flags |= root.Flags & (FPointer | FTarget)
			}
		}
		c.block(csc, s.Body)
	case *syntax.Block:
		csc := c.constructScope(sc)
		c.block(csc, s.Body)
	case *syntax.ComputedGoto:
		c.numeric(sc, s.Expr, "Computed GOTO expression")
	case *syntax.AssignLabel:
		c.expr(sc, &s.Var)
	case *syntax.AssignedGoto:
		c.expr(sc, &s.Var)
	case *syntax.Call:
		c.call(sc, s)
	case *syntax.Return:
		if s.Alt != nil {
			c.expr(sc, s.Alt)
		}
	case *syntax.Stop:
		if s.Code != nil {
			tv := c.expr(sc, s.Code)
			if !tv.Type.Lenient() && tv.Type.Basic != Integer && tv.Type.Basic != Character {
				c.errorf(s.Code.P(), "STOP code at (1) must be either INTEGER or CHARACTER type, it is %s", tv.Type)
			}
		}
		if s.Quiet != nil {
			c.expr(sc, s.Quiet)
		}
	case *syntax.Pause:
		if s.Code != nil {
			c.expr(sc, s.Code)
		}
	case *syntax.IOStmt:
		c.ioStmt(sc, s)
	case *syntax.Allocate:
		if s.Type != nil {
			c.typeSpec(sc, s.Type)
		}
		for _, o := range s.Objects {
			c.expr(sc, o)
		}
		for i := range s.Specs {
			if v := s.Specs[i].Value; v != nil {
				c.expr(sc, v)
			}
		}
	case *syntax.Nullify:
		for _, o := range s.Objects {
			c.expr(sc, o)
		}
	case *syntax.DataStmt:
		for _, d := range s.Sets {
			for _, o := range d.Objects {
				c.expr(sc, o)
			}
			for _, v := range d.Values {
				c.expr(sc, v)
			}
		}
	case *syntax.EntryStmt:
		us := sc.unit()
		for _, p := range s.Params {
			if _, ok := us.Names[p.Name]; !ok && p.Name != "*" {
				d := c.newSym(us, p.Name, SymVar, p.Pos)
				d.Flags |= FDummy
			}
		}
		if us.Kind == syntax.FunctionUnit {
			rn := s.Ident.Name
			if s.Result != nil {
				rn = s.Result.Name
			}
			if _, ok := us.Names[rn]; !ok {
				r := c.newSym(us, rn, SymVar, s.Ident.Pos)
				r.Flags |= FResult
				if us.Unit != nil {
					urn := us.Unit.Ident.Name
					if us.Unit.Result != nil {
						urn = us.Unit.Result.Name
					}
					if ur := us.Names[urn]; ur != nil {
						r.Type = ur.Type
					}
				}
			}
		}
	}
}

func (c *checker) cond1(sc *Scope, e syntax.Expr, what string) {
	tv := c.expr(sc, e)
	if !tv.Type.Lenient() && tv.Type.Basic != Logical {
		c.errorf(e.P(), "%s at (1) must be LOGICAL, it is %s", what, tv.Type)
	}
}

func (c *checker) execBody(sc *Scope, body []syntax.Stmt) {
	for _, s := range body {
		if c.stop {
			return
		}
		if isSpec(s) { // a labelled DO's terminal statement can be anything
			continue
		}
		c.stmt(sc, s)
	}
}

func (c *checker) assignment(sc *Scope, s *syntax.Assignment) {
	// A statement function definition f(x) = expr looks like an element
	// assignment to a name that is not an array (F2018 15.6.4, deleted
	// feature of Annex B).
	if ix, ok := s.LHS.(*syntax.Index); ok {
		if id, ok := ix.X.(*syntax.Ident); ok && c.isStmtFunc(sc, id) {
			c.stmtFunc(sc, s, ix, id)
			return
		}
	}
	l := c.expr(sc, s.LHS)
	r := c.expr(sc, s.RHS)
	if l.Type.Lenient() && l.Sym == nil {
		return
	}
	c.definable(sc, s.LHS, l, "assignment")
	if l.Var {
		c.assignable(s.Pos, l.Type, l.Rank, r, "assignment")
	}
}

func (c *checker) isStmtFunc(sc *Scope, id *syntax.Ident) bool {
	sym := sc.Names[id.Name]
	if sym == nil {
		if sc.Lookup(id.Name) != nil {
			return false // a host name: an element assignment
		}
		return !sc.Opaque
	}
	return sym.Kind == SymVar && sym.Rank == 0 && sym.Flags&(FPointer|FAllocatable|FDummy|FResult) == 0 && sym.Type.Basic != Character && sym.Type.Basic != Derived
}

func (c *checker) stmtFunc(sc *Scope, s *syntax.Assignment, ix *syntax.Index, id *syntax.Ident) {
	us := sc.unit()
	sym := us.Names[id.Name]
	if sym == nil {
		sym = c.newSym(us, id.Name, SymVar, id.Pos)
		c.typeOf(sym, id.Pos)
	}
	sym.Kind = SymProc
	sym.Flags |= FStmtFunc | FFunction
	csc := c.constructScope(sc)
	for i := range ix.Args {
		a, ok := ix.Args[i].Value.(*syntax.Ident)
		if !ok {
			c.errorf(ix.Args[i].Pos, "Statement function '%s' at (1) has a dummy argument that is not a name", id.Name)
			continue
		}
		d := c.newSym(csc, a.Name, SymVar, a.Pos)
		d.Flags |= FDummy
		if o := sc.Lookup(a.Name); o != nil && o.Kind == SymVar && o.Type.Basic != Invalid {
			d.Type = o.Type
		} else if t, ok := implicitFor(sc, a.Name); ok {
			d.Type = t
		} else if !sc.Opaque {
			c.errorf(a.Pos, "Symbol '%s' at (1) has no IMPLICIT type", a.Name)
			d.Type = Type{Basic: Unknown}
		}
		sym.Params = append(sym.Params, a.Name)
	}
	r := c.expr(csc, s.RHS)
	c.assignable(s.Pos, sym.Type, 0, r, "statement function definition")
}

func (c *checker) indices(csc, outer *Scope, ix []syntax.ForallIdx) {
	for i := range ix {
		f := &ix[i]
		for _, p := range []syntax.Expr{f.Lo, f.Hi, f.Stride} {
			c.numeric(outer, p, "Index range expression")
		}
		c.doVar(csc, outer, &f.Var)
	}
}

func (c *checker) doStmt(sc *Scope, s *syntax.Do) {
	body := sc
	switch {
	case s.Concurrent:
		body = c.constructScope(sc)
		c.indices(body, sc, s.Controls)
		if s.Mask != nil {
			c.cond(body, s.Mask, "DO CONCURRENT mask")
		}
	case s.While != nil:
		c.cond(sc, s.While, "DO WHILE condition")
	case s.HasVar:
		tv := c.expr(sc, &s.Var)
		if !tv.Type.Lenient() && tv.Type.Basic != Integer && tv.Type.Basic != Real {
			c.errorf(s.Var.Pos, "Loop variable at (1) must be integer, it is %s", tv.Type)
		} else if tv.Rank != 0 && tv.Rank != RankUnknown {
			c.errorf(s.Var.Pos, "Loop variable at (1) must be a scalar")
		}
		c.definable(sc, &s.Var, tv, "DO")
		for _, p := range []syntax.Expr{s.Lo, s.Hi, s.Step} {
			c.numeric(sc, p, "DO control expression")
		}
		if sym := sc.Lookup(s.Var.Name); sym != nil {
			was := sym.Flags & FDoVar
			sym.Flags |= FDoVar
			defer func() { sym.Flags = sym.Flags&^FDoVar | was }()
		}
	}
	c.execBody(body, s.Body)
}

func (c *checker) selectCase(sc *Scope, s *syntax.Select) {
	tv := c.expr(sc, s.Expr)
	if !tv.Type.Lenient() {
		switch tv.Type.Basic {
		case Integer, Logical, Character:
		default:
			c.errorf(s.Expr.P(), "Argument of SELECT statement at (1) cannot be %s", tv.Type)
		}
	}
	for i := range s.Cases {
		cs := &s.Cases[i]
		for _, sel := range cs.Selectors {
			parts := []syntax.Expr{sel}
			if t, ok := sel.(*syntax.Triplet); ok {
				parts = []syntax.Expr{t.Lo, t.Hi}
			}
			for _, p := range parts {
				if p == nil {
					continue
				}
				v := c.expr(sc, p)
				if !v.Type.Lenient() && !tv.Type.Lenient() && v.Type.Basic != tv.Type.Basic {
					c.errorf(p.P(), "Expression in CASE selector at (1) is %s but the SELECT expression is %s", v.Type, tv.Type)
				}
			}
		}
		c.execBody(sc, cs.Body)
	}
}

func (c *checker) selectType(sc *Scope, s *syntax.SelectType) {
	tv := c.expr(sc, s.Expr)
	for i := range s.Cases {
		cs := &s.Cases[i]
		csc := c.constructScope(sc)
		if s.Assoc != "" {
			a := c.newSym(csc, s.Assoc, SymVar, cs.Pos)
			a.Flags |= FAssoc
			a.Rank = tv.Rank
			a.Type = tv.Type
			if cs.Type != nil {
				a.Type = c.typeSpec(sc, cs.Type)
			}
		} else if cs.Type != nil {
			c.typeSpec(sc, cs.Type)
		}
		c.execBody(csc, cs.Body)
	}
}

func (c *checker) call(sc *Scope, s *syntax.Call) {
	switch p := s.Proc.(type) {
	case *syntax.Ident:
		sym := sc.Lookup(p.Name)
		switch {
		case sym == nil:
			if !IsIntrinsicSub(p.Name) && !sc.Opaque {
				us := sc.unit()
				n := c.newSym(us, p.Name, SymProc, p.Pos)
				n.Flags |= FSub | FExternal
				c.info.Uses[p] = n
			}
		default:
			c.info.Uses[p] = sym
			switch sym.Kind {
			case SymVar:
				if sym.Type.Basic == Invalid && sym.Flags&(FPointer|FAllocatable) == 0 {
					sym.Kind = SymProc // a dummy procedure used as a subroutine
					sym.Flags |= FSub
				}
			case SymParam, SymType, SymModule:
				c.errorf(p.Pos, "'%s' at (1) is not a subroutine", p.Name)
			}
		}
	default:
		c.expr(sc, s.Proc)
	}
	c.args(sc, s.Args)
}

func (c *checker) ioStmt(sc *Scope, s *syntax.IOStmt) {
	for i := range s.Controls {
		a := &s.Controls[i]
		if a.Value == nil {
			continue
		}
		if id, ok := a.Value.(*syntax.Ident); ok && sc.Lookup(id.Name) == nil && (a.Keyword == "nml" || a.Keyword == "" || a.Keyword == "fmt") {
			continue // a namelist group name, not a variable
		}
		c.expr(sc, a.Value)
	}
	if s.Format != nil {
		if _, lab := s.Format.(*syntax.IntLit); !lab {
			if id, ok := s.Format.(*syntax.Ident); !ok || sc.Lookup(id.Name) != nil {
				c.expr(sc, s.Format)
			}
		}
	}
	for _, it := range s.Items {
		tv := c.expr(sc, it)
		if s.Kind == "read" {
			if _, impl := it.(*syntax.ImpliedDo); !impl {
				c.definable(sc, it, tv, "READ")
			}
		}
	}
}
