package syntax

import (
	"fmt"
	"strconv"
)

type bailout struct{}

type parser struct {
	sc   *scanner
	lx   lexer
	fset *FileSet
	lim  Limits
	errs ErrorList
	stop bool

	toks  []Token // tokens of the current logical line
	nx    int     // next token of the line not yet assigned to a statement
	lend  int
	i, hi int // cursor and end of the current statement
	have  bool
	label int
	eos   Pos
	seq   int

	depth       int
	closedLabel int
	noSlash     bool
	cname       string
}

// Parse parses one source file. The File is never nil; the error, if any, is
// an ErrorList of "file:line:col: message" diagnostics. Parse never panics.
func Parse(name string, src []byte, opts *Options) (*File, error) {
	if opts == nil {
		opts = &Options{}
	}
	lim := opts.Limits.fill()
	form := opts.Form
	if form == Auto {
		form = DetectForm(name)
	}
	fset := &FileSet{}
	file := &File{Name: name, Form: form, Set: fset}
	p := &parser{fset: fset, lim: lim}
	p.lx = lexer{intern: map[string]string{}, errf: p.errorAt}
	if len(src) > lim.MaxSourceBytes {
		f := fset.add(name, nil)
		p.errorAt(f.pos(0), fmt.Sprintf("source is %d bytes, over the limit of %d: refused", len(src), lim.MaxSourceBytes))
		return file, p.errs
	}
	f := fset.add(name, src)
	p.sc = &scanner{fset: fset, opts: opts, form: form, lim: lim, errf: p.errorAt, total: len(src)}
	st := &fileState{f: f}
	if clean := cleanPath(name); clean != "" {
		st.path = clean
	}
	p.sc.stack = append(p.sc.stack, st)
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(bailout); !ok {
					p.errorAt(f.pos(0), fmt.Sprintf("internal parser error: %v", r))
				}
			}
		}()
		file.Units = p.file()
	}()
	if len(p.errs) == 0 {
		return file, nil
	}
	return file, p.errs
}

func cleanPath(name string) string {
	if name == "" {
		return ""
	}
	return name
}

func (p *parser) errorAt(pos Pos, msg string) {
	if p.stop {
		return
	}
	if len(p.errs) >= p.lim.MaxErrors {
		p.stop = true
		p.errs = append(p.errs, Error{Pos: p.fset.Position(pos), Msg: fmt.Sprintf("too many errors (limit %d): parse stopped", p.lim.MaxErrors)})
		return
	}
	p.errs = append(p.errs, Error{Pos: p.fset.Position(pos), Msg: msg})
}

// fail reports an error and abandons the current statement.
func (p *parser) fail(pos Pos, format string, args ...any) {
	p.errorAt(pos, fmt.Sprintf(format, args...))
	panic(bailout{})
}

// try runs f, discarding its errors; it reports whether f succeeded.
func (p *parser) try(f func()) (ok bool) {
	n, i, d := len(p.errs), p.i, p.depth
	stop := p.stop
	defer func() {
		if r := recover(); r != nil {
			if _, isB := r.(bailout); !isB {
				panic(r)
			}
			ok = false
		}
		if !ok {
			p.errs = p.errs[:n]
			p.i, p.depth = i, d
			p.stop = stop
		}
	}()
	f()
	return true
}

func (p *parser) enter() {
	p.depth++
	if p.depth > p.lim.MaxDepth {
		p.fail(p.tok().Pos, "nesting deeper than %d: refused", p.lim.MaxDepth)
	}
}
func (p *parser) leave() { p.depth-- }

// ---- statement cursor ----

// load makes the next statement current; it reports false at the end of input.
func (p *parser) load() bool {
	if p.have {
		return true
	}
	for !p.stop {
		for p.nx < p.lend && p.toks[p.nx].Kind == Semi {
			p.nx++
		}
		if p.nx >= p.lend {
			line, ok := p.sc.next()
			if !ok {
				return false
			}
			p.toks = p.lx.tokenize(line)
			p.lend = len(p.toks)
			p.nx = 0
			p.eos = line.posAt(len(line.text))
			if p.eos == 0 && len(p.toks) > 0 {
				p.eos = p.toks[len(p.toks)-1].Pos + 1
			}
			if p.lend > p.lim.MaxStatementToks {
				p.errorAt(p.toks[0].Pos, fmt.Sprintf("statement has more than %d tokens: refused", p.lim.MaxStatementToks))
				p.nx, p.lend = 0, 0
			}
			continue
		}
		end := p.nx
		for end < p.lend && p.toks[end].Kind != Semi {
			end++
		}
		p.i, p.hi = p.nx, end
		p.nx = end
		p.have = true
		p.seq++
		p.label = 0
		if t := p.toks[p.i]; t.Kind == Int && t.X == "" {
			n, err := strconv.Atoi(t.S)
			if err != nil || n > 99999 {
				p.errorAt(t.Pos, "invalid statement label "+t.S)
			}
			p.label = n
			p.i++
			if p.i >= p.hi {
				p.errorAt(t.Pos, "statement label without a statement")
				p.have = false
				continue
			}
		}
		return true
	}
	return false
}

// done consumes the current statement.
func (p *parser) done() {
	p.have = false
	p.i = p.hi
}

func (p *parser) tok() Token {
	if p.i < p.hi {
		return p.toks[p.i]
	}
	return Token{Kind: EOF, Pos: p.eos}
}

func (p *parser) peek(n int) Token {
	if p.i+n < p.hi {
		return p.toks[p.i+n]
	}
	return Token{Kind: EOF, Pos: p.eos}
}

func (p *parser) next() Token {
	t := p.tok()
	if p.i < p.hi {
		p.i++
	}
	return t
}

func (p *parser) atEOS() bool { return p.i >= p.hi }

func (p *parser) isWord(w string) bool {
	t := p.tok()
	return t.Kind == Word && t.S == w
}

func (p *parser) acceptWord(w string) bool {
	if p.isWord(w) {
		p.i++
		return true
	}
	return false
}

func (p *parser) accept(k Kind) bool {
	if p.tok().Kind == k {
		p.i++
		return true
	}
	return false
}

func (p *parser) describe(t Token) string {
	switch t.Kind {
	case Word:
		return "'" + t.S + "'"
	case Int, Real, DefOp:
		return t.Kind.String() + " " + t.S
	}
	return t.Kind.String()
}

func (p *parser) expect(k Kind) Token {
	t := p.tok()
	if t.Kind != k {
		p.fail(t.Pos, "expected %s, found %s", k, p.describe(t))
	}
	p.i++
	return t
}

func (p *parser) expectWord(w string) Token {
	t := p.tok()
	if t.Kind != Word || t.S != w {
		p.fail(t.Pos, "expected '%s', found %s", w, p.describe(t))
	}
	p.i++
	return t
}

func (p *parser) ident() Ident {
	t := p.tok()
	if t.Kind != Word {
		p.fail(t.Pos, "expected a name, found %s", p.describe(t))
	}
	p.i++
	return Ident{Span{t.Pos}, t.S, t.X}
}

// endStmt checks that the statement is fully consumed.
func (p *parser) endStmt() {
	if p.i < p.hi {
		t := p.tok()
		p.fail(t.Pos, "unexpected %s at the end of the statement", p.describe(t))
	}
}

// word returns the keyword the current statement starts with, "" if none.
func (p *parser) word() string {
	if p.i < p.hi && p.toks[p.i].Kind == Word {
		return p.toks[p.i].S
	}
	return ""
}

func (p *parser) base() StmtBase {
	return StmtBase{Span: Span{p.tok().Pos}, Label: p.label, Name: p.cname}
}

// endKind recognises the end statements: "" for a bare END, otherwise the
// construct ("if", "do", "program", "blockdata", ...).
func (p *parser) endKind() (kind string, width int, ok bool) {
	t := p.tok()
	if t.Kind != Word {
		return "", 0, false
	}
	if t.S == "end" {
		n := p.peek(1)
		if n.Kind == Word {
			if n.S == "block" && p.peek(2).Kind == Word && p.peek(2).S == "data" {
				return "blockdata", 3, true
			}
			if n.S == "file" || n.S == "double" {
				return "", 0, false
			}
			return n.S, 2, true
		}
		return "", 1, true
	}
	if len(t.S) > 3 && t.S[:3] == "end" {
		switch rest := t.S[3:]; rest {
		case "if", "do", "select", "where", "forall", "type", "interface", "program", "subroutine", "function",
			"module", "submodule", "blockdata", "block", "associate", "enum", "critical", "procedure":
			return rest, 1, true
		}
	}
	return "", 0, false
}

func (p *parser) atUnitEnd() bool {
	k, _, ok := p.endKind()
	if !ok {
		return false
	}
	switch k {
	case "", "program", "subroutine", "function", "module", "submodule", "blockdata", "procedure":
		return true
	}
	return false
}

func (p *parser) atEnd(kind string) bool {
	k, _, ok := p.endKind()
	return ok && k == kind
}

// blockEnd is true at a statement that no construct body may contain.
func (p *parser) blockEnd() bool { return p.atUnitEnd() || p.word() == "contains" }

// block parses statements until stop (or a unit end) holds; the stopping
// statement is left current.
func (p *parser) block(stop func() bool) []Stmt {
	p.enter()
	defer p.leave()
	var out []Stmt
	for p.load() {
		if p.blockEnd() || stop() {
			break
		}
		out = append(out, p.statement())
	}
	return out
}

// statement parses and consumes the current statement, recovering from errors.
func (p *parser) statement() (s Stmt) {
	p.closedLabel = 0
	seq := p.seq
	pos := p.tok().Pos
	label := p.label
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(bailout); !ok {
				p.errorAt(pos, fmt.Sprintf("internal parser error: %v", r))
			}
			s = &BadStmt{StmtBase{Span: Span{pos}, Label: label}}
		}
		if p.seq == seq {
			p.done()
		}
	}()
	p.cname = ""
	return p.stmt()
}

// ---- file and program units ----

func (p *parser) file() []*Unit {
	var units []*Unit
	for p.load() {
		if p.atUnitEnd() {
			p.errorAt(p.tok().Pos, "unexpected end statement outside a program unit")
			p.done()
			continue
		}
		if p.isUnitStart() {
			units = append(units, p.unit())
			continue
		}
		units = append(units, p.implicitMain())
	}
	return units
}

// implicitMain parses a main program without a program statement (the
// statements of an F77 file, or of a cell) up to its END.
func (p *parser) implicitMain() *Unit {
	u := &Unit{StmtBase: p.base(), Kind: ProgramUnit, Implicit: true}
	u.Body = p.block(func() bool { return false })
	p.unitTail(u)
	return u
}

var unitPrefixes = map[string]bool{"recursive": true, "pure": true, "elemental": true, "impure": true}

func isTypeWord(w string) bool {
	switch w {
	case "integer", "real", "complex", "logical", "character", "doubleprecision", "double", "type", "class", "byte":
		return true
	}
	return false
}

// hasFunctionWord scans a statement for "function" before any "::", ",",
// "=" outside parentheses: the cheap pre-test of "real function f(x)".
func (p *parser) hasFunctionWord() bool {
	depth := 0
	for j := p.i; j < p.hi; j++ {
		t := p.toks[j]
		switch t.Kind {
		case LParen:
			depth++
		case RParen:
			depth--
		case DColon, Comma, Assign:
			if depth == 0 {
				return false
			}
		case Word:
			if depth == 0 && t.S == "function" {
				return true
			}
		}
	}
	return false
}

func (p *parser) isUnitStart() bool {
	save := p.i
	defer func() { p.i = save }()
	if p.assignLike() {
		return false
	}
	for {
		t := p.tok()
		if t.Kind != Word {
			return false
		}
		switch w := t.S; {
		case w == "program" || w == "submodule" || w == "subroutine" || w == "function" || w == "blockdata":
			return true
		case w == "block":
			n := p.peek(1)
			return n.Kind == Word && n.S == "data"
		case w == "module":
			n := p.peek(1)
			if n.Kind != Word || n.S == "procedure" {
				return false
			}
			if n.S == "subroutine" || n.S == "function" {
				p.i++
				continue
			}
			return true
		case unitPrefixes[w]:
			p.i++
		case isTypeWord(w):
			if !p.hasFunctionWord() {
				return false
			}
			if !p.try(func() { p.typeSpec(true) }) {
				return false
			}
			for p.tok().Kind == Word && unitPrefixes[p.tok().S] {
				p.i++
			}
			return p.isWord("function")
		default:
			return false
		}
	}
}

// unit parses a program unit whose header is the current statement.
func (p *parser) unit() *Unit {
	u := &Unit{}
	hdrSeq := p.seq
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(bailout); !ok {
					panic(r)
				}
				// bad header: skip it, still parse the body so that its END is consumed
				if p.seq == hdrSeq {
					p.done()
				}
			}
		}()
		p.unitHeader(u)
		p.endStmt()
		p.done()
	}()
	p.unitBody(u)
	return u
}

func (p *parser) unitHeader(u *Unit) {
	u.StmtBase = p.base()
	for {
		t := p.tok()
		if t.Kind == Word && (unitPrefixes[t.S] || (t.S == "module" && p.peek(1).Kind == Word && (p.peek(1).S == "subroutine" || p.peek(1).S == "function"))) {
			u.Prefix = append(u.Prefix, t.S)
			p.i++
			continue
		}
		if t.Kind == Word && isTypeWord(t.S) && u.ResultType == nil {
			u.ResultType = p.typeSpec(true)
			continue
		}
		break
	}
	t := p.tok()
	switch t.S {
	case "program":
		p.i++
		u.Kind = ProgramUnit
		u.Ident = p.ident()
	case "module":
		p.i++
		u.Kind = ModuleUnit
		u.Ident = p.ident()
	case "submodule":
		p.i++
		u.Kind = SubmoduleUnit
		p.expect(LParen)
		u.Ancestor = p.ident()
		if p.accept(Colon) {
			u.Parent = p.ident()
		}
		p.expect(RParen)
		u.Ident = p.ident()
	case "block", "blockdata":
		p.i++
		if t.S == "block" {
			p.expectWord("data")
		}
		u.Kind = BlockDataUnit
		if p.tok().Kind == Word {
			u.Ident = p.ident()
		}
	case "subroutine", "function":
		p.i++
		u.Kind = SubroutineUnit
		if t.S == "function" {
			u.Kind = FunctionUnit
		}
		u.Ident = p.ident()
		if p.accept(LParen) {
			for p.tok().Kind != RParen {
				if p.tok().Kind == Star {
					u.Params = append(u.Params, Ident{Span{p.next().Pos}, "*", ""})
				} else {
					u.Params = append(u.Params, p.ident())
				}
				if !p.accept(Comma) {
					break
				}
			}
			p.expect(RParen)
		} else if u.Kind == FunctionUnit {
			p.fail(p.tok().Pos, "expected '(' after the function name")
		}
		for p.tok().Kind == Word {
			switch p.tok().S {
			case "result":
				p.i++
				p.expect(LParen)
				r := p.ident()
				p.expect(RParen)
				u.Result = &r
			case "bind":
				p.i++
				u.HasBind = true
				p.expect(LParen)
				u.Bind = p.exprList(RParen)
				p.expect(RParen)
			default:
				p.fail(p.tok().Pos, "unexpected %s in the procedure header", p.describe(p.tok()))
			}
		}
	default:
		p.fail(t.Pos, "expected a program unit, found %s", p.describe(t))
	}
}

func (p *parser) unitBody(u *Unit) {
	u.Body = p.block(func() bool { return false })
	if p.load() && p.word() == "contains" {
		u.HasContains = true
		p.done()
		p.enter()
		for p.load() {
			if p.atUnitEnd() {
				break
			}
			if p.isUnitStart() {
				u.Contains = append(u.Contains, p.unit())
				continue
			}
			p.errorAt(p.tok().Pos, "expected a procedure after 'contains', found "+p.describe(p.tok()))
			p.statement()
		}
		p.leave()
	}
	p.unitTail(u)
}

var unitEndWords = [...]string{"program", "module", "submodule", "subroutine", "function", "blockdata"}

// unitTail consumes the END statement of u.
func (p *parser) unitTail(u *Unit) {
	if !p.load() {
		p.errorAt(p.eos, fmt.Sprintf("missing end statement for %s %s", u.Kind, u.Ident.Name))
		return
	}
	pos := p.tok().Pos
	u.EndPos = pos
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(bailout); !ok {
					panic(r)
				}
			}
		}()
		k, w, _ := p.endKind()
		p.i += w
		want := unitEndWords[u.Kind]
		switch u.Kind {
		case SubroutineUnit, FunctionUnit:
			if k == "procedure" {
				want = "procedure"
			}
		}
		if k != "" && k != want {
			p.fail(pos, "end %s does not match %s %s", k, u.Kind, u.Ident.Name)
		}
		if p.tok().Kind == Word {
			n := p.ident()
			u.EndName = n.Name
			if u.Ident.Name != "" && n.Name != u.Ident.Name {
				p.errorAt(n.Pos, fmt.Sprintf("end name '%s' does not match '%s'", n.Name, u.Ident.Name))
			}
		}
		p.endStmt()
	}()
	p.done()
}
