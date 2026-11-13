package syntax

// Kind is a token kind. Fortran has no reserved words: keywords are Ident
// tokens, told apart by the parser from their position (F2018 6.2.2).
type Kind uint8

// Token kinds.
const (
	EOF Kind = iota
	Illegal
	Word
	Int
	Real
	String
	BOZ
	Logical // .true. / .false.: S is "true" or "false"
	DefOp   // .foo. : S is the lower-case name without dots
	FormatText

	LParen
	RParen
	Comma
	Colon
	DColon
	Assign // =
	Arrow  // =>
	Percent
	Semi
	LBrack
	RBrack
	LParenSlash // (/
	SlashRParen // /)
	Star
	Pow
	Slash
	Concat
	Plus
	Minus
	Lt
	Gt
	Le
	Ge
	Eq
	Ne
	And
	Or
	Not
	Eqv
	Neqv
	Dollar
	DotDot
)

var kindNames = [...]string{"end of statement", "illegal character", "name", "integer literal", "real literal", "character literal",
	"BOZ literal", "logical literal", "defined operator", "format", "'('", "')'", "','", "':'", "'::'", "'='", "'=>'", "'%'",
	"';'", "'['", "']'", "'(/'", "'/)'", "'*'", "'**'", "'/'", "'//'", "'+'", "'-'", "'<'", "'>'", "'<='", "'>='", "'=='", "'/='",
	"'.and.'", "'.or.'", "'.not.'", "'.eqv.'", "'.neqv.'", "'$'", "'..'"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "token"
}

// Token is a lexical token. For Ident, S is the lower-case name and X the
// original spelling when it differs; for numbers S is the literal text
// (lower-cased) and X the kind parameter after "_"; for String S is the
// unescaped content and X the kind parameter before the quote; for BOZ S is
// the digits and X the base letter.
type Token struct {
	Kind Kind
	Pos  Pos
	S    string
	X    string
}

type lexer struct {
	l      *logLine
	b      []byte
	i      int
	toks   []Token
	intern map[string]string
	errf   func(Pos, string)
	buf    []byte
}

func (lx *lexer) str(b []byte) string {
	if s, ok := lx.intern[string(b)]; ok {
		return s
	}
	s := string(b)
	if len(lx.intern) < 1<<16 {
		lx.intern[s] = s
	}
	return s
}

func (lx *lexer) lowerStr(b []byte) (low, orig string) {
	changed := false
	for _, c := range b {
		if c >= 'A' && c <= 'Z' {
			changed = true
			break
		}
	}
	if !changed {
		return lx.str(b), ""
	}
	lx.buf = lx.buf[:0]
	for _, c := range b {
		lx.buf = append(lx.buf, lower(c))
	}
	return lx.str(lx.buf), string(b)
}

func (lx *lexer) emit(k Kind, start int, s, x string) {
	lx.toks = append(lx.toks, Token{Kind: k, Pos: lx.l.posAt(start), S: s, X: x})
}

var dotWords = map[string]Kind{
	"and": And, "or": Or, "not": Not, "eqv": Eqv, "neqv": Neqv,
	"eq": Eq, "ne": Ne, "lt": Lt, "le": Le, "gt": Gt, "ge": Ge,
}

// tokenize lexes one logical line into lx.toks (reset first).
func (lx *lexer) tokenize(l *logLine) []Token {
	lx.l, lx.b, lx.i, lx.toks = l, l.text, 0, lx.toks[:0]
	b := lx.b
	// FORMAT statement: kept as text
	lx.formatStmt()
	for lx.i < len(b) {
		c := b[lx.i]
		start := lx.i
		switch {
		case isBlank(c):
			lx.i++
		case isLetter(c):
			j := lx.i + 1
			for j < len(b) && isNameCh(b[j]) {
				j++
			}
			// BOZ literal: b'..' o'..' z'..' x'..'
			if j-lx.i == 1 && j < len(b) && (b[j] == '\'' || b[j] == '"') && (lower(c) == 'b' || lower(c) == 'o' || lower(c) == 'z' || lower(c) == 'x') {
				q := b[j]
				k := j + 1
				for k < len(b) && b[k] != q && isHex(b[k]) {
					k++
				}
				if k < len(b) && b[k] == q {
					lx.emit(BOZ, start, lx.str(b[j+1:k]), string(lower(c)))
					lx.i = k + 1
					continue
				}
			}
			// kind-parameter prefix of a character literal: name_'text'
			if j < len(b) && (b[j] == '\'' || b[j] == '"') && b[j-1] == '_' {
				name := lx.str(b[lx.i : j-1])
				lx.i = j
				lx.str2(name)
				continue
			}
			low, orig := lx.lowerStr(b[lx.i:j])
			lx.emit(Word, start, low, orig)
			lx.i = j
		case isDigit(c) || (c == '.' && lx.i+1 < len(b) && isDigit(b[lx.i+1])):
			lx.number()
		case c == '\'' || c == '"':
			lx.str2("")
		case c == '.':
			lx.dot()
		default:
			lx.punct()
		}
	}
	return lx.toks
}

// formatStmt turns "[label] FORMAT ( ... )" into Ident format + FormatText.
func (lx *lexer) formatStmt() {
	b := lx.b
	i := 0
	for i < len(b) && isBlank(b[i]) {
		i++
	}
	for i < len(b) && isDigit(b[i]) {
		i++
	}
	for i < len(b) && isBlank(b[i]) {
		i++
	}
	if !hasPrefixFold(b[i:], "format") {
		return
	}
	j := i + 6
	for j < len(b) && isBlank(b[j]) {
		j++
	}
	if j >= len(b) || b[j] != '(' {
		return
	}
	q := matchParen(b, j)
	if q < 0 {
		return
	}
	for k := q + 1; k < len(b); k++ {
		if !isBlank(b[k]) {
			return
		}
	}
	// label tokens first
	k := 0
	for k < len(b) && isBlank(b[k]) {
		k++
	}
	if k < i {
		lx.emit(Int, k, lx.str(b[k:i]), "")
		// trim blanks of the label text
		t := &lx.toks[len(lx.toks)-1]
		for len(t.S) > 0 && isBlank(t.S[len(t.S)-1]) {
			t.S = t.S[:len(t.S)-1]
		}
	}
	lx.emit(Word, i, "format", "")
	if string(b[i:i+6]) != "format" {
		lx.toks[len(lx.toks)-1].X = string(b[i : i+6])
	}
	lx.emit(FormatText, j, string(b[j:q+1]), "")
	lx.i = len(b)
}

// str2 lexes a character literal at lx.i (kind parameter kind).
func (lx *lexer) str2(kind string) {
	b := lx.b
	start := lx.i
	q := b[lx.i]
	lx.i++
	var out []byte
	for lx.i < len(b) {
		c := b[lx.i]
		if c == q {
			if lx.i+1 < len(b) && b[lx.i+1] == q {
				out = append(out, q)
				lx.i += 2
				continue
			}
			lx.i++
			lx.emit(String, start, string(out), kind)
			return
		}
		out = append(out, c)
		lx.i++
	}
	lx.errf(lx.l.posAt(start), "unterminated character literal")
	lx.emit(String, start, string(out), kind)
}

func (lx *lexer) number() {
	b := lx.b
	start := lx.i
	i := lx.i
	for i < len(b) && isDigit(b[i]) {
		i++
	}
	isReal := false
	if i < len(b) && b[i] == '.' {
		// "1.eq.2": the dot starts an operator when letters and a dot follow
		k := i + 1
		for k < len(b) && isLetter(b[k]) {
			k++
		}
		if !(k > i+1 && k < len(b) && b[k] == '.') {
			isReal = true
			i++
			for i < len(b) && isDigit(b[i]) {
				i++
			}
		}
	}
	// exponent
	if i < len(b) && (lower(b[i]) == 'e' || lower(b[i]) == 'd' || lower(b[i]) == 'q') {
		k := i + 1
		if k < len(b) && (b[k] == '+' || b[k] == '-') {
			k++
		}
		if k < len(b) && isDigit(b[k]) {
			for k < len(b) && isDigit(b[k]) {
				k++
			}
			i = k
			isReal = true
		}
	}
	text := lx.lowerNum(b[start:i])
	if !isReal && i+1 < len(b) && b[i] == '_' && (b[i+1] == '\'' || b[i+1] == '"') {
		lx.i = i + 1 // 4_"text": a character literal of kind 4
		lx.str2(text)
		return
	}
	kind := ""
	if i < len(b) && b[i] == '_' && i+1 < len(b) && (isNameCh(b[i+1])) {
		k := i + 1
		for k < len(b) && isNameCh(b[k]) {
			k++
		}
		kind, _ = lx.lowerStr(b[i+1 : k])
		i = k
	}
	if isReal {
		lx.emit(Real, start, text, kind)
	} else {
		lx.emit(Int, start, text, kind)
	}
	lx.i = i
}

func isHex(c byte) bool {
	return isDigit(c) || (lower(c) >= 'a' && lower(c) <= 'f')
}

func (lx *lexer) lowerNum(b []byte) string {
	l, _ := lx.lowerStr(b)
	return l
}

// dot lexes ".name." operators and logical literals, or a lone '.'.
func (lx *lexer) dot() {
	b := lx.b
	start := lx.i
	k := lx.i + 1
	for k < len(b) && isLetter(b[k]) {
		k++
	}
	if k > lx.i+1 && k < len(b) && b[k] == '.' {
		name, _ := lx.lowerStr(b[lx.i+1 : k])
		lx.i = k + 1
		switch name {
		case "true", "false":
			// optional kind: .true._k
			kind := ""
			if lx.i < len(b) && b[lx.i] == '_' {
				j := lx.i + 1
				for j < len(b) && isNameCh(b[j]) {
					j++
				}
				kind, _ = lx.lowerStr(b[lx.i+1 : j])
				lx.i = j
			}
			lx.emit(Logical, start, name, kind)
		default:
			if kd, ok := dotWords[name]; ok {
				lx.emit(kd, start, "."+name+".", "")
			} else {
				lx.emit(DefOp, start, name, "")
			}
		}
		return
	}
	if lx.i+1 < len(b) && b[lx.i+1] == '.' {
		lx.emit(DotDot, start, "", "") // assumed rank "(..)"
		lx.i += 2
		return
	}
	lx.errf(lx.l.posAt(start), "unexpected '.'")
	lx.emit(Illegal, start, ".", "")
	lx.i++
}

func (lx *lexer) punct() {
	b := lx.b
	i := lx.i
	c := b[i]
	n := byte(0)
	if i+1 < len(b) {
		n = b[i+1]
	}
	k, w := Illegal, 1
	switch c {
	case '(':
		k = LParen
		// "(/" opens an array constructor, but "(/)", "(//)" and "(/=)" are
		// the parenthesised operators of operator(/) and friends
		if n == '/' && !(i+2 < len(b) && (b[i+2] == ')' || b[i+2] == '=' || (b[i+2] == '/' && i+3 < len(b) && b[i+3] == ')'))) {
			k, w = LParenSlash, 2
		}
	case ')':
		k = RParen
	case ',':
		k = Comma
	case ':':
		k = Colon
		if n == ':' {
			k, w = DColon, 2
		}
	case '=':
		k = Assign
		switch n {
		case '=':
			k, w = Eq, 2
		case '>':
			k, w = Arrow, 2
		}
	case '%':
		k = Percent
	case ';':
		k = Semi
	case '[':
		k = LBrack
	case ']':
		k = RBrack
	case '*':
		k = Star
		if n == '*' {
			k, w = Pow, 2
		}
	case '/':
		k = Slash
		switch n {
		case '/':
			k, w = Concat, 2
		case ')':
			k, w = SlashRParen, 2
		case '=':
			k, w = Ne, 2
		}
	case '+':
		k = Plus
	case '-':
		k = Minus
	case '<':
		k = Lt
		if n == '=' {
			k, w = Le, 2
		}
	case '>':
		k = Gt
		if n == '=' {
			k, w = Ge, 2
		}
	case '$':
		k = Dollar
	default:
		lx.errf(lx.l.posAt(i), "illegal character "+quoteByte(c))
	}
	lx.emit(k, i, "", "")
	lx.i += w
}

func quoteByte(c byte) string {
	if c >= 32 && c < 127 {
		return "'" + string(rune(c)) + "'"
	}
	const hex = "0123456789abcdef"
	return "0x" + string([]byte{hex[c>>4], hex[c&15]})
}
