package syntax

// Expressions, F2018 10.1.1 (precedence, lowest to highest):
//
//	defined binary op < .eqv. .neqv. < .or. < .and. < .not. < relational
//	< // < binary + - (and unary + -) < * / < ** (right associative)
//	< defined unary op < primary

func (p *parser) expr() Expr { return p.defBinary() }

func (p *parser) defBinary() Expr {
	x := p.eqv()
	for p.tok().Kind == DefOp {
		t := p.next()
		y := p.eqv()
		x = &Binary{Span{t.Pos}, "." + t.S + ".", x, y}
	}
	return x
}

func (p *parser) eqv() Expr {
	x := p.or()
	for k := p.tok().Kind; k == Eqv || k == Neqv; k = p.tok().Kind {
		t := p.next()
		y := p.or()
		x = &Binary{Span{t.Pos}, opText(t), x, y}
	}
	return x
}

func (p *parser) or() Expr {
	x := p.and()
	for p.tok().Kind == Or {
		t := p.next()
		y := p.and()
		x = &Binary{Span{t.Pos}, ".or.", x, y}
	}
	return x
}

func (p *parser) and() Expr {
	x := p.not()
	for p.tok().Kind == And {
		t := p.next()
		y := p.not()
		x = &Binary{Span{t.Pos}, ".and.", x, y}
	}
	return x
}

func (p *parser) not() Expr {
	if p.tok().Kind == Not {
		t := p.next()
		p.enter()
		x := p.not()
		p.leave()
		return &Unary{Span{t.Pos}, ".not.", x}
	}
	return p.rel()
}

func relOp(k Kind) string {
	switch k {
	case Eq:
		return "=="
	case Ne:
		return "/="
	case Lt:
		return "<"
	case Le:
		return "<="
	case Gt:
		return ">"
	case Ge:
		return ">="
	}
	return ""
}

func (p *parser) rel() Expr {
	x := p.concat()
	if op := relOp(p.tok().Kind); op != "" {
		t := p.next()
		y := p.concat()
		x = &Binary{Span{t.Pos}, op, x, y}
	}
	return x
}

func (p *parser) concat() Expr {
	x := p.add()
	for p.tok().Kind == Concat {
		t := p.next()
		y := p.add()
		x = &Binary{Span{t.Pos}, "//", x, y}
	}
	return x
}

func (p *parser) add() Expr {
	var x Expr
	if k := p.tok().Kind; k == Plus || k == Minus {
		t := p.next()
		y := p.mult()
		x = &Unary{Span{t.Pos}, opText(t), y}
	} else {
		x = p.mult()
	}
	for k := p.tok().Kind; k == Plus || k == Minus; k = p.tok().Kind {
		t := p.next()
		y := p.mult()
		x = &Binary{Span{t.Pos}, opText(t), x, y}
	}
	return x
}

func opText(t Token) string {
	switch t.Kind {
	case Plus:
		return "+"
	case Minus:
		return "-"
	case Eqv:
		return ".eqv."
	case Neqv:
		return ".neqv."
	}
	return t.S
}

func (p *parser) mult() Expr {
	x := p.signedPower()
	for {
		k := p.tok().Kind
		if k == Slash && p.noSlash {
			return x
		}
		if k != Star && k != Slash {
			return x
		}
		t := p.next()
		y := p.signedPower()
		op := "*"
		if k == Slash {
			op = "/"
		}
		x = &Binary{Span{t.Pos}, op, x, y}
	}
}

// signedPower accepts a sign after an operator ("a*-b", "a**-b"), as gfortran does.
func (p *parser) signedPower() Expr {
	if k := p.tok().Kind; k == Plus || k == Minus {
		t := p.next()
		p.enter()
		x := p.signedPower()
		p.leave()
		return &Unary{Span{t.Pos}, opText(t), x}
	}
	return p.power()
}

func (p *parser) power() Expr {
	x := p.defUnary()
	if p.tok().Kind == Pow {
		t := p.next()
		p.enter()
		y := p.signedPower() // right associative
		p.leave()
		return &Binary{Span{t.Pos}, "**", x, y}
	}
	return x
}

func (p *parser) defUnary() Expr {
	if p.tok().Kind == DefOp {
		t := p.next()
		p.enter()
		x := p.defUnary()
		p.leave()
		return &Unary{Span{t.Pos}, "." + t.S + ".", x}
	}
	return p.primary()
}

func (p *parser) primary() Expr {
	t := p.tok()
	switch t.Kind {
	case Int:
		p.i++
		return &IntLit{Span{t.Pos}, t.S, t.X}
	case Real:
		p.i++
		return &RealLit{Span{t.Pos}, t.S, t.X}
	case String:
		p.i++
		var lit Expr = &StringLit{Span{t.Pos}, t.S, t.X}
		if p.tok().Kind == LParen { // substring of a literal: "abc"(i:j)
			o := p.next()
			p.enter()
			args := p.args()
			p.leave()
			p.expect(RParen)
			lit = &Index{Span{o.Pos}, lit, args}
		}
		return lit
	case Percent: // legacy %loc(x), %val(x), %ref(x)
		p.i++
		n := p.ident()
		x := &Ident{Span{t.Pos}, "%" + n.Name, ""}
		if p.tok().Kind == LParen {
			o := p.next()
			p.enter()
			args := p.args()
			p.leave()
			p.expect(RParen)
			return &Index{Span{o.Pos}, x, args}
		}
		return x
	case Logical:
		p.i++
		return &LogicalLit{Span{t.Pos}, t.S == "true", t.X}
	case BOZ:
		p.i++
		return &BozLit{Span{t.Pos}, t.X[0], t.S}
	case LParen:
		return p.parenOrComplex()
	case LBrack:
		p.i++
		return p.arrayCons(t.Pos, RBrack)
	case LParenSlash:
		p.i++
		return p.arrayCons(t.Pos, SlashRParen)
	case Word:
		return p.designator()
	}
	p.fail(t.Pos, "expected an expression, found %s", p.describe(t))
	return nil
}

// parenOrComplex parses "(e)", the complex literal "(re, im)" and the
// implied-do "(items, v = lo, hi [, step])".
func (p *parser) parenOrComplex() Expr {
	open := p.next()
	p.enter()
	defer p.leave()
	save := p.noSlash
	p.noSlash = false
	defer func() { p.noSlash = save }()
	x := p.expr()
	if p.accept(RParen) {
		return &Paren{Span{open.Pos}, x}
	}
	items := []Expr{x}
	for p.accept(Comma) {
		y := p.expr()
		if v, ok := y.(*Ident); ok && p.tok().Kind == Assign {
			return p.impliedDoRest(open.Pos, items, *v)
		}
		items = append(items, y)
	}
	p.expect(RParen)
	if len(items) == 2 {
		return &ComplexLit{Span{open.Pos}, items[0], items[1]}
	}
	p.fail(open.Pos, "expected ')' or an implied-do control")
	return nil
}

func (p *parser) impliedDoRest(pos Pos, items []Expr, v Ident) Expr {
	p.expect(Assign)
	d := &ImpliedDo{Span: Span{pos}, Items: items, Var: v}
	d.Lo = p.expr()
	p.expect(Comma)
	d.Hi = p.expr()
	if p.accept(Comma) {
		d.Step = p.expr()
	}
	p.expect(RParen)
	return d
}

func (p *parser) arrayCons(pos Pos, end Kind) Expr {
	p.enter()
	defer p.leave()
	save := p.noSlash
	p.noSlash = false
	defer func() { p.noSlash = save }()
	a := &ArrayCons{Span: Span{pos}}
	if t := p.tok(); t.Kind == Word && p.hasDColon() {
		if !isTypeWord(t.S) && p.peek(1).Kind == DColon {
			p.i += 2
			a.Type = &TypeSpec{Span: Span{t.Pos}, Name: "type", Derived: t.S}
		} else if isTypeWord(t.S) {
			var ts *TypeSpec
			if p.try(func() { ts = p.typeSpec(true); p.expect(DColon) }) {
				a.Type = ts
			}
		}
	}
	for p.tok().Kind != end {
		a.Elems = append(a.Elems, p.expr())
		if !p.accept(Comma) {
			break
		}
	}
	p.expect(end)
	return a
}

// designator parses name { (args) | % name }.
func (p *parser) designator() Expr {
	id := p.ident()
	var x Expr = &id
	for {
		switch p.tok().Kind {
		case LParen:
			t := p.next()
			p.enter()
			args := p.args()
			p.leave()
			p.expect(RParen)
			x = &Index{Span{t.Pos}, x, args}
		case Percent:
			t := p.next()
			x = &Component{Span{t.Pos}, x, p.ident()}
		default:
			return x
		}
	}
}

// args parses an argument or subscript list up to (not including) ")".
func (p *parser) args() []Arg {
	save := p.noSlash
	p.noSlash = false
	defer func() { p.noSlash = save }()
	var out []Arg
	if p.tok().Kind == RParen {
		return nil
	}
	for {
		out = append(out, p.arg())
		if !p.accept(Comma) {
			return out
		}
	}
}

func (p *parser) arg() Arg {
	t := p.tok()
	a := Arg{Span: Span{t.Pos}}
	if t.Kind == Star {
		if n := p.peek(1); n.Kind == Int {
			p.i += 2
			a.AltReturn = n.S
			return a
		}
		p.i++
		a.Value = &Asterisk{Span{t.Pos}}
		return a
	}
	if t.Kind == Word && p.peek(1).Kind == Assign {
		p.i += 2
		a.Keyword = t.S
		if p.tok().Kind == Star {
			a.Value = &Asterisk{Span{p.next().Pos}}
			return a
		}
	}
	a.Value = p.subscript()
	return a
}

// subscript parses an expression or a triplet lo:hi:stride.
func (p *parser) subscript() Expr {
	t := p.tok()
	var lo Expr
	if t.Kind != Colon && t.Kind != DColon {
		lo = p.expr()
	}
	switch p.tok().Kind {
	case Colon:
		p.i++
		tr := &Triplet{Span: Span{t.Pos}, Lo: lo}
		if k := p.tok().Kind; k != Comma && k != RParen && k != Colon && k != EOF {
			tr.Hi = p.expr()
		}
		if p.accept(Colon) {
			tr.Stride = p.expr()
		}
		return tr
	case DColon:
		p.i++
		return &Triplet{Span: Span{t.Pos}, Lo: lo, Stride: p.expr()}
	}
	return lo
}

// exprList parses expressions up to (not including) the closing token.
func (p *parser) exprList(end Kind) []Expr {
	var out []Expr
	for p.tok().Kind != end && p.tok().Kind != EOF {
		out = append(out, p.expr())
		if !p.accept(Comma) {
			break
		}
	}
	return out
}

// assignLike reports whether the statement is "designator = ..." or
// "designator => ...", so that keywords may be used as names.
func (p *parser) assignLike() bool {
	j := p.i
	if j >= p.hi || p.toks[j].Kind != Word {
		return false
	}
	for {
		j++ // the name
		for j < p.hi && p.toks[j].Kind == LParen {
			depth := 0
			for ; j < p.hi; j++ {
				switch p.toks[j].Kind {
				case LParen:
					depth++
				case RParen:
					depth--
				}
				if depth == 0 {
					break
				}
			}
			if j >= p.hi {
				return false
			}
			j++
		}
		if j < p.hi && p.toks[j].Kind == Percent && j+1 < p.hi && p.toks[j+1].Kind == Word {
			j++
			continue
		}
		break
	}
	return j < p.hi && (p.toks[j].Kind == Assign || p.toks[j].Kind == Arrow)
}
