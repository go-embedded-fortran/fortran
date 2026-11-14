package eval

import (
	"math/bits"

	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// bitIntrinsic compiles the bit manipulation functions of F2018 16.9 on
// integers of any kind (an integer(k) value is held sign-extended in an
// int64; the width of the operation is that of the argument's kind).
func (c *compiler) bitIntrinsic(n *syntax.Index, name string, args []*ex) (*ex, error) {
	for _, a := range args {
		if a.k != kInt {
			return nil, c.errf(n, "%s needs integer arguments", name)
		}
	}
	want := 1
	switch name {
	case "iand", "ior", "ieor", "ishft", "btest", "ibset", "ibclr":
		want = 2
	}
	if len(args) != want {
		return nil, c.errf(n, "%s takes %d arguments", name, want)
	}
	tv, err := c.tv(n.Args[0].Value)
	if err != nil {
		return nil, err
	}
	if tv.Type.Basic != sema.Integer {
		return nil, c.errf(n, "%s needs integer arguments", name)
	}
	w := uint(8 * tv.Type.Kind)
	mask := ^uint64(0) >> (64 - w)
	a := args[0].i
	wrap := func(v int64) int64 { return narrow(tv.Type.Kind, v) }
	if tv.Type.Kind == 8 {
		wrap = func(v int64) int64 { return v }
	}
	one := func(g func(f *frame, x int64) int64, kd int) *ex {
		return &ex{k: kInt, kd: kd, slot: -1, i: func(f *frame) int64 { return g(f, a(f)) }}
	}
	two := func(g func(f *frame, x, y int64) int64) *ex {
		b := args[1].i
		return &ex{k: kInt, kd: kdOf(tv.Type), slot: -1, i: func(f *frame) int64 { x := a(f); return g(f, x, b(f)) }}
	}
	switch name {
	case "iand":
		return two(func(_ *frame, x, y int64) int64 { return x & y }), nil
	case "ior":
		return two(func(_ *frame, x, y int64) int64 { return x | y }), nil
	case "ieor":
		return two(func(_ *frame, x, y int64) int64 { return x ^ y }), nil
	case "not":
		return one(func(_ *frame, x int64) int64 { return ^x }, kdOf(tv.Type)), nil
	case "popcnt":
		return one(func(_ *frame, x int64) int64 { return int64(bits.OnesCount64(uint64(x) & mask)) }, 0), nil
	case "poppar":
		return one(func(_ *frame, x int64) int64 { return int64(bits.OnesCount64(uint64(x)&mask) & 1) }, 0), nil
	case "leadz":
		return one(func(_ *frame, x int64) int64 { return int64(bits.LeadingZeros64(uint64(x)&mask)) - int64(64-w) }, 0), nil
	case "trailz":
		return one(func(_ *frame, x int64) int64 {
			if uint64(x)&mask == 0 {
				return int64(w)
			}
			return int64(bits.TrailingZeros64(uint64(x)))
		}, 0), nil
	case "ishft":
		return two(func(f *frame, x, s int64) int64 {
			if s > int64(w) || s < -int64(w) {
				fail(f, "ishft: |shift| %d is larger than the bit size %d", s, w)
			}
			switch {
			case s >= 0 && s < 64:
				return wrap(int64(uint64(x) << uint(s)))
			case s < 0 && -s < 64:
				return wrap(int64((uint64(x) & mask) >> uint(-s)))
			}
			return 0
		}), nil
	case "btest":
		b := args[1].i
		return &ex{k: kLog, slot: -1, b: func(f *frame) bool {
			x, p := a(f), b(f)
			if p < 0 || p >= int64(w) {
				fail(f, "btest: bit position %d is outside 0:%d", p, w-1)
			}
			return x>>uint(p)&1 != 0
		}}, nil
	}
	// ibset, ibclr
	set := name == "ibset"
	return two(func(f *frame, x, p int64) int64 {
		if p < 0 || p >= int64(w) {
			fail(f, "%s: bit position %d is outside 0:%d", name, p, w-1)
		}
		if set {
			return wrap(x | int64(uint64(1)<<uint(p)))
		}
		return wrap(x &^ int64(uint64(1)<<uint(p)))
	}), nil
}
