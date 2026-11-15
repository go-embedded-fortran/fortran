package eval

import (
	"math"

	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// arrayFormals are the dummy argument names of the array intrinsics, in order
// (F2018 16.9), so that keyword arguments find their place.
var arrayFormals = map[string][]string{
	"reshape":     {"source", "shape", "pad", "order"},
	"size":        {"array", "dim"},
	"shape":       {"source"},
	"lbound":      {"array", "dim"},
	"ubound":      {"array", "dim"},
	"sum":         {"array", "dim", "mask"},
	"product":     {"array", "dim", "mask"},
	"maxval":      {"array", "dim", "mask"},
	"minval":      {"array", "dim", "mask"},
	"maxloc":      {"array", "dim", "mask"},
	"minloc":      {"array", "dim", "mask"},
	"any":         {"mask", "dim"},
	"all":         {"mask", "dim"},
	"count":       {"mask", "dim"},
	"pack":        {"array", "mask", "vector"},
	"spread":      {"source", "dim", "ncopies"},
	"transpose":   {"matrix"},
	"cshift":      {"array", "shift", "dim"},
	"eoshift":     {"array", "shift", "boundary", "dim"},
	"dot_product": {"vector_a", "vector_b"},
	"matmul":      {"matrix_a", "matrix_b"},
	"allocated":   {"array"},
}

// arrIntrinsic compiles a call of an array intrinsic.
func (c *compiler) arrIntrinsic(n *syntax.Index, name string, tv sema.TV, formals []string) (*ex, error) {
	if name == "allocated" {
		return c.allocatedGA(n)
	}
	args := make([]*ex, len(formals))
	pos := true
	for i, a := range n.Args {
		if a.Value == nil {
			return nil, c.errf(n, "an alternate return in a call of %s", name)
		}
		at := i
		if a.Keyword != "" {
			pos, at = false, -1
			for j, fn := range formals {
				if fn == a.Keyword {
					at = j
				}
			}
			if at < 0 {
				return nil, c.errf(n, "%s has no argument %s", name, a.Keyword)
			}
		} else if !pos || i >= len(formals) {
			return nil, c.errf(n, "too many arguments to %s", name)
		}
		v, err := c.expr(a.Value)
		if err != nil {
			return nil, err
		}
		args[at] = v
	}
	for i, a := range args {
		if a != nil && a.k == kArr {
			return nil, c.errf(n, "%s of a real(8) array of the kernels' kind (declared with bounds 1) is not supported yet", name)
		}
		if i < len(formals) && a == nil && (i == 0 || (name == "reshape" && i == 1) || (name == "spread") && i < 3 || (name == "cshift" || name == "eoshift") && i == 1 || (name == "dot_product" || name == "matmul" || name == "pack") && i < 2) {
			return nil, c.errf(n, "%s: argument %s is missing", name, formals[i])
		}
	}
	for i := range args {
		if args[i] != nil && (formals[i] == "dim" || formals[i] == "shift" || formals[i] == "ncopies") && args[i].k != kInt {
			return nil, c.errf(n, "%s: %s must be a scalar integer", name, formals[i])
		}
	}
	if (name == "sum" || name == "product" || name == "maxval" || name == "minval" || name == "maxloc" || name == "minloc") && (args[1] != nil || args[2] != nil) {
		return nil, c.errf(n, "%s with dim= or mask= is not supported yet", name)
	}
	if (name == "any" || name == "all" || name == "count") && args[1] != nil {
		return nil, c.errf(n, "%s with dim= is not supported yet", name)
	}
	rk, _ := classOf(tv.Type)
	kd := kdOf(tv.Type)
	arrArg := func(i int) (gaFn, error) {
		if args[i] == nil || args[i].k != kGA {
			return nil, c.errf(n, "%s: argument %s must be an array", name, formals[i])
		}
		return args[i].g, nil
	}
	arr := func(rank int, et kind, fn gaFn) *ex {
		return &ex{k: kGA, slot: -1, rank: rank, et: et, kd: kd, g: fn}
	}
	intRes := func(fn intFn) *ex { return &ex{k: kInt, kd: kd, slot: -1, i: fn} }
	switch name {
	case "size", "lbound", "ubound":
		g, err := arrArg(0)
		if err != nil {
			return nil, err
		}
		var dim intFn
		if args[1] != nil {
			dim = args[1].i
		}
		pick := func(f *frame, a *garr, d int) int64 {
			switch name {
			case "size":
				return int64(a.ext[d])
			case "lbound":
				return a.lb[d]
			}
			return a.lb[d] + int64(a.ext[d]) - 1
		}
		if dim != nil {
			return intRes(func(f *frame) int64 {
				a := g(f)
				d := dim(f)
				if d < 1 || d > int64(len(a.ext)) {
					fail(f, "%s: dim %d is out of range 1:%d", name, d, len(a.ext))
				}
				return pick(f, a, int(d-1))
			}), nil
		}
		if name == "size" {
			return intRes(func(f *frame) int64 { return int64(g(f).size()) }), nil
		}
		return arr(1, kInt, func(f *frame) *garr {
			a := g(f)
			out := newGarr(f, kInt, kd, []int64{1}, []int{len(a.ext)})
			for d := range a.ext {
				out.i[d] = pick(f, a, d)
			}
			return out
		}), nil
	case "shape":
		g, err := arrArg(0)
		if err != nil {
			return nil, err
		}
		return arr(1, kInt, func(f *frame) *garr {
			a := g(f)
			out := newGarr(f, kInt, kd, []int64{1}, []int{len(a.ext)})
			for d, e := range a.ext {
				out.i[d] = int64(e)
			}
			return out
		}), nil
	case "sum", "product", "maxval", "minval":
		g, err := arrArg(0)
		if err != nil {
			return nil, err
		}
		reduce := func(f *frame) (int64, float64) {
			a := g(f)
			cnt := a.size()
			var iv int64
			var rv float64
			switch name {
			case "sum":
			case "product":
				iv, rv = 1, 1
			case "maxval":
				iv, rv = math.MinInt64, math.Inf(-1)
			default:
				iv, rv = math.MaxInt64, math.Inf(1)
			}
			for k := 0; k < cnt; k++ {
				x, y := a.at(k)
				switch name {
				case "sum":
					iv, rv = iv+x, rv+y
				case "product":
					iv, rv = iv*x, rv*y
				case "maxval":
					iv, rv = max(iv, x), math.Max(rv, y)
				default:
					iv, rv = min(iv, x), math.Min(rv, y)
				}
			}
			return iv, rv
		}
		if rk == kReal {
			return &ex{k: kReal, kd: kd, slot: -1, r: func(f *frame) float64 {
				_, r := reduce(f)
				if kd == 4 {
					r = float64(float32(r))
				}
				return r
			}}, nil
		}
		return intRes(func(f *frame) int64 { i, _ := reduce(f); return narrow(kd, i) }), nil
	case "maxloc", "minloc":
		g, err := arrArg(0)
		if err != nil {
			return nil, err
		}
		isMax := name == "maxloc"
		return arr(1, kInt, func(f *frame) *garr {
			a := g(f)
			best, bi := -1, 0
			var bv float64
			for k, cnt := 0, a.size(); k < cnt; k++ {
				_, v := a.at(k)
				if a.et != kReal {
					v = float64(a.i[k])
				}
				if best < 0 || (isMax && v > bv) || (!isMax && v < bv) {
					best, bi, bv = k, k, v
				}
			}
			out := newGarr(f, kInt, kd, []int64{1}, []int{len(a.ext)})
			if best >= 0 {
				for d, e := range a.ext {
					out.i[d] = int64(bi%e) + 1
					bi /= e
				}
			}
			return out
		}), nil
	case "any", "all", "count":
		g, err := arrArg(0)
		if err != nil {
			return nil, err
		}
		cnt := func(f *frame) (int, int) {
			a := g(f)
			t := 0
			for _, v := range a.i {
				if v != 0 {
					t++
				}
			}
			return t, len(a.i)
		}
		switch name {
		case "any":
			return &ex{k: kLog, slot: -1, b: func(f *frame) bool { t, _ := cnt(f); return t > 0 }}, nil
		case "all":
			return &ex{k: kLog, slot: -1, b: func(f *frame) bool { t, m := cnt(f); return t == m }}, nil
		}
		return intRes(func(f *frame) int64 { t, _ := cnt(f); return int64(t) }), nil
	case "dot_product":
		a, err := arrArg(0)
		if err != nil {
			return nil, err
		}
		b, err := arrArg(1)
		if err != nil {
			return nil, err
		}
		dot := func(f *frame) (int64, float64) {
			p, q := a(f), b(f)
			if len(p.ext) != 1 || len(q.ext) != 1 || p.ext[0] != q.ext[0] {
				fail(f, "dot_product of arrays of extents %v and %v", p.ext, q.ext)
			}
			var iv int64
			var rv float64
			for k := 0; k < p.ext[0]; k++ {
				x, y := p.at(k)
				u, v := q.at(k)
				iv += x * u
				rv += y * v
			}
			return iv, rv
		}
		if rk == kReal {
			return &ex{k: kReal, kd: kd, slot: -1, r: func(f *frame) float64 { _, r := dot(f); return r }}, nil
		}
		return intRes(func(f *frame) int64 { i, _ := dot(f); return narrow(kd, i) }), nil
	case "transpose":
		g, err := arrArg(0)
		if err != nil {
			return nil, err
		}
		return arr(2, rk, func(f *frame) *garr {
			a := g(f)
			if len(a.ext) != 2 {
				fail(f, "transpose of an array of rank %d", len(a.ext))
			}
			out := newGarr(f, a.et, a.kd, []int64{1, 1}, []int{a.ext[1], a.ext[0]})
			for i := 0; i < a.ext[0]; i++ {
				for j := 0; j < a.ext[1]; j++ {
					x, y := a.at(i + j*a.ext[0])
					out.put(j+i*a.ext[1], x, y)
				}
			}
			return out
		}), nil
	case "matmul":
		a, err := arrArg(0)
		if err != nil {
			return nil, err
		}
		b, err := arrArg(1)
		if err != nil {
			return nil, err
		}
		return arr(tv.Rank, rk, func(f *frame) *garr {
			p, q := a(f), b(f)
			// a vector is a matrix with one row (first operand) or one column
			pr, pc := 1, p.ext[0]
			if len(p.ext) == 2 {
				pr, pc = p.ext[0], p.ext[1]
			}
			qr, qc := q.ext[0], 1
			if len(q.ext) == 2 {
				qc = q.ext[1]
			}
			if len(p.ext) == 1 && len(q.ext) == 1 || pc != qr {
				fail(f, "matmul of arrays of extents %v and %v", p.ext, q.ext)
			}
			var ext []int
			if len(p.ext) == 2 {
				ext = append(ext, pr)
			}
			if len(q.ext) == 2 {
				ext = append(ext, qc)
			}
			out := newGarr(f, rk, kd, ones(len(ext)), ext)
			for i := 0; i < pr; i++ {
				for j := 0; j < qc; j++ {
					var si int64
					var sr float64
					for k := 0; k < pc; k++ {
						x, y := p.at(i + k*pr)
						u, v := q.at(k + j*qr)
						si += x * u
						sr += y * v
					}
					out.put(i+j*pr, si, sr)
				}
			}
			return out
		}), nil
	case "reshape":
		src, err := arrArg(0)
		if err != nil {
			return nil, err
		}
		shp, err := arrArg(1)
		if err != nil {
			return nil, err
		}
		var pad, ord gaFn
		if args[2] != nil {
			if pad, err = arrArg(2); err != nil {
				return nil, err
			}
		}
		if args[3] != nil {
			if ord, err = arrArg(3); err != nil {
				return nil, err
			}
		}
		return arr(tv.Rank, rk, func(f *frame) *garr {
			s, sh := src(f), shp(f)
			ext := make([]int, len(sh.i))
			for d, v := range sh.i {
				if v < 0 {
					fail(f, "reshape: negative extent %d", v)
				}
				ext[d] = int(v)
			}
			order := make([]int, len(ext))
			for d := range order {
				order[d] = d
			}
			if ord != nil {
				o := ord(f)
				if len(o.i) != len(ext) {
					fail(f, "reshape: order has %d entries, the shape %d", len(o.i), len(ext))
				}
				seen := make([]bool, len(ext))
				for d, v := range o.i {
					if v < 1 || int(v) > len(ext) || seen[v-1] {
						fail(f, "reshape: order is not a permutation")
					}
					seen[v-1] = true
					order[d] = int(v - 1)
				}
			}
			out := newGarr(f, rk, kd, ones(len(ext)), ext)
			total := out.size()
			var p *garr
			if pad != nil {
				p = pad(f)
			}
			if total > s.size() && (p == nil || p.size() == 0) {
				fail(f, "reshape: the source has %d elements, the shape %d and no pad", s.size(), total)
			}
			strides := make([]int, len(ext))
			st := 1
			for d, e := range ext {
				strides[d], st = st, st*e
			}
			idx := make([]int, len(ext))
			for k := 0; k < total; k++ {
				var x int64
				var y float64
				if k < s.size() {
					x, y = s.at(k)
				} else {
					x, y = p.at((k - s.size()) % p.size())
				}
				o := 0
				for d := range ext {
					o += idx[d] * strides[d]
				}
				out.put(o, x, y)
				for _, d := range order { // order[0] varies fastest
					if idx[d]++; idx[d] < ext[d] {
						break
					}
					idx[d] = 0
				}
			}
			return out
		}), nil
	case "pack":
		a, err := arrArg(0)
		if err != nil {
			return nil, err
		}
		m, err := arrArg(1)
		if err != nil {
			return nil, err
		}
		if args[2] != nil {
			return nil, c.errf(n, "pack with vector= is not supported yet")
		}
		return arr(1, rk, func(f *frame) *garr {
			s, mk := a(f), m(f)
			if !sameShape(s.ext, mk.ext) {
				fail(f, "pack: array and mask of extents %v and %v", s.ext, mk.ext)
			}
			var is []int64
			var rs []float64
			for k, v := range mk.i {
				if v != 0 {
					x, y := s.at(k)
					is, rs = append(is, x), append(rs, y)
				}
			}
			out := newGarr(f, rk, kd, []int64{1}, []int{len(is)})
			for k := range is {
				out.put(k, is[k], rs[k])
			}
			return out
		}), nil
	case "spread":
		dimF, ncF := args[1].i, args[2].i
		var srcG gaFn
		var srcS valFn
		if args[0].k == kGA {
			srcG = args[0].g
		} else if srcS = scalarVal(args[0]); srcS == nil {
			return nil, c.errf(n, "spread of this argument is not supported")
		}
		return arr(tv.Rank, rk, func(f *frame) *garr {
			var s *garr
			var sx int64
			var sy float64
			var sext []int
			if srcG != nil {
				s = srcG(f)
				sext = s.ext
			} else {
				sx, sy = srcS(f)
			}
			d, nc := int(dimF(f)), int(ncF(f))
			if d < 1 || d > len(sext)+1 {
				fail(f, "spread: dim %d is out of range 1:%d", d, len(sext)+1)
			}
			nc = max(nc, 0)
			ext := append(append(append([]int(nil), sext[:d-1]...), nc), sext[d-1:]...)
			out := newGarr(f, rk, kd, ones(len(ext)), ext)
			total := out.size()
			inner := 1
			for _, e := range ext[:d-1] {
				inner *= e
			}
			for k := 0; k < total; k++ {
				if s == nil {
					out.put(k, sx, sy)
					continue
				}
				// k = i + inner*(c + nc*o): the source element is i + inner*o
				i := k % inner
				o := k / (inner * nc)
				x, y := s.at(i + inner*o)
				out.put(k, x, y)
			}
			return out
		}), nil
	case "cshift", "eoshift":
		g, err := arrArg(0)
		if err != nil {
			return nil, err
		}
		shift := args[1].i
		var bnd valFn
		if name == "eoshift" && args[2] != nil {
			if bnd = scalarVal(args[2]); bnd == nil {
				return nil, c.errf(n, "eoshift: boundary must be a scalar")
			}
		}
		if args[len(formals)-1] != nil {
			return nil, c.errf(n, "%s with dim= is not supported yet", name)
		}
		return arr(1, rk, func(f *frame) *garr {
			a := g(f)
			if len(a.ext) != 1 {
				fail(f, "%s of an array of rank %d is not supported yet", name, len(a.ext))
			}
			m := a.ext[0]
			sh := shift(f)
			out := newGarr(f, a.et, a.kd, append([]int64(nil), a.lb...), []int{m})
			var bi int64
			var br float64
			if bnd != nil {
				bi, br = bnd(f)
			}
			for k := 0; k < m; k++ {
				j := int64(k) + sh
				if name == "cshift" && m > 0 {
					j = ((j % int64(m)) + int64(m)) % int64(m)
				}
				if j >= 0 && j < int64(m) {
					x, y := a.at(int(j))
					out.put(k, x, y)
				} else {
					out.put(k, bi, br)
				}
			}
			return out
		}), nil
	}
	return nil, c.errf(n, "%s is not supported", name)
}

// allocatedGA compiles ALLOCATED of an allocatable array of the general kind.
func (c *compiler) allocatedGA(n *syntax.Index) (*ex, error) {
	if len(n.Args) != 1 || n.Args[0].Value == nil {
		return nil, c.errf(n, "allocated takes one argument")
	}
	id, ok := n.Args[0].Value.(*syntax.Ident)
	if !ok {
		return nil, c.errf(n, "allocated needs an allocatable array")
	}
	s, _, err := c.symOf(id)
	if err != nil || !s.ga || !s.alloc || s.owner != c.cur {
		return nil, c.errf(n, "allocated: %s is not an allocatable array of the general kind of this unit", id.Name)
	}
	slot := s.slot
	return &ex{k: kLog, slot: -1, b: func(f *frame) bool { return f.gas[slot] != nil }}, nil
}

// elementalGA compiles an elemental intrinsic applied to arrays: the scalar
// intrinsic is compiled once on placeholders that read the current element.
func (c *compiler) elementalGA(n *syntax.Index, name string, tv sema.TV, args []*ex) (*ex, error) {
	rk, ok := classOf(tv.Type)
	if !ok || tv.Rank < 1 {
		return nil, c.errf(n, "%s of an array is not supported", name)
	}
	type slotInfo struct {
		g    gaFn
		et   kind
		slot int
	}
	var arrs []slotInfo
	sargs := make([]*ex, len(args))
	for i, a := range args {
		if a.k != kGA {
			sargs[i] = a
			continue
		}
		slot := 0
		switch a.et {
		case kReal:
			slot = c.cur.nReals
			c.cur.nReals++
			s := slot
			sargs[i] = &ex{k: kReal, kd: a.kd, slot: -1, r: func(f *frame) float64 { return f.reals[s] }}
		case kLog:
			slot = c.cur.nInts
			c.cur.nInts++
			s := slot
			sargs[i] = &ex{k: kLog, slot: -1, b: func(f *frame) bool { return f.ints[s] != 0 }}
		default:
			slot = c.cur.nInts
			c.cur.nInts++
			s := slot
			sargs[i] = &ex{k: kInt, kd: a.kd, slot: -1, i: func(f *frame) int64 { return f.ints[s] }}
		}
		arrs = append(arrs, slotInfo{a.g, a.et, slot})
	}
	stv := tv
	stv.Rank = 0
	sc, err := c.intrinsicWith(n, name, stv, sargs)
	if err != nil {
		return nil, err
	}
	sc = fit(sc, tv.Type)
	val := scalarVal(sc)
	if val == nil {
		return nil, c.errf(n, "%s of an array is not supported", name)
	}
	kd := kdOf(tv.Type)
	return &ex{k: kGA, slot: -1, rank: tv.Rank, et: rk, kd: kd, g: func(f *frame) *garr {
		gs := make([]*garr, len(arrs))
		for k, a := range arrs {
			gs[k] = a.g(f)
			if k > 0 && !sameShape(gs[0].ext, gs[k].ext) {
				fail(f, "arrays of extents %v and %v are not conformable", gs[0].ext, gs[k].ext)
			}
		}
		out := newGarr(f, rk, kd, ones(len(gs[0].ext)), append([]int(nil), gs[0].ext...))
		for e, cnt := 0, out.size(); e < cnt; e++ {
			for k, a := range arrs {
				x, y := gs[k].at(e)
				if a.et == kReal {
					f.reals[a.slot] = y
				} else {
					f.ints[a.slot] = x
				}
			}
			x, y := val(f)
			out.put(e, x, y)
		}
		return out
	}}, nil
}
