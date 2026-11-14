// Format specifications (F2018 13.2, 13.3): a FORMAT string compiled to tokens.
//
// The grammar of the edit descriptors follows LLVM flang-rt,
// flang-rt/include/flang-rt/runtime/format-implementation.h
// (FormatControl::GetNextDataEdit) at tag llvmorg-23.1.4.
// Part of the LLVM Project, under the Apache License v2.0 with LLVM
// Exceptions. See https://llvm.org/LICENSE.txt and the NOTICE file.
// SPDX-License-Identifier: Apache-2.0 WITH LLVM-exception
//
// Changes: the whole format is parsed and validated before the statement
// transfers anything (as gfortran does), with gfortran's rules for commas and
// for what follows the final parenthesis.

package rt

import (
	"fmt"
	"strings"
	"sync"
)

// Bounds on a FORMAT. A format string is untrusted input: every number in it
// is bounded before anything is allocated or looped over.
const (
	MaxFormatLen   = 1 << 16 // characters in a format string
	MaxFormatDepth = 32      // nesting of parentheses
	MaxRepeat      = 1 << 30 // a repeat count or an X/T count
	MaxFieldWidth  = 1 << 20 // w of a data edit descriptor
	MaxDigits      = 1 << 16 // d, m and e of a data edit descriptor
)

type tkind uint8

const (
	tData  tkind = iota // a data edit descriptor
	tOpen               // ( with a repeat count
	tClose              // )
	tLit                // a character string edit descriptor or nH
	tCtrl               // a control edit descriptor
)

// tok is one element of a compiled format (F2018 13.2).
type tok struct {
	kind tkind
	// c is the descriptor letter: I B O Z F E D G L A for data edit
	// descriptors; for control edit descriptors X, 'T' (T), 'l' (TL), 'r'
	// (TR), '/', ':', 'P' (kP), 'S' (S/SP/SS in v), 'b' (BN/BZ in v), 'R'
	// (rounding, v is N U D Z C P), 'C' (DC/DP, v is C or P).
	c byte
	// v is the variation: N S X for E (EN, ES, EX); '+', '-', ' ' for sign
	// control; 'N' 'Z' for blank control; the rounding letter for R; 'C' or
	// 'P' for the decimal mode.
	v          byte
	rep        int // repeat count (-1: unlimited, for *( ))
	w, d, e    int
	hasW, hasD bool
	hasE       bool
	str        string
	skip       int // for tOpen the index of its tClose; for tClose of its tOpen
}

// Format is a compiled FORMAT specification: F2018 13.2 to 13.4.
type Format struct {
	toks     []tok
	hasData  bool
	src      string
	nonAdv   bool // a $ or \ edit descriptor (extension)
	groupRev []int
}

// FormatError reports an invalid FORMAT.
type FormatError struct {
	Format string
	Pos    int
	Msg    string
}

func (e *FormatError) Error() string {
	return fmt.Sprintf("%s in format %q at position %d", e.Msg, e.Format, e.Pos+1)
}

var (
	fmtCacheMu sync.Mutex
	fmtCache   = map[string]*Format{}
)

const fmtCacheMax = 256

// CompileFormat parses a FORMAT specification. The result is cached by
// string (a program formats with a few literals in a loop).
func CompileFormat(s string) (*Format, error) {
	fmtCacheMu.Lock()
	f, ok := fmtCache[s]
	fmtCacheMu.Unlock()
	if ok {
		return f, nil
	}
	f, err := parseFormat(s)
	if err != nil {
		return nil, err
	}
	fmtCacheMu.Lock()
	if len(fmtCache) >= fmtCacheMax {
		clear(fmtCache)
	}
	fmtCache[s] = f
	fmtCacheMu.Unlock()
	return f, nil
}

type fparser struct {
	s    string
	i    int
	f    *Format
	open []int // indices of open tOpen tokens
}

func (p *fparser) fail(msg string) error {
	return &FormatError{Format: p.s, Pos: p.i, Msg: msg}
}

func (p *fparser) skipBlanks() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *fparser) peek() byte {
	p.skipBlanks()
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 32
	}
	return c
}

func (p *fparser) peekUpper() byte { return upper(p.peek()) }

// number reads an unsigned decimal integer, bounded by max.
func (p *fparser) number(max int, what string) (int, bool, error) {
	p.skipBlanks()
	start := p.i
	n := 0
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		n = n*10 + int(p.s[p.i]-'0')
		if n > max {
			return 0, false, p.fail(what + " is too large")
		}
		p.i++
		// blanks inside a number are not significant in free form, but a
		// digit string is contiguous in a format; keep it strict.
	}
	return n, p.i > start, nil
}

func parseFormat(s string) (*Format, error) {
	if len(s) > MaxFormatLen {
		return nil, &FormatError{Format: s[:32] + "...", Msg: "format is too long"}
	}
	p := &fparser{s: s, f: &Format{src: s}}
	if p.peek() != '(' {
		return nil, p.fail("format lacks an initial '('")
	}
	p.i++
	p.f.toks = append(p.f.toks, tok{kind: tOpen, rep: 1})
	p.open = append(p.open, 0)
	if err := p.items(); err != nil {
		return nil, err
	}
	// like libgfortran, characters after the closing parenthesis are ignored
	return p.f, nil
}

// separator state of the item list (F2018 13.2.1: a comma separates edit
// descriptors, except that it may be omitted around / and : and after P).
const (
	sepStart    = iota // just after '(' or ','
	sepRequired        // after an item: a comma, ')', '/' or ':' must follow
	sepOptional        // after '/', ':' or kP
)

// items parses a list of format items up to and including the matching ')'.
func (p *fparser) items() error {
	state := sepStart
	for {
		c := p.peek()
		switch {
		case c == 0:
			return p.fail("missing ')' in format")
		case c == ')':
			p.i++
			o := p.open[len(p.open)-1]
			p.open = p.open[:len(p.open)-1]
			p.f.toks[o].skip = len(p.f.toks)
			p.f.toks = append(p.f.toks, tok{kind: tClose, skip: o})
			if len(p.open) == 0 {
				return nil
			}
			state = sepRequired
			continue
		case c == ',':
			if state == sepStart {
				return p.fail("unexpected comma in format")
			}
			p.i++
			state = sepStart
			continue
		}
		before := len(p.f.toks)
		if state == sepRequired && c != '/' && c != ':' {
			return p.fail("missing comma in format")
		}
		if err := p.item(); err != nil {
			return err
		}
		if t := p.f.toks[before]; len(p.f.toks) > before && t.kind == tCtrl && (t.c == '/' || t.c == ':' || t.c == 'P') {
			state = sepOptional
		} else if len(p.f.toks) > before && p.f.toks[before].kind == tOpen {
			state = sepStart
		} else {
			state = sepRequired
		}
	}
}

func (p *fparser) item() error {
	f := p.f
	start := p.i
	c := p.peek()
	rep, hasRep := 1, false
	neg := false
	if c == '-' || c == '+' {
		neg = c == '-'
		p.i++
		c = p.peek()
		if c < '0' || c > '9' {
			return p.fail("a sign must be followed by a scale factor")
		}
		n, _, err := p.number(MaxRepeat, "scale factor")
		if err != nil {
			return err
		}
		if upper(p.peek()) != 'P' {
			return p.fail("a signed integer may appear only before P")
		}
		p.i++
		if neg {
			n = -n
		}
		f.toks = append(f.toks, tok{kind: tCtrl, c: 'P', w: n})
		return nil
	}
	if c >= '0' && c <= '9' {
		n, _, err := p.number(MaxRepeat, "repeat count")
		if err != nil {
			return err
		}
		rep, hasRep = n, true
		c = p.peek()
		if n == 0 && upper(c) != 'P' {
			return p.fail("a repeat count must be positive")
		}
	} else if c == '*' {
		p.i++
		if p.peek() != '(' {
			return p.fail("'*' may appear only before '('")
		}
		if len(p.open) != 1 {
			return p.fail("'*(' must be nested in exactly one set of parentheses")
		}
		rep, hasRep = -1, true
		c = '('
	}
	switch {
	case c == '(':
		p.i++
		if len(p.open) >= MaxFormatDepth {
			return p.fail("format nesting is too deep")
		}
		p.open = append(p.open, len(f.toks))
		f.toks = append(f.toks, tok{kind: tOpen, rep: rep})
		return nil
	case c == '\'' || c == '"':
		if hasRep {
			return p.fail("a repeat count cannot precede a character string")
		}
		p.i++
		var b strings.Builder
		for {
			if p.i >= len(p.s) {
				return p.fail("missing closing quote of a character string")
			}
			ch := p.s[p.i]
			p.i++
			if ch == c {
				if p.i < len(p.s) && p.s[p.i] == c {
					b.WriteByte(c)
					p.i++
					continue
				}
				break
			}
			b.WriteByte(ch)
		}
		f.toks = append(f.toks, tok{kind: tLit, str: b.String()})
		return nil
	case c == '/':
		p.i++
		f.toks = append(f.toks, tok{kind: tCtrl, c: '/', rep: rep})
		return nil
	case c == ':':
		if hasRep {
			return p.fail("a repeat count cannot precede ':'")
		}
		p.i++
		f.toks = append(f.toks, tok{kind: tCtrl, c: ':'})
		return nil
	case c == '$' || c == '\\':
		p.i++
		f.nonAdv = true
		f.toks = append(f.toks, tok{kind: tCtrl, c: '$'})
		return nil
	}
	u := upper(c)
	if u < 'A' || u > 'Z' {
		p.i = start
		return p.fail("invalid character in format")
	}
	p.i++
	switch u {
	case 'H':
		if !hasRep {
			return p.fail("Hollerith without a count")
		}
		if p.i+rep > len(p.s) {
			return p.fail("Hollerith string runs past the end of the format")
		}
		f.toks = append(f.toks, tok{kind: tLit, str: p.s[p.i : p.i+rep]})
		p.i += rep
		return nil
	case 'X':
		f.toks = append(f.toks, tok{kind: tCtrl, c: 'X', w: rep})
		return nil
	case 'P':
		if !hasRep {
			return p.fail("P without a scale factor")
		}
		f.toks = append(f.toks, tok{kind: tCtrl, c: 'P', w: rep})
		return nil
	case 'T':
		kind := byte('T')
		if n := upper(p.peekRaw()); n == 'L' || n == 'R' {
			p.i++
			kind = map[byte]byte{'L': 'l', 'R': 'r'}[n]
		}
		if hasRep {
			return p.fail("a repeat count cannot precede T")
		}
		n, ok, err := p.number(MaxRepeat, "position")
		if err != nil {
			return err
		}
		if !ok || (kind == 'T' && n == 0) {
			return p.fail("T, TL and TR need a positive position")
		}
		f.toks = append(f.toks, tok{kind: tCtrl, c: kind, w: n})
		return nil
	case 'S':
		if hasRep {
			return p.fail("a repeat count cannot precede S")
		}
		v := byte(' ')
		switch upper(p.peekRaw()) {
		case 'P':
			p.i++
			v = '+'
		case 'S':
			p.i++
			v = '-'
		}
		f.toks = append(f.toks, tok{kind: tCtrl, c: 'S', v: v})
		return nil
	case 'B':
		if n := upper(p.peekRaw()); n == 'N' || n == 'Z' {
			if hasRep {
				return p.fail("a repeat count cannot precede BN or BZ")
			}
			p.i++
			f.toks = append(f.toks, tok{kind: tCtrl, c: 'b', v: n})
			return nil
		}
	case 'R':
		n := upper(p.peekRaw())
		if strings.IndexByte("NUDZCP", n) >= 0 && n != 0 {
			p.i++
			f.toks = append(f.toks, tok{kind: tCtrl, c: 'R', v: n})
			return nil
		}
		return p.fail("unknown rounding edit descriptor")
	case 'D':
		if n := upper(p.peekRaw()); n == 'C' || n == 'P' {
			p.i++
			f.toks = append(f.toks, tok{kind: tCtrl, c: 'C', v: n})
			return nil
		}
	}
	// a data edit descriptor
	t := tok{kind: tData, c: u, rep: rep}
	if u == 'E' {
		switch n := upper(p.peekRaw()); n {
		case 'N', 'S', 'X':
			t.v = n
			p.i++
		}
	}
	if !strings.ContainsRune("IBOZFEDGLA", rune(u)) {
		p.i--
		return p.fail("unknown edit descriptor " + string(u))
	}
	w, hasW, err := p.number(MaxFieldWidth, "field width")
	if err != nil {
		return err
	}
	t.w, t.hasW = w, hasW
	if hasW && p.peekRaw() == '.' {
		p.i++
		d, ok, err := p.number(MaxDigits, "digits")
		if err != nil {
			return err
		}
		if !ok {
			return p.fail("missing digits after '.'")
		}
		t.d, t.hasD = d, true
		switch u {
		case 'E', 'D', 'G':
			if n := upper(p.peekRaw()); n == 'E' || n == 'D' {
				p.i++
				e, ok, err := p.number(MaxDigits, "exponent width")
				if err != nil {
					return err
				}
				if !ok {
					return p.fail("missing exponent width")
				}
				t.e, t.hasE = e, true
			}
		}
	}
	switch u {
	case 'F':
		// gfortran reads F0 as F0.0
		if !t.hasD && hasW && w == 0 {
			t.hasD = true
		} else if !t.hasD {
			return p.fail("a w.d is required")
		}
	case 'E', 'D':
		// gfortran checks the digits when it converts a finite value: E0
		// writes Inf and refuses 1.5
		if !t.hasD && t.v != 'X' && !(hasW && w == 0) {
			return p.fail("period required in format specifier")
		}
	case 'G':
		if !t.hasD && !(hasW && w == 0) {
			return p.fail("period required in format specifier")
		}
	case 'L':
		if !hasW {
			return p.fail("a field width is required for L")
		}
	case 'I', 'B', 'O', 'Z':
		if !hasW {
			return p.fail("a field width is required")
		}
	}
	if t.hasD && (u == 'L' || u == 'A') {
		return p.fail("L and A take no digits")
	}
	f.hasData = true
	f.toks = append(f.toks, t)
	return nil
}

func (p *fparser) peekRaw() byte {
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}
