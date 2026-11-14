// List-directed input (F2018 13.10.3).
//
// Follows the list-directed state machine of LLVM flang-rt, flang-rt/lib/
// runtime/io-stmt.cpp and edit-input.cpp (ListDirectedStatementState<Input>,
// EditListDirectedInput) at tag llvmorg-23.1.4.
// Part of the LLVM Project, under the Apache License v2.0 with LLVM
// Exceptions. See https://llvm.org/LICENSE.txt and the NOTICE file.
// SPDX-License-Identifier: Apache-2.0 WITH LLVM-exception
//
// Changes: the repeat-count and cut-short behaviour of gfortran 15.2.0 (see
// doc.go).

package rt

import "strconv"

// ldIn reads list-directed input (F2018 13.10.3): values separated by blanks,
// commas, a slash that ends the statement, or the end of a record; null
// values; r*c repeat counts; quoted character values; (re,im) complex values.
type ldIn struct {
	s *inStmt
	// the value a repeat count r*c is still delivering
	rep      int
	repText  []byte
	repNull  bool
	repQuote bool
	repKind  Kind
	repSize  int
	done     bool // a slash ended the statement
}

func (s *inStmt) listDirected(items []Val) *Error {
	r := ldIn{s: s}
	if len(items) > 0 {
		if e := s.record(); e != nil {
			return e
		}
	}
	for i := range items {
		s.item = i + 1
		if r.done {
			break
		}
		if e := r.value(&items[i]); e != nil {
			return e
		}
	}
	return nil
}

func (r *ldIn) sepChar(c byte) bool {
	return c == ' ' || c == '\t' || c == ',' || c == '/' || (r.s.m.comma && c == ';')
}

// peek returns the next character of the record, or 0 at its end.
func (r *ldIn) peek() byte {
	u := r.s.u
	if u.ipos < len(u.cur) {
		return u.cur[u.ipos]
	}
	return 0
}

// skipBlanks skips blanks and the ends of records; it reports EOF when the
// input ends first.
func (r *ldIn) skipBlanks() *Error {
	u := r.s.u
	for {
		for u.ipos < len(u.cur) && (u.cur[u.ipos] == ' ' || u.cur[u.ipos] == '\t') {
			u.ipos++
		}
		if u.ipos < len(u.cur) {
			return nil
		}
		// end of record: the next record continues the list
		u.haveRec = false
		if e := u.nextRecord(); e != nil {
			return e
		}
	}
}

// value reads one item.
func (r *ldIn) value(v *Val) *Error {
	if r.rep > 0 {
		r.rep--
		if r.repNull {
			return nil
		}
		if v.K != r.repKind || v.Size != r.repSize {
			// libgfortran converts the saved value only for the kind that
			// started the repeat
			return errf(ErrReadValue, "Bad repeat count or value in item %d of list input", r.s.item)
		}
		return r.convert(v, r.repText, r.repQuote)
	}
	u := r.s.u
	if e := r.skipBlanks(); e != nil {
		return e
	}
	switch c := r.peek(); {
	case c == '/':
		r.done = true
		return nil
	case c == ',' || (r.s.m.comma && c == ';'):
		u.ipos++ // a null value
		return nil
	}
	n, text, quoted, e := r.token(v)
	if e != nil {
		if e.IsEOF() && quoted && v.K == Character {
			// an unterminated string delivers what was read
			r.convert(v, text, true)
		}
		return e
	}
	if n < 0 {
		r.afterToken()
		return r.convert(v, text, quoted)
	}
	if n == 0 {
		return errf(ErrReadValue, "Zero repeat count in item %d of list input", r.s.item)
	}
	r.rep = n - 1
	r.repKind, r.repSize = v.K, v.Size
	r.afterToken()
	if len(text) == 0 && !quoted { // "r*" alone: r null values
		r.repNull = true
		return nil
	}
	r.repNull, r.repText, r.repQuote = false, text, quoted
	return r.convert(v, text, quoted)
}

// afterToken consumes the blanks and the single comma that follow a value.
func (r *ldIn) afterToken() {
	u := r.s.u
	for u.ipos < len(u.cur) && (u.cur[u.ipos] == ' ' || u.cur[u.ipos] == '\t') {
		u.ipos++
	}
	if u.ipos < len(u.cur) && (u.cur[u.ipos] == ',' || (r.s.m.comma && u.cur[u.ipos] == ';')) {
		u.ipos++
	}
}

// token reads the next value up to a separator: its repeat count (-1 when
// there is none) and its text. A quoted character value has its quotes removed
// and doubled quotes undone (quoted is then true); a complex value keeps its
// parentheses.
func (r *ldIn) token(v *Val) (n int, text []byte, quoted bool, e *Error) {
	u := r.s.u
	start := u.ipos
	n = -1
	i := start
	for i < len(u.cur) && u.cur[i] >= '0' && u.cur[i] <= '9' {
		i++
	}
	if i > start && i < len(u.cur) && u.cur[i] == '*' {
		cnt, err := strconv.Atoi(string(u.cur[start:i]))
		if err != nil || cnt > MaxRepeat {
			cnt = MaxRepeat + 1
		}
		n = cnt
		start = i + 1
		u.ipos = start
	}
	if start >= len(u.cur) || r.sepChar(u.cur[start]) {
		return n, nil, false, nil
	}
	switch c := u.cur[start]; {
	case c == '\'' || c == '"':
		q, e := r.quoted(start)
		return n, q, true, e
	case c == '(' && v.K == Complex:
		j := start
		for j < len(u.cur) && u.cur[j] != ')' {
			j++
		}
		if j < len(u.cur) {
			j++
		}
		u.ipos = j
		return n, append([]byte(nil), u.cur[start:j]...), false, nil
	}
	j := start
	for j < len(u.cur) && !r.sepChar(u.cur[j]) {
		j++
	}
	u.ipos = j
	return n, append([]byte(nil), u.cur[start:j]...), false, nil
}

// quoted reads a quoted string starting at index i of the record. An
// unterminated string continues on the next record; at the end of the input
// the text read so far comes back with the EOF error.
func (r *ldIn) quoted(i int) ([]byte, *Error) {
	u := r.s.u
	q := u.cur[i]
	var out []byte
	i++
	for {
		if i >= len(u.cur) {
			u.haveRec = false
			if e := u.nextRecord(); e != nil {
				return out, e
			}
			i = 0
			continue
		}
		c := u.cur[i]
		i++
		if c == q {
			if i < len(u.cur) && u.cur[i] == q {
				out = append(out, q)
				i++
				continue
			}
			break
		}
		out = append(out, c)
	}
	u.ipos = i
	return out, nil
}

// convert stores the text of a value into the item.
func (r *ldIn) convert(v *Val, text []byte, quoted bool) *Error {
	s := r.s
	switch v.K {
	case Character:
		n := len(v.S)
		out := make([]byte, n)
		copy(out, text)
		for i := len(text); i < n; i++ {
			out[i] = ' '
		}
		v.S = string(out)
	case Integer:
		if quoted {
			return errf(ErrReadValue, "Bad integer for item %d in list input", s.item)
		}
		x, e := s.readInt(text, &tok{c: 'I'}, v.Size)
		if e != nil {
			if e.Stat == ErrReadOverflow {
				e.Stat = ErrReadValue // list-directed input reports an overflow as a bad value
			}
			return e
		}
		if len(text) == 0 {
			return nil
		}
		v.I = x
	case Real:
		x, ok := realText(text, -1, 0, v.bits(), s.m.comma, false)
		if !ok || len(text) == 0 || quoted {
			return errf(ErrReadValue, "Bad real number in item %d of list input", s.item)
		}
		v.F = x
	case Complex:
		if len(text) < 2 || text[0] != '(' || text[len(text)-1] != ')' {
			return errf(ErrReadValue, "Bad complex number in item %d of list input", s.item)
		}
		inner := text[1 : len(text)-1]
		comma := -1
		for i, c := range inner {
			if c == ',' || (s.m.comma && c == ';') {
				comma = i
				break
			}
		}
		if comma < 0 {
			return errf(ErrReadValue, "Bad complex number in item %d of list input", s.item)
		}
		reT, imT := trimSpace(inner[:comma]), trimSpace(inner[comma+1:])
		// the real part is stored before the imaginary part is checked
		re, ok1 := realText(reT, -1, 0, v.bits(), s.m.comma, false)
		if !ok1 || len(reT) == 0 {
			return errf(ErrReadValue, "Bad complex number in item %d of list input", s.item)
		}
		v.F = re
		im, ok2 := realText(imT, -1, 0, v.bits(), s.m.comma, false)
		if !ok2 || len(imT) == 0 {
			return errf(ErrReadValue, "Bad complex number in item %d of list input", s.item)
		}
		v.G = im
	case Logical:
		b := text
		if len(b) > 0 && b[0] == '.' {
			b = b[1:]
		}
		if len(b) == 0 || quoted {
			return errf(ErrReadValue, "Bad logical value while reading item %d", s.item)
		}
		switch b[0] {
		case 'T', 't':
			v.B = true
		case 'F', 'f':
			v.B = false
		default:
			return errf(ErrReadValue, "Bad logical value while reading item %d", s.item)
		}
	}
	return nil
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && b[0] == ' ' {
		b = b[1:]
	}
	for len(b) > 0 && b[len(b)-1] == ' ' {
		b = b[:len(b)-1]
	}
	return b
}
