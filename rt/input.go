// Formatted input: I, B, O, Z, F, E, D, G, L and A editing (F2018 13.7).
//
// Follows LLVM flang-rt, flang-rt/lib/runtime/edit-input.cpp (EditIntegerInput,
// EditRealInput, EditLogicalInput, EditCharacterInput) at tag llvmorg-23.1.4.
// Part of the LLVM Project, under the Apache License v2.0 with LLVM
// Exceptions. See https://llvm.org/LICENSE.txt and the NOTICE file.
// SPDX-License-Identifier: Apache-2.0 WITH LLVM-exception
//
// Changes: the conversion of the text of a real number is strconv's correctly
// rounded ParseFloat on a canonical decimal string, and the cases where
// gfortran 15.2.0 reads differently (see doc.go) follow gfortran.

package rt

import (
	"math"
	"strconv"
)

// quietNaN is the NaN that reading "NaN" produces (the default quiet NaN of
// the hardware, 0x7FF8000000000000, as libgfortran and flang-rt give it).
var quietNaN = math.Float64frombits(0x7FF8000000000000)

// inStmt is the state of one input statement.
type inStmt struct {
	u      *Unit
	m      modes
	nonAdv bool
	item   int // 1-based index of the item being read, for messages
}

// Read performs a READ of items from the unit; the items are updated in
// place. A nil result is success; otherwise the Error carries IOSTAT and
// IOMSG, with IsEOF and IsEOR for the END= and EOR= branches.
func (u *Unit) Read(sp Spec, items []Val) *Error {
	if !u.canRead {
		return errf(ErrBadAction, "Cannot read from a file opened for writing")
	}
	if u.pending {
		if e := u.endRecord(); e != nil {
			return e
		}
	}
	s := inStmt{u: u, m: u.modes(), nonAdv: sp.NonAdvancing}
	if u.atEOF {
		return errf(ErrEOF, "End of file")
	}
	var err *Error
	if sp.HasFormat {
		err = s.formatted(sp.Format, items)
	} else {
		err = s.listDirected(items)
	}
	if err != nil {
		if err.Stat != ErrEOR {
			u.haveRec = false
		}
		return err
	}
	if !s.nonAdv {
		u.haveRec = false
	}
	return nil
}

// record makes sure there is a current record.
func (s *inStmt) record() *Error {
	if s.u.haveRec {
		return nil
	}
	return s.u.nextRecord()
}

func (s *inStmt) endRecord() *Error {
	s.u.haveRec = false
	return s.record()
}

func (s *inStmt) ctrl(t *tok) *Error {
	u := s.u
	if t.kind == tLit {
		return errf(ErrFormat, "Constant string in input format")
	}
	switch t.c {
	case 'X':
		u.ipos += t.w
	case 'T':
		u.ipos = t.w - 1
	case 'l':
		u.ipos = max(0, u.ipos-t.w)
	case 'r':
		u.ipos += t.w
	case '/':
		for i := 0; i < t.rep; i++ {
			if e := s.endRecord(); e != nil {
				return e
			}
		}
	case 'P':
		s.m.scale = t.w
	case 'S':
	case 'b':
		s.m.blankZero = t.v == 'Z'
	case 'R':
		s.m.round = map[byte]Round{'N': RoundNearest, 'U': RoundUp, 'D': RoundDown,
			'Z': RoundZero, 'C': RoundCompatible, 'P': RoundProcessor}[t.v]
	case 'C':
		s.m.comma = t.v == 'C'
	}
	return nil
}

func (s *inStmt) formatted(format string, items []Val) *Error {
	f, err := CompileFormat(format)
	if err != nil {
		return errf(ErrFormat, "%v", err)
	}
	var c fctl
	c.init(f)
	if len(items) > 0 {
		if e := s.record(); e != nil {
			return e
		}
	} else if e := s.record(); e != nil && !e.IsEOF() {
		return e
	}
	for i := range items {
		v := &items[i]
		s.item = i + 1
		n := 1
		if v.K == Complex {
			n = 2
		}
		for part := 0; part < n; part++ {
			t, e := c.next(true, s)
			if e != nil {
				return e
			}
			if e := s.edit(t, v, part); e != nil {
				return e
			}
		}
	}
	_, e := c.next(false, s)
	return e
}

// field returns the next w characters of the record (w <= 0: the rest) and
// moves past them. A short record is padded with blanks when PAD='YES'.
func (s *inStmt) field(w int) ([]byte, *Error) {
	u := s.u
	rec := u.cur
	start := min(u.ipos, len(rec))
	end := len(rec)
	if w > 0 {
		end = min(start+w, len(rec))
	}
	f := rec[start:end]
	if w > 0 {
		if len(f) < w && !u.Pad && u.ipos+w > len(rec) {
			return nil, errf(ErrEOR, "End of record")
		}
		u.ipos += w
	} else {
		u.ipos = len(rec)
	}
	if w > 0 && len(f) < w {
		// PAD='YES': the missing characters are blanks
		p := make([]byte, w)
		n := copy(p, f)
		for i := n; i < w; i++ {
			p[i] = ' '
		}
		return p, nil
	}
	return f, nil
}

func (s *inStmt) edit(t *tok, v *Val, part int) *Error {
	switch v.K {
	case Integer:
		switch t.c {
		case 'I', 'G', 'B', 'O', 'Z':
			if t.w == 0 && t.c != 'G' || (t.c == 'G' && t.w == 0) {
				return errf(ErrFormat, "Zero width in input format")
			}
			f, e := s.field(t.w)
			if e != nil {
				return e
			}
			n, e := s.readInt(f, t, v.Size)
			if e != nil {
				return e
			}
			v.I = n
			return nil
		case 'A':
			return s.readChar(t, func(b []byte) {
				var x uint64
				for i := len(b) - 1; i >= 0; i-- {
					x = x<<8 | uint64(b[i])
				}
				v.I = int64(x)
			}, v.Size)
		case 'L':
			f, e := s.field(t.w)
			if e != nil {
				return e
			}
			b, e := s.readLogical(f)
			if e != nil {
				return e
			}
			if b {
				v.I = 1
			} else {
				v.I = 0
			}
			return nil
		}
		return descErr(t, "INTEGER")
	case Real, Complex:
		switch t.c {
		case 'F', 'E', 'D', 'G':
			if t.w == 0 {
				return errf(ErrFormat, "Zero width in input format")
			}
			f, e := s.field(t.w)
			if e != nil {
				return e
			}
			x, e := s.readReal(f, t, v.bits())
			if e != nil {
				return e
			}
			if part == 1 {
				v.G = x
			} else {
				v.F = x
			}
			return nil
		}
		return descErr(t, "REAL")
	case Logical:
		switch t.c {
		case 'L', 'G':
			if t.w == 0 {
				return errf(ErrFormat, "Zero width in input format")
			}
			f, e := s.field(t.w)
			if e != nil {
				return e
			}
			b, e := s.readLogical(f)
			if e != nil {
				return e
			}
			v.B = b
			return nil
		}
		return descErr(t, "LOGICAL")
	case Character:
		switch t.c {
		case 'A', 'G':
			return s.readChar(t, func(b []byte) { v.S = string(b) }, len(v.S))
		}
		return descErr(t, "CHARACTER")
	}
	return nil
}

// readChar is Aw input (F2018 13.7.4): the rightmost len characters of a
// field wider than the variable, a short field padded with blanks.
func (s *inStmt) readChar(t *tok, set func([]byte), n int) *Error {
	w := n
	if t.hasW && t.w > 0 {
		w = t.w
	}
	f, e := s.field(w)
	if e != nil {
		return e
	}
	out := make([]byte, n)
	switch {
	case w >= n:
		copy(out, f[len(f)-n:])
	default:
		copy(out, f)
		for i := len(f); i < n; i++ {
			out[i] = ' '
		}
	}
	set(out)
	return nil
}

func (s *inStmt) readLogical(f []byte) (bool, *Error) {
	i := 0
	for i < len(f) && f[i] == ' ' {
		i++
	}
	if i < len(f) && f[i] == '.' {
		i++
	}
	if i >= len(f) {
		return false, errf(ErrReadValue, "Bad logical value while reading item %d", s.item)
	}
	switch f[i] {
	case 'T', 't':
		return true, nil
	case 'F', 'f':
		return false, nil
	}
	return false, errf(ErrReadValue, "Bad logical value while reading item %d", s.item)
}

// blanks applies BN or BZ to the part of a numeric field after its leading
// blanks: BN removes the blanks, BZ turns them into zeros.
func (s *inStmt) blanks(f []byte) []byte {
	i := 0
	for i < len(f) && f[i] == ' ' {
		i++
	}
	f = f[i:]
	out := make([]byte, 0, len(f))
	for _, c := range f {
		switch {
		case c != ' ':
			out = append(out, c)
		case s.m.blankZero:
			out = append(out, '0')
		}
	}
	return out
}

func (s *inStmt) readInt(f []byte, t *tok, size int) (int64, *Error) {
	base := uint64(10)
	switch t.c {
	case 'B':
		base = 2
	case 'O':
		base = 8
	case 'Z':
		base = 16
	}
	if t := trimSpace(f); len(t) == 1 && (t[0] == '-' || t[0] == '+') && f[len(f)-1] != ' ' {
		// a sign that ends the field is an error; one followed by blanks is zero
		return 0, errf(ErrReadValue, "Bad integer for item %d in list input", s.item)
	}
	b := s.blanks(f)
	if len(b) == 0 {
		return 0, nil
	}
	neg := false
	if b[0] == '-' || b[0] == '+' {
		neg = b[0] == '-'
		b = b[1:]
		if len(b) == 0 {
			return 0, nil // libgfortran reads a lone sign as zero
		}
	}
	var n uint64
	over := false
	limit := uint64(1) << (uint(size)*8 - 1) // magnitude limit for decimal
	for _, c := range b {
		var d uint64
		switch {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case base == 16 && c >= 'a' && c <= 'f':
			d = uint64(c-'a') + 10
		case base == 16 && c >= 'A' && c <= 'F':
			d = uint64(c-'A') + 10
		default:
			return 0, errf(ErrReadValue, "Bad integer for item %d in list input", s.item)
		}
		if d >= base {
			return 0, errf(ErrReadValue, "Bad integer for item %d in list input", s.item)
		}
		if n > (math.MaxUint64-d)/base {
			over = true
		}
		n = n*base + d
	}
	if base == 10 {
		if over || (!neg && n > limit-1) || (neg && n > limit) {
			return 0, errf(ErrReadOverflow, "Integer overflow while reading item %d", s.item)
		}
		if neg {
			return -int64(n), nil
		}
		return int64(n), nil
	}
	bitsN := uint(size) * 8
	if over || (bitsN < 64 && n >= 1<<bitsN) {
		return 0, errf(ErrReadOverflow, "Integer overflow while reading item %d", s.item)
	}
	// B, O and Z give the bit pattern: sign-extend from the kind's width
	r := int64(n)
	if bitsN < 64 {
		shift := 64 - bitsN
		r = int64(n<<shift) >> shift
	}
	if neg {
		r = -r
	}
	return r, nil
}

// realText converts the text of a real number (F2018 13.7.2.3.2 input forms
// and the list-directed forms of 13.10.3) with the implied decimal point d
// (-1: none implied) and scale factor k.
func realText(b []byte, d, k int, bits int, comma bool, endBlank bool) (float64, bool) {
	i := 0
	neg := false
	if i < len(b) && (b[i] == '-' || b[i] == '+') {
		neg = b[i] == '-'
		i++
	}
	rest := b[i:]
	// Inf, Infinity, NaN, NaN(...)
	if len(rest) > 0 && (rest[0]|0x20 == 'i' || rest[0]|0x20 == 'n') {
		switch lower(string(rest)) {
		case "inf", "infinity":
			if neg {
				return math.Inf(-1), true
			}
			return math.Inf(1), true
		case "nan":
			return quietNaN, true
		}
		if len(rest) > 5 && lower(string(rest[:4])) == "nan(" && rest[len(rest)-1] == ')' {
			return quietNaN, true
		}
		return 0, false
	}
	var digits []byte
	pointAt := -1
	hasExp := false
	exp := 0
	j := i
	for ; j < len(b); j++ {
		c := b[j]
		switch {
		case c >= '0' && c <= '9':
			digits = append(digits, c)
			continue
		case (!comma && c == '.') || (comma && c == ','):
			if pointAt >= 0 {
				return 0, false
			}
			pointAt = len(digits)
			continue
		}
		break
	}
	if j < len(b) {
		c := b[j]
		switch {
		case c == 'e' || c == 'E' || c == 'd' || c == 'D' || c == 'q' || c == 'Q':
			j++
			hasExp = true
		case c == '+' || c == '-':
			hasExp = true
		default:
			return 0, false
		}
		if hasExp {
			eneg := false
			if j < len(b) && (b[j] == '+' || b[j] == '-') {
				eneg = b[j] == '-'
				j++
			}
			// libgfortran reads "E+" followed by blanks as a zero exponent;
			// a bare "E" or a sign ending the field is an error
			if j >= len(b) && !(endBlank && j > 0 && (b[j-1] == '+' || b[j-1] == '-')) {
				return 0, false
			}
			for ; j < len(b); j++ {
				if b[j] < '0' || b[j] > '9' {
					return 0, false
				}
				if exp < 1<<24 {
					exp = exp*10 + int(b[j]-'0')
				}
			}
			if eneg {
				exp = -exp
			}
		}
	}
	if len(digits) == 0 && pointAt < 0 && !hasExp {
		return 0, true // an empty field
	}
	if pointAt < 0 {
		if d >= 0 {
			pointAt = len(digits) - d // implied decimal point
		} else {
			pointAt = len(digits)
		}
	}
	if !hasExp {
		exp -= k // the scale factor applies when there is no exponent
	}
	// canonical: DIGITS e (exp + pointAt - len(digits))
	e10 := exp + pointAt - len(digits)
	buf := make([]byte, 0, len(digits)+24)
	if neg {
		buf = append(buf, '-')
	}
	if len(digits) == 0 {
		buf = append(buf, '0')
	}
	buf = append(buf, digits...)
	buf = append(buf, 'e')
	buf = strconv.AppendInt(buf, int64(e10), 10)
	x, err := strconv.ParseFloat(string(buf), bits)
	if err != nil && !math.IsInf(x, 0) {
		return 0, false
	}
	// like libgfortran, a value out of range is delivered as Inf or 0
	return x, true
}

func nonZero(d []byte) bool {
	for _, c := range d {
		if c != '0' {
			return true
		}
	}
	return false
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func (s *inStmt) readReal(f []byte, t *tok, bits int) (float64, *Error) {
	b := s.blanks(f)
	if len(b) == 0 {
		return 0, nil
	}
	d := -1
	if t.hasD {
		d = t.d
	}
	x, ok := realText(b, d, s.m.scale, bits, s.m.comma, len(f) > 0 && f[len(f)-1] == ' ')
	if !ok {
		return 0, errf(ErrReadValue, "Bad real number in item %d of list input", s.item)
	}
	return x, nil
}
