package eval

import (
	"context"
	"io"
	"strings"
	"testing"
)

func cellErr(t *testing.T, s *Session, src string) error {
	t.Helper()
	_, err := s.Run(context.Background(), src, io.Discard, io.Discard)
	return err
}

// An array that would take the heap past SessionOptions.MaxAlloc is refused
// before it is allocated, with an error of the cell, and the session works
// after it. The bombs are far over the bound, and over what a test host may
// give a process, so a refusal that did not come would be the host's failure.
func TestMaxAllocRefusesABomb(t *testing.T) {
	s := NewSession(SessionOptions{MaxAlloc: 256 << 20})
	for name, src := range map[string]string{
		"one big array":  "integer, allocatable :: a(:)\nallocate(a(2**28))\n",
		"two arrays":     "integer, allocatable :: a(:), b(:)\nallocate(a(2**27))\nallocate(b(2**27))\n",
		"real, rank two": "real(8), allocatable :: m(:,:)\nallocate(m(2**14, 2**14))\n",
	} {
		err := cellErr(t, s, src)
		if err == nil || !strings.Contains(err.Error(), "memory limit exceeded") {
			t.Errorf("%s: %v, want a memory limit error", name, err)
		}
	}
	if err := cellErr(t, s, "integer :: k = 6\nprint *, k * 7\n"); err != nil {
		t.Errorf("the session after the refusals: %v", err)
	}
}

// Arrays that are let go are not counted again: a loop that makes a 4 MiB
// array at each of 2000 iterations, 8 GiB in all, stays under a 128 MiB bound
// because the collector takes the old ones back.
func TestMaxAllocCountsWhatIsHeldNotWhatWasMade(t *testing.T) {
	s := NewSession(SessionOptions{MaxAlloc: 128 << 20})
	src := "integer, allocatable :: a(:)\ninteger :: i, t\nt = 0\ndo i = 1, 2000\n  allocate(a(2**19))\n  a(1) = 1\n  t = t + a(1)\nend do\nprint *, t\n"
	var out strings.Builder
	_, err := s.Run(context.Background(), src, &out, io.Discard)
	if err != nil {
		// a second allocate of an allocated array is Fortran's own error: the
		// loop must deallocate, as a program does
		t.Logf("without deallocate: %v", err)
	}
	src = "integer, allocatable :: b(:)\ninteger :: i, t\nt = 0\ndo i = 1, 2000\n  allocate(b(2**19))\n  b(1) = 1\n  t = t + b(1)\n  deallocate(b)\nend do\nprint *, t\n"
	out.Reset()
	if _, err := s.Run(context.Background(), src, &out, io.Discard); err != nil || strings.TrimSpace(out.String()) != "2000" {
		t.Errorf("%q, %v", out.String(), err)
	}
}

// Without the option nothing is checked: the default is the evaluator's own
// per-array bound.
func TestMaxAllocZeroIsNoBound(t *testing.T) {
	s := NewSession(SessionOptions{})
	if err := cellErr(t, s, "integer, allocatable :: a(:)\nallocate(a(2**20))\nprint *, size(a)\n"); err != nil {
		t.Errorf("%v", err)
	}
}
