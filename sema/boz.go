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
//
// CMPLX(x [, y [, kind]]) and DCMPLX take a BOZ x or y the same way, as a real
// of the result's kind (F2018 16.9.55): cmplx(z'3FF0000000000000', 0, 8) is
// (1, 0). It is folded when the other part is an integer or real constant and
// left unfolded otherwise, never refused here: gfortran accepts the program,
// and the evaluator is the one that says what it cannot run (a refusal by the
// analyser costs a file of the differential floor).
func bozReal(name string, acts []actual, rt Type) (*Const, string) {
	if name == "int" && len(acts) > 0 && rt.Basic == Integer {
		return bozInt(acts[0].e, rt)
	}
	if (name == "cmplx" || name == "dcmplx") && rt.Basic == Complex {
		return bozCmplx(acts, rt)
	}
	if name != "real" && name != "dble" || len(acts) == 0 || rt.Basic != Real {
		return nil, ""
	}
	boz, ok := acts[0].e.(*syntax.BozLit)
	if !ok {
		return nil, ""
	}
	f, msg := bozAsReal(boz, rt.Kind)
	if msg != "" {
		return nil, msg
	}
	return &Const{Type: rt, F: f}, ""
}

// bozAsReal is the real of kind kind whose representation is boz.
func bozAsReal(boz *syntax.BozLit, kind int) (float64, string) {
	base := map[byte]int{'b': 2, 'o': 8, 'z': 16}[boz.Base]
	bits, err := strconv.ParseUint(boz.Digits, max(base, 2), 64)
	if err != nil {
		return 0, "BOZ constant is wider than 64 bits or malformed"
	}
	switch kind {
	case 4:
		if bits > math.MaxUint32 {
			return 0, "BOZ constant is wider than REAL(4)"
		}
		return float64(math.Float32frombits(uint32(bits))), ""
	case 8:
		return math.Float64frombits(bits), ""
	}
	return 0, "BOZ constant as REAL(" + strconv.Itoa(kind) + ") is not supported"
}

// bozCmplx folds CMPLX with a BOZ x or y (see bozReal); its message is always
// "", and the constant nil when neither part is one or it cannot fold.
func bozCmplx(acts []actual, rt Type) (*Const, string) {
	var parts [2]*actual
	for i := range acts {
		a := &acts[i]
		switch {
		case a.kw == "x" || a.kw == "" && i == 0:
			parts[0] = a
		case a.kw == "y" || a.kw == "" && i == 1:
			parts[1] = a
		}
	}
	has := false
	for _, p := range parts {
		if p != nil {
			if _, ok := p.e.(*syntax.BozLit); ok {
				has = true
			}
		}
	}
	if !has {
		return nil, ""
	}
	var v [2]float64
	for i, p := range parts {
		if p == nil {
			continue // y absent: zero
		}
		if boz, ok := p.e.(*syntax.BozLit); ok {
			f, msg := bozAsReal(boz, rt.Kind)
			if msg != "" {
				return nil, ""
			}
			v[i] = f
			continue
		}
		k := p.tv.Const
		switch {
		case k == nil || p.tv.Rank != 0:
			return nil, ""
		case k.Type.Basic == Integer:
			v[i] = float64(k.I)
		case k.Type.Basic == Real:
			v[i] = k.F
		default:
			return nil, ""
		}
		if rt.Kind == 4 {
			v[i] = float64(float32(v[i]))
		}
	}
	return &Const{Type: rt, C: complex(v[0], v[1])}, ""
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
