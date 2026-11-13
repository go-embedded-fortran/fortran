package syntax

// stmt parses the current statement (not yet consumed) by its first word.
func (p *parser) stmt() Stmt {
	t := p.tok()
	if t.Kind != Word {
		p.fail(t.Pos, "expected a statement, found %s", p.describe(t))
	}
	// construct name: "name: do ..."
	if p.peek(1).Kind == Colon && isConstructWord(p.peek(2).S) && p.peek(2).Kind == Word {
		save := p.i
		p.i += 2
		if p.assignLike() { // "name: where = 1": an assignment cannot carry a construct name
			p.i = save
		} else {
			p.cname = t.S
			t = p.tok()
		}
	}
	if p.assignLike() {
		return p.assignment()
	}
	w := t.S
	switch w {
	case "if":
		return p.ifStmt()
	case "do":
		return p.doStmt()
	case "select", "selectcase", "selecttype":
		return p.selectStmt()
	case "where":
		return p.whereStmt()
	case "forall":
		return p.forallStmt()
	case "associate":
		return p.associateStmt()
	case "block":
		if n := p.peek(1); n.Kind == EOF {
			return p.blockStmt()
		}
	case "call":
		return p.callStmt()
	case "goto", "go":
		return p.gotoStmt()
	case "continue":
		p.i++
		p.endStmt()
		return &Continue{p.base()}
	case "return":
		b := p.base()
		p.i++
		r := &Return{StmtBase: b}
		if !p.atEOS() {
			r.Alt = p.expr()
		}
		p.endStmt()
		return r
	case "stop", "errorstop":
		return p.stopStmt()
	case "error":
		if p.peek(1).Kind == Word && p.peek(1).S == "stop" {
			return p.stopStmt()
		}
	case "pause":
		b := p.base()
		p.i++
		s := &Pause{StmtBase: b}
		if !p.atEOS() {
			s.Code = p.expr()
		}
		p.endStmt()
		return s
	case "exit", "cycle":
		b := p.base()
		p.i++
		target := ""
		if p.tok().Kind == Word {
			target = p.next().S
		}
		p.endStmt()
		if w == "exit" {
			return &Exit{b, target}
		}
		return &Cycle{b, target}
	case "print", "read", "write", "open", "close", "inquire", "rewind", "backspace", "endfile", "flush", "wait":
		return p.ioStmt(w)
	case "end":
		if p.peek(1).Kind == Word && p.peek(1).S == "file" {
			p.i++
			return p.ioStmt("file")
		}
	case "allocate", "deallocate":
		return p.allocateStmt()
	case "nullify":
		return p.nullifyStmt()
	case "format":
		b := p.base()
		p.i++
		s := &FormatStmt{StmtBase: b}
		if p.tok().Kind == FormatText {
			s.Text = p.next().S
		} else {
			p.fail(p.tok().Pos, "malformed FORMAT statement")
		}
		p.endStmt()
		return s
	case "implicit":
		return p.implicitStmt()
	case "use":
		return p.useStmt()
	case "parameter":
		return p.parameterStmt()
	case "common":
		return p.commonStmt()
	case "equivalence":
		return p.equivalenceStmt()
	case "data":
		return p.dataStmt()
	case "namelist":
		return p.namelistStmt()
	case "import":
		return p.importStmt()
	case "entry":
		return p.entryStmt()
	case "interface", "abstract":
		return p.interfaceStmt()
	case "sequence":
		b := p.base()
		p.i++
		p.endStmt()
		return &KeywordStmt{b, "sequence"}
	case "type", "class":
		if w == "type" && p.peek(1).Kind != LParen {
			return p.typeDef()
		}
		return p.decl()
	case "integer", "real", "complex", "logical", "character", "doubleprecision", "doublecomplex", "double", "byte":
		return p.decl()
	case "bind":
		return p.bindStmt()
	case "dimension", "allocatable", "pointer", "target", "optional", "public", "private", "save", "external", "intrinsic",
		"value", "volatile", "protected", "contiguous", "asynchronous", "intent":
		return p.attrStmt(w)
	case "enum":
		return p.enumDef()
	case "enumerator":
		s := &Enumerator{StmtBase: p.base()}
		p.i++
		s.DColon = p.accept(DColon)
		s.Items = p.entities(true)
		p.endStmt()
		return s
	case "procedure":
		return p.procBinding(p.base())
	case "assign":
		return p.assignLabelStmt()
	case "contains":
		p.fail(t.Pos, "unexpected 'contains'")
	}
	if _, _, ok := p.endKind(); ok {
		p.fail(t.Pos, "unexpected %s statement", p.describe(t))
	}
	p.fail(t.Pos, "unrecognised statement beginning with %s", p.describe(t))
	return nil
}

func (p *parser) enumDef() Stmt {
	e := &EnumDef{StmtBase: p.base()}
	p.i++
	for p.accept(Comma) {
		e.Attrs = append(e.Attrs, p.attr())
	}
	p.endStmt()
	p.done()
	e.Body = p.block(func() bool { return p.atEnd("enum") })
	p.expectEnd("enum", e.Pos, "")
	return e
}

// isConstructWord: the statements that may carry a construct name.
func isConstructWord(w string) bool {
	switch w {
	case "do", "if", "select", "selectcase", "selecttype", "where", "forall", "associate", "block":
		return true
	}
	return false
}

func (p *parser) assignment() Stmt {
	b := p.base()
	lhs := p.designator()
	if p.accept(Arrow) {
		rhs := p.expr()
		p.endStmt()
		return &PointerAssign{b, lhs, rhs}
	}
	p.expect(Assign)
	rhs := p.expr()
	p.endStmt()
	return &Assignment{b, lhs, rhs}
}

// ---- types and declarations ----

// typeSpec parses a type specifier. With kinds false a following "(" is not
// taken as a kind selector (implicit statements).
func (p *parser) typeSpec(kinds bool) *TypeSpec {
	t := p.tok()
	if t.Kind != Word {
		p.fail(t.Pos, "expected a type, found %s", p.describe(t))
	}
	p.i++
	ts := &TypeSpec{Span: Span{t.Pos}, Name: t.S}
	switch t.S {
	case "double":
		if p.acceptWord("complex") {
			ts.Name = "doublecomplex"
		} else {
			p.expectWord("precision")
			ts.Name = "doubleprecision"
		}
	case "doubleprecision", "doublecomplex":
	case "integer", "real", "complex", "logical", "byte":
		if p.accept(Star) {
			ts.KindStar = true
			ts.Kind = &IntLit{Span{p.tok().Pos}, p.expect(Int).S, ""}
		} else if kinds && p.tok().Kind == LParen {
			p.i++
			if p.isWord("kind") && p.peek(1).Kind == Assign {
				p.i += 2
			}
			ts.Kind = p.expr()
			p.expect(RParen)
		}
	case "character":
		if p.accept(Star) {
			ts.LenStar = true
			if p.accept(LParen) {
				ts.Len = p.lenExpr()
				p.expect(RParen)
			} else {
				ts.Len = &IntLit{Span{p.tok().Pos}, p.expect(Int).S, ""}
			}
		} else if kinds && p.tok().Kind == LParen {
			p.i++
			first := true
			for p.tok().Kind != RParen {
				switch {
				case p.isWord("len") && p.peek(1).Kind == Assign:
					p.i += 2
					ts.Len = p.lenExpr()
				case p.isWord("kind") && p.peek(1).Kind == Assign:
					p.i += 2
					ts.Kind = p.expr()
				case first:
					ts.Len = p.lenExpr()
				default:
					ts.Kind = p.expr()
				}
				first = false
				if !p.accept(Comma) {
					break
				}
			}
			p.expect(RParen)
		}
	case "type", "class":
		p.expect(LParen)
		if p.tok().Kind == Star {
			p.i++
			ts.Derived = "*"
		} else if n := p.tok(); n.Kind == Word && isTypeWord(n.S) {
			// type(integer) of F2008 select type guards: keep the intrinsic type
			inner := p.typeSpec(true)
			p.expect(RParen)
			return inner
		} else {
			ts.Derived = p.ident().Name
		}
		// derived-type parameters are v1-excluded (bib 1.1): skip a parenthesised list
		if p.tok().Kind == LParen {
			p.fail(p.tok().Pos, "derived-type parameters are not supported")
		}
		p.expect(RParen)
	default:
		p.fail(t.Pos, "expected a type, found %s", p.describe(t))
	}
	return ts
}

func (p *parser) lenExpr() Expr {
	if p.tok().Kind == Star {
		return &Asterisk{Span{p.next().Pos}}
	}
	if p.tok().Kind == Colon {
		return &Triplet{Span: Span{p.next().Pos}}
	}
	return p.expr()
}

func (p *parser) dims() []Dim {
	p.expect(LParen)
	var out []Dim
	for {
		t := p.tok()
		d := Dim{Span: Span{t.Pos}}
		switch {
		case t.Kind == Star:
			p.i++
			d.Star = true
		case t.Kind == DotDot:
			p.i++
			d.Rank = true
		case t.Kind == Colon:
			p.i++
			d.Colon = true
			if k := p.tok().Kind; k != Comma && k != RParen {
				d.Hi = p.expr()
			}
		default:
			e := p.expr()
			if p.accept(Colon) {
				d.Lo, d.Colon = e, true
				if p.tok().Kind == Star {
					p.i++
					d.Star = true
				} else if k := p.tok().Kind; k != Comma && k != RParen {
					d.Hi = p.expr()
				}
			} else {
				d.Hi = e
			}
		}
		out = append(out, d)
		if !p.accept(Comma) {
			break
		}
	}
	p.expect(RParen)
	return out
}

func (p *parser) attr() Attr {
	t := p.tok()
	if t.Kind != Word {
		p.fail(t.Pos, "expected an attribute, found %s", p.describe(t))
	}
	p.i++
	a := Attr{Span: Span{t.Pos}, Name: t.S}
	switch t.S {
	case "dimension":
		if p.tok().Kind == LParen {
			a.Dims = p.dims()
		}
	case "intent":
		p.expect(LParen)
		w := p.ident().Name
		if w == "in" && p.isWord("out") {
			p.i++
			w = "inout"
		}
		a.Intent = w
		p.expect(RParen)
	case "codimension":
		p.fail(t.Pos, "coarrays are not supported (bib 1.1)")
	default:
		if p.tok().Kind == LParen {
			p.i++
			a.Args = p.argExprs()
			p.expect(RParen)
		}
	}
	return a
}

// argExprs parses a list of expressions allowing keyword = value (bind(c, name='x')).
func (p *parser) argExprs() []Expr {
	var out []Expr
	for p.tok().Kind != RParen && p.tok().Kind != EOF {
		if p.tok().Kind == Word && p.peek(1).Kind == Assign {
			k := p.next()
			p.i++
			out = append(out, &Binary{Span{k.Pos}, "=", &Ident{Span{k.Pos}, k.S, k.X}, p.expr()})
		} else {
			out = append(out, p.expr())
		}
		if !p.accept(Comma) {
			break
		}
	}
	return out
}

func (p *parser) entity(allowInit bool) Entity {
	id := p.ident()
	e := Entity{Span: id.Span, Name: id}
	if p.tok().Kind == LParen {
		e.Dims, e.HasDims = p.dims(), true
	}
	if p.tok().Kind == Star {
		p.i++
		if p.accept(LParen) {
			e.CharLen = p.lenExpr()
			p.expect(RParen)
		} else {
			e.CharLen = &IntLit{Span{p.tok().Pos}, p.expect(Int).S, ""}
		}
	}
	if allowInit {
		switch p.tok().Kind {
		case Assign:
			p.i++
			e.Init = p.expr()
		case Arrow:
			p.i++
			e.PtrInit = true
			e.Init = p.expr()
		}
	}
	return e
}

func (p *parser) entities(allowInit bool) []Entity {
	var out []Entity
	for {
		out = append(out, p.entity(allowInit))
		if !p.accept(Comma) {
			return out
		}
	}
}

func (p *parser) decl() Stmt {
	d := &Decl{StmtBase: p.base()}
	d.Type = p.typeSpec(true)
	// old "character*1, c": a comma after a star length without "::"
	oldComma := (d.Type.KindStar || d.Type.LenStar) && p.tok().Kind == Comma && !p.hasDColon()
	if oldComma {
		p.i++
	}
	for !oldComma && p.accept(Comma) {
		d.Attrs = append(d.Attrs, p.attr())
	}
	d.DColon = p.accept(DColon)
	if len(d.Attrs) > 0 && !d.DColon {
		p.fail(p.tok().Pos, "expected '::' after the attributes")
	}
	d.Entities = p.entities(true)
	p.endStmt()
	return d
}

// bindStmt parses bind(c [, name = "x"]) :: entities or /common blocks/.
func (p *parser) bindStmt() Stmt {
	s := &AttrStmt{StmtBase: p.base(), Attr: "bind"}
	p.i++
	p.expect(LParen)
	s.Args = p.argExprs()
	p.expect(RParen)
	s.DColon = p.accept(DColon)
	for !p.atEOS() {
		if p.accept(Slash) {
			s.Blocks = append(s.Blocks, p.ident().Name)
			p.expect(Slash)
		} else {
			s.Items = append(s.Items, p.entity(false))
		}
		if !p.accept(Comma) {
			break
		}
	}
	p.endStmt()
	return s
}

// hasDColon reports a "::" outside parentheses in the rest of the statement.
func (p *parser) hasDColon() bool {
	depth := 0
	for j := p.i; j < p.hi; j++ {
		switch p.toks[j].Kind {
		case LParen, LBrack, LParenSlash:
			depth++
		case RParen, RBrack, SlashRParen:
			depth--
		case DColon:
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

func (p *parser) attrStmt(w string) Stmt {
	s := &AttrStmt{StmtBase: p.base(), Attr: w}
	p.i++
	if w == "intent" {
		p.expect(LParen)
		s.Intent = p.ident().Name
		if s.Intent == "in" && p.isWord("out") {
			p.i++
			s.Intent = "inout"
		}
		p.expect(RParen)
	}
	s.DColon = p.accept(DColon)
	for !p.atEOS() {
		switch {
		case w == "save" && p.tok().Kind == Slash:
			p.i++
			s.Blocks = append(s.Blocks, p.ident().Name)
			p.expect(Slash)
		case (w == "public" || w == "private") && p.tok().Kind == Word && (p.tok().S == "operator" || p.tok().S == "assignment" || p.tok().S == "read" || p.tok().S == "write") && p.peek(1).Kind == LParen:
			t := p.tok()
			s.Items = append(s.Items, Entity{Span: Span{t.Pos}, Name: Ident{Span{t.Pos}, p.genericSpec(), ""}})
		default:
			s.Items = append(s.Items, p.entity(false))
		}
		if !p.accept(Comma) {
			break
		}
	}
	p.endStmt()
	return s
}

// genericSpec parses a name, operator(op), assignment(=) or read/write(formatted|unformatted).
func (p *parser) genericSpec() string {
	t := p.tok()
	if t.Kind != Word {
		p.fail(t.Pos, "expected a name or a generic specification, found %s", p.describe(t))
	}
	p.i++
	if p.tok().Kind != LParen || (t.S != "operator" && t.S != "assignment" && t.S != "read" && t.S != "write") {
		return t.S
	}
	p.i++
	var inner string
	switch o := p.next(); o.Kind {
	case DefOp:
		inner = "." + o.S + "."
	case Word:
		inner = o.S
	case Assign:
		inner = "="
	case Plus:
		inner = "+"
	case Minus:
		inner = "-"
	case Star:
		inner = "*"
	case Slash:
		inner = "/"
	case Pow:
		inner = "**"
	case Concat:
		inner = "//"
	case Eq, Ne, Lt, Le, Gt, Ge:
		inner = relOp(o.Kind)
	case And, Or, Not, Eqv, Neqv:
		inner = o.S
	default:
		p.fail(o.Pos, "unexpected %s in a generic specification", p.describe(o))
	}
	p.expect(RParen)
	return t.S + "(" + inner + ")"
}

func (p *parser) implicitStmt() Stmt {
	s := &ImplicitStmt{StmtBase: p.base()}
	p.i++
	if p.acceptWord("none") {
		s.None = true
		if p.accept(LParen) { // implicit none (type, external)
			for p.tok().Kind != RParen && !p.atEOS() {
				p.i++
			}
			p.expect(RParen)
		}
		p.endStmt()
		return s
	}
	for {
		var sp ImplicitSpec
		kinds := !p.looksLikeLetters()
		sp.Type = p.typeSpec(kinds)
		if !kinds || p.tok().Kind == LParen {
			p.expect(LParen)
			for {
				lo := p.ident()
				if len(lo.Name) != 1 {
					p.fail(lo.Pos, "expected a letter in the implicit range, found '%s'", lo.Name)
				}
				hi := lo
				if p.accept(Minus) {
					hi = p.ident()
					if len(hi.Name) != 1 {
						p.fail(hi.Pos, "expected a letter in the implicit range, found '%s'", hi.Name)
					}
				}
				sp.Ranges = append(sp.Ranges, [2]byte{lo.Name[0], hi.Name[0]})
				if !p.accept(Comma) {
					break
				}
			}
			p.expect(RParen)
		}
		s.Specs = append(s.Specs, sp)
		if !p.accept(Comma) {
			break
		}
	}
	p.endStmt()
	return s
}

// looksLikeLetters: at "type-name (" followed by letter ranges and ")".
func (p *parser) looksLikeLetters() bool {
	j := p.i + 1
	if p.tok().S == "double" {
		j++
	}
	if j >= p.hi || p.toks[j].Kind != LParen {
		return false
	}
	j++
	for {
		if j >= p.hi || p.toks[j].Kind != Word || len(p.toks[j].S) != 1 {
			return false
		}
		j++
		if j < p.hi && p.toks[j].Kind == Minus {
			j++
			if j >= p.hi || p.toks[j].Kind != Word || len(p.toks[j].S) != 1 {
				return false
			}
			j++
		}
		if j < p.hi && p.toks[j].Kind == Comma {
			j++
			continue
		}
		break
	}
	return j < p.hi && p.toks[j].Kind == RParen && (j+1 >= p.hi || p.toks[j+1].Kind == Comma)
}

func (p *parser) useStmt() Stmt {
	s := &UseStmt{StmtBase: p.base()}
	p.i++
	if p.accept(Comma) {
		s.Nature = p.ident().Name
		p.expect(DColon)
	} else {
		p.accept(DColon)
	}
	s.Module = p.ident()
	for p.accept(Comma) {
		if p.isWord("only") && p.peek(1).Kind == Colon {
			p.i += 2
			s.Only = true
			for !p.atEOS() {
				s.Items = append(s.Items, p.useItem())
				if !p.accept(Comma) {
					break
				}
			}
			break
		}
		s.Items = append(s.Items, p.useItem())
	}
	p.endStmt()
	return s
}

func (p *parser) useItem() UseItem {
	t := p.tok()
	it := UseItem{Span: Span{t.Pos}}
	if t.Kind == Word && (t.S == "operator" || t.S == "assignment" || t.S == "read" || t.S == "write") && p.peek(1).Kind == LParen {
		it.Op = p.genericSpec()
		if p.accept(Arrow) {
			it.Local, it.Remote = Ident{Span: Span{t.Pos}, Name: it.Op}, Ident{Span: Span{t.Pos}, Name: p.genericSpec()}
			it.Op = "rename"
		}
		return it
	}
	id := p.ident()
	if p.accept(Arrow) {
		it.Local, it.Remote = id, p.ident()
	} else {
		it.Remote = id
	}
	return it
}

func (p *parser) parameterStmt() Stmt {
	s := &ParameterStmt{StmtBase: p.base()}
	p.i++
	p.expect(LParen)
	for {
		id := p.ident()
		p.expect(Assign)
		s.Items = append(s.Items, Entity{Span: id.Span, Name: id, Init: p.expr()})
		if !p.accept(Comma) {
			break
		}
	}
	p.expect(RParen)
	p.endStmt()
	return s
}

func (p *parser) commonStmt() Stmt {
	s := &CommonStmt{StmtBase: p.base()}
	p.i++
	cur := CommonBlock{}
	started := false
	flush := func() {
		if started || len(cur.Items) > 0 {
			s.Blocks = append(s.Blocks, cur)
		}
	}
	for !p.atEOS() {
		switch p.tok().Kind {
		case Concat:
			p.i++
			flush()
			cur, started = CommonBlock{}, true
		case Slash:
			p.i++
			flush()
			cur, started = CommonBlock{}, true
			if p.tok().Kind == Word {
				cur.Name = p.ident().Name
			}
			p.expect(Slash)
		default:
			cur.Items = append(cur.Items, p.entity(false))
			p.accept(Comma)
		}
	}
	flush()
	p.endStmt()
	return s
}

func (p *parser) equivalenceStmt() Stmt {
	s := &EquivalenceStmt{StmtBase: p.base()}
	p.i++
	for {
		p.expect(LParen)
		set := p.exprList(RParen)
		p.expect(RParen)
		s.Sets = append(s.Sets, set)
		if !p.accept(Comma) {
			break
		}
	}
	p.endStmt()
	return s
}

func (p *parser) dataStmt() Stmt {
	s := &DataStmt{StmtBase: p.base()}
	p.i++
	for !p.atEOS() {
		var ds DataSet
		save := p.noSlash
		p.noSlash = true
		for {
			ds.Objects = append(ds.Objects, p.expr())
			if !p.accept(Comma) {
				break
			}
		}
		p.expect(Slash)
		for {
			ds.Values = append(ds.Values, p.expr())
			if !p.accept(Comma) {
				break
			}
		}
		p.noSlash = save
		p.expect(Slash)
		s.Sets = append(s.Sets, ds)
		p.accept(Comma)
	}
	p.endStmt()
	return s
}

func (p *parser) namelistStmt() Stmt {
	s := &NamelistStmt{StmtBase: p.base()}
	p.i++
	for !p.atEOS() {
		p.expect(Slash)
		g := NamelistGroup{Name: p.ident().Name}
		p.expect(Slash)
		for p.tok().Kind == Word {
			g.Items = append(g.Items, p.ident())
			if !p.accept(Comma) {
				break
			}
		}
		s.Groups = append(s.Groups, g)
		p.accept(Comma)
	}
	p.endStmt()
	return s
}

func (p *parser) importStmt() Stmt {
	s := &ImportStmt{StmtBase: p.base()}
	p.i++
	p.accept(DColon)
	for p.tok().Kind == Word {
		s.Names = append(s.Names, p.ident())
		if !p.accept(Comma) {
			break
		}
	}
	p.endStmt()
	return s
}

func (p *parser) entryStmt() Stmt {
	s := &EntryStmt{StmtBase: p.base()}
	p.i++
	s.Ident = p.ident()
	if p.accept(LParen) {
		for p.tok().Kind != RParen {
			if p.tok().Kind == Star {
				s.Params = append(s.Params, Ident{Span{p.next().Pos}, "*", ""})
			} else {
				s.Params = append(s.Params, p.ident())
			}
			if !p.accept(Comma) {
				break
			}
		}
		p.expect(RParen)
	}
	for p.tok().Kind == Word {
		switch p.tok().S {
		case "result":
			p.i++
			p.expect(LParen)
			r := p.ident()
			p.expect(RParen)
			s.Result = &r
		case "bind":
			p.i++
			s.HasBind = true
			p.expect(LParen)
			s.Bind = p.argExprs()
			p.expect(RParen)
		default:
			p.fail(p.tok().Pos, "unexpected %s in the entry statement", p.describe(p.tok()))
		}
	}
	p.endStmt()
	return s
}

// ---- derived types and interfaces ----

func (p *parser) typeDef() Stmt {
	d := &TypeDef{StmtBase: p.base()}
	p.i++
	for p.accept(Comma) {
		d.Attrs = append(d.Attrs, p.attr())
	}
	p.accept(DColon)
	d.Ident = p.ident()
	if p.tok().Kind == LParen {
		p.fail(p.tok().Pos, "derived-type parameters are not supported (bib 1.1)")
	}
	p.endStmt()
	p.done()
	bound := false
	p.enter()
	defer p.leave()
	for p.load() {
		if p.blockEnd() && p.word() != "contains" || p.atEnd("type") {
			break
		}
		if p.word() == "contains" {
			bound = true
			d.HasContains = true
			p.done()
			continue
		}
		var s Stmt
		if bound {
			s = p.boundStmt()
		} else if p.word() == "private" || p.word() == "public" || p.word() == "sequence" {
			s = p.typeKeywordStmt()
		} else {
			s = p.statement()
			d.Body = append(d.Body, s)
			continue
		}
		if bound {
			d.Bound = append(d.Bound, s)
		} else {
			d.Body = append(d.Body, s)
		}
	}
	if p.load() && p.atEnd("type") {
		_, w, _ := p.endKind()
		p.i += w
		func() {
			defer p.recoverStmt()
			if p.tok().Kind == Word {
				d.EndName = p.ident().Name
			}
			p.endStmt()
		}()
		p.done()
	} else {
		p.errorAt(p.eos, "missing end type for type "+d.Ident.Name)
	}
	return d
}

// recoverStmt is deferred by code that parses a statement tail it can skip.
func (p *parser) recoverStmt() {
	if r := recover(); r != nil {
		if _, ok := r.(bailout); !ok {
			panic(r)
		}
	}
}

func (p *parser) typeKeywordStmt() Stmt {
	return p.guarded(func() Stmt {
		b := p.base()
		w := p.next().S
		p.endStmt()
		return &KeywordStmt{b, w}
	})
}

// guarded runs a statement parser with the recovery of statement().
func (p *parser) guarded(f func() Stmt) (s Stmt) {
	seq := p.seq
	pos := p.tok().Pos
	label := p.label
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(bailout); !ok {
				panic(r)
			}
			s = &BadStmt{StmtBase{Span: Span{pos}, Label: label}}
		}
		if p.seq == seq {
			p.done()
		}
	}()
	return f()
}

func (p *parser) boundStmt() Stmt {
	return p.guarded(func() Stmt {
		t := p.tok()
		b := p.base()
		switch t.S {
		case "private", "public", "sequence":
			p.i++
			p.endStmt()
			return &KeywordStmt{b, t.S}
		case "procedure", "generic", "final":
			return p.procBinding(b)
		}
		p.fail(t.Pos, "expected a type-bound procedure binding, found %s", p.describe(t))
		return nil
	})
}

func (p *parser) procBinding(b StmtBase) Stmt {
	s := &ProcBinding{StmtBase: b}
	kind := p.next().S
	s.Kind = kind
	if kind == "procedure" && p.tok().Kind == LParen {
		p.i++
		s.HasIface = true
		if p.tok().Kind != RParen {
			if n := p.tok(); n.Kind == Word && isTypeWord(n.S) {
				s.Iface = PrintTypeSpec(p.typeSpec(true))
			} else {
				s.Iface = p.ident().Name
			}
		}
		p.expect(RParen)
	}
	for p.accept(Comma) {
		s.Attrs = append(s.Attrs, p.attr())
	}
	s.DColon = p.accept(DColon)
	for !p.atEOS() {
		t := p.tok()
		it := ProcItem{Span: Span{t.Pos}}
		if kind == "generic" {
			it.Name = p.genericSpec()
		} else {
			it.Name = p.ident().Name
		}
		if p.accept(Arrow) {
			if kind == "generic" {
				it.Target = p.ident().Name
				for p.accept(Comma) {
					it.Target += "," + p.ident().Name
				}
			} else if kind == "procedure" {
				it.Target = PrintExpr(p.expr()) // "=> null()" for a procedure pointer
			} else {
				it.Target = p.ident().Name
			}
		}
		s.Items = append(s.Items, it)
		if !p.accept(Comma) {
			break
		}
	}
	p.endStmt()
	return s
}

func (p *parser) interfaceStmt() Stmt {
	s := &Interface{StmtBase: p.base()}
	if p.acceptWord("abstract") {
		s.Abstract = true
	}
	p.expectWord("interface")
	if !p.atEOS() {
		s.Generic = p.genericSpec()
	}
	p.endStmt()
	p.done()
	p.enter()
	defer p.leave()
	for p.load() {
		if p.atEnd("interface") || p.atUnitEnd() {
			break
		}
		switch {
		case p.isUnitStart():
			s.Body = append(s.Body, p.unit())
		case p.word() == "module" && p.peek(1).Kind == Word && p.peek(1).S == "procedure":
			s.Body = append(s.Body, p.guarded(func() Stmt {
				b := p.base()
				p.i++
				pb := p.procBinding(b).(*ProcBinding)
				pb.Kind = "module procedure"
				return pb
			}))
		case p.word() == "procedure":
			s.Body = append(s.Body, p.guarded(func() Stmt { return p.procBinding(p.base()) }))
		default:
			s.Body = append(s.Body, p.statement())
		}
	}
	if p.load() && p.atEnd("interface") {
		_, w, _ := p.endKind()
		p.i += w
		func() {
			defer p.recoverStmt()
			if !p.atEOS() {
				p.genericSpec()
			}
			p.endStmt()
		}()
		p.done()
	} else {
		p.errorAt(p.eos, "missing end interface")
	}
	return s
}
