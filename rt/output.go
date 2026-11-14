// Output statements: integer, B/O/Z, logical and character editing, and
// list-directed output (F2018 13.7.2.2, 13.7.3, 13.7.4, 13.10.4).
//
// Portions are ported from LLVM flang-rt, flang-rt/lib/runtime/edit-output.cpp
// (EditIntegerOutput, EditBOZOutput, EditLogicalOutput, EditCharacterOutput)
// at tag llvmorg-23.1.4.
// Part of the LLVM Project, under the Apache License v2.0 with LLVM
// Exceptions. See https://llvm.org/LICENSE.txt and the NOTICE file.
// SPDX-License-Identifier: Apache-2.0 WITH LLVM-exception
//
// Changes: list-directed output follows gfortran 15.2.0, not flang (field
// widths, E form, COMPLEX layout, see doc.go).

package rt

import (
	"math"
	"strconv"
)

// Kind is the type of a Val.
type Kind uint8

const (
	Integer Kind = iota + 1
	Real
	Complex
	Logical
	Character
)

// Val is one item of an I/O list. Size is the KIND in bytes (1, 2, 4, 8 for
// INTEGER and LOGICAL; 4 or 8 for REAL and for each part of COMPLEX); for
// CHARACTER the length is len(S). On input the fields are updated in place.
type Val struct {
	K    Kind
	Size int
	I    int64   // INTEGER
	F, G float64 // REAL (F), COMPLEX (F real part, G imaginary part)
	B    bool    // LOGICAL
	S    string  // CHARACTER
}

// Constructors for the common items.
func Int(size int, v int64) Val          { return Val{K: Integer, Size: size, I: v} }
func Real4(v float32) Val                { return Val{K: Real, Size: 4, F: float64(v)} }
func Real8(v float64) Val                { return Val{K: Real, Size: 8, F: v} }
func Cmplx(size int, re, im float64) Val { return Val{K: Complex, Size: size, F: re, G: im} }
func Bool(size int, v bool) Val          { return Val{K: Logical, Size: size, B: v} }
func Str(s string) Val                   { return Val{K: Character, S: s} }

func (v *Val) bits() int {
	if v.Size == 4 {
		return 32
	}
	return 64
}

// Spec describes one data transfer statement.
type Spec struct {
	// Format is the FORMAT; it is used when HasFormat is set, else the
	// transfer is list-directed.
	Format    string
	HasFormat bool
	// NonAdvancing is ADVANCE='NO': the record is left open on output, and
	// input leaves the position within the record.
	NonAdvancing bool
}

// outStmt is the state of one output statement.
type outStmt struct {
	u        *Unit
	m        modes
	buf      []byte // field under construction
	dig      []byte // digit scratch
	lastChar bool   // list-directed: the previous item was an undelimited character value
	nonAdv   bool
}

func (u *Unit) modes() modes {
	m := modes{plus: ' ', round: u.Round, blankZero: u.BlankZ, comma: u.DecComma}
	return m
}

// Write performs a WRITE (or PRINT) of items on the unit.
func (u *Unit) Write(sp Spec, items []Val) *Error {
	if !u.canWrite {
		return errf(ErrBadAction, "Cannot write to a file opened for reading")
	}
	// the statement state lives in the unit, so a steady-state statement
	// allocates nothing but what its data needs
	s := &u.ost
	*s = outStmt{u: u, m: u.modes(), nonAdv: sp.NonAdvancing, buf: s.buf[:0], dig: s.dig[:0]}
	if cap(s.dig) == 0 {
		s.dig = make([]byte, 0, 128)
	}
	var err *Error
	if sp.HasFormat {
		err = s.formatted(sp.Format, items)
	} else {
		err = s.listDirected(items)
	}
	if err != nil {
		u.abortRecord()
		return err
	}
	if s.nonAdv {
		return nil
	}
	return u.endRecord()
}

func (s *outStmt) ctrl(t *tok) *Error {
	u := s.u
	switch t.kind {
	case tLit:
		return u.put([]byte(t.str))
	}
	switch t.c {
	case 'X':
		return u.move(u.pos + t.w)
	case 'T':
		return u.move(t.w - 1)
	case 'l':
		return u.move(u.pos - t.w)
	case 'r':
		return u.move(u.pos + t.w)
	case '/':
		for i := 0; i < t.rep; i++ {
			if e := u.endRecord(); e != nil {
				return e
			}
		}
	case 'P':
		s.m.scale = t.w
	case 'S':
		s.m.plus = t.v
	case 'b':
		s.m.blankZero = t.v == 'Z'
	case 'R':
		s.m.round = map[byte]Round{'N': RoundNearest, 'U': RoundUp, 'D': RoundDown,
			'Z': RoundZero, 'C': RoundCompatible, 'P': RoundProcessor}[t.v]
	case 'C':
		s.m.comma = t.v == 'C'
	case '$':
		s.nonAdv = true
	}
	return nil
}

func (s *outStmt) endRecord() *Error { return s.u.endRecord() }

func (s *outStmt) formatted(format string, items []Val) *Error {
	f, err := CompileFormat(format)
	if err != nil {
		return errf(ErrFormat, "%v", err)
	}
	c := &s.u.fc
	c.init(f)
	for i := range items {
		v := &items[i]
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
	if _, e := c.next(false, s); e != nil {
		return e
	}
	return nil
}

// edit performs one data edit descriptor on an item (part 1 of a COMPLEX is
// its imaginary part).
func (s *outStmt) edit(t *tok, v *Val, part int) *Error {
	s.buf = s.buf[:0]
	var e *Error
	switch v.K {
	case Integer:
		s.buf, e = s.editInteger(s.buf, t, v)
	case Real, Complex:
		x := v.F
		if part == 1 {
			x = v.G
		}
		s.buf, e = s.editRealItem(s.buf, t, x, v.bits(), v)
	case Logical:
		s.buf, e = s.editLogical(s.buf, t, v)
	case Character:
		s.buf, e = s.editChar(s.buf, t, v.S, v)
	}
	if e != nil {
		return e
	}
	return s.u.put(s.buf)
}

func descErr(t *tok, what string) *Error {
	name := string(t.c)
	if t.c == 'E' && t.v != 0 {
		name += string(t.v)
	}
	return errf(ErrFormat, "Data edit descriptor '%s' may not be used with a %s data item", name, what)
}

func (s *outStmt) editRealItem(dst []byte, t *tok, x float64, bits int, v *Val) ([]byte, *Error) {
	switch t.c {
	case 'F':
		return editF(dst, s.dig, x, bits, t, &s.m, 0, false)
	case 'E', 'D':
		if t.v == 'X' {
			if !EnableEX {
				// gfortran 15.2 rejects EX; it is the equality oracle
				return dst, errf(ErrFormat, "Unsupported edit descriptor EX")
			}
			return editEX(dst, x, bits, t, &s.m), nil
		}
		return editE(dst, s.dig, x, bits, t, &s.m, false)
	case 'G':
		return editG(dst, s.dig, x, bits, t, &s.m)
	case 'B', 'O', 'Z':
		var raw uint64
		size := 8
		if bits == 32 {
			raw, size = uint64(math.Float32bits(float32(x))), 4
		} else {
			raw = math.Float64bits(x)
		}
		return editBOZ(dst, t, raw, size)
	}
	return dst, descErr(t, "REAL")
}

// editInteger is flang-rt's EditIntegerOutput (F2018 13.7.2.2).
func (s *outStmt) editInteger(dst []byte, t *tok, v *Val) ([]byte, *Error) {
	switch t.c {
	case 'I', 'G':
	case 'B', 'O', 'Z':
		return editBOZ(dst, t, uint64(v.I), v.Size)
	case 'L': // extension: a non-zero value is true
		dst = repeat(dst, ' ', max(0, t.w-1))
		if v.I != 0 {
			return append(dst, 'T'), nil
		}
		return append(dst, 'F'), nil
	case 'A': // extension: the bytes of the value, least significant first
		return s.editChar(dst, t, string(leBytes(uint64(v.I), v.Size)), v)
	default:
		return dst, descErr(t, "INTEGER")
	}
	if t.hasD && t.w > 0 && t.d > t.w {
		return dst, errf(ErrFormat, "Precision (%d) larger than the width (%d) in format specifier I", t.d, t.w)
	}
	var tmp [24]byte
	p := len(tmp)
	neg := v.I < 0
	un := uint64(v.I)
	if neg {
		un = -un
	}
	for ; un > 0; un /= 10 {
		p--
		tmp[p] = byte('0' + un%10)
	}
	digits := len(tmp) - p
	sign := 0
	if neg || s.m.plus == '+' {
		sign = 1
	}
	zeros := 0
	w := t.w
	if t.c == 'I' && t.hasD && digits <= t.d {
		if t.d == 0 && v.I == 0 {
			sign = 0
			w = max(1, w)
		} else {
			zeros = t.d - digits
		}
	} else if v.I == 0 {
		zeros = 1
	}
	sub := sign + zeros + digits
	spaces := max(0, w-sub)
	if w > 0 && spaces+sub > w {
		return repeat(dst, '*', w), nil
	}
	dst = repeat(dst, ' ', spaces)
	if sign > 0 {
		if neg {
			dst = append(dst, '-')
		} else {
			dst = append(dst, '+')
		}
	}
	dst = repeat(dst, '0', zeros)
	return append(dst, tmp[p:]...), nil
}

// editBOZ is flang-rt's EditBOZOutput: the bits of a value of size bytes as
// one unsigned number in base 2, 8 or 16.
func editBOZ(dst []byte, t *tok, raw uint64, size int) ([]byte, *Error) {
	if t.hasD && t.w > 0 && t.d > t.w {
		return dst, errf(ErrFormat, "Precision (%d) larger than the width (%d) in format specifier %c", t.d, t.w, t.c)
	}
	var shift uint
	switch t.c {
	case 'B':
		shift = 1
	case 'O':
		shift = 3
	default:
		shift = 4
	}
	bitsN := uint(size * 8)
	if bitsN < 64 {
		raw &= 1<<bitsN - 1
	}
	if t.c != 'B' && t.hasD && t.d == 0 && raw == 1<<63 && size == 8 {
		// libgfortran quirk: B/O/Z w.0 of the most negative INTEGER(8) is
		// written as a blank field, as if the value were zero
		raw = 0
	}
	var tmp [72]byte
	p := len(tmp)
	for u := raw; u > 0; u >>= shift {
		p--
		tmp[p] = hexDigits[u&(1<<shift-1)]
	}
	sig := len(tmp) - p
	w := t.w
	zeros := 0
	if t.hasD && sig <= t.d {
		if t.d == 0 && raw == 0 {
			w = max(1, w)
			sig = 0
		} else {
			zeros = t.d - sig
		}
	} else if raw == 0 {
		zeros = 1
	}
	sub := zeros + sig
	spaces := max(0, w-sub)
	if w > 0 && spaces+sub > w {
		return repeat(dst, '*', w), nil
	}
	dst = repeat(dst, ' ', spaces)
	dst = repeat(dst, '0', zeros)
	return append(dst, tmp[p:p+sig]...), nil
}

func (s *outStmt) editLogical(dst []byte, t *tok, v *Val) ([]byte, *Error) {
	switch t.c {
	case 'L', 'G':
		dst = repeat(dst, ' ', max(0, t.w-1))
		if v.B {
			return append(dst, 'T'), nil
		}
		return append(dst, 'F'), nil
	case 'B', 'O', 'Z':
		var x uint64
		if v.B {
			x = 1
		}
		return editBOZ(dst, t, x, v.Size)
	case 'A':
		var x uint64
		if v.B {
			x = 1
		}
		return s.editChar(dst, t, string(leBytes(x, v.Size)), v)
	}
	return dst, descErr(t, "LOGICAL")
}

func leBytes(x uint64, size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(x >> (8 * uint(i)))
	}
	return b
}

func (s *outStmt) editChar(dst []byte, t *tok, str string, v *Val) ([]byte, *Error) {
	w := len(str)
	switch t.c {
	case 'A':
		if t.hasW {
			if t.w == 0 {
				return dst, errf(ErrFormat, "Positive width required in format specifier A")
			}
			w = t.w
		}
	case 'G':
		if t.w != 0 {
			w = t.w
		}
	default:
		return dst, descErr(t, "CHARACTER")
	}
	dst = repeat(dst, ' ', max(0, w-len(str)))
	return append(dst, str[:min(w, len(str))]...), nil
}

// ---- list-directed output (F2018 13.10.4) -----------------------------------

// listIntWidth is the field width of a list-directed integer of a kind.
func listIntWidth(size int) int {
	switch size {
	case 1:
		return 4
	case 2:
		return 6
	case 8:
		return 20
	case 16:
		return 40
	}
	return 11
}

func (s *outStmt) listDirected(items []Val) *Error {
	u := s.u
	for i := range items {
		v := &items[i]
		s.buf = s.buf[:0]
		isChar := v.K == Character
		sep := !(s.lastChar && isChar)
		switch v.K {
		case Integer:
			w := listIntWidth(v.Size)
			s.buf = strconv.AppendInt(s.buf[:0], v.I, 10)
			s.buf = padLeft(s.buf, w)
		case Logical:
			s.buf = append(s.buf, ' ')
			s.buf = s.buf[:0]
			if v.B {
				s.buf = append(s.buf, 'T')
			} else {
				s.buf = append(s.buf, 'F')
			}
		case Real:
			s.buf = s.listReal(s.buf, v.F, v.bits(), false)
		case Complex:
			a := trimBlanks(s.listReal(nil, v.F, v.bits(), true))
			b := trimBlanks(s.listReal(nil, v.G, v.bits(), true))
			s.buf = append(s.buf, '(')
			s.buf = append(s.buf, a...)
			s.buf = append(s.buf, ',')
			s.buf = append(s.buf, b...)
			s.buf = append(s.buf, ')')
			w := 2*listRealWidth(v.bits()) + 3
			s.buf = padLeft(s.buf, w)
		case Character:
			if u.Delim != 0 {
				q := u.Delim
				s.buf = append(s.buf, q)
				for j := 0; j < len(v.S); j++ {
					if v.S[j] == q {
						s.buf = append(s.buf, q)
					}
					s.buf = append(s.buf, v.S[j])
				}
				s.buf = append(s.buf, q)
				sep = true
			} else {
				s.buf = append(s.buf, v.S...)
			}
		}
		if sep {
			if e := u.putByte(' '); e != nil {
				return e
			}
		}
		if e := u.put(s.buf); e != nil {
			return e
		}
		s.lastChar = isChar && u.Delim == 0
	}
	return nil
}

func padLeft(b []byte, w int) []byte {
	if len(b) >= w {
		return b
	}
	n := w - len(b)
	for i := 0; i < n; i++ {
		b = append(b, ' ')
	}
	copy(b[n:], b[:len(b)-n])
	for i := 0; i < n; i++ {
		b[i] = ' '
	}
	return b
}

func trimBlanks(b []byte) []byte {
	for len(b) > 0 && b[0] == ' ' {
		b = b[1:]
	}
	for len(b) > 0 && b[len(b)-1] == ' ' {
		b = b[:len(b)-1]
	}
	return b
}

func listRealWidth(bits int) int {
	if bits == 32 {
		return 16
	}
	return 25
}

// listReal formats a list-directed REAL as gfortran does: G editing with
// 9 (kind 4) or 17 (kind 8) significant digits in a field of 16 or 25, an
// E form of the 1PE kind (E+02 and E+002 exponents) outside 0.1 <= |x| <
// 10**digits.
func (s *outStmt) listReal(dst []byte, x float64, bits int, cplx bool) []byte {
	prec, w, e := 9, 16, 2
	if bits == 64 {
		prec, w, e = 17, 25, 3
	}
	m := s.m
	m.scale = 0
	if math.IsNaN(x) || math.IsInf(x, 0) {
		var d dec
		d.classify(x)
		if cplx {
			w = 0 // "Inf", not "Infinity", inside a COMPLEX
		}
		return special(dst, &d, w, &m, 0)
	}
	expo := 1
	if x != 0 {
		var d dec
		d.convert(s.dig, x, bits, prec, m.round, false)
		expo = d.exp
	}
	if expo < 0 || expo > prec {
		// the parts of a COMPLEX carry one more digit in the E form
		d := prec - 1
		if cplx {
			d = prec
		}
		t := tok{c: 'E', v: 'S', w: w, d: d, hasD: true, e: e, hasE: true}
		if cplx {
			t.w = 0
			t.hasD = true
		}
		out, _ := editE(dst, s.dig, x, bits, &t, &m, false)
		return out
	}
	t := tok{c: 'F', w: w, d: max(0, prec-expo), hasD: true}
	out, _ := editF(dst, s.dig, x, bits, &t, &m, e+2, false)
	return out
}
