package gate

import (
	"fmt"
	"time"
)

// Direct is the baseline of the array kernels: go-ndarray called straight from
// Go, with no evaluator in between and no copy. It prepares the inputs and
// returns a function that runs the timed part.
//
// K3-matmul: a column-major n x n buffer read row-major is the transpose, so
// c = a b is the row-major product b' a'. K4: Mul then AddInto the result.
func Direct(k Kernel, s Size) (func() (checksum string, seconds float64, err error), error) {
	switch k.ID {
	case "K3M":
		n := s.N
		A, da, err := newBuf(n, n)
		if err != nil {
			return nil, err
		}
		B, db, err := newBuf(n, n)
		if err != nil {
			return nil, err
		}
		for j := 1; j <= n; j++ {
			for i := 1; i <= n; i++ {
				da[(i-1)+(j-1)*n] = float64((i+2*j)%7) / 7
				db[(i-1)+(j-1)*n] = float64((3*i+j)%5) / 5
			}
		}
		return func() (string, float64, error) {
			t0 := time.Now()
			r, err := B.MatMul(A)
			dt := time.Since(t0).Seconds()
			if err != nil {
				return "", 0, err
			}
			return fmt.Sprintf("%.16E", r.Sum()), dt, nil
		}, nil
	case "K4":
		n := s.N
		a, _, err := newBuf(n)
		if err != nil {
			return nil, err
		}
		b, db, err := newBuf(n)
		if err != nil {
			return nil, err
		}
		c, dc, err := newBuf(n)
		if err != nil {
			return nil, err
		}
		d, dd, err := newBuf(n)
		if err != nil {
			return nil, err
		}
		for i := 1; i <= n; i++ {
			db[i-1] = float64(i%13) / 13
			dc[i-1] = float64(i%11) / 11
			dd[i-1] = float64(i%7) / 7
		}
		return func() (string, float64, error) {
			t0 := time.Now()
			for r := 1; r <= s.Reps; r++ {
				db[0] = float64(r)
				t, err := b.Mul(c)
				if err != nil {
					return "", 0, err
				}
				if err := t.AddInto(a, d); err != nil {
					return "", 0, err
				}
			}
			dt := time.Since(t0).Seconds()
			return fmt.Sprintf("%.16E", a.Sum()), dt, nil
		}, nil
	}
	return nil, fmt.Errorf("gate: no direct go-ndarray baseline for %s", k.ID)
}
