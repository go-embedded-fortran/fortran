package eval

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// runBoth runs src with the strip kernels off and on and returns the output and
// error text of each, and the number of kernel runs of the second.
func runBoth(t *testing.T, src string) (plain, vec string, hits int64) {
	t.Helper()
	one := func(off bool) string {
		disableVec = off
		defer func() { disableVec = false }()
		p, err := compile(t, src)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		var out bytes.Buffer
		before := atomic.LoadInt64(&vecHits)
		_, err = p.Run(&out)
		hits = atomic.LoadInt64(&vecHits) - before
		if err != nil {
			out.WriteString("ERROR: " + err.Error())
		}
		return out.String()
	}
	plain = one(true)
	vec = one(false)
	return plain, vec, hits
}

// Every loop shape the kernels accept gives the same output, bit for bit, as the
// ordinary loop, and the ones that must not be vectorised are not.
func TestStripKernelsAgree(t *testing.T) {
	const decl = `program p
  implicit none
  integer, parameter :: n = 300
  integer :: i, j, k, m
  real(8) :: a(n), b(n), c(n,n), d(n,n), x, y
  real(8), allocatable :: e(:,:)
  m = 7
  x = 1.5d0
  y = -0.25d0
  do i = 1, n
    a(i) = sin(real(i, 8)) + 2d0
    b(i) = cos(real(i, 8)) * 3d0
  end do
  do j = 1, n
    do i = 1, n
      c(i,j) = real(mod(i*7 + j*13, 17), 8) / 17d0 + 0.5d0
      d(i,j) = real(mod(i*3 + j, 5), 8) / 5d0
    end do
  end do
`
	const tail = `  print '(es24.16)', sum(a), sum(b), sum(c), sum(d)
  print *, i, j, k
end program p
`
	cases := []struct {
		name, body string
		kernel     bool
	}{
		{"saxpy", "do i = 1, n\n a(i) = a(i) + x*b(i)\n end do\n", true},
		{"neg and div", "do i = 1, n\n a(i) = -b(i)/(a(i) + 4d0) - y\n end do\n", true},
		{"copy", "do i = 1, n\n b(i) = a(i)\n end do\n", true},
		{"fill", "do i = 1, n\n a(i) = x*y\n end do\n", true},
		{"stencil", "do j = 2, n-1\n do i = 2, n-1\n d(i,j) = 0.25d0*(c(i-1,j) + c(i+1,j) + c(i,j-1) + c(i,j+1))\n end do\n end do\n", true},
		{"matmul loops", "d = 0d0\n do j = 1, 40\n do k = 1, 40\n do i = 1, n\n d(i,j) = d(i,j) + c(i,k)*c(k,j)\n end do\n end do\n end do\n", true},
		{"second subscript runs", "do j = 1, n\n d(5,j) = c(7,j) - c(j,3)\n end do\n", true},
		{"invariant element m", "do i = 1, n-8\n a(i+7) = b(i) + b(m) + c(m,m)\n end do\n", true},
		{"variable offset", "do i = 1, n-8\n a(i+m) = b(i)\n end do\n", false},
		{"short trip", "do i = 1, 5\n a(i) = b(i) * 2d0\n end do\n", false},
		{"empty", "do i = 5, 1\n a(i) = 0d0\n end do\n", false},
		{"recurrence", "do i = 2, n\n a(i) = a(i-1) + b(i)\n end do\n", false},
		{"reverse dependence", "do i = 1, n-1\n a(i) = a(i+1) * 0.5d0\n end do\n", false},
		{"same array other column", "do i = 1, n\n c(i,2) = c(i,1) + 1d0\n end do\n", false},
		{"step", "do i = 1, n, 2\n a(i) = b(i)\n end do\n", false},
		{"cycle", "do i = 1, n\n if (a(i) > 100d0) cycle\n a(i) = b(i)\n end do\n", false},
		{"power", "do i = 1, n\n a(i) = b(i)**2\n end do\n", false},
		{"intrinsic", "do i = 1, n\n a(i) = sqrt(abs(b(i)))\n end do\n", false},
		{"integer operand", "do i = 1, n\n a(i) = b(i) + i\n end do\n", false},
		{"single precision", "do i = 1, n\n a(i) = b(i) + 1.0\n end do\n", false},
		{"low bound error", "do i = 1, n\n a(i) = b(i-1)\n end do\n", false},
		{"high bound error", "do i = 1, n\n a(i+1) = b(i)\n end do\n", false},
		{"second dim error", "do j = 1, n\n d(1,j+1) = c(1,j)\n end do\n", false},
		{"unallocated", "do i = 1, n\n e(i,1) = a(i)\n end do\n", false},
		{"allocated", "allocate (e(n, 3))\n e = 0d0\n do i = 1, n\n e(i,2) = a(i) + e(i,2)\n end do\n print *, sum(e)\n", true},
		{"division by zero", "do i = 1, n\n a(i) = b(i) / (a(i) - a(i))\n end do\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plain, vec, hits := runBoth(t, decl+c.body+tail)
			if plain != vec {
				t.Fatalf("outputs differ\nordinary: %q\nkernel:   %q", plain, vec)
			}
			if c.kernel && hits == 0 {
				t.Errorf("the loop was not run as a kernel")
			}
			if !c.kernel && hits != 0 {
				t.Errorf("the loop ran as a kernel %d times, it must not", hits)
			}
		})
	}
}

// spend charges n steps as n ticks do: a total that is spent, or a context that
// is done, ends the run.
func TestBudgetSpend(t *testing.T) {
	b := newBudget(nil, 3*chunk)
	b.spend(chunk) // within the first chunk
	b.spend(chunk) // crosses one boundary
	func() {
		defer func() {
			e, _ := recover().(*Error)
			if e == nil || !strings.Contains(e.Msg, "step limit") {
				t.Fatalf("recovered %v, want the step limit", e)
			}
		}()
		b.spend(2 * chunk)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	b = newBudget(ctx, 0)
	cancel()
	func() {
		defer func() {
			e, _ := recover().(*Error)
			if e == nil || !errors.Is(e, context.Canceled) {
				t.Fatalf("recovered %v, want the context error", e)
			}
		}()
		b.spend(10 * chunk)
	}()
}
