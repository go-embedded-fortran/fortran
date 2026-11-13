package syntax

import (
	"fmt"
	"strconv"
)

// clause runs f on the current statement (a continuation line of a
// construct: else, case, end ...) with recovery, then consumes it.
func (p *parser) clause(f func()) {
	seq := p.seq
	func() {
		defer p.recoverStmt()
		f()
	}()
	if p.seq == seq {
		p.done()
	}
}

// expectEnd consumes "end <kind> [name]" or reports the construct unterminated.
func (p *parser) expectEnd(kind string, start Pos, name string) {
	if p.load() && p.atEnd(kind) {
		p.clause(func() {
			pos := p.tok().Pos
			_, w, _ := p.endKind()
			p.i += w
			if p.tok().Kind == Word {
				n := p.next()
				if name != "" && n.S != name {
					p.errorAt(n.Pos, fmt.Sprintf("end name '%s' does not match '%s'", n.S, name))
				}
			} else if name != "" {
				p.errorAt(pos, fmt.Sprintf("missing construct name '%s' on end %s", name, kind))
			}
			p.endStmt()
		})
		return
	}
	pos := p.eos
	if p.have {
		pos = p.tok().Pos
	}
	p.errorAt(pos, fmt.Sprintf("missing 'end %s' for the construct starting at %s", kind, p.fset.Position(start)))
}

func (p *parser) labelNum(t Token) int {
	n, err := strconv.Atoi(t.S)
	if err != nil || n > 99999 {
		p.fail(t.Pos, "invalid label %s", t.S)
	}
	return n
}

func (p *parser) ifStmt() Stmt {
	b := p.base()
	p.i++
	p.expect(LParen)
	cond := p.expr()
	p.expect(RParen)
	t := p.tok()
	switch {
	case t.Kind == Word && t.S == "then":
		p.i++
		p.endStmt()
		p.done()
	case t.Kind == Int:
		a := &ArithIf{StmtBase: b, Expr: cond}
		a.Neg = p.labelNum(p.expect(Int))
		p.expect(Comma)
		a.Zero = p.labelNum(p.expect(Int))
		p.expect(Comma)
		a.Plus = p.labelNum(p.expect(Int))
		p.endStmt()
		return a
	default:
		p.enter()
		inner := p.stmt()
		p.leave()
		return &LogicalIf{b, cond, inner}
	}
	n := &If{StmtBase: b, Cond: cond}
	stop := func() bool {
		w := p.word()
		return w == "elseif" || (w == "else") || p.atEnd("if")
	}
	n.Then = p.block(stop)
	for p.load() {
		w := p.word()
		if w == "elseif" || (w == "else" && p.peek(1).Kind == Word && p.peek(1).S == "if") {
			var ei ElseIf
			ei.Pos = p.tok().Pos
			p.clause(func() {
				p.i++
				if w == "else" {
					p.i++
				}
				p.expect(LParen)
				ei.Cond = p.expr()
				p.expect(RParen)
				p.expectWord("then")
				if p.tok().Kind == Word {
					p.i++
				}
				p.endStmt()
			})
			ei.Body = p.block(stop)
			n.ElseIfs = append(n.ElseIfs, ei)
			continue
		}
		if w == "else" {
			n.HasElse = true
			p.clause(func() {
				p.i++
				if p.tok().Kind == Word {
					p.i++
				}
				p.endStmt()
			})
			n.Else = p.block(func() bool { return p.atEnd("if") })
		}
		break
	}
	p.expectEnd("if", b.Pos, b.Name)
	return n
}

func (p *parser) forallControl(mask *Expr) []ForallIdx {
	var out []ForallIdx
	for {
		if p.tok().Kind == Word && p.peek(1).Kind == Assign {
			v := p.ident()
			p.i++
			f := ForallIdx{Span: v.Span, Var: v}
			f.Lo = p.expr()
			p.expect(Colon)
			f.Hi = p.expr()
			if p.accept(Colon) {
				f.Stride = p.expr()
			}
			out = append(out, f)
		} else {
			*mask = p.expr()
		}
		if !p.accept(Comma) {
			return out
		}
	}
}

func (p *parser) doStmt() Stmt {
	b := p.base()
	p.i++
	d := &Do{StmtBase: b}
	if t := p.tok(); t.Kind == Int {
		d.TermLabel = p.labelNum(t)
		p.i++
		p.accept(Comma)
	}
	switch {
	case p.isWord("while"):
		p.i++
		p.expect(LParen)
		d.While = p.expr()
		p.expect(RParen)
	case p.isWord("concurrent"):
		p.i++
		d.Concurrent = true
		p.expect(LParen)
		d.Controls = p.forallControl(&d.Mask)
		p.expect(RParen)
	case p.tok().Kind == Word:
		d.Var = p.ident()
		d.HasVar = true
		p.expect(Assign)
		d.Lo = p.expr()
		p.expect(Comma)
		d.Hi = p.expr()
		if p.accept(Comma) {
			d.Step = p.expr()
		}
	}
	p.endStmt()
	p.done()
	if d.TermLabel == 0 {
		d.Body = p.block(func() bool { return p.atEnd("do") })
		p.expectEnd("do", b.Pos, b.Name)
		return d
	}
	// labelled DO: the body runs to the statement carrying the label; a
	// terminal statement may be shared by enclosing labelled DOs (Annex B).
	p.enter()
	defer p.leave()
	for p.load() {
		if p.blockEnd() {
			p.errorAt(p.tok().Pos, fmt.Sprintf("missing terminal statement with label %d for the DO at %s", d.TermLabel, p.fset.Position(b.Pos)))
			return d
		}
		term := p.label == d.TermLabel
		d.Body = append(d.Body, p.statement())
		if term || p.closedLabel == d.TermLabel {
			p.closedLabel = d.TermLabel
			return d
		}
	}
	p.errorAt(p.eos, fmt.Sprintf("missing terminal statement with label %d for the DO at %s", d.TermLabel, p.fset.Position(b.Pos)))
	return d
}

func (p *parser) caseStart() bool {
	return p.word() == "case" || p.word() == "casedefault"
}

func (p *parser) typeGuardStart() bool {
	w := p.word()
	if w == "typeis" || w == "classis" || w == "classdefault" {
		return true
	}
	if (w == "type" || w == "class") && p.peek(1).Kind == Word {
		n := p.peek(1).S
		return n == "is" || n == "default"
	}
	return false
}

func (p *parser) selectStmt() Stmt {
	b := p.base()
	w := p.next().S
	isType := w == "selecttype"
	if w == "select" {
		k := p.ident()
		switch k.Name {
		case "case":
		case "type":
			isType = true
		default:
			p.fail(k.Pos, "expected 'case' or 'type' after 'select'")
		}
	}
	p.expect(LParen)
	var assoc string
	var x Expr
	if isType && p.tok().Kind == Word && p.peek(1).Kind == Arrow {
		assoc = p.next().S
		p.i++
	}
	x = p.expr()
	p.expect(RParen)
	p.endStmt()
	p.done()
	end := p.atEnd
	if isType {
		st := &SelectType{StmtBase: b, Assoc: assoc, Expr: x}
		for p.load() && !p.blockEnd() && !end("select") {
			if !p.typeGuardStart() {
				p.errorAt(p.tok().Pos, "expected 'type is', 'class is' or 'class default' in select type")
				p.statement()
				continue
			}
			var tc TypeCase
			tc.Pos = p.tok().Pos
			p.clause(func() {
				k := p.next().S
				switch k {
				case "typeis":
					tc.Kind = "type is"
				case "classis":
					tc.Kind = "class is"
				case "classdefault":
					tc.Kind = "class default"
				default:
					n := p.next().S
					tc.Kind = k + " " + n
				}
				if tc.Kind != "class default" {
					p.expect(LParen)
					if n := p.tok(); n.Kind == Word && isTypeWord(n.S) && n.S != "type" && n.S != "class" {
						tc.Type = p.typeSpec(true)
					} else {
						tc.Type = &TypeSpec{Span: Span{n.Pos}, Name: "type", Derived: p.ident().Name}
					}
					p.expect(RParen)
				}
				if p.tok().Kind == Word {
					p.i++
				}
				p.endStmt()
			})
			tc.Body = p.block(func() bool { return p.typeGuardStart() || end("select") })
			st.Cases = append(st.Cases, tc)
		}
		p.expectEnd("select", b.Pos, b.Name)
		return st
	}
	st := &Select{StmtBase: b, Expr: x}
	for p.load() && !p.blockEnd() && !end("select") {
		if !p.caseStart() {
			p.errorAt(p.tok().Pos, "expected 'case' in select case")
			p.statement()
			continue
		}
		var c Case
		c.Pos = p.tok().Pos
		p.clause(func() {
			k := p.next().S
			if k == "casedefault" || p.acceptWord("default") {
				c.Default = true
			} else {
				p.expect(LParen)
				for {
					c.Selectors = append(c.Selectors, p.subscript())
					if !p.accept(Comma) {
						break
					}
				}
				p.expect(RParen)
			}
			if p.tok().Kind == Word {
				p.i++
			}
			p.endStmt()
		})
		c.Body = p.block(func() bool { return p.caseStart() || end("select") })
		st.Cases = append(st.Cases, c)
	}
	p.expectEnd("select", b.Pos, b.Name)
	return st
}

func (p *parser) elsewhereStart() bool {
	return p.word() == "elsewhere" || (p.word() == "else" && p.peek(1).Kind == Word && p.peek(1).S == "where")
}

func (p *parser) whereStmt() Stmt {
	b := p.base()
	p.i++
	p.expect(LParen)
	w := &Where{StmtBase: b}
	w.Mask = p.expr()
	p.expect(RParen)
	if !p.atEOS() {
		p.enter()
		w.Single = p.stmt()
		p.leave()
		return w
	}
	p.done()
	stop := func() bool { return p.elsewhereStart() || p.atEnd("where") }
	w.Body = p.block(stop)
	for p.load() && p.elsewhereStart() {
		var br WhereBranch
		br.Pos = p.tok().Pos
		p.clause(func() {
			if p.next().S == "else" {
				p.i++
			}
			if p.accept(LParen) {
				br.Mask = p.expr()
				p.expect(RParen)
			}
			if p.tok().Kind == Word {
				p.i++
			}
			p.endStmt()
		})
		br.Body = p.block(stop)
		w.Else = append(w.Else, br)
	}
	p.expectEnd("where", b.Pos, b.Name)
	return w
}

func (p *parser) forallStmt() Stmt {
	b := p.base()
	p.i++
	p.expect(LParen)
	f := &Forall{StmtBase: b}
	f.Indices = p.forallControl(&f.Mask)
	p.expect(RParen)
	if !p.atEOS() {
		p.enter()
		f.Single = p.stmt()
		p.leave()
		return f
	}
	p.done()
	f.Body = p.block(func() bool { return p.atEnd("forall") })
	p.expectEnd("forall", b.Pos, b.Name)
	return f
}

func (p *parser) associateStmt() Stmt {
	b := p.base()
	p.i++
	a := &Associate{StmtBase: b}
	p.expect(LParen)
	for {
		id := p.ident()
		p.expect(Arrow)
		a.Assocs = append(a.Assocs, Assoc{id.Span, id, p.expr()})
		if !p.accept(Comma) {
			break
		}
	}
	p.expect(RParen)
	p.endStmt()
	p.done()
	a.Body = p.block(func() bool { return p.atEnd("associate") })
	p.expectEnd("associate", b.Pos, b.Name)
	return a
}

func (p *parser) blockStmt() Stmt {
	b := p.base()
	p.i++
	p.endStmt()
	p.done()
	s := &Block{StmtBase: b}
	s.Body = p.block(func() bool { return p.atEnd("block") })
	p.expectEnd("block", b.Pos, b.Name)
	return s
}

func (p *parser) callStmt() Stmt {
	b := p.base()
	p.i++
	d := p.designator()
	c := &Call{StmtBase: b}
	if ix, ok := d.(*Index); ok {
		c.Proc, c.Args, c.HasArgs = ix.X, ix.Args, true
	} else {
		c.Proc = d
	}
	p.endStmt()
	return c
}

func (p *parser) gotoStmt() Stmt {
	b := p.base()
	if p.next().S == "go" {
		p.expectWord("to")
	}
	t := p.tok()
	switch t.Kind {
	case Int:
		p.i++
		p.endStmt()
		return &Goto{b, p.labelNum(t)}
	case LParen:
		p.i++
		g := &ComputedGoto{StmtBase: b}
		for {
			g.Labels = append(g.Labels, p.labelNum(p.expect(Int)))
			if !p.accept(Comma) {
				break
			}
		}
		p.expect(RParen)
		p.accept(Comma)
		g.Expr = p.expr()
		p.endStmt()
		return g
	}
	p.fail(t.Pos, "assigned GO TO is not supported (deleted feature)")
	return nil
}

func (p *parser) stopStmt() Stmt {
	b := p.base()
	s := &Stop{StmtBase: b}
	switch p.next().S {
	case "error":
		p.expectWord("stop")
		s.Error = true
	case "errorstop":
		s.Error = true
	}
	if !p.atEOS() && p.tok().Kind != Comma {
		s.Code = p.expr()
	}
	if p.accept(Comma) {
		p.expectWord("quiet")
		p.expect(Assign)
		s.Quiet = p.expr()
	}
	p.endStmt()
	return s
}

func (p *parser) ioFormat() Expr {
	t := p.tok()
	switch t.Kind {
	case Star:
		p.i++
		return &Asterisk{Span{t.Pos}}
	}
	return p.expr()
}

func (p *parser) ioStmt(kind string) Stmt {
	s := &IOStmt{StmtBase: p.base(), Kind: kind}
	p.i++
	switch kind {
	case "file":
		s.Kind = "endfile"
	case "print":
		s.Format = p.ioFormat()
		if p.accept(Comma) {
			s.Items = p.exprList(EOF)
		}
	case "read", "write", "inquire":
		if p.tok().Kind == LParen {
			p.i++
			s.HasCtrl = true
			s.Controls = p.args()
			p.expect(RParen)
			p.accept(Comma)
			s.Items = p.exprList(EOF)
		} else if kind == "read" {
			s.Format = p.ioFormat()
			if p.accept(Comma) {
				s.Items = p.exprList(EOF)
			}
		} else {
			p.fail(p.tok().Pos, "expected '(' after '%s'", kind)
		}
	default:
		if p.tok().Kind == LParen {
			p.i++
			s.HasCtrl = true
			s.Controls = p.args()
			p.expect(RParen)
		} else {
			t := p.tok()
			s.Controls = []Arg{{Span: Span{t.Pos}, Value: p.expr()}}
		}
	}
	p.endStmt()
	return s
}

var allocSpecs = map[string]bool{"stat": true, "errmsg": true, "source": true, "mold": true}

func (p *parser) allocateStmt() Stmt {
	b := p.base()
	w := p.next().S
	a := &Allocate{StmtBase: b, Dealloc: w == "deallocate"}
	p.expect(LParen)
	if !a.Dealloc && isTypeWord(p.tok().S) && p.tok().Kind == Word {
		var ts *TypeSpec
		if p.try(func() { ts = p.typeSpec(true); p.expect(DColon) }) {
			a.Type = ts
		}
	}
	for p.tok().Kind != RParen {
		if t := p.tok(); t.Kind == Word && p.peek(1).Kind == Assign && allocSpecs[t.S] {
			p.i += 2
			a.Specs = append(a.Specs, Arg{Span: Span{t.Pos}, Keyword: t.S, Value: p.expr()})
		} else {
			a.Objects = append(a.Objects, p.expr())
		}
		if !p.accept(Comma) {
			break
		}
	}
	p.expect(RParen)
	p.endStmt()
	return a
}

func (p *parser) nullifyStmt() Stmt {
	b := p.base()
	p.i++
	n := &Nullify{StmtBase: b}
	p.expect(LParen)
	n.Objects = p.exprList(RParen)
	p.expect(RParen)
	p.endStmt()
	return n
}
