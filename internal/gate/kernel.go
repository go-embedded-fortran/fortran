package gate

import (
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Size is the size of one kernel run. Fields a kernel does not use are zero.
type Size struct {
	N      int // n (K1, K4: elements; K2, K3: the matrix is N x N)
	Sweeps int // K2
	Reps   int // K4
}

// Kernel is one of the gate kernels of §4.2.
type Kernel struct {
	ID   string // K1, K2, K3L (loops), K3M (matmul), K4
	Stem string // file stem in the kernels directory: <stem>.f90 and c/<stem>.c
	// Loop kernels (K1, K2, K3 as a triple loop) enter the geometric mean g.
	Loop bool
	// Host is true for the array kernels, which B runs through a host import
	// (the Go library) with the copies of §4.2, and which have a direct
	// go-ndarray baseline.
	Host        bool
	Full, Smoke Size

	// fortran lists the literal texts of the Fortran source that carry the
	// size, in the order of the sizes they take.
	fortran func(Size) []sub
	// defs are the -D macros of the C source.
	defs func(Size) []string
	// factor is the a-priori error factor of the bound, see Bound.
	factor func(Size) float64
	// exact returns the exact value of the checksum for the kernel's inputs.
	exact func(Size) *big.Float
}

type sub struct{ old, new string }

// Kernels returns the gate kernels, in the order of §4.2.
func Kernels() []Kernel {
	return []Kernel{
		{ID: "K1", Stem: "k1_scalar_loop", Loop: true,
			Full: Size{N: 100_000_000}, Smoke: Size{N: 100_000},
			fortran: func(s Size) []sub {
				return []sub{{"n = 100000000_8", fmt.Sprintf("n = %d_8", s.N)}}
			},
			defs:   func(s Size) []string { return []string{fmt.Sprintf("-DN=%d", s.N)} },
			factor: func(s Size) float64 { return float64(s.N) + 4 },
			exact:  exactK1},
		{ID: "K2", Stem: "k2_jacobi", Loop: true,
			Full: Size{N: 1024, Sweeps: 100}, Smoke: Size{N: 64, Sweeps: 4},
			fortran: func(s Size) []sub {
				return []sub{{"n = 1024, sweeps = 100", fmt.Sprintf("n = %d, sweeps = %d", s.N, s.Sweeps)}}
			},
			defs: func(s Size) []string {
				return []string{fmt.Sprintf("-DN=%d", s.N), fmt.Sprintf("-DSWEEPS=%d", s.Sweeps)}
			},
			factor: func(s Size) float64 { return 3*float64(s.Sweeps) + float64(s.N)*float64(s.N) + 4 },
			exact:  exactK2},
		{ID: "K3L", Stem: "k3_matmul_loops", Loop: true,
			Full: Size{N: 512}, Smoke: Size{N: 32},
			fortran: func(s Size) []sub { return []sub{{"n = 512", fmt.Sprintf("n = %d", s.N)}} },
			defs:    func(s Size) []string { return []string{fmt.Sprintf("-DN=%d", s.N)} },
			factor:  func(s Size) float64 { return float64(s.N) + float64(s.N)*float64(s.N) + 4 },
			exact:   exactK3},
		{ID: "K3M", Stem: "k3_matmul_intrinsic", Host: true,
			Full: Size{N: 512}, Smoke: Size{N: 32},
			fortran: func(s Size) []sub { return []sub{{"n = 512", fmt.Sprintf("n = %d", s.N)}} },
			defs:    func(s Size) []string { return []string{fmt.Sprintf("-DN=%d", s.N)} },
			factor:  func(s Size) float64 { return float64(s.N) + float64(s.N)*float64(s.N) + 4 },
			exact:   exactK3},
		{ID: "K4", Stem: "k4_array_expr", Host: true,
			Full: Size{N: 10_000_000, Reps: 10}, Smoke: Size{N: 100_000, Reps: 2},
			fortran: func(s Size) []sub {
				return []sub{{"n = 10000000, reps = 10", fmt.Sprintf("n = %d, reps = %d", s.N, s.Reps)}}
			},
			defs: func(s Size) []string {
				return []string{fmt.Sprintf("-DN=%d", s.N), fmt.Sprintf("-DREPS=%d", s.Reps)}
			},
			factor: func(s Size) float64 { return float64(s.N) + 4 },
			exact:  exactK4},
	}
}

// Fortran reads the kernel's source from dir and sets its size. Every size
// text must be found exactly once: a smoke run that silently ran the full size
// (or the reverse) would be worse than a failure.
func (k Kernel) Fortran(dir string, s Size) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(dir, k.Stem+".f90"))
	if err != nil {
		return nil, err
	}
	src := string(b)
	for _, r := range k.fortran(s) {
		if n := strings.Count(src, r.old); n != 1 {
			return nil, fmt.Errorf("gate: %s.f90: the size text %q occurs %d times, want 1", k.Stem, r.old, n)
		}
		src = strings.Replace(src, r.old, r.new, 1)
	}
	return []byte(src), nil
}

// CSource reads the kernel's C transliteration.
func (k Kernel) CSource(dir string) (string, error) {
	return filepath.Join(dir, "c", k.Stem+".c"), nil
}

// Defs returns the -D macros that set the C kernel's size.
func (k Kernel) Defs(s Size) []string { return k.defs(s) }

const u = 1.0 / (1 << 53) // the unit roundoff of float64

// Bound is the error bound a contender's checksum must meet at size s: an
// a-priori bound of recursive floating-point evaluation of the kernel,
//
//	|checksum - exact| <= 1.01 * factor * 2^-53 * exact
//
// where factor is the number of rounding steps on the longest chain the kernel
// can accumulate (K1: n terms and their own roundings; K2: 3 per sweep for the
// sum of four neighbours, plus the n^2 additions of sum(u); K3: n products
// accumulated into a c(i,j), plus the n^2 additions of sum(c); K4: the n
// additions of sum(a)). Every kernel's values are nonnegative, so the bound is
// relative to the exact checksum, and it holds for any summation order, FMA
// contraction, or vectorisation: the contenders are not required to reproduce
// gfortran's bits (the fleet's contract is a stated bound against an
// extended-precision oracle, not equality with another float64 code).
func (k Kernel) Bound(s Size) (exact float64, bound float64) {
	e := k.exact(s)
	ev, _ := e.Float64()
	return ev, 1.01 * k.factor(s) * u * ev
}

// Check returns an error unless the printed checksum is within the bound of
// the exact value (math/big, see ref.go).
func (k Kernel) Check(checksum string, exact, bound float64) error {
	v, err := strconv.ParseFloat(strings.TrimSpace(checksum), 64)
	if err != nil {
		return fmt.Errorf("checksum %q: %w", checksum, err)
	}
	if math.IsNaN(v) || math.Abs(v-exact) > bound {
		return fmt.Errorf("%s checksum %s differs from the exact %.17g by %.3g, over the bound %.3g",
			k.ID, strings.TrimSpace(checksum), exact, math.Abs(v-exact), bound)
	}
	return nil
}
