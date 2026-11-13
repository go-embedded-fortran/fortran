package oracle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MaxLoad is the 1-minute load average above which a measurement is refused
// (the fleet's rule: never measure at load above about 8).
const MaxLoad = 8.0

// BenchOpt is the optimisation flag of the reference column of the gate
// (bib/fortran.md §4.2: "gfortran 15.2.0 -O2 natively").
const BenchOpt = "-O2"

// Load1 returns the 1-minute load average of the host.
func Load1(ctx context.Context) (float64, error) {
	switch runtime.GOOS {
	case "linux":
		b, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return 0, err
		}
		return ParseLoad(string(b))
	case "darwin":
		out, err := exec.CommandContext(ctx, "sysctl", "-n", "vm.loadavg").Output()
		if err != nil {
			return 0, err
		}
		return ParseLoad(string(out))
	}
	return 0, errors.New("oracle: load average unknown on " + runtime.GOOS)
}

// ParseLoad reads the first load figure of "0.52 0.58 0.59 1/467 12345"
// (Linux) or "{ 3.17 2.99 2.74 }" (macOS).
func ParseLoad(s string) (float64, error) {
	f := strings.Fields(strings.Trim(strings.TrimSpace(s), "{}"))
	if len(f) == 0 {
		return 0, fmt.Errorf("oracle: empty load average %q", s)
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("oracle: bad load average %q", s)
	}
	return v, nil
}

// ParseKernel reads a gate kernel's output: the checksum on the first line and
// "time <seconds>" on the second.
func ParseKernel(out []byte) (checksum string, seconds float64, err error) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return "", 0, fmt.Errorf("oracle: kernel printed %d lines, want 2", len(lines))
	}
	checksum = strings.TrimSpace(lines[0])
	rest, ok := strings.CutPrefix(strings.TrimSpace(lines[1]), "time ")
	if !ok || checksum == "" {
		return "", 0, fmt.Errorf("oracle: kernel output %q is not \"checksum\\ntime S\"", out)
	}
	seconds, err = strconv.ParseFloat(rest, 64)
	if err != nil || seconds < 0 {
		return "", 0, fmt.Errorf("oracle: bad kernel time %q", rest)
	}
	return checksum, seconds, nil
}

// Median is the median of xs (the mean of the middle two for an even count);
// it does not modify xs.
func Median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	if n := len(s); n%2 == 0 {
		return (s[n/2-1] + s[n/2]) / 2
	}
	return s[len(s)/2]
}

// KernelTime is the reference column of the gate for one kernel.
type KernelTime struct {
	Name     string
	Checksum string
	Seconds  []float64 // one per round
	Median   float64
	Spread   float64 // max - min
}

// Bench compiles every .f90 below dir with gfortran -O2 and runs each one
// rounds times, kernels interleaved within a round, and returns the times the
// kernels report for themselves (cpu_time around the kernel proper). The
// checksum must be the same in every round. This is only the reference column
// of the §4.2 gate; the contenders arrive with M1.
func (t *Toolchain) Bench(ctx context.Context, dir string, rounds int, lim Limits) ([]KernelTime, error) {
	if rounds < 1 {
		return nil, errors.New("oracle: rounds must be at least 1")
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.f90"))
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("oracle: no kernels (*.f90) in %s", dir)
	}
	slices.Sort(names)
	build, err := os.MkdirTemp("", "foracle-bench-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(build)
	exes := make([]string, len(names))
	for i, n := range names {
		src, err := os.ReadFile(n)
		if err != nil {
			return nil, err
		}
		d := filepath.Join(build, strconv.Itoa(i))
		if err := os.Mkdir(d, 0o755); err != nil {
			return nil, err
		}
		if exes[i], err = t.Compile(ctx, d, src, CompileOptions{Opt: BenchOpt, Timeout: 2 * time.Minute}); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(n), err)
		}
	}
	res := make([]KernelTime, len(names))
	for i, n := range names {
		res[i].Name = strings.TrimSuffix(filepath.Base(n), ".f90")
	}
	for range rounds {
		for i := range names {
			r, err := Run(ctx, exes[i], filepath.Dir(exes[i]), nil, nil, lim)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", res[i].Name, err)
			}
			if r.TimedOut || r.Truncated || r.Signal != "" || r.Exit != 0 {
				return nil, fmt.Errorf("%s: did not finish cleanly (exit %d, signal %q, timed out %v)", res[i].Name, r.Exit, r.Signal, r.TimedOut)
			}
			sum, secs, err := ParseKernel(bytes.TrimSpace(r.Stdout))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", res[i].Name, err)
			}
			if res[i].Checksum != "" && res[i].Checksum != sum {
				return nil, fmt.Errorf("%s: checksum changed between rounds: %s then %s", res[i].Name, res[i].Checksum, sum)
			}
			res[i].Checksum = sum
			res[i].Seconds = append(res[i].Seconds, secs)
		}
	}
	for i := range res {
		res[i].Median = Median(res[i].Seconds)
		res[i].Spread = slices.Max(res[i].Seconds) - slices.Min(res[i].Seconds)
	}
	return res, nil
}
