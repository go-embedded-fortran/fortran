package sema

import (
	"math"
	"strings"
)

// RankUnknown is the rank of a transformational result the analyser does not
// compute (a reshape, a pack): no conformance check is made on it.
const RankUnknown = -2

type result uint8

const (
	rSame result = iota // type of the first argument
	rInt
	rReal
	rLogical
	rChar
	rPromote // numeric promotion of the first two arguments
	rUnknown
)

type rankPol uint8

const (
	pElem    rankPol = iota // elemental: the largest rank of the arguments
	pScalar                 // inquiry: a scalar
	pUnknown                // transformational
)

type intrinsic struct {
	res     result
	pol     rankPol
	numeric bool   // the first argument must be numeric
	kindArg int    // position of an optional kind argument, -1 none
	min     int    // fewest arguments
	desc    string // used only to keep the table readable
}

// intrinsicFuncs is the table of intrinsic functions of F2018 16.9 the
// analyser types. A name that is a function of the standard but absent here is
// not an error: it takes the Unknown type (see intrinsicNames).
var intrinsicFuncs = map[string]intrinsic{}

// intrinsicSubs are the intrinsic subroutines of F2018 16.9 (and gfortran's
// extensions that the corpora use); they are accepted in a CALL.
var intrinsicSubs = map[string]bool{}

func init() {
	def := func(r result, p rankPol, numeric bool, kindArg, min int, names string) {
		for _, n := range strings.Fields(names) {
			intrinsicFuncs[n] = intrinsic{res: r, pol: p, numeric: numeric, kindArg: kindArg, min: min}
		}
	}
	def(rSame, pElem, true, -1, 1, "sqrt exp log sin cos tan asin acos atan sinh cosh tanh asinh acosh atanh aint anint abs conjg aimag_ erf erfc erfc_scaled gamma log_gamma bessel_j0 bessel_j1 bessel_y0 bessel_y1 sind cosd tand")
	def(rSame, pElem, true, -1, 2, "sign dim mod modulo max min atan2 hypot max0 min0 amax1 amin1 dmax1 dmin1 bessel_jn bessel_yn")
	def(rSame, pElem, false, -1, 1, "iand ior ieor not ishft ishftc ibits ibset ibclr shifta shiftl shiftr fraction rrspacing spacing set_exponent scale nearest dshiftl dshiftr merge_bits")
	def(rSame, pUnknown, false, -1, 1, "merge cshift eoshift transpose reshape pack spread unpack sum product maxval minval norm2 parity iall iany iparity findloc_ ")
	def(rInt, pElem, true, 1, 1, "int nint floor ceiling idint ifix")
	def(rInt, pElem, false, 1, 1, "ichar iachar")
	def(rInt, pScalar, false, -1, 1, "minexponent maxexponent len len_trim digits radix kind precision range exponent bit_size popcnt poppar leadz trailz storage_size command_argument_count selected_int_kind selected_real_kind selected_char_kind this_image num_images")
	def(rInt, pElem, false, -1, 2, "index scan verify")
	def(rInt, pUnknown, false, -1, 1, "size lbound ubound shape count maxloc minloc findloc")
	def(rReal, pElem, true, 1, 1, "real float sngl")
	def(rReal, pElem, true, -1, 1, "dble dfloat dprod")
	def(rSame, pScalar, false, -1, 1, "epsilon huge tiny")
	def(rPromote, pUnknown, false, -1, 2, "dot_product matmul")
	def(rLogical, pUnknown, false, -1, 1, "all any")
	def(rLogical, pScalar, false, -1, 1, "associated allocated present is_contiguous extends_type_of same_type_as")
	def(rLogical, pElem, false, -1, 1, "isnan is_iostat_end is_iostat_eor btest lge lgt lle llt logical out_of_range ieee_is_nan")
	def(rChar, pElem, false, -1, 1, "char achar adjustl adjustr trim repeat")
	def(rChar, pScalar, false, -1, 0, "new_line")
	for _, n := range strings.Fields("system_clock random_number random_seed date_and_time cpu_time get_command_argument get_command get_environment_variable move_alloc mvbits execute_command_line flush exit abort sleep fdate getenv getarg gettime") {
		intrinsicSubs[n] = true
	}
	for _, n := range strings.Fields("loc c_loc c_funloc c_associated c_f_pointer c_f_procpointer time clock etime dtime second rand irand getpid getcwd access cmplx dcmplx imag aimag ieee_value ieee_class ieee_support_datatype trim_ null cosh_ lbound_ iargc irand secnds ran srand chdir hostnm signal unlink fnum ttynam kill alarm iachar_ lnblnk malloc free ishftc_ compiler_version compiler_options acosd asind atand atan2d cosd sind tand coshape cobound image_index team_number failed_images stopped_images get_team coarray_ event_query atomic_ ") {
		if _, ok := intrinsicFuncs[n]; !ok {
			intrinsicFuncs[n] = intrinsic{res: rUnknown, pol: pUnknown, kindArg: -1}
		}
	}
	// "cmplx" and "dcmplx" have a type: complex of default or given kind.
	intrinsicFuncs["cmplx"] = intrinsic{res: rUnknown, pol: pElem, kindArg: 2}
	intrinsicFuncs["dcmplx"] = intrinsic{res: rUnknown, pol: pElem, kindArg: -1}
	intrinsicFuncs["imag"] = intrinsic{res: rUnknown, pol: pElem, kindArg: -1}
	intrinsicFuncs["aimag"] = intrinsic{res: rUnknown, pol: pElem, kindArg: -1}
	delete(intrinsicFuncs, "aimag_")
	delete(intrinsicFuncs, "findloc_")
}

// IsIntrinsicFunc reports whether name is an intrinsic function known to the analyser.
func IsIntrinsicFunc(name string) bool { _, ok := intrinsicFuncs[name]; return ok }

// IsIntrinsicSub reports whether name is an intrinsic subroutine known to the analyser.
func IsIntrinsicSub(name string) bool { return intrinsicSubs[name] }

// argTV is an actual argument of an intrinsic call.
type argTV struct {
	kw string
	tv TV
}

// intrinsicType gives the result of an intrinsic function reference; msg is a
// diagnostic when an argument violates the function's rules (F2018 16.9).
func (c *checker) intrinsicType(name string, in intrinsic, args []argTV) (TV, string) {
	if len(args) < in.min {
		return TV{Type: Type{Basic: Unknown}, Rank: RankUnknown, Call: true}, "not enough actual arguments for intrinsic '" + name + "'"
	}
	rank := 0
	for _, a := range args {
		if a.tv.Rank > rank {
			rank = a.tv.Rank
		}
		if a.tv.Rank == RankUnknown {
			rank = RankUnknown
			break
		}
	}
	switch in.pol {
	case pScalar:
		rank = 0
	case pUnknown:
		rank = RankUnknown
	}
	a0 := Type{Basic: Unknown}
	if len(args) > 0 {
		a0 = args[0].tv.Type
	}
	if in.numeric && !a0.Lenient() && !a0.Numeric() {
		return TV{Type: Type{Basic: Unknown}, Rank: rank, Call: true}, "argument of intrinsic '" + name + "' must be numeric, it is " + a0.String()
	}
	kindOf := func(def int) int {
		if in.kindArg < 0 {
			return def
		}
		for i, a := range args {
			if (a.kw == "kind") || (a.kw == "" && i == in.kindArg) {
				if a.tv.Const != nil && a.tv.Const.Type.Basic == Integer {
					return int(a.tv.Const.I)
				}
				return def
			}
		}
		return def
	}
	tv := TV{Rank: rank, Call: true}
	switch in.res {
	case rSame:
		tv.Type = a0
		switch name {
		case "abs":
			if a0.Basic == Complex {
				tv.Type = Type{Basic: Real, Kind: a0.Kind}
			}
		case "max", "min", "sign", "dim", "mod", "modulo", "atan2", "hypot":
			if len(args) > 1 && a0.Numeric() && args[1].tv.Type.Numeric() && args[1].tv.Type.Basic == a0.Basic {
				tv.Type.Kind = max(a0.Kind, args[1].tv.Type.Kind)
			}
		case "merge", "cshift", "eoshift", "transpose", "reshape", "pack", "spread", "unpack":
			// type of the first argument
		}
	case rInt:
		tv.Type = Type{Basic: Integer, Kind: kindOf(DefaultInt)}
	case rReal:
		switch name {
		case "dble", "dfloat", "dprod":
			tv.Type = Type{Basic: Real, Kind: 8}
		case "real":
			k := kindOf(DefaultReal)
			if in.kindArg >= 0 && k == DefaultReal && a0.Basic == Complex && !hasKind(args, in.kindArg) {
				k = a0.Kind
			}
			tv.Type = Type{Basic: Real, Kind: k}
		default:
			tv.Type = Type{Basic: Real, Kind: DefaultReal}
		}
	case rLogical:
		tv.Type = Type{Basic: Logical, Kind: kindOf(DefaultInt)}
	case rChar:
		tv.Type = Type{Basic: Character, Kind: DefaultChar, Len: -1}
		if name == "char" || name == "achar" || name == "new_line" {
			tv.Type.Len = 1
		}
		if name == "adjustl" || name == "adjustr" || name == "trim" && false {
			tv.Type.Len = a0.Len
		}
	case rPromote:
		tv.Type = a0
		if len(args) > 1 && a0.Numeric() && args[1].tv.Type.Numeric() {
			tv.Type = Promote(a0, args[1].tv.Type)
		}
		if name == "matmul" || name == "dot_product" {
			tv.Rank = RankUnknown
			if name == "dot_product" {
				tv.Rank = 0
			}
		}
	default:
		tv.Type = Type{Basic: Unknown}
		switch name {
		case "cmplx", "dcmplx":
			k := DefaultReal
			if name == "dcmplx" {
				k = 8
			} else {
				k = kindOf(DefaultReal)
				if !hasKind(args, 2) {
					for _, a := range args {
						if a.tv.Type.Basic == Real || a.tv.Type.Basic == Complex {
							k = max(k, a.tv.Type.Kind)
						}
					}
				}
			}
			tv.Type = Type{Basic: Complex, Kind: k}
		case "imag", "aimag":
			if a0.Basic == Complex {
				tv.Type = Type{Basic: Real, Kind: a0.Kind}
			}
		}
	}
	return tv, ""
}

func hasKind(args []argTV, pos int) bool {
	for i, a := range args {
		if a.kw == "kind" || (a.kw == "" && i == pos) {
			return true
		}
	}
	return false
}

// foldIntrinsic folds the intrinsic functions that may appear in a constant
// expression and that the analyser needs for kind parameters and array
// bounds (F2018 10.1.12: elemental intrinsics with constant arguments and the
// inquiry functions kind, len, huge, epsilon, selected_*_kind).
func (c *checker) foldIntrinsic(name string, args []argTV, rt Type) *Const {
	cs := make([]*Const, len(args))
	for i, a := range args {
		if a.tv.Const == nil && !(name == "kind" || name == "huge" || name == "epsilon" || name == "len" || name == "tiny" || name == "bit_size" || name == "digits" || name == "precision" || name == "range") {
			return nil
		}
		cs[i] = a.tv.Const
	}
	if len(args) == 0 {
		return nil
	}
	a0 := args[0].tv.Type
	switch name {
	case "kind":
		if a0.Basic == Unknown || a0.Basic == Invalid || a0.Basic == Derived {
			return nil
		}
		return &Const{Type: rt, I: int64(a0.Kind)}
	case "len":
		if a0.Basic == Character && a0.Len >= 0 {
			return &Const{Type: rt, I: a0.Len}
		}
	case "huge":
		switch a0.Basic {
		case Integer:
			if _, hi, ok := intRange(a0.Kind); ok {
				return &Const{Type: a0, I: hi}
			}
		case Real:
			if a0.Kind == 4 {
				return &Const{Type: a0, F: math.MaxFloat32}
			} else if a0.Kind == 8 {
				return &Const{Type: a0, F: math.MaxFloat64}
			}
		}
	case "epsilon":
		if a0.Basic == Real {
			if a0.Kind == 4 {
				return &Const{Type: a0, F: 1.1920928955078125e-07}
			} else if a0.Kind == 8 {
				return &Const{Type: a0, F: 2.220446049250313e-16}
			}
		}
	case "bit_size":
		if a0.Basic == Integer {
			return &Const{Type: rt, I: int64(8 * a0.Kind)}
		}
	case "selected_int_kind":
		if cs[0] != nil && cs[0].Type.Basic == Integer {
			r := cs[0].I
			k := int64(-1)
			switch {
			case r <= 2:
				k = 1
			case r <= 4:
				k = 2
			case r <= 9:
				k = 4
			case r <= 18:
				k = 8
			case r <= 38:
				k = 16
			}
			return &Const{Type: rt, I: k}
		}
	case "selected_real_kind":
		p := int64(0)
		r := int64(0)
		for i, a := range args {
			if cs[i] == nil || cs[i].Type.Basic != Integer {
				return nil
			}
			if a.kw == "r" || (a.kw == "" && i == 1) {
				r = cs[i].I
			} else if a.kw == "" || a.kw == "p" {
				p = cs[i].I
			}
		}
		k := int64(-1)
		switch {
		case p <= 6 && r <= 37:
			k = 4
		case p <= 15 && r <= 307:
			k = 8
		case p <= 18 && r <= 4931:
			k = 10
		case p <= 33 && r <= 4931:
			k = 16
		}
		if p > 33 {
			k = -1
		}
		return &Const{Type: rt, I: k}
	case "int", "nint", "idint", "ifix", "floor", "ceiling":
		x := cs[0]
		if x == nil || !x.Type.Numeric() {
			return nil
		}
		var v float64
		switch name {
		case "int", "idint", "ifix":
			if x.Type.Basic == Integer {
				return convertOK(x, rt)
			}
			v = math.Trunc(x.float())
		case "nint":
			v = math.Round(x.float())
		case "floor":
			v = math.Floor(x.float())
		case "ceiling":
			v = math.Ceil(x.float())
		}
		if math.IsNaN(v) || math.Abs(v) >= 9.2e18 {
			return nil
		}
		return convertOK(&Const{Type: Type{Basic: Integer, Kind: 8}, I: int64(v)}, rt)
	case "real", "dble", "float", "sngl", "dfloat":
		if cs[0] != nil && cs[0].Type.Numeric() && rt.Basic == Real {
			return convertOK(cs[0], rt)
		}
	case "abs":
		x := cs[0]
		switch x.Type.Basic {
		case Integer:
			if x.I != math.MinInt64 {
				return convertOK(&Const{Type: x.Type, I: abs64(x.I)}, rt)
			}
		case Real:
			return &Const{Type: rt, F: math.Abs(x.F)}
		}
	case "mod":
		if len(cs) == 2 && cs[1] != nil && cs[0].Type.Basic == Integer && cs[1].Type.Basic == Integer && cs[1].I != 0 {
			return &Const{Type: rt, I: cs[0].I % cs[1].I}
		}
	case "max", "min":
		if len(cs) >= 2 {
			allInt := true
			for _, x := range cs {
				if x == nil || x.Type.Basic != Integer {
					allInt = false
				}
			}
			if allInt {
				v := cs[0].I
				for _, x := range cs[1:] {
					if (name == "max" && x.I > v) || (name == "min" && x.I < v) {
						v = x.I
					}
				}
				return &Const{Type: rt, I: v}
			}
		}
	case "sqrt":
		if cs[0].Type.Basic == Real && cs[0].F >= 0 {
			return &Const{Type: rt, F: roundKind(math.Sqrt(cs[0].F), rt.Kind)}
		}
	}
	return nil
}

func convertOK(x *Const, t Type) *Const {
	r, msg := convert(x, t)
	if msg != "" {
		return nil
	}
	return r
}
