package eval

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-ndarray/ndarray"

	"github.com/go-embedded-fortran/fortran/syntax"
)

// Error is a refusal or a run-time failure, positioned in the source when the
// compiler knows where.
type Error struct {
	Pos string
	Msg string
}

func (e *Error) Error() string {
	if e.Pos == "" {
		return "eval: " + e.Msg
	}
	return "eval: " + e.Pos + ": " + e.Msg
}

// array is a Fortran real(8) array: the ndarray of the reversed shape and its
// own storage (see the package documentation).
type array struct {
	nd     *ndarray.Array
	data   []float64
	n1, n2 int // extents of the first and second subscript; n2 is 1 for rank 1
	rank   int
}

// frame is the activation record: one slice per slot type.
type frame struct {
	ints   []int64
	reals  []float64
	arrs   []*array
	out    io.Writer
	start  time.Time
	copied int64
}

type (
	intFn  func(*frame) int64
	realFn func(*frame) float64
	arrFn  func(*frame) *array
	stmtFn func(*frame)
)

// Program is a compiled main program.
type Program struct {
	nInts, nReals, nArrs int
	init                 []stmtFn
	body                 []stmtFn
}

// Copies is the number of array elements the last Run copied between storage
// the evaluator owns and arrays go-ndarray allocated for itself (the result of
// matmul). It is zero for a program that makes no such call.
type Stats struct {
	Copies int64
}

// Run executes the program, printing to out. A bounds violation or a
// non-conformable whole-array operation is returned as an *Error.
func (p *Program) Run(out io.Writer) (st Stats, err error) {
	f := &frame{
		ints:  make([]int64, p.nInts),
		reals: make([]float64, p.nReals),
		arrs:  make([]*array, p.nArrs),
		out:   out,
		start: time.Now(),
	}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*Error); ok {
				err = e
				return
			}
			panic(r)
		}
		st.Copies = f.copied
	}()
	for _, s := range p.init {
		s(f)
	}
	for _, s := range p.body {
		s(f)
	}
	return
}

func fail(f *frame, format string, a ...any) {
	panic(&Error{Msg: fmt.Sprintf(format, a...)})
}

// ---- compiler ----

type kind uint8

const (
	kInt kind = iota + 1
	kReal
	kArr
)

type sym struct {
	k      kind
	slot   int
	isC    bool // a parameter
	ci     int64
	cr     float64
	rank   int
	shape  []intFn // constant-shape arrays: allocated at start
	hasDim bool
}

// ex is a compiled expression.
type ex struct {
	k kind
	i intFn
	r realFn
	a arrFn

	isC  bool
	ci   int64
	cr   float64
	slot int // >= 0 when the expression is a plain scalar variable
	rank int

	// array expressions that go-ndarray can write into an existing array.
	op   string
	l, x *ex
	// a plain array variable
	v *sym
}

type compiler struct {
	file *syntax.File
	syms map[string]*sym
	p    *Program
}

func (c *compiler) errf(n syntax.Node, format string, a ...any) error {
	e := &Error{Msg: fmt.Sprintf(format, a...)}
	if n != nil {
		e.Pos = c.file.Position(n.P()).String()
	}
	return e
}

// Compile compiles a parsed file. Anything outside the prototype's language is
// refused with a positioned error.
func Compile(f *syntax.File) (*Program, error) {
	c := &compiler{file: f, syms: map[string]*sym{}, p: &Program{}}
	if len(f.Units) != 1 || f.Units[0].Kind != syntax.ProgramUnit {
		return nil, c.errf(nil, "exactly one main program is supported")
	}
	u := f.Units[0]
	if u.HasContains {
		return nil, c.errf(u, "internal procedures are not supported")
	}
	for _, s := range u.Body {
		if err := c.stmt(s, &c.p.body); err != nil {
			return nil, err
		}
	}
	return c.p, nil
}

func (c *compiler) stmts(body []syntax.Stmt) ([]stmtFn, error) {
	var out []stmtFn
	for _, s := range body {
		if err := c.stmt(s, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *compiler) stmt(s syntax.Stmt, out *[]stmtFn) error {
	if s.StmtLabel() != 0 {
		return c.errf(s, "statement labels are not supported")
	}
	switch s := s.(type) {
	case *syntax.ImplicitStmt:
		if !s.None {
			return c.errf(s, "only implicit none is supported")
		}
	case *syntax.Decl:
		return c.decl(s)
	case *syntax.Assignment:
		fn, err := c.assign(s)
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	case *syntax.Do:
		fn, err := c.do(s)
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	case *syntax.Call:
		fn, err := c.call(s)
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	case *syntax.IOStmt:
		fn, err := c.print(s)
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	case *syntax.Allocate:
		fn, err := c.allocate(s)
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	default:
		return c.errf(s, "%T is not supported by the prototype", s)
	}
	return nil
}

// ---- declarations ----

func (c *compiler) typeKind(t *syntax.TypeSpec) (kind, error) {
	switch t.Name {
	case "integer":
		return kInt, nil
	case "doubleprecision":
		return kReal, nil
	case "real":
		if t.Kind == nil {
			return 0, c.errf(t, "default real is real(4): only real(8) is supported")
		}
		if c.isInt(t.Kind, 8) {
			return kReal, nil
		}
	}
	return 0, c.errf(t, "type %q (kind %v) is not supported", t.Name, t.Kind != nil)
}

func (c *compiler) isInt(e syntax.Expr, v int64) bool {
	l, ok := e.(*syntax.IntLit)
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(l.Text, 10, 64)
	return err == nil && n == v
}

func (c *compiler) decl(d *syntax.Decl) error {
	k, err := c.typeKind(d.Type)
	if err != nil {
		return err
	}
	var param, alloc bool
	var attrDims []syntax.Dim
	for _, a := range d.Attrs {
		switch a.Name {
		case "parameter":
			param = true
		case "allocatable":
			alloc = true
		case "dimension":
			attrDims = a.Dims
		default:
			return c.errf(d, "attribute %q is not supported", a.Name)
		}
	}
	for _, e := range d.Entities {
		if _, dup := c.syms[e.Name.Name]; dup {
			return c.errf(e, "%s is declared twice", e.Name.Name)
		}
		dims := e.Dims
		if dims == nil {
			dims = attrDims
		}
		s := &sym{k: k}
		switch {
		case len(dims) > 0:
			if k != kReal || param {
				return c.errf(e, "only real(8) variable arrays are supported")
			}
			if len(dims) > 2 {
				return c.errf(e, "rank %d: only ranks 1 and 2 are supported", len(dims))
			}
			s.k, s.rank, s.slot = kArr, len(dims), c.p.nArrs
			c.p.nArrs++
			if !alloc {
				for _, d := range dims {
					if d.Colon || d.Star || d.Rank || d.Hi == nil || d.Lo != nil {
						return c.errf(e, "an explicit-shape array needs 1-based constant bounds")
					}
					fn, err := c.intExpr(d.Hi)
					if err != nil {
						return err
					}
					s.shape = append(s.shape, fn)
				}
				c.syms[e.Name.Name] = s
				c.p.init = append(c.p.init, allocFn(s.slot, s.shape))
				continue
			}
			for _, d := range dims {
				if !d.Colon {
					return c.errf(e, "an allocatable array is declared with (:,...)")
				}
			}
		case k == kInt:
			s.slot = c.p.nInts
			c.p.nInts++
		default:
			s.slot = c.p.nReals
			c.p.nReals++
		}
		c.syms[e.Name.Name] = s
		if param {
			if e.Init == nil {
				return c.errf(e, "parameter %s has no value", e.Name.Name)
			}
			v, err := c.expr(e.Init)
			if err != nil {
				return err
			}
			if !v.isC {
				return c.errf(e, "parameter %s is not a constant expression", e.Name.Name)
			}
			s.isC = true
			if k == kInt {
				if v.k != kInt {
					return c.errf(e, "integer parameter %s with a real value", e.Name.Name)
				}
				s.ci = v.ci
			} else {
				s.cr = v.asReal().cr
			}
			continue
		}
		if e.Init != nil {
			v, err := c.expr(e.Init)
			if err != nil {
				return err
			}
			fn, err := c.store(s, v, e)
			if err != nil {
				return err
			}
			c.p.init = append(c.p.init, fn)
		}
	}
	return nil
}

func allocFn(slot int, shape []intFn) stmtFn {
	return func(f *frame) {
		var n [2]int
		n[1] = 1
		for i, sh := range shape {
			n[i] = int(sh(f))
		}
		f.arrs[slot] = newArray(f, len(shape), n[0], n[1])
	}
}

func newArray(f *frame, rank, n1, n2 int) *array {
	if n1 < 0 || n2 < 0 || n1 > 1<<31 || n2 > 1<<31 || (n2 > 0 && n1 > (1<<40)/n2) {
		fail(f, "array extent (%d,%d) is out of range", n1, n2)
	}
	var nd *ndarray.Array
	var err error
	if rank == 1 {
		nd, err = ndarray.Zeros(n1)
	} else {
		nd, err = ndarray.Zeros(n2, n1)
	}
	if err != nil {
		fail(f, "allocate: %v", err)
	}
	data, ok := ndarray.Data[float64](nd)
	if !ok {
		fail(f, "allocate: not a float64 array")
	}
	return &array{nd: nd, data: data, n1: n1, n2: n2, rank: rank}
}

// ---- expressions ----

func (e *ex) asReal() *ex {
	switch e.k {
	case kReal:
		return e
	case kInt:
		if e.isC {
			return &ex{k: kReal, isC: true, cr: float64(e.ci), slot: -1, r: func(*frame) float64 { return float64(e.ci) }}
		}
		fi := e.i
		return &ex{k: kReal, slot: -1, r: func(f *frame) float64 { return float64(fi(f)) }}
	}
	return e
}

func (c *compiler) intExpr(n syntax.Expr) (intFn, error) {
	e, err := c.expr(n)
	if err != nil {
		return nil, err
	}
	if e.k != kInt {
		return nil, c.errf(n, "an integer expression is required")
	}
	return e.i, nil
}

func constInt(v int64) *ex {
	return &ex{k: kInt, isC: true, ci: v, slot: -1, i: func(*frame) int64 { return v }}
}

func constReal(v float64) *ex {
	return &ex{k: kReal, isC: true, cr: v, slot: -1, r: func(*frame) float64 { return v }}
}

func (c *compiler) expr(n syntax.Expr) (*ex, error) {
	switch n := n.(type) {
	case *syntax.IntLit:
		txt, _, _ := strings.Cut(n.Text, "_")
		v, err := strconv.ParseInt(txt, 10, 64)
		if err != nil {
			return nil, c.errf(n, "integer literal %q: %v", n.Text, err)
		}
		return constInt(v), nil
	case *syntax.RealLit:
		if n.Kind != "" && n.Kind != "8" && !strings.ContainsAny(n.Text, "d") {
			return nil, c.errf(n, "only double precision literals are supported")
		}
		txt, _, _ := strings.Cut(n.Text, "_")
		v, err := strconv.ParseFloat(strings.ReplaceAll(txt, "d", "e"), 64)
		if err != nil {
			return nil, c.errf(n, "real literal %q: %v", n.Text, err)
		}
		return constReal(v), nil
	case *syntax.Paren:
		return c.expr(n.X)
	case *syntax.Ident:
		return c.ident(n)
	case *syntax.Unary:
		x, err := c.expr(n.X)
		if err != nil {
			return nil, err
		}
		switch n.Op {
		case "+":
			return x, nil
		case "-":
			return c.negate(n, x)
		}
		return nil, c.errf(n, "operator %q is not supported", n.Op)
	case *syntax.Binary:
		return c.binary(n)
	case *syntax.Index:
		return c.index(n)
	}
	return nil, c.errf(n, "%T is not supported in an expression", n)
}

func (c *compiler) ident(n *syntax.Ident) (*ex, error) {
	s, ok := c.syms[n.Name]
	if !ok {
		return nil, c.errf(n, "%s is not declared", n.Name)
	}
	switch {
	case s.isC && s.k == kInt:
		return constInt(s.ci), nil
	case s.isC:
		return constReal(s.cr), nil
	case s.k == kInt:
		slot := s.slot
		return &ex{k: kInt, slot: slot, i: func(f *frame) int64 { return f.ints[slot] }}, nil
	case s.k == kReal:
		slot := s.slot
		return &ex{k: kReal, slot: slot, r: func(f *frame) float64 { return f.reals[slot] }}, nil
	}
	slot, name := s.slot, n.Name
	return &ex{k: kArr, slot: -1, rank: s.rank, v: s, a: func(f *frame) *array {
		a := f.arrs[slot]
		if a == nil {
			fail(f, "array %s is not allocated", name)
		}
		return a
	}}, nil
}

func (c *compiler) negate(n syntax.Node, x *ex) (*ex, error) {
	switch x.k {
	case kInt:
		if x.isC {
			return constInt(-x.ci), nil
		}
		fi := x.i
		return &ex{k: kInt, slot: -1, i: func(f *frame) int64 { return -fi(f) }}, nil
	case kReal:
		if x.isC {
			return constReal(-x.cr), nil
		}
		fr := x.r
		return &ex{k: kReal, slot: -1, r: func(f *frame) float64 { return -fr(f) }}, nil
	}
	return nil, c.errf(n, "negation of an array is not supported")
}

func (c *compiler) binary(n *syntax.Binary) (*ex, error) {
	x, err := c.expr(n.X)
	if err != nil {
		return nil, err
	}
	y, err := c.expr(n.Y)
	if err != nil {
		return nil, err
	}
	switch n.Op {
	case "+", "-", "*", "/", "**":
	default:
		return nil, c.errf(n, "operator %q is not supported", n.Op)
	}
	switch {
	case x.k == kArr || y.k == kArr:
		return c.arrayBinary(n, x, y)
	case x.k == kInt && y.k == kInt:
		return c.intBinary(n, x, y)
	}
	if n.Op == "**" && y.k == kInt && y.isC {
		return powInt(x.asReal(), y.ci), nil
	}
	return realBinary(n.Op, x.asReal(), y.asReal()), nil
}

func (c *compiler) intBinary(n *syntax.Binary, x, y *ex) (*ex, error) {
	if x.isC && y.isC {
		switch n.Op {
		case "+":
			return constInt(x.ci + y.ci), nil
		case "-":
			return constInt(x.ci - y.ci), nil
		case "*":
			return constInt(x.ci * y.ci), nil
		case "/":
			if y.ci != 0 {
				return constInt(x.ci / y.ci), nil
			}
		}
	}
	a, b := x.i, y.i
	var fn intFn
	switch n.Op {
	case "+":
		if y.isC {
			k := y.ci
			fn = func(f *frame) int64 { return a(f) + k }
		} else {
			fn = func(f *frame) int64 { return a(f) + b(f) }
		}
	case "-":
		if y.isC {
			k := y.ci
			fn = func(f *frame) int64 { return a(f) - k }
		} else {
			fn = func(f *frame) int64 { return a(f) - b(f) }
		}
	case "*":
		fn = func(f *frame) int64 { return a(f) * b(f) }
	case "/":
		fn = func(f *frame) int64 {
			d := b(f)
			if d == 0 {
				fail(f, "integer division by zero")
			}
			return a(f) / d
		}
	default:
		return nil, c.errf(n, "integer ** is not supported")
	}
	return &ex{k: kInt, slot: -1, i: fn}, nil
}

// powInt is x**k for a constant integer k.
func powInt(x *ex, k int64) *ex {
	a := x.r
	switch {
	case k == 2:
		if x.slot >= 0 {
			s := x.slot
			return &ex{k: kReal, slot: -1, r: func(f *frame) float64 { v := f.reals[s]; return v * v }}
		}
		return &ex{k: kReal, slot: -1, r: func(f *frame) float64 { v := a(f); return v * v }}
	case k == 1:
		return x
	}
	return &ex{k: kReal, slot: -1, r: func(f *frame) float64 { return math.Pow(a(f), float64(k)) }}
}

func realBinary(op string, x, y *ex) *ex {
	a, b := x.r, y.r
	if x.isC && y.isC && op != "**" {
		switch op {
		case "+":
			return constReal(x.cr + y.cr)
		case "-":
			return constReal(x.cr - y.cr)
		case "*":
			return constReal(x.cr * y.cr)
		case "/":
			return constReal(x.cr / y.cr)
		}
	}
	var fn realFn
	switch op {
	case "+":
		switch {
		case x.slot >= 0:
			s := x.slot
			fn = func(f *frame) float64 { return f.reals[s] + b(f) }
		case y.isC:
			k := y.cr
			fn = func(f *frame) float64 { return a(f) + k }
		default:
			fn = func(f *frame) float64 { return a(f) + b(f) }
		}
	case "-":
		fn = func(f *frame) float64 { return a(f) - b(f) }
	case "*":
		if y.isC {
			k := y.cr
			fn = func(f *frame) float64 { return a(f) * k }
		} else if x.isC {
			k := x.cr
			fn = func(f *frame) float64 { return k * b(f) }
		} else {
			fn = func(f *frame) float64 { return a(f) * b(f) }
		}
	case "/":
		if x.isC {
			k := x.cr
			fn = func(f *frame) float64 { return k / b(f) }
		} else {
			fn = func(f *frame) float64 { return a(f) / b(f) }
		}
	default:
		fn = func(f *frame) float64 { return math.Pow(a(f), b(f)) }
	}
	return &ex{k: kReal, slot: -1, r: fn}
}

func (c *compiler) arrayBinary(n *syntax.Binary, x, y *ex) (*ex, error) {
	if x.k == kInt || y.k == kInt {
		return nil, c.errf(n, "an integer operand of a whole-array expression is not supported")
	}
	if x.k == kArr && y.k == kArr {
		if x.rank != y.rank {
			return nil, c.errf(n, "ranks %d and %d are not conformable", x.rank, y.rank)
		}
		a, b := x.a, y.a
		var op func(p, q *ndarray.Array) (*ndarray.Array, error)
		switch n.Op {
		case "+":
			op = (*ndarray.Array).Add
		case "-":
			op = (*ndarray.Array).Sub
		case "*":
			op = (*ndarray.Array).Mul
		case "/":
			op = (*ndarray.Array).Div
		default:
			return nil, c.errf(n, "operator %q on arrays is not supported", n.Op)
		}
		fn := func(f *frame) *array {
			p, q := a(f), b(f)
			conform(f, p, q)
			nd, err := op(p.nd, q.nd)
			if err != nil {
				fail(f, "%v", err)
			}
			return wrap(f, nd, p)
		}
		return &ex{k: kArr, slot: -1, rank: x.rank, a: fn, op: n.Op, l: x, x: y}, nil
	}
	// array with a scalar: + and * only
	arr, sc := x, y
	if y.k == kArr {
		arr, sc = y, x
	}
	if n.Op != "+" && n.Op != "*" {
		return nil, c.errf(n, "operator %q between an array and a scalar is not supported", n.Op)
	}
	a, s := arr.a, sc.asReal().r
	mul := n.Op == "*"
	fn := func(f *frame) *array {
		p, v := a(f), s(f)
		if mul {
			return wrap(f, p.nd.MulScalar(v), p)
		}
		return wrap(f, p.nd.AddScalar(v), p)
	}
	return &ex{k: kArr, slot: -1, rank: arr.rank, a: fn}, nil
}

func conform(f *frame, p, q *array) {
	if p.n1 != q.n1 || p.n2 != q.n2 {
		fail(f, "arrays of extents (%d,%d) and (%d,%d) are not conformable", p.n1, p.n2, q.n1, q.n2)
	}
}

// wrap adopts the result of a go-ndarray operation as an array of the shape of
// like (the result's storage is its own, so no copy is made).
func wrap(f *frame, nd *ndarray.Array, like *array) *array {
	data, ok := ndarray.Data[float64](nd)
	if !ok {
		fail(f, "result of go-ndarray is not float64")
	}
	return &array{nd: nd, data: data, n1: like.n1, n2: like.n2, rank: like.rank}
}

func (c *compiler) index(n *syntax.Index) (*ex, error) {
	id, ok := n.X.(*syntax.Ident)
	if !ok {
		return nil, c.errf(n, "only a name can be subscripted or called")
	}
	if s, ok := c.syms[id.Name]; ok {
		if s.k != kArr {
			return nil, c.errf(n, "%s is not an array", id.Name)
		}
		return c.element(n, id.Name, s)
	}
	return c.intrinsic(n, id.Name)
}

func (c *compiler) element(n *syntax.Index, name string, s *sym) (*ex, error) {
	if len(n.Args) != s.rank {
		return nil, c.errf(n, "%s has rank %d, subscripted with %d", name, s.rank, len(n.Args))
	}
	var ix [2]intFn
	for i, a := range n.Args {
		if a.Keyword != "" || a.Value == nil {
			return nil, c.errf(n, "a keyword or alternate-return subscript is not supported")
		}
		if _, sec := a.Value.(*syntax.Triplet); sec {
			return nil, c.errf(n, "array sections are not supported")
		}
		fn, err := c.intExpr(a.Value)
		if err != nil {
			return nil, err
		}
		ix[i] = fn
	}
	slot := s.slot
	i0, i1 := ix[0], ix[1]
	var fn realFn
	if s.rank == 1 {
		fn = func(f *frame) float64 {
			a := f.arrs[slot]
			if a == nil {
				unallocated(f, name)
			}
			i := i0(f) - 1
			if uint64(i) >= uint64(a.n1) {
				bounds(f, name, 1, i+1, a.n1)
			}
			return a.data[i]
		}
	} else {
		fn = func(f *frame) float64 {
			a := f.arrs[slot]
			if a == nil {
				unallocated(f, name)
			}
			i, j := i0(f)-1, i1(f)-1
			if uint64(i) >= uint64(a.n1) {
				bounds(f, name, 1, i+1, a.n1)
			}
			if uint64(j) >= uint64(a.n2) {
				bounds(f, name, 2, j+1, a.n2)
			}
			return a.data[int(i)+int(j)*a.n1]
		}
	}
	return &ex{k: kReal, slot: -1, r: fn, v: s}, nil
}

func unallocated(f *frame, name string) { fail(f, "array %s is not allocated", name) }

func bounds(f *frame, name string, dim int, i int64, n int) {
	fail(f, "subscript %d of %s is %d, outside 1:%d", dim, name, i, n)
}

func (c *compiler) intrinsic(n *syntax.Index, name string) (*ex, error) {
	args := make([]*ex, len(n.Args))
	for i, a := range n.Args {
		if a.Keyword != "" || a.Value == nil {
			return nil, c.errf(n, "keyword arguments are not supported")
		}
		v, err := c.expr(a.Value)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	want := func(k int) error {
		if len(args) != k {
			return c.errf(n, "%s takes %d arguments", name, k)
		}
		return nil
	}
	switch name {
	case "real", "dble":
		if name == "real" {
			if len(args) != 2 || !args[1].isC || args[1].ci != 8 {
				return nil, c.errf(n, "real(x, 8) is the only supported conversion")
			}
		} else if err := want(1); err != nil {
			return nil, err
		}
		if args[0].k == kArr {
			return nil, c.errf(n, "%s of an array is not supported", name)
		}
		return args[0].asReal(), nil
	case "mod":
		if err := want(2); err != nil {
			return nil, err
		}
		if args[0].k != kInt || args[1].k != kInt {
			return nil, c.errf(n, "only mod of two integers is supported")
		}
		a, b := args[0].i, args[1].i
		return &ex{k: kInt, slot: -1, i: func(f *frame) int64 {
			d := b(f)
			if d == 0 {
				fail(f, "mod by zero")
			}
			return a(f) % d
		}}, nil
	case "sqrt", "abs":
		if err := want(1); err != nil {
			return nil, err
		}
		if args[0].k == kArr {
			return nil, c.errf(n, "%s of an array is not supported", name)
		}
		if name == "abs" && args[0].k == kInt {
			a := args[0].i
			return &ex{k: kInt, slot: -1, i: func(f *frame) int64 { return max(a(f), -a(f)) }}, nil
		}
		a := args[0].asReal().r
		if name == "sqrt" {
			return &ex{k: kReal, slot: -1, r: func(f *frame) float64 { return math.Sqrt(a(f)) }}, nil
		}
		return &ex{k: kReal, slot: -1, r: func(f *frame) float64 { return math.Abs(a(f)) }}, nil
	case "sum":
		if err := want(1); err != nil {
			return nil, err
		}
		if args[0].k != kArr {
			return nil, c.errf(n, "sum needs an array")
		}
		a := args[0].a
		return &ex{k: kReal, slot: -1, r: func(f *frame) float64 { return a(f).nd.Sum() }}, nil
	case "matmul":
		if err := want(2); err != nil {
			return nil, err
		}
		if args[0].k != kArr || args[1].k != kArr || args[0].rank != 2 || args[1].rank != 2 {
			return nil, c.errf(n, "matmul of two rank-2 arrays is the only supported form")
		}
		a, b := args[0].a, args[1].a
		// c = a b is, on the reversed (row-major) arrays, c' = b' a'.
		fn := func(f *frame) *array {
			p, q := a(f), b(f)
			if p.n2 != q.n1 {
				fail(f, "matmul: (%d,%d) by (%d,%d) is not conformable", p.n1, p.n2, q.n1, q.n2)
			}
			nd, err := q.nd.MatMul(p.nd)
			if err != nil {
				fail(f, "%v", err)
			}
			r := wrap(f, nd, p)
			r.n1, r.n2 = p.n1, q.n2
			return r
		}
		return &ex{k: kArr, slot: -1, rank: 2, a: fn, op: "matmul"}, nil
	}
	return nil, c.errf(n, "%s is not a variable or a supported intrinsic", name)
}

// ---- statements ----

// store compiles "variable = value" for a scalar variable.
func (c *compiler) store(s *sym, v *ex, at syntax.Node) (stmtFn, error) {
	slot := s.slot
	switch {
	case s.k == kInt && v.k == kInt:
		if v.isC {
			k := v.ci
			return func(f *frame) { f.ints[slot] = k }, nil
		}
		fn := v.i
		return func(f *frame) { f.ints[slot] = fn(f) }, nil
	case s.k == kReal && v.k != kArr:
		fn := v.asReal().r
		return func(f *frame) { f.reals[slot] = fn(f) }, nil
	}
	return nil, c.errf(at, "this assignment is not supported (a real into an integer variable, or an array into a scalar)")
}

func (c *compiler) assign(s *syntax.Assignment) (stmtFn, error) {
	rhs, err := c.expr(s.RHS)
	if err != nil {
		return nil, err
	}
	switch l := s.LHS.(type) {
	case *syntax.Ident:
		sy, ok := c.syms[l.Name]
		if !ok {
			return nil, c.errf(l, "%s is not declared", l.Name)
		}
		if sy.isC {
			return nil, c.errf(l, "%s is a parameter", l.Name)
		}
		if sy.k == kArr {
			return c.assignArray(s, sy, l.Name, rhs)
		}
		return c.store(sy, rhs, s)
	case *syntax.Index:
		id, ok := l.X.(*syntax.Ident)
		if !ok {
			return nil, c.errf(l, "unsupported assignment target")
		}
		sy, ok := c.syms[id.Name]
		if !ok || sy.k != kArr {
			return nil, c.errf(l, "%s is not an array", id.Name)
		}
		if rhs.k == kArr {
			return nil, c.errf(s, "an array value into an element")
		}
		return c.assignElement(l, id.Name, sy, rhs.asReal().r)
	}
	return nil, c.errf(s, "unsupported assignment target")
}

func (c *compiler) assignElement(l *syntax.Index, name string, s *sym, val realFn) (stmtFn, error) {
	var ix [2]intFn
	for i, a := range l.Args {
		fn, err := c.intExpr(a.Value)
		if err != nil {
			return nil, err
		}
		ix[i] = fn
	}
	slot := s.slot
	i0, i1 := ix[0], ix[1]
	if s.rank == 1 {
		return func(f *frame) {
			v := val(f)
			a := f.arrs[slot]
			if a == nil {
				unallocated(f, name)
			}
			i := i0(f) - 1
			if uint64(i) >= uint64(a.n1) {
				bounds(f, name, 1, i+1, a.n1)
			}
			a.data[i] = v
		}, nil
	}
	return func(f *frame) {
		v := val(f)
		a := f.arrs[slot]
		if a == nil {
			unallocated(f, name)
		}
		i, j := i0(f)-1, i1(f)-1
		if uint64(i) >= uint64(a.n1) {
			bounds(f, name, 1, i+1, a.n1)
		}
		if uint64(j) >= uint64(a.n2) {
			bounds(f, name, 2, j+1, a.n2)
		}
		a.data[int(i)+int(j)*a.n1] = v
	}, nil
}

// assignArray compiles a whole-array assignment. go-ndarray's *Into forms write
// the result of a top-level + - * / straight into the destination.
func (c *compiler) assignArray(at syntax.Node, s *sym, name string, rhs *ex) (stmtFn, error) {
	slot := s.slot
	dest := func(f *frame) *array {
		d := f.arrs[slot]
		if d == nil {
			fail(f, "array %s is not allocated", name)
		}
		return d
	}
	if rhs.k != kArr {
		v := rhs.asReal().r
		return func(f *frame) {
			d, x := dest(f), v(f)
			for i := range d.data {
				d.data[i] = x
			}
		}, nil
	}
	if rhs.rank != s.rank {
		return nil, c.errf(at, "rank %d assigned to rank %d", rhs.rank, s.rank)
	}
	if rhs.op == "+" || rhs.op == "-" || rhs.op == "*" || rhs.op == "/" {
		la, xa := rhs.l.a, rhs.x.a
		var into func(p, out, q *ndarray.Array) error
		switch rhs.op {
		case "+":
			into = (*ndarray.Array).AddInto
		case "-":
			into = (*ndarray.Array).SubInto
		case "*":
			into = (*ndarray.Array).MulInto
		default:
			into = (*ndarray.Array).DivInto
		}
		return func(f *frame) {
			d, p, q := dest(f), la(f), xa(f)
			conform(f, p, q)
			conform(f, d, p)
			if err := into(p.nd, d.nd, q.nd); err != nil {
				fail(f, "%v", err)
			}
		}, nil
	}
	res := rhs.a
	matmul := rhs.op == "matmul"
	return func(f *frame) {
		d, r := dest(f), res(f)
		conform(f, d, r)
		copy(d.data, r.data)
		if matmul {
			f.copied += int64(len(r.data))
		}
	}, nil
}

func (c *compiler) do(d *syntax.Do) (stmtFn, error) {
	if d.While != nil || d.Concurrent || !d.HasVar || d.Mask != nil {
		return nil, c.errf(d, "only counted do loops are supported")
	}
	sy, ok := c.syms[d.Var.Name]
	if !ok || sy.k != kInt || sy.isC {
		return nil, c.errf(d, "the do variable %s must be a declared integer", d.Var.Name)
	}
	lo, err := c.intExpr(d.Lo)
	if err != nil {
		return nil, err
	}
	hi, err := c.intExpr(d.Hi)
	if err != nil {
		return nil, err
	}
	step := intFn(func(*frame) int64 { return 1 })
	if d.Step != nil {
		if step, err = c.intExpr(d.Step); err != nil {
			return nil, err
		}
	}
	body, err := c.stmts(d.Body)
	if err != nil {
		return nil, err
	}
	slot := sy.slot
	run := func(f *frame) {
		for _, s := range body {
			s(f)
		}
	}
	if len(body) == 1 {
		run = body[0]
	}
	return func(f *frame) {
		l, h, st := lo(f), hi(f), step(f)
		if st == 0 {
			fail(f, "do step is zero")
		}
		n := (h - l + st) / st
		v := l
		for ; n > 0; n-- {
			f.ints[slot] = v
			run(f)
			v += st
		}
		f.ints[slot] = v
	}, nil
}

func (c *compiler) call(s *syntax.Call) (stmtFn, error) {
	id, ok := s.Proc.(*syntax.Ident)
	if !ok || id.Name != "cpu_time" || len(s.Args) != 1 {
		return nil, c.errf(s, "only call cpu_time(t) is supported")
	}
	t, ok := s.Args[0].Value.(*syntax.Ident)
	if !ok {
		return nil, c.errf(s, "cpu_time needs a real variable")
	}
	sy, ok := c.syms[t.Name]
	if !ok || sy.k != kReal || sy.isC {
		return nil, c.errf(s, "cpu_time needs a declared real(8) variable")
	}
	slot := sy.slot
	return func(f *frame) { f.reals[slot] = time.Since(f.start).Seconds() }, nil
}

func (c *compiler) allocate(a *syntax.Allocate) (stmtFn, error) {
	if a.Dealloc || a.Type != nil || len(a.Specs) > 0 {
		return nil, c.errf(a, "only plain allocate(name(extents)) is supported")
	}
	var fns []stmtFn
	for _, o := range a.Objects {
		ix, ok := o.(*syntax.Index)
		if !ok {
			return nil, c.errf(o, "allocate needs name(extents)")
		}
		id, ok := ix.X.(*syntax.Ident)
		if !ok {
			return nil, c.errf(o, "allocate needs a name")
		}
		sy, ok := c.syms[id.Name]
		if !ok || sy.k != kArr || sy.shape != nil {
			return nil, c.errf(o, "%s is not an allocatable array", id.Name)
		}
		if len(ix.Args) != sy.rank {
			return nil, c.errf(o, "%s has rank %d", id.Name, sy.rank)
		}
		var shape []intFn
		for _, ar := range ix.Args {
			if ar.Keyword != "" || ar.Value == nil {
				return nil, c.errf(o, "bad extent")
			}
			if _, tr := ar.Value.(*syntax.Triplet); tr {
				return nil, c.errf(o, "lower bounds other than 1 are not supported")
			}
			fn, err := c.intExpr(ar.Value)
			if err != nil {
				return nil, err
			}
			shape = append(shape, fn)
		}
		fns = append(fns, allocFn(sy.slot, shape))
	}
	return func(f *frame) {
		for _, fn := range fns {
			fn(f)
		}
	}, nil
}
