package sema

import (
	"math"
	"strconv"

	"github.com/go-embedded-fortran/fortran/syntax"
)

// bozReal folds REAL(boz [, kind]) and DBLE(boz): F2018 16.9.160 and 16.9.65
// take a boz-literal-constant as the internal representation of the result
// (16.3.3), so dble(z"400921FB54411744") is pi, not 4.6e18 converted. It
// returns (nil, "") when the reference is not of that form. A constant wider
// than the result kind is refused instead of truncated, and a real kind other
// than 4 or 8 is refused: their representations are not modelled here.
//
// INT(boz [, kind]) is folded the same way (F2018 16.9.100): the bits are the
// integer's representation, so int(z'FFFFFFFF', 4) is -1 (no_range_check_3
// of gfortran.dg), and a constant wider than the kind is refused.
func bozReal(name string, acts []actual, rt Type) (*Const, string) {
	if name == "int" && len(acts) > 0 && rt.Basic == Integer {
		return bozInt(acts[0].e, rt)
	}
	if name != "real" && name != "dble" || len(acts) == 0 || rt.Basic != Real {
		return nil, ""
	}
	boz, ok := acts[0].e.(*syntax.BozLit)
	if !ok {
		return nil, ""
	}
	base := map[byte]int{'b': 2, 'o': 8, 'z': 16}[boz.Base]
	bits, err := strconv.ParseUint(boz.Digits, max(base, 2), 64)
	if err != nil {
		return nil, "BOZ constant is wider than 64 bits or malformed"
	}
	switch rt.Kind {
	case 4:
		if bits > math.MaxUint32 {
			return nil, "BOZ constant is wider than REAL(4)"
		}
		return &Const{Type: rt, F: float64(math.Float32frombits(uint32(bits)))}, ""
	case 8:
		return &Const{Type: rt, F: math.Float64frombits(bits)}, ""
	}
	return nil, "BOZ constant as REAL(" + strconv.Itoa(rt.Kind) + ") is not supported"
}

func bozInt(e syntax.Expr, rt Type) (*Const, string) {
	boz, ok := e.(*syntax.BozLit)
	if !ok {
		return nil, ""
	}
	base := map[byte]int{'b': 2, 'o': 8, 'z': 16}[boz.Base]
	bits, err := strconv.ParseUint(boz.Digits, max(base, 2), 64)
	if err != nil {
		return nil, "BOZ constant is wider than 64 bits or malformed"
	}
	if rt.Kind != 1 && rt.Kind != 2 && rt.Kind != 4 && rt.Kind != 8 {
		return nil, "BOZ constant as INTEGER(" + strconv.Itoa(rt.Kind) + ") is not supported"
	}
	width := uint(8 * rt.Kind)
	if width < 64 && bits>>width != 0 {
		return nil, "BOZ constant is wider than INTEGER(" + strconv.Itoa(rt.Kind) + ")"
	}
	// sign-extend from the kind's width: the leftmost bit is the sign
	v := int64(bits<<(64-width)) >> (64 - width)
	return &Const{Type: rt, I: v}, ""
}
