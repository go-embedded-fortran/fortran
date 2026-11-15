package eval

import (
	"io"
	"testing"
)

// BenchmarkDoLoop is the cost of the step budget on the tightest loop: 10^7
// iterations of a scalar update.
func BenchmarkDoLoop(b *testing.B) {
	benchSource(b, "program p\n  implicit none\n  integer :: i\n  real(8) :: s\n  s = 0d0\n  do i = 1, 10000000\n    s = s + 1d0\n  end do\n  if (s < 0d0) print *, s\nend program p\n")
}

// The three scalar-loop kernels of the gate (testdata/gate), at a size a
// benchmark can repeat: same statements, smaller extents.
const (
	benchK1 = "program k1\n  implicit none\n  integer(8) :: i, n\n  real(8) :: s\n  n = 2000000_8\n  s = 0d0\n  do i = 1, n\n    s = s + 1d0/real(i,8)**2\n  end do\n  if (s < 0d0) print *, s\nend program k1\n"

	benchK2 = `program k2
  implicit none
  integer, parameter :: n = 256, sweeps = 10
  real(8), allocatable :: u(:,:), v(:,:)
  integer :: i, j, s
  allocate (u(n,n), v(n,n))
  do j = 1, n
    do i = 1, n
      u(i,j) = real(mod(i*7 + j*13, 17), 8)/17d0
    end do
  end do
  v = u
  do s = 1, sweeps
    do j = 2, n - 1
      do i = 2, n - 1
        v(i,j) = 0.25d0*(u(i-1,j) + u(i+1,j) + u(i,j-1) + u(i,j+1))
      end do
    end do
    do j = 2, n - 1
      do i = 2, n - 1
        u(i,j) = v(i,j)
      end do
    end do
  end do
  if (sum(u) < 0d0) print *, sum(u)
end program k2
`

	benchK3L = `program k3loops
  implicit none
  integer, parameter :: n = 128
  real(8), allocatable :: a(:,:), b(:,:), c(:,:)
  integer :: i, j, k
  allocate (a(n,n), b(n,n), c(n,n))
  do j = 1, n
    do i = 1, n
      a(i,j) = real(mod(i + 2*j, 7), 8)/7d0
      b(i,j) = real(mod(3*i + j, 5), 8)/5d0
    end do
  end do
  c = 0d0
  do j = 1, n
    do k = 1, n
      do i = 1, n
        c(i,j) = c(i,j) + a(i,k)*b(k,j)
      end do
    end do
  end do
  if (sum(c) < 0d0) print *, sum(c)
end program k3loops
`
)

func benchSource(b *testing.B, src string) {
	prog, err := compile(b, src)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkK1(b *testing.B)  { benchSource(b, benchK1) }
func BenchmarkK2(b *testing.B)  { benchSource(b, benchK2) }
func BenchmarkK3L(b *testing.B) { benchSource(b, benchK3L) }
