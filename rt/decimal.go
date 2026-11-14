package rt

import (
	"math"
	"strconv"
)

// Round is a rounding mode of F2018 13.7.2.3.7: the RN, RU, RD, RZ, RC and
// RP edit descriptors (and the ROUND= specifier).
type Round uint8

const (
	RoundUnspecified Round = iota // processor defined: nearest, ties to even on the exact value
	RoundNearest                  // RN: nearest, ties to even on the exact value
	RoundUp                       // RU
	RoundDown                     // RD
	RoundZero                     // RZ
	RoundCompatible               // RC: nearest, ties away from zero
	RoundProcessor                // RP
)

// dec is the decimal conversion of one floating-point value: the digits of
// 0.DIGITS x 10^exp (flang-rt's ConversionToDecimalResult, with the sign kept
// apart). A zero value has digits "0" and exp 0. Trailing zero digits are
// trimmed.
type dec struct {
	neg     bool
	special byte // 0, 'I' (infinity) or 'N' (NaN)
	digits  []byte
	exp     int
}

func (d *dec) isZero() bool {
	return d.special == 0 && len(d.digits) == 1 && d.digits[0] == '0' && d.exp == 0
}

// classify fills the sign and the special classes of x.
func (d *dec) classify(x float64) (ax float64) {
	d.neg = math.Signbit(x)
	switch {
	case math.IsNaN(x):
		d.special = 'N'
		d.neg = false // gfortran and flang print NaN unsigned
	case math.IsInf(x, 0):
		d.special = 'I'
	}
	return math.Abs(x)
}

// maxExactDigits bounds the significant digits of the exact decimal value of
// a binary64 (a denormal needs 751; 767 is the classic bound).
const maxExactDigits = 770

// convert sets d to x rounded to n significant digits (n >= 0), with mode;
// or, when minimize is set, to the shortest digits that read back as x with
// the given binary precision (32 or 64).
func (d *dec) convert(buf []byte, x float64, bits, n int, mode Round, minimize bool) {
	ax := d.classify(x)
	if d.special != 0 {
		return
	}
	if ax == 0 {
		d.digits, d.exp = append(buf[:0], '0'), 0
		return
	}
	var raw []byte
	switch {
	case minimize:
		raw = strconv.AppendFloat(buf[:0], ax, 'e', -1, bits)
	case n > 0 && (mode == RoundUnspecified || mode == RoundNearest || mode == RoundProcessor):
		raw = strconv.AppendFloat(buf[:0], ax, 'e', n-1, bits)
	default:
		raw = strconv.AppendFloat(buf[:0], ax, 'e', maxExactDigits-1, 64)
	}
	// raw is d.ddddde+XX: gather the digits and the exponent.
	e := 0
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] == 'e' {
			ev, _ := strconv.Atoi(string(raw[i+1:]))
			e = ev
			raw = raw[:i]
			break
		}
	}
	// compact the digits in place (drop the point)
	k := 0
	for _, c := range raw {
		if c != '.' {
			raw[k] = c
			k++
		}
	}
	digits := raw[:k]
	exp := e + 1
	exact := !minimize && !(n > 0 && (mode == RoundUnspecified || mode == RoundNearest || mode == RoundProcessor))
	if exact {
		digits = trimZeros(digits)
		digits, exp = roundDigits(digits, exp, n, mode, d.neg)
	} else {
		digits = trimZeros(digits)
	}
	if len(digits) == 0 {
		digits = append(digits, '0')
		exp = 0
	}
	d.digits, d.exp = digits, exp
}

func trimZeros(d []byte) []byte {
	for len(d) > 1 && d[len(d)-1] == '0' {
		d = d[:len(d)-1]
	}
	return d
}

// roundDigits rounds the exact digits D (0.D x 10^exp, D free of trailing
// zeros) to n significant digits under mode. A result of no digits is
// zero; carrying out of the first digit gives "1" and a larger exponent.
func roundDigits(D []byte, exp, n int, mode Round, neg bool) ([]byte, int) {
	if n >= len(D) {
		return D, exp
	}
	// the first dropped digit, and whether anything non-zero follows it
	var first byte = '0'
	rest := false
	last := byte(0) // last kept digit
	if n >= 0 {
		first = D[n]
		rest = len(D) > n+1
		if n > 0 {
			last = D[n-1]
		}
	} else {
		rest = true
	}
	inc := false
	switch mode {
	case RoundZero:
	case RoundDown:
		inc = neg
	case RoundUp:
		inc = !neg
	case RoundCompatible:
		inc = first >= '5'
	default: // nearest, ties to even
		switch {
		case first > '5':
			inc = true
		case first == '5':
			inc = rest || (last != 0 && (last-'0')%2 == 1)
		}
	}
	if n < 0 {
		if inc && (mode == RoundUp || mode == RoundDown) {
			return append(D[:0], '1'), exp - n + 1
		}
		return D[:0], exp
	}
	kept := D[:n]
	if !inc {
		return trimZeros(kept), exp
	}
	i := n - 1
	for ; i >= 0; i-- {
		if kept[i] == '9' {
			kept[i] = '0'
			continue
		}
		kept[i]++
		break
	}
	if i < 0 { // carried out of the first digit
		if n == 0 {
			return append(D[:0], '1'), exp + 1
		}
		kept[0] = '1'
		for j := 1; j < n; j++ {
			kept[j] = '0'
		}
		return trimZeros(kept[:n]), exp + 1
	}
	return trimZeros(kept), exp
}
