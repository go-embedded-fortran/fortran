// Real number output editing: F, E, D, EN, ES, EX and G (F2018 13.7.2.3).
//
// Portions are ported from LLVM flang-rt, flang-rt/lib/runtime/edit-output.cpp
// (RealOutputEditing::EditEorDOutput, EditFOutput, EditForGOutput,
// EditEXOutput, FormatExponent) at tag llvmorg-23.1.4.
// Part of the LLVM Project, under the Apache License v2.0 with LLVM
// Exceptions. See https://llvm.org/LICENSE.txt and the NOTICE file.
// SPDX-License-Identifier: Apache-2.0 WITH LLVM-exception
//
// Changes: flang's repeated conversions are replaced by one conversion at the
// digit count the layout needs (decimal.go); and the places where gfortran
// 15.2.0 prints something else (see doc.go) follow gfortran.

package rt

import (
	"math"
	"strconv"
)

// modes are the mutable edit modes of a data transfer statement (F2018 13.8):
// the scale factor, sign, blank interpretation, rounding and decimal mode.
type modes struct {
	scale     int  // kP
	plus      byte // ' ' processor defined, '+' SP, '-' SS
	blankZero bool // BZ (the default is BN)
	round     Round
	comma     bool // DC
}

func (m *modes) point() byte {
	if m.comma {
		return ','
	}
	return '.'
}

// expProbe returns the decimal exponent e0 of a finite non-zero ax such that
// ax = 0.D x 10^e0 with D beginning with a non-zero digit.
func expProbe(buf []byte, ax float64) int {
	b := strconv.AppendFloat(buf[:0], ax, 'e', 16, 64)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == 'e' {
			e, _ := strconv.Atoi(string(b[i+1:]))
			return e + 1
		}
	}
	return 0
}

func repeat(dst []byte, c byte, n int) []byte {
	for ; n > 0; n-- {
		dst = append(dst, c)
	}
	return dst
}

// signOf returns the sign character of a converted value: '-', '+' (SP) or 0.
func signOf(neg bool, m *modes) byte {
	if neg {
		return '-'
	}
	if m.plus == '+' {
		return '+'
	}
	return 0
}

// special writes Inf or NaN right-justified in w (F2018 13.7.2.3.1), or
// asterisks when it does not fit.
func special(dst []byte, d *dec, w int, m *modes, trail int) []byte {
	var s []byte
	sg := signOf(d.neg, m)
	if d.special == 'N' {
		s = append(s, "NaN"...)
	} else {
		if sg != 0 {
			s = append(s, sg)
		}
		if w >= len(s)+8 {
			s = append(s, "Infinity"...)
		} else {
			s = append(s, "Inf"...)
		}
	}
	if w > 0 && len(s)+trail > w {
		return repeat(dst, '*', w)
	}
	if w > len(s)+trail {
		dst = repeat(dst, ' ', w-len(s)-trail)
	}
	dst = append(dst, s...)
	return repeat(dst, ' ', trail)
}

// editF edits x under Fw.d (F2018 13.7.2.3.2). trail is the number of blanks
// that follow the field when it stands for a G edit descriptor.
func editF(dst, buf []byte, x float64, bits int, t *tok, m *modes, trail int, lz bool) ([]byte, *Error) {
	var d dec
	w, fd := t.w, t.d
	minimize := w == 0 && !t.hasD
	k := m.scale
	ax := math.Abs(x)
	if math.IsNaN(x) || math.IsInf(x, 0) {
		d.classify(x)
		return special(dst, &d, w, m, trail), nil
	}
	if ax == 0 {
		d.classify(x)
		d.digits, d.exp = append(buf[:0], '0'), 0
	} else if minimize {
		d.convert(buf, x, bits, 0, m.round, true)
		fd = len(d.digits) - (d.exp + k)
		if fd < 0 {
			fd = 0
		}
	} else {
		d.neg = math.Signbit(x)
		n := expProbe(buf, ax) + k + fd
		if n < -19 {
			// libgfortran looks at d+20 decimals only: RU of 1e-30 in F6.1
			// is 0.0, not 0.1
			d.digits, d.exp = append(buf[:0], '0'), 0
		} else {
			d.convert(buf, x, bits, n, m.round, false)
		}
	}
	expo := d.exp + k
	nd := len(d.digits)
	if d.isZero() || (nd == 1 && d.digits[0] == '0') {
		expo, nd = 0, 0
	}
	sg := signOf(d.neg, m)
	sl := 0
	if sg != 0 {
		sl = 1
	}
	digitsBefore := max(0, min(expo, nd))
	zerosBefore := max(0, expo-digitsBefore)
	zerosAfter := min(fd, max(0, -expo))
	digitsAfter := nd - digitsBefore
	trailingZeros := 0
	if !minimize {
		trailingZeros = max(0, fd-(zerosAfter+digitsAfter))
	}
	if digitsBefore+zerosBefore+zerosAfter+digitsAfter+trailingZeros == 0 {
		zerosBefore = 1
	}
	total := sl + digitsBefore + zerosBefore + 1 + zerosAfter + digitsAfter + trailingZeros + trail
	width := w
	if w == 0 && trail == 0 {
		width = total
	}
	if total > width {
		return repeat(dst, '*', width), nil
	}
	if digitsBefore+zerosBefore == 0 && (total < width || (lz && w == 0)) {
		zerosBefore = 1
		total++
	}
	dst = repeat(dst, ' ', width-total)
	if sl > 0 {
		dst = append(dst, sg)
	}
	dst = append(dst, d.digits[:digitsBefore]...)
	dst = repeat(dst, '0', zerosBefore)
	dst = append(dst, m.point())
	dst = repeat(dst, '0', zerosAfter)
	dst = append(dst, d.digits[digitsBefore:digitsBefore+digitsAfter]...)
	dst = repeat(dst, '0', trailingZeros)
	return repeat(dst, ' ', trail), nil
}

// formatExponent builds the exponent field of E, D, EN, ES, EX and G editing
// (F2018 table 13.1); ok is false when the exponent does not fit in Ee.
func formatExponent(tmp *[16]byte, expo int, t *tok, listDirected bool) (s []byte, ok bool) {
	p := len(tmp)
	e := expo
	if e < 0 {
		e = -e
	}
	for ; e > 0; e /= 10 {
		p--
		tmp[p] = byte('0' + e%10)
	}
	ok = true
	switch {
	case t.hasE:
		if t.e > 0 {
			ok = len(tmp)-p <= t.e
			for len(tmp)-p < t.e && p > 2 {
				p--
				tmp[p] = '0'
			}
		} else if p == len(tmp) {
			p--
			tmp[p] = '0'
		}
	case t.v == 'X':
		if expo == 0 {
			p--
			tmp[p] = '0'
		}
	default:
		for len(tmp)-p < 2 {
			p--
			tmp[p] = '0'
		}
	}
	p--
	if expo < 0 {
		tmp[p] = '-'
	} else {
		tmp[p] = '+'
	}
	switch {
	case t.v == 'X':
		p--
		tmp[p] = 'P'
	case t.hasE || listDirected || len(tmp)-p == 3:
		p--
		if t.c == 'D' {
			tmp[p] = 'D'
		} else {
			tmp[p] = 'E'
		}
	}
	return tmp[p:], ok
}

// editE edits x under Ew.d, Dw.d, ENw.d and ESw.d (F2018 13.7.2.3.3 to
// 13.7.2.3.5). g marks the descriptor as the E form chosen for a G edit.
func editE(dst, buf []byte, x float64, bits int, t *tok, m *modes, g bool) ([]byte, *Error) {
	var d dec
	w, ed := t.w, t.d
	isEN, isES := t.v == 'N', t.v == 'S'
	k := m.scale
	ax := math.Abs(x)
	if math.IsNaN(x) || math.IsInf(x, 0) {
		d.classify(x)
		return special(dst, &d, w, m, 0), nil
	}
	if !g && t.v == 0 && (!t.hasD || ed == 0) {
		return nil, &Error{Stat: ErrFormat, Msg: "Scale factor out of range in format specifier 'E' or 'D'"}
	}
	sig := ed
	minimize := false
	if w == 0 {
		if t.hasD {
			if ed == 0 && k <= 0 {
				if isEN || isES {
					sig = 0
				} else {
					sig = 1
				}
			}
		} else {
			minimize = true
		}
	}
	zerosAfterPoint := 0
	switch {
	case isEN:
	case isES:
		k = 1
		sig++
	case k < 0:
		if k <= -ed && !g {
			return nil, &Error{Stat: ErrFormat, Msg: "Scale factor (kP) cannot be less than -d"}
		}
		zerosAfterPoint = -k
		sig = max(0, sig-zerosAfterPoint)
	case k > 0:
		if k >= ed+2 && !g {
			return nil, &Error{Stat: ErrFormat, Msg: "Scale factor (kP) cannot be greater than d+2"}
		}
		sig++
	default:
		if t.hasD && ed == 0 && !t.hasE && t.v == 0 && !g {
			return nil, &Error{Stat: ErrFormat, Msg: "Output edit descriptor must have d>0"}
		}
	}
	d.neg = math.Signbit(x)
	var before int // digits before the point in the printed field
	var expo int
	switch {
	case ax == 0:
		d.digits, d.exp = append(buf[:0], '0'), 0
		if isEN {
			before = 1
		} else {
			before = max(k, 0)
			if k < 0 {
				before = 0
			}
		}
		expo = 0
		if isES {
			before = 1
		}
	case isEN:
		if minimize {
			d.convert(buf, x, bits, 0, m.round, true)
			sig = len(d.digits)
		} else {
			e0 := expProbe(buf, ax)
			b := mod3(e0-1) + 1
			d.convert(buf, x, bits, ed+b, m.round, false)
		}
		x1 := d.exp - 1
		before = mod3(x1) + 1
		expo = x1 - (before - 1)
		sig = ed + before
	default:
		if minimize {
			d.convert(buf, x, bits, 0, m.round, true)
			sig = len(d.digits)
		} else {
			d.convert(buf, x, bits, sig, m.round, false)
		}
		expo = d.exp - k
		if k > 0 || isES {
			before = k
		}
	}
	et := *t
	if w == 0 && !et.hasE {
		// gfortran: Ew.d with w=0 writes the exponent field of list-directed
		// output: E+00 for kind 4, E+000 for kind 8
		et.hasE, et.e = true, 2
		if bits == 64 {
			et.e = 3
		}
	}
	var exbuf [16]byte
	ex, ok := formatExponent(&exbuf, expo, &et, false)
	sg := signOf(d.neg, m)
	sl := 0
	if sg != 0 {
		sl = 1
	}
	nd := len(d.digits)
	zerosBefore := max(0, before-nd)
	digitsBefore := max(0, before-zerosBefore)
	digitsAfter := nd - digitsBefore
	trailingZeros := 0
	if !minimize {
		trailingZeros = max(0, sig-(nd+zerosBefore))
	}
	if ax == 0 {
		// zero: digits "0" stand for no digit at all
		digitsBefore, digitsAfter = 0, 0
		zerosBefore = before
		trailingZeros = 0
		if !minimize {
			trailingZeros = max(0, sig-zerosBefore)
		}
		if isEN || isES {
			trailingZeros = max(0, ed+before-zerosBefore)
		}
	}
	total := sl + digitsBefore + zerosBefore + 1 + zerosAfterPoint + digitsAfter + trailingZeros + len(ex)
	width := w
	if w == 0 {
		width = total
	}
	if total > width || !ok {
		return repeat(dst, '*', width), nil
	}
	// with w = 0 libgfortran writes the leading zero, except for a REAL(4)
	// asked for more than its 9 digits
	if (total < width || (w == 0 && !(bits == 32 && ed > 9))) && digitsBefore == 0 && zerosBefore == 0 {
		zerosBefore = 1
		total++
		width = max(width, total)
	}
	dst = repeat(dst, ' ', width-total)
	if sl > 0 {
		dst = append(dst, sg)
	}
	if ax == 0 {
		dst = repeat(dst, '0', zerosBefore)
	} else {
		dst = append(dst, d.digits[:digitsBefore]...)
		dst = repeat(dst, '0', zerosBefore)
	}
	dst = append(dst, m.point())
	dst = repeat(dst, '0', zerosAfterPoint)
	if ax != 0 {
		dst = append(dst, d.digits[digitsBefore:digitsBefore+digitsAfter]...)
	}
	dst = repeat(dst, '0', trailingZeros)
	return append(dst, ex...), nil
}

func mod3(x int) int {
	r := x % 3
	if r < 0 {
		r += 3
	}
	return r
}

// EnableEX turns on the EX edit descriptor (F2018 13.7.2.3.6), which gfortran
// 15.2 does not implement (it fails with iostat 5006), so by default rt
// refuses it too. The implementation follows flang-rt and is not covered by
// the gfortran differential.
var EnableEX = false

// hexDigits is the digit set of Z and EX editing.
const hexDigits = "0123456789ABCDEF"

// editEX edits x under EXw.d (F2018 13.7.2.3.6): hexadecimal significand with
// a binary exponent, in flang-rt's framing of the nybbles (2. is 0X8.0P-2).
func editEX(dst []byte, x float64, bits int, t *tok, m *modes) []byte {
	var d dec
	w := t.w
	if math.IsNaN(x) || math.IsInf(x, 0) {
		d.classify(x)
		return special(dst, &d, w, m, 0)
	}
	d.neg = math.Signbit(x)
	var mant uint64
	var e int
	maxHex := 13
	switch bits {
	case 32:
		f := math.Float32bits(float32(x))
		fr := uint64(f & 0x7fffff)
		be := int(f>>23) & 0xff
		if be != 0 {
			fr |= 1 << 23
			e = be - 127
		} else if fr != 0 {
			e = -126
		}
		mant, maxHex = fr, 6
		// 24 significant bits: nybbles are framed from the top
		if mant != 0 {
			e -= 3
			for mant>>23 == 0 {
				mant <<= 1
				e--
			}
		}
		mant <<= 0
	default:
		f := math.Float64bits(x)
		fr := f & (1<<52 - 1)
		be := int(f>>52) & 0x7ff
		if be != 0 {
			fr |= 1 << 52
			e = be - 1023
		} else if fr != 0 {
			e = -1022
		}
		mant = fr
		if mant != 0 {
			e -= 3
			for mant>>52 == 0 {
				mant <<= 1
				e--
			}
		}
	}
	// the significand as hexadecimal digits, most significant first
	var hex []byte
	if mant == 0 {
		hex = []byte{'0'}
		e = 0
	} else {
		top := 52
		if bits == 32 {
			top = 23
		}
		// bring the leading nybble (bits top..top-3) to the front
		shift := top - 3
		for digits := 0; digits < maxHex+1; digits++ {
			var h uint64
			if shift >= 0 {
				h = (mant >> uint(shift)) & 0xf
			} else if shift >= -3 {
				h = (mant << uint(-shift)) & 0xf
			}
			hex = append(hex, hexDigits[h])
			shift -= 4
		}
	}
	minimize := (w == 0 && !t.hasD) || (t.hasD && t.d == 0)
	nd := t.d + 1
	if minimize {
		nd = len(hex)
		for nd > 1 && hex[nd-1] == '0' {
			nd--
		}
		hex = hex[:nd]
	} else if nd < len(hex) {
		// round to nd hexadecimal digits under the current mode
		hex = roundHex(hex, nd, m.round, d.neg)
		if len(hex) > nd { // carry out: 0xF.. -> 0x10..
			hex = hex[:nd]
			e += 4
		}
	}
	var exbuf [16]byte
	ex, ok := formatExponent(&exbuf, e, t, false)
	sg := signOf(d.neg, m)
	trailing := 0
	if !minimize {
		trailing = max(0, nd-len(hex))
	}
	total := len(hex) + trailing + len(ex) + 3 // 0X.
	if sg != 0 {
		total++
	}
	width := w
	if w == 0 {
		width = total
	}
	if total > width || !ok {
		return repeat(dst, '*', width)
	}
	dst = repeat(dst, ' ', width-total)
	if sg != 0 {
		dst = append(dst, sg)
	}
	dst = append(dst, '0', 'X', hex[0], m.point())
	dst = append(dst, hex[1:]...)
	dst = repeat(dst, '0', trailing)
	return append(dst, ex...)
}

// roundHex rounds hexadecimal digits to n digits; the result is one digit
// longer when it carried out of the first digit.
func roundHex(hex []byte, n int, mode Round, neg bool) []byte {
	rest := false
	for _, c := range hex[n:] {
		if c != '0' {
			rest = true
		}
	}
	first := hex[n]
	val := func(c byte) int {
		if c >= 'A' {
			return int(c-'A') + 10
		}
		return int(c - '0')
	}
	inc := false
	switch mode {
	case RoundZero:
	case RoundDown:
		inc = neg && rest
	case RoundUp:
		inc = !neg && rest
	case RoundCompatible:
		inc = val(first) >= 8
	default:
		h := val(first)
		switch {
		case h > 8:
			inc = true
		case h == 8:
			inc = len(hex) > n+1 && rest && anyNonZero(hex[n+1:]) || val(hex[n-1])%2 == 1
		}
	}
	out := append([]byte(nil), hex[:n]...)
	if !inc {
		return out
	}
	for i := n - 1; i >= 0; i-- {
		if out[i] == 'F' {
			out[i] = '0'
			continue
		}
		out[i] = hexDigits[val(out[i])+1]
		return out
	}
	return append([]byte{'1'}, out...)
}

func anyNonZero(b []byte) bool {
	for _, c := range b {
		if c != '0' {
			return true
		}
	}
	return false
}

// editG edits x under Gw.d[Ee] (F2018 13.7.5.2.3): F editing when the value
// is in range, otherwise E editing.
func editG(dst, buf []byte, x float64, bits int, t *tok, m *modes) ([]byte, *Error) {
	et := *t
	et.c = 'E'
	w := t.w
	sig := t.d
	if !t.hasD {
		sig = 9
		if bits == 64 {
			sig = 17
		}
		et.d, et.hasD = sig, true
	}
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return editE(dst, buf, x, bits, &et, m, true)
	}
	if t.hasD && t.d == 0 {
		return nil, &Error{Stat: ErrFormat, Msg: "Positive width required in format specifier G"}
	}
	expo := 1
	if x != 0 {
		expo = gExponent(buf, x, bits, sig)
	}
	if expo < 0 || expo > sig {
		return editE(dst, buf, x, bits, &et, m, true)
	}
	ft := *t
	ft.c = 'F'
	save := m.scale
	m.scale = 0
	trail := 0
	if w > 0 {
		trail = 4
		if t.hasE && t.e > 0 {
			trail = t.e + 2
		}
	}
	if t.hasD {
		ft.d = max(0, t.d-expo)
	} else {
		ft.d, ft.hasD = max(0, sig-expo), true
	}
	out, err := editF(dst, buf, x, bits, &ft, m, trail, !(bits == 32 && sig > 9))
	m.scale = save
	return out, err
}

// gExponent is the exponent s of 0.D x 10^s that decides between the F and
// the E form of G editing: x is rounded to sig digits starting from its
// shortest decimal representation, ties away from zero (libgfortran does not
// round the exact binary value here: G8.1 of 0.95 is "1.", not "0.9").
func gExponent(buf []byte, x float64, bits, sig int) int {
	var d dec
	d.convert(buf, x, bits, 0, RoundNearest, true)
	if len(d.digits) > sig && d.digits[sig] >= '5' {
		// round up: a carry out of all nines raises the exponent
		for i := sig - 1; i >= 0; i-- {
			if d.digits[i] != '9' {
				return d.exp
			}
		}
		return d.exp + 1
	}
	return d.exp
}
