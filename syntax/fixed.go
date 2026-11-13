package syntax

import "sort"

// Fixed form has no reserved words and ignores blanks (F2018 6.3.3.1), so
// "DO 10 I = 1.10" is an assignment. classifyFixed takes the statement with
// its blanks removed and returns the indices where a blank is to be inserted
// so that the ordinary tokenizer sees the keywords as separate words.

var fixedKeywords = func() []string {
	k := []string{
		"program", "module", "submodule", "subroutine", "function", "blockdata", "block", "endprogram", "endmodule",
		"endsubroutine", "endfunction", "endblockdata", "end", "implicit", "integer", "real",
		"doubleprecision", "doublecomplex", "byte", "complex", "logical", "character", "parameter", "dimension", "common", "equivalence",
		"data", "external", "intrinsic", "save", "include", "entry", "call", "if", "elseif", "else", "endif",
		"do", "enddo", "continue", "goto", "stop", "errorstop", "pause", "return", "read", "write", "print",
		"open", "close", "rewind", "backspace", "endfile", "inquire", "flush", "allocate", "deallocate",
		"nullify", "selectcase", "selecttype", "endselect", "case", "casedefault", "where", "elsewhere", "endwhere",
		"forall", "endforall", "use", "interface", "endinterface", "contains", "type", "endtype", "cycle", "exit",
		"recursive", "pure", "elemental", "impure", "allocatable", "pointer", "target", "optional", "intent",
		"public", "private", "namelist", "sequence", "associate", "endassociate", "endblock", "import", "volatile",
		"value", "protected", "class", "typeis", "classis", "classdefault", "procedure", "generic", "final",
		"abstract", "enum", "endenum", "enumerator", "bind", "assign", "format",
	}
	sort.Slice(k, func(i, j int) bool { return len(k[i]) > len(k[j]) || (len(k[i]) == len(k[j]) && k[i] < k[j]) })
	return k
}()

type classifier struct {
	ins []int
}

func isLetter(c byte) bool { c = lower(c); return c >= 'a' && c <= 'z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isNameCh(c byte) bool { return isLetter(c) || isDigit(c) || c == '_' }

// matchParen returns the index of the parenthesis closing the one at s[i], or
// -1; character literals are skipped.
func matchParen(s []byte, i int) int {
	depth := 0
	var q byte
	for ; i < len(s); i++ {
		c := s[i]
		if q != 0 {
			if c == q {
				q = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			q = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func classifyFixed(text []byte) []int {
	c := &classifier{}
	// split at top-level ';'
	start := 0
	var q byte
	depth := 0
	for i := 0; i <= len(text); i++ {
		if i < len(text) {
			ch := text[i]
			if q != 0 {
				if ch == q {
					q = 0
				}
				continue
			}
			switch ch {
			case '\'', '"':
				q = ch
			case '(':
				depth++
			case ')':
				depth--
			}
			if ch != ';' || depth > 0 {
				continue
			}
		}
		c.statement(text, start, i)
		start = i + 1
	}
	sort.Ints(c.ins)
	return c.ins
}

func (c *classifier) insert(i int) { c.ins = append(c.ins, i) }

// statement classifies text[a:b] (one statement, its label already first
// when the line has one: a label is digits followed by a blank, so skip it).
func (c *classifier) statement(text []byte, a, b int) {
	// a leading label was joined with a blank by the caller only in the
	// output, so the text here never contains it.
	if b-a < 2 {
		return
	}
	s := make([]byte, b-a)
	for i := range s {
		s[i] = lower(text[a+i])
	}
	c.stmt(s, a)
}

// assignLike reports whether s is "designator = ..." or "designator => ...".
func assignLike(s []byte) bool {
	if len(s) == 0 || !isLetter(s[0]) {
		return false
	}
	i := 0
	for {
		for i < len(s) && isNameCh(s[i]) {
			i++
		}
		for i < len(s) && s[i] == '(' {
			j := matchParen(s, i)
			if j < 0 {
				return false
			}
			i = j + 1
		}
		if i < len(s) && s[i] == '%' {
			i++
			continue
		}
		break
	}
	if i >= len(s) || s[i] != '=' {
		return false
	}
	return i+1 >= len(s) || s[i+1] != '='
}

// doLoopHead reports "do[label][,]var=expr,expr...": a top-level "=" followed
// by a top-level comma.
func doLoopHead(s []byte) bool {
	if len(s) < 3 || s[0] != 'd' || s[1] != 'o' {
		return false
	}
	depth := 0
	var q byte
	eq := false
	for i := 2; i < len(s); i++ {
		ch := s[i]
		if q != 0 {
			if ch == q {
				q = 0
			}
			continue
		}
		switch ch {
		case '\'', '"':
			q = ch
		case '(':
			depth++
		case ')':
			depth--
		case '=':
			if depth == 0 {
				eq = true
			}
		case ',':
			if depth == 0 && eq {
				return true
			}
		}
	}
	return false
}

func hasPre(s []byte, p string) bool {
	if len(s) < len(p) {
		return false
	}
	return string(s[:len(p)]) == p
}

func (c *classifier) stmt(s []byte, base int) {
	if len(s) == 0 {
		return
	}
	if assignLike(s) && !doLoopHead(s) {
		return
	}
	kw := ""
	for _, k := range fixedKeywords {
		if hasPre(s, k) {
			kw = k
			break
		}
	}
	if kw == "" {
		return
	}
	p := len(kw)
	rest := func() bool { return p < len(s) }
	switch kw {
	case "format":
		return
	case "if", "elseif":
		if p < len(s) && s[p] == '(' {
			c.insert(base + p)
			q := matchParen(s, p)
			if q < 0 {
				return
			}
			r := s[q+1:]
			switch {
			case len(r) == 0:
			case hasPre(r, "then"):
				c.insert(base + q + 1)
			case kw == "if" && isDigit(r[0]):
				c.insert(base + q + 1)
			case kw == "if":
				c.insert(base + q + 1)
				c.stmt(r, base+q+1)
			}
		} else if rest() {
			c.insert(base + p)
		}
	case "do":
		if !rest() {
			return
		}
		c.insert(base + p)
		q := p
		for q < len(s) && isDigit(s[q]) {
			q++
		}
		if q > p {
			if q < len(s) {
				c.insert(base + q)
			}
			return
		}
		if hasPre(s[p:], "while") {
			c.insert(base + p + 5)
		} else if hasPre(s[p:], "concurrent") {
			c.insert(base + p + 10)
		}
	case "integer", "real", "doubleprecision", "doublecomplex", "byte", "complex", "logical", "character", "type", "class":
		if !rest() {
			return
		}
		switch {
		case s[p] == '*':
			p++
			if p < len(s) && s[p] == '(' {
				if q := matchParen(s, p); q >= 0 {
					p = q + 1
				}
			} else {
				for p < len(s) && isDigit(s[p]) {
					p++
				}
			}
		case s[p] == '(' && kw != "doubleprecision":
			if q := matchParen(s, p); q >= 0 {
				p = q + 1
			}
		}
		if p < len(s) {
			c.insert(base + p)
			if hasPre(s[p:], "function") {
				c.insert(base + p + 8)
			}
		}
	case "recursive", "pure", "elemental", "impure":
		if rest() {
			c.insert(base + p)
			c.stmt(s[p:], base+p)
		}
	case "implicit":
		if rest() {
			c.insert(base + p)
			c.stmt(s[p:], base+p)
		}
	case "assign":
		if rest() {
			c.insert(base + p)
			q := p
			for q < len(s) && isDigit(s[q]) {
				q++
			}
			if q > p && hasPre(s[q:], "to") {
				c.insert(base + q)
				c.insert(base + q + 2)
			}
		}
	default:
		if rest() {
			c.insert(base + p)
		}
	}
}
