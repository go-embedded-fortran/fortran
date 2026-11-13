// Package gate is the back-end decision gate of bib/fortran.md §4.2: it runs
// the contenders (A, a pure-Go evaluator; B-near and B-upper, a C
// transliteration compiled by clang to wasm32-wasip1 at -O0 and -O2 and run on
// wazero; the reference, gfortran -O2) on the gate kernels and applies the
// decision rule. The rule is in this file and nowhere else.
package gate

import (
	"errors"
	"fmt"
	"math"
)

// The thresholds of the decision rule, as written in §4.2. They are constants
// of this package and the tests pin them; changing one is changing the spec.
const (
	// SwitchToB: B if g(rA)/g(rB0) is at least this in either home.
	SwitchToB = 5.0
	// AllowA: A needs g(rA)/g(rB0) at most this in both homes.
	AllowA = 2.0
	// ArrayWithin: and A's K4 and K3-matmul within this factor of calling
	// go-ndarray directly.
	ArrayWithin = 1.2
)

// Decision is the verdict of the rule.
type Decision int

// The three outcomes of §4.2.
const (
	// B: compile to wasm32 on go-embedded-c.
	B Decision = iota + 1
	// A: the pure-Go evaluator.
	A
	// AFirst is "between": A first, re-gate after go-embedded-c M3.
	AFirst
)

func (d Decision) String() string {
	switch d {
	case B:
		return "B"
	case A:
		return "A"
	case AFirst:
		return "A first (between; re-gate after go-embedded-c M3)"
	}
	return fmt.Sprintf("Decision(%d)", int(d))
}

// Home is the loop-kernel result of one home (desktop, playground): the
// geometric means g over K1-K3 of time / gfortran time, for A and for B-near.
type Home struct {
	Name string
	// Run is false when the home was not measured.
	Run bool
	GA  float64 // g(rA)
	GB0 float64 // g(rB0)
}

// Ratio is g(rA) / g(rB0).
func (h Home) Ratio() float64 { return h.GA / h.GB0 }

// Inputs are the inputs of the rule.
type Inputs struct {
	Desktop, Playground Home
	// K4, Matmul are A's time over the time of calling go-ndarray directly
	// from Go, for K4 and for K3-matmul.
	K4, Matmul float64
}

// Verdict is the decision and the reasons the rule gives for it.
type Verdict struct {
	Decision Decision
	Reasons  []string
}

// ErrNoInput is returned when the rule has nothing to decide on.
var ErrNoInput = errors.New("gate: no home was measured")

func valid(x float64) bool { return x > 0 && !math.IsInf(x, 0) && !math.IsNaN(x) }

// Decide applies the rule of §4.2 exactly:
//
//   - B if g(rA)/g(rB0) >= 5 in either home;
//   - A if g(rA)/g(rB0) <= 2 in both homes and A's K4 and K3-matmul are within
//     1.2x of go-ndarray called directly;
//   - otherwise A first.
//
// A home that was not measured cannot satisfy "in both homes": with only one
// home the rule cannot pick A, and says so.
func Decide(in Inputs) (Verdict, error) {
	homes := []Home{in.Desktop, in.Playground}
	var v Verdict
	measured := 0
	for _, h := range homes {
		if !h.Run {
			continue
		}
		if !valid(h.GA) || !valid(h.GB0) {
			return Verdict{}, fmt.Errorf("gate: %s: g(rA)=%v g(rB0)=%v are not positive finite times", h.Name, h.GA, h.GB0)
		}
		measured++
	}
	if measured == 0 {
		return Verdict{}, ErrNoInput
	}
	for _, h := range homes {
		if h.Run && h.Ratio() >= SwitchToB {
			v.Decision = B
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s: g(rA)/g(rB0) = %.3f >= %g", h.Name, h.Ratio(), SwitchToB))
			return v, nil
		}
	}
	bothHomes := in.Desktop.Run && in.Playground.Run
	loopOK := bothHomes
	for _, h := range homes {
		if !h.Run {
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s: not measured, so \"both homes\" is unmet and the rule cannot pick A", h.Name))
			continue
		}
		if h.Ratio() > AllowA {
			loopOK = false
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s: g(rA)/g(rB0) = %.3f is above %g", h.Name, h.Ratio(), AllowA))
		} else {
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s: g(rA)/g(rB0) = %.3f <= %g", h.Name, h.Ratio(), AllowA))
		}
	}
	arrayOK := true
	for _, k := range []struct {
		name string
		r    float64
	}{{"K4", in.K4}, {"K3-matmul", in.Matmul}} {
		switch {
		case !valid(k.r):
			arrayOK = false
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s: A / direct go-ndarray not measured", k.name))
		case k.r > ArrayWithin:
			arrayOK = false
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s: A / direct go-ndarray = %.3f is above %g", k.name, k.r, ArrayWithin))
		default:
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s: A / direct go-ndarray = %.3f <= %g", k.name, k.r, ArrayWithin))
		}
	}
	if loopOK && arrayOK {
		v.Decision = A
		return v, nil
	}
	v.Decision = AFirst
	return v, nil
}
