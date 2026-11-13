package gate

import (
	"context"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/oracle/oracletest"
)

const kernelDir = "../../testdata/gate"

func kernel(t *testing.T, id string) Kernel {
	t.Helper()
	for _, k := range Kernels() {
		if k.ID == id {
			return k
		}
	}
	t.Fatalf("no kernel %s", id)
	return Kernel{}
}

// The size texts of the Fortran sources are found exactly once, and the full
// size rewrites a source to itself: the committed file IS the full size.
func TestFortranSizeTexts(t *testing.T) {
	for _, k := range Kernels() {
		b, err := os.ReadFile(filepath.Join(kernelDir, k.Stem+".f90"))
		if err != nil {
			t.Fatal(err)
		}
		full, err := k.Fortran(kernelDir, k.Full)
		if err != nil || string(full) != string(b) {
			t.Errorf("%s: full size does not reproduce the committed source (%v)", k.ID, err)
		}
		smoke, err := k.Fortran(kernelDir, k.Smoke)
		if err != nil || string(smoke) == string(b) {
			t.Errorf("%s: smoke size did not change the source (%v)", k.ID, err)
		}
		if _, err := os.Stat(filepath.Join(kernelDir, "c", k.Stem+".c")); err != nil {
			t.Errorf("%s: no C transliteration: %v", k.ID, err)
		}
	}
	if _, err := Kernels()[0].Fortran(t.TempDir(), Size{N: 1}); err == nil {
		t.Error("a missing source was accepted")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "k1_scalar_loop.f90"), []byte("program p\nend program p\n"), 0o644)
	if _, err := Kernels()[0].Fortran(dir, Size{N: 1}); err == nil || !strings.Contains(err.Error(), "0 times") {
		t.Errorf("a source without the size text: %v", err)
	}
}

func f(x *big.Float) float64 { v, _ := x.Float64(); return v }

// The exact checksums against a direct, independent float64 evaluation of each
// kernel at a small size (agreement far inside the bound of the kernel) and,
// for K1, against exact rational arithmetic.
func TestExactAgainstFloat64(t *testing.T) {
	// K1 n=10: sum 1/i^2 as a rational.
	r := new(big.Rat)
	for i := int64(1); i <= 10; i++ {
		r.Add(r, big.NewRat(1, i*i))
	}
	want, _ := r.Float64()
	if got := f(exactK1(Size{N: 10})); got != want {
		t.Errorf("K1: %v, want %v", got, want)
	}

	// K2: float64 Jacobi.
	n, sweeps := 12, 5
	u, v := make([]float64, n*n), make([]float64, n*n)
	for j := 1; j <= n; j++ {
		for i := 1; i <= n; i++ {
			u[(i-1)+(j-1)*n] = float64((i*7+j*13)%17) / 17
		}
	}
	copy(v, u)
	for range sweeps {
		for j := 1; j < n-1; j++ {
			for i := 1; i < n-1; i++ {
				k := i + j*n
				v[k] = 0.25 * (u[k-1] + u[k+1] + u[k-n] + u[k+n])
			}
		}
		for j := 1; j < n-1; j++ {
			for i := 1; i < n-1; i++ {
				u[i+j*n] = v[i+j*n]
			}
		}
	}
	var s2 float64
	for _, x := range u {
		s2 += x
	}
	if got := f(exactK2(Size{N: n, Sweeps: sweeps})); math.Abs(got-s2) > 1e-12*s2 {
		t.Errorf("K2: %v, float64 %v", got, s2)
	}

	// K3: brute-force triple loop.
	m := 9
	var s3 float64
	for j := 1; j <= m; j++ {
		for k := 1; k <= m; k++ {
			for i := 1; i <= m; i++ {
				s3 += float64((i+2*k)%7) / 7 * (float64((3*k+j)%5) / 5)
			}
		}
	}
	if got := f(exactK3(Size{N: m})); math.Abs(got-s3) > 1e-12*s3 {
		t.Errorf("K3: %v, brute force %v", got, s3)
	}

	// K4: with b(1) = reps.
	var s4 float64
	for i := 1; i <= 50; i++ {
		b := float64(i%13) / 13
		if i == 1 {
			b = 4
		}
		s4 += b*float64(i%11)/11 + float64(i%7)/7
	}
	if got := f(exactK4(Size{N: 50, Reps: 4})); math.Abs(got-s4) > 1e-12*s4 {
		t.Errorf("K4: %v, float64 %v", got, s4)
	}
}

func TestCheck(t *testing.T) {
	k := kernel(t, "K1")
	exact, bound := k.Bound(Size{N: 1000})
	if bound <= 0 || bound > 1e-9 {
		t.Fatalf("bound %v", bound)
	}
	ok := func(v float64) string { return strings.Repeat(" ", 3) + formatE(v) }
	if err := k.Check(ok(exact), exact, bound); err != nil {
		t.Errorf("the exact value: %v", err)
	}
	if err := k.Check(ok(exact+bound/2), exact, bound); err != nil {
		t.Errorf("inside the bound: %v", err)
	}
	for name, v := range map[string]string{
		"over":      ok(exact + 2*bound),
		"under":     ok(exact - 2*bound),
		"nan":       "NaN",
		"garbage":   "x",
		"empty":     "",
		"one ulp x": ok(exact * 1.001),
	} {
		if err := k.Check(v, exact, bound); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func formatE(v float64) string {
	return strings.TrimSpace(big.NewFloat(v).Text('E', 16))
}

func TestBoundIsAStatedFunctionOfTheSize(t *testing.T) {
	k := kernel(t, "K1")
	_, b1 := k.Bound(Size{N: 1000})
	_, b2 := k.Bound(Size{N: 2000})
	if !(b2 > b1) {
		t.Errorf("bound did not grow with n: %v %v", b1, b2)
	}
}

// The gate refuses to measure at load above 8, before any toolchain is needed.
func TestRefusesAtLoad(t *testing.T) {
	for _, load := range []float64{8.01, 40} {
		_, err := Run(context.Background(), Options{Rounds: 1,
			Load: func(context.Context) (float64, error) { return load, nil }})
		if err == nil || !strings.Contains(err.Error(), "load") {
			t.Errorf("load %v: %v", load, err)
		}
	}
	// at exactly 8 the load check passes (and the next failure is about the toolchain)
	_, err := Run(context.Background(), Options{Rounds: 1,
		Load: func(context.Context) (float64, error) { return 8, nil }})
	if err == nil || strings.Contains(err.Error(), "above") {
		t.Errorf("load 8: %v", err)
	}
	_, err = Run(context.Background(), Options{Rounds: 1,
		Load: func(context.Context) (float64, error) { return 0, os.ErrInvalid }})
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("unknown load: %v", err)
	}
	if _, err := Run(context.Background(), Options{Rounds: 0}); err == nil {
		t.Error("zero rounds")
	}
}

func liveOptions(t *testing.T, dir string) Options {
	t.Helper()
	tc := oracletest.Require(t)
	home := os.Getenv(EnvCoracle)
	if home == "" {
		// Only the gate-smoke lane, which provisions B's toolchain, requires it.
		if os.Getenv("GATE_REQUIRE") != "" {
			t.Fatalf("%s is not set and GATE_REQUIRE is", EnvCoracle)
		}
		t.Skipf("%s is not set: no B toolchain", EnvCoracle)
	}
	return Options{Dir: dir, Rounds: 1, Smoke: true, Coracle: home, Gfortran: tc}
}

// Every contender runs every kernel at the smoke size and meets the bound.
func TestSmokeLive(t *testing.T) {
	o := liveOptions(t, kernelDir)
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range res.Kernels {
		want := []string{CA, CB0, CB2, CGF}
		if k.Host {
			want = append(want, CDire)
		}
		for _, c := range want {
			if res.Cells[k.ID][c] == nil {
				t.Errorf("%s: no %s", k.ID, c)
			}
		}
	}
	if res.Decided {
		t.Error("a smoke run decided")
	}
	var out strings.Builder
	res.Report(&out)
	if strings.Contains(out.String(), "VERDICT") || strings.Contains(out.String(), "median") {
		t.Errorf("a smoke report publishes times or a verdict:\n%s", out.String())
	}
	if c := res.Cells["K3M"][CA]; c.Copies != int64(res.Sizes[3].N*res.Sizes[3].N) {
		t.Errorf("K3-matmul: A copied %d elements", c.Copies)
	}
	if c := res.Cells["K3M"][CB0]; len(c.CopySeconds) != 1 {
		t.Errorf("B host kernel recorded no copy time: %+v", c)
	}
}

// A wrong C kernel is a failure, not a time: the check has teeth.
func TestSmokeRefusesAWrongKernel(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "c"), 0o755)
	for _, k := range Kernels() {
		for _, p := range []string{k.Stem + ".f90", filepath.Join("c", k.Stem+".c")} {
			b, err := os.ReadFile(filepath.Join(kernelDir, p))
			if err != nil {
				t.Fatal(err)
			}
			if p == filepath.Join("c", "k1_scalar_loop.c") {
				s := strings.Replace(string(b), "s = s + 1.0 / (x * x);", "s = s + 1.0 / (x * x * 1.0001);", 1)
				if s == string(b) {
					t.Fatal("the break did not apply")
				}
				b = []byte(s)
			}
			os.WriteFile(filepath.Join(dir, p), b, 0o644)
		}
	}
	o := liveOptions(t, dir)
	_, err := Run(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "out of bound") || !strings.Contains(err.Error(), "B-near") {
		t.Fatalf("got %v", err)
	}
}
