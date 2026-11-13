package gate

import (
	"math/big"
)

// The exact values of the checksums, by math/big (§4.2: "each result's
// checksum within a stated error bound against a math/big evaluation of the
// kernel"). The kernels' inputs are float64 values (real(mod(..),8)/17d0 and
// the like, one correctly rounded division each), the same in every contender;
// what is evaluated exactly is everything after the inputs. This oracle shares
// the inputs with the contenders on purpose: the inputs are defined as float64
// by the Fortran source.

const refPrec = 128 // bits; the arithmetic below is exact or within 2^-120 per operation

func newF() *big.Float { return new(big.Float).SetPrec(refPrec) }

// exactK1 is sum over i = 1..n of 1/i^2.
func exactK1(s Size) *big.Float {
	one, sum, x, t := newF().SetInt64(1), newF(), newF(), newF()
	for i := int64(1); i <= int64(s.N); i++ {
		x.SetInt64(i * i) // exact: i*i < 2^63 for i <= 1e9
		t.Quo(one, x)
		sum.Add(sum, t)
	}
	return sum
}

// exactK2 is sum(u) after s.Sweeps Jacobi sweeps of u(i,j) = real(mod(i*7+j*13,17),8)/17d0
// on an N x N grid with fixed boundary. The two arrays are swapped instead of
// copied: the boundary of both holds the initial values, which are never
// written.
func exactK2(s Size) *big.Float {
	n := s.N
	cur, nxt := make([]big.Float, n*n), make([]big.Float, n*n)
	for j := 1; j <= n; j++ {
		for i := 1; i <= n; i++ {
			v := float64((i*7+j*13)%17) / 17
			k := (i - 1) + (j-1)*n
			cur[k].SetPrec(refPrec).SetFloat64(v)
			nxt[k].SetPrec(refPrec).SetFloat64(v)
		}
	}
	t := newF()
	for range s.Sweeps {
		for j := 1; j < n-1; j++ {
			for i := 1; i < n-1; i++ {
				k := i + j*n
				t.Add(&cur[k-1], &cur[k+1])
				t.Add(t, &cur[k-n])
				t.Add(t, &cur[k+n])
				nxt[k].SetMantExp(t, -2)
			}
		}
		cur, nxt = nxt, cur
	}
	sum := newF()
	for k := range cur {
		sum.Add(sum, &cur[k])
	}
	return sum
}

// exactK3 is sum(c) for c = a b, a(i,j) = real(mod(i+2j,7),8)/7d0 and
// b(i,j) = real(mod(3i+j,5),8)/5d0: sum_ij c(i,j) = sum_k (sum_i a(i,k)) (sum_j b(k,j)),
// in exact rational arithmetic.
func exactK3(s Size) *big.Float {
	n := s.N
	total := new(big.Rat)
	for k := 1; k <= n; k++ {
		ca, rb, t := new(big.Rat), new(big.Rat), new(big.Rat)
		for i := 1; i <= n; i++ {
			ca.Add(ca, t.SetFloat64(float64((i+2*k)%7)/7))
		}
		for j := 1; j <= n; j++ {
			rb.Add(rb, t.SetFloat64(float64((3*k+j)%5)/5))
		}
		total.Add(total, ca.Mul(ca, rb))
	}
	return new(big.Float).SetPrec(2 * refPrec).SetRat(total)
}

// exactK4 is sum(a) for a = b*c + d, b(i) = real(mod(i,13),8)/13d0,
// c(i) = real(mod(i,11),8)/11d0, d(i) = real(mod(i,7),8)/7d0, with b(1) set to
// the last repetition number (the earlier repetitions are overwritten).
func exactK4(s Size) *big.Float {
	sum, p, x, y := newF(), newF(), newF(), newF()
	for i := 1; i <= s.N; i++ {
		b := float64(i%13) / 13
		if i == 1 {
			b = float64(s.Reps)
		}
		p.SetFloat64(b)
		x.SetFloat64(float64(i%11) / 11)
		p.Mul(p, x) // 53+53 bits: exact
		y.SetFloat64(float64(i%7) / 7)
		p.Add(p, y)
		sum.Add(sum, p)
	}
	return sum
}
