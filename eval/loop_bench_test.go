package eval

import (
	"io"
	"testing"
)

// BenchmarkDoLoop is the cost of the step budget on the tightest loop: 10^7
// iterations of a scalar update.
func BenchmarkDoLoop(b *testing.B) {
	prog, err := compile(b, "program p\n  implicit none\n  integer :: i\n  real(8) :: s\n  s = 0d0\n  do i = 1, 10000000\n    s = s + 1d0\n  end do\n  if (s < 0d0) print *, s\nend program p\n")
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}
