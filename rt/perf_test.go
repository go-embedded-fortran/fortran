package rt

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-embedded-fortran/fortran/internal/oracle"
	"github.com/go-embedded-fortran/fortran/internal/oracle/oracletest"
)

// load1 is the one-minute load average, from /proc/loadavg or `uptime`.
func load1() (float64, error) {
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			return strconv.ParseFloat(f[0], 64)
		}
	}
	out, err := exec.Command("uptime").Output()
	if err != nil {
		return 0, err
	}
	s := string(out)
	i := strings.Index(s, "load average")
	if i < 0 {
		return 0, fmt.Errorf("no load average in %q", s)
	}
	rest := s[i:]
	rest = rest[strings.Index(rest, ":")+1:]
	f := strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == ',' })
	if len(f) == 0 {
		return 0, fmt.Errorf("no load average in %q", s)
	}
	return strconv.ParseFloat(f[0], 64)
}

// maxLoad is the load above which timings are not taken (the repository's
// measurement rule): the test reports "not measured: load".
const maxLoad = 8.0

// TestPerfVsGfortran times n formatted writes of one REAL(8) with ES24.16,
// F15.6 and the list-directed form, in rt and in a Fortran program compiled with
// the pinned gfortran writing to an internal file (no terminal, no disk), in
// interleaved rounds, single core. It runs only with RT_PERF=1 and only at a
// one-minute load below 8; otherwise it logs why it did not measure.
func TestPerfVsGfortran(t *testing.T) {
	if os.Getenv("RT_PERF") != "1" {
		t.Skip("set RT_PERF=1 to measure")
	}
	if l, err := load1(); err != nil || l >= maxLoad {
		t.Skipf("not measured: load %v (limit %v) err %v", l, maxLoad, err)
	}
	tc := oracletest.Require(t)
	const n = 2_000_000
	formats := []struct{ name, f string }{{"ES24.16", "(ES24.16)"}, {"F15.6", "(F15.6)"}, {"list-directed", "*"}}
	for _, fm := range formats {
		spec := fm.f
		if spec != "*" {
			spec = fq(spec)
		}
		src := fmt.Sprintf(`program p
  implicit none
  integer, parameter :: n = %d
  integer :: i
  real(8) :: x
  character(len=64) :: buf
  integer(8) :: c0, c1, rate
  real(8) :: s
  s = 0
  call system_clock(c0, rate)
  do i = 1, n
    x = 1d0 + 1d-7*real(i, 8)*3.14159265358979d0
    write (buf, %s) x
    s = s + ichar(buf(30:30))
  end do
  call system_clock(c1)
  print '(es24.16)', real(c1-c0,8)/real(rate,8)
  print '(f0.0)', s
end program p
`, n, spec)
		var bestG, bestR float64 = math.Inf(1), math.Inf(1)
		dir := t.TempDir()
		exe, err := tc.Compile(context.Background(), dir, []byte(src), oracle.CompileOptions{Opt: "-O2"})
		if err != nil {
			t.Fatal(err)
		}
		for round := 0; round < 5; round++ {
			if l, _ := load1(); l >= maxLoad {
				t.Skipf("not measured: load %v rose above %v during the run", l, maxLoad)
			}
			res, err := oracle.Run(context.Background(), exe, dir, nil, nil, oracle.Limits{MemoryBytes: -1})
			if err != nil {
				t.Fatal(err)
			}
			g, _ := strconv.ParseFloat(strings.TrimSpace(strings.Fields(string(res.Stdout))[0]), 64)
			bestG = math.Min(bestG, g)

			var out bytes.Buffer
			u := NewInternalWrite(1, 64)
			_ = out
			items := []Val{Real8(0)}
			sp := Spec{}
			if fm.f != "*" {
				sp = Spec{Format: fm.f, HasFormat: true}
			}
			start := time.Now()
			for i := 1; i <= n; i++ {
				items[0].F = 1 + 1e-7*float64(i)*3.14159265358979
				u.irec = 0
				u.Write(sp, items)
			}
			bestR = math.Min(bestR, time.Since(start).Seconds())
		}
		t.Logf("%-14s %d writes: rt %.3fs (%.0f ns/write), gfortran -O2 %.3fs (%.0f ns/write), rt/gfortran %.2f",
			fm.name, n, bestR, bestR/n*1e9, bestG, bestG/n*1e9, bestR/bestG)
	}
}
