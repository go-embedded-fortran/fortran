package eval

import (
	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// program compiles the units of the file: one main program, its internal
// procedures and external subroutines and functions. Every unit is first
// declared (its variables get their slots, so that a call can be compiled
// before the body of its callee), then its body is compiled.
func (c *compiler) program() (*Program, error) {
	var main *syntax.Unit
	var order []*procCode
	for _, u := range c.file.Units {
		switch u.Kind {
		case syntax.ProgramUnit:
			if main != nil {
				return nil, c.errf(u, "exactly one main program is supported")
			}
			main = u
		case syntax.SubroutineUnit, syntax.FunctionUnit:
			c.externals[u.Ident.Name] = u
		case syntax.ModuleUnit:
		default:
			return nil, c.errf(u, "a %s is not supported yet", u.Kind)
		}
	}
	if main == nil {
		return nil, c.errf(nil, "exactly one main program is supported")
	}
	var add func(u *syntax.Unit, host *procCode) error
	add = func(u *syntax.Unit, host *procCode) error {
		sc := c.info.Scopes[u]
		if sc == nil {
			return c.errf(u, "the program unit was not analysed")
		}
		p := &procCode{name: u.Ident.Name, unit: u, scope: sc, host: host}
		if u.Kind == syntax.ModuleUnit {
			p.module, p.modIdx = true, len(c.modules)
			c.modules = append(c.modules, p)
		}
		c.procs[u] = p
		order = append(order, p)
		for _, cu := range u.Contains {
			if err := add(cu, p); err != nil {
				return err
			}
		}
		return nil
	}
	// modules first, in file order (a module is defined before it is used),
	// then the main program, then the external procedures
	for _, u := range c.file.Units {
		if u.Kind == syntax.ModuleUnit {
			if err := add(u, nil); err != nil {
				return nil, err
			}
		}
	}
	if err := add(main, nil); err != nil {
		return nil, err
	}
	pmain := c.procs[main]
	for _, u := range c.file.Units {
		if u != main && u.Kind != syntax.ModuleUnit {
			if err := add(u, nil); err != nil {
				return nil, err
			}
		}
	}
	for _, p := range order {
		if err := c.declareProc(p); err != nil {
			return nil, err
		}
	}
	for _, p := range order {
		if err := c.compileProc(p); err != nil {
			return nil, err
		}
	}
	return &Program{main: pmain, mods: c.modules}, nil
}

func (c *compiler) enter(p *procCode) {
	c.cur, c.scope, c.loops, c.jumped = p, p.scope, nil, false
}

// declareProc compiles the specification part of a unit and fixes its dummy
// arguments and result.
func (c *compiler) declareProc(p *procCode) error {
	c.enter(p)
	for _, s := range p.unit.Body {
		if isSpec(s) {
			var none []stmtFn
			if err := c.stmt(s, &none); err != nil {
				return err
			}
		}
	}
	for _, name := range p.unit.Params {
		if name.Name == "*" {
			return c.errf(p.unit, "alternate returns are not supported")
		}
		ss := p.scope.Names[name.Name]
		if ss == nil {
			return c.errf(p.unit, "dummy argument %s was not analysed", name.Name)
		}
		if _, ok := c.syms[ss]; !ok {
			if err := c.declSym(ss, p.unit, nil); err != nil {
				return err
			}
		}
		s := c.syms[ss]
		if s.isC || s.k == kArr {
			return c.errf(p.unit, "dummy argument %s: only scalar integer, real, logical and character dummy arguments are supported yet", name.Name)
		}
		intent := ss.Intent
		if ss.Flags&sema.FValue != 0 {
			// F2018 8.5.18: a VALUE dummy is a copy of the actual made on entry,
			// and nothing it becomes is copied back (value_7, pr57910 of
			// gfortran.dg); scalars only, as the copy of an object is not built
			if s.k != kInt && s.k != kReal && s.k != kLog && s.k != kStr {
				return c.errf(p.unit, "dummy argument %s: the VALUE attribute is supported on scalar integer, real, logical and character dummies only", name.Name)
			}
			intent = "value"
		}
		if ss.Flags&sema.FOptional != 0 {
			// (pointer and allocatable dummies are refused by declSym)
			if s.k != kInt && s.k != kReal && s.k != kLog && s.k != kStr {
				return c.errf(p.unit, "dummy argument %s: OPTIONAL is supported on scalar integer, real, logical and character dummies only", name.Name)
			}
			s.opt, s.pres = true, p.nInts
			p.nInts++
		}
		p.params = append(p.params, s)
		p.intent = append(p.intent, intent)
	}
	if p.unit.Kind == syntax.FunctionUnit {
		for _, ss := range p.scope.Order {
			if ss.Flags&sema.FResult == 0 {
				continue
			}
			if _, ok := c.syms[ss]; !ok {
				if err := c.declSym(ss, p.unit, nil); err != nil {
					return err
				}
			}
			s := c.syms[ss]
			if s.isC || s.k == kArr {
				return c.errf(p.unit, "function %s: only scalar integer, real, logical and character results are supported yet", p.name)
			}
			p.result = s
		}
		if p.result == nil {
			return c.errf(p.unit, "function %s has no result variable", p.name)
		}
	}
	// A variable typed by the implicit rules has no declaration statement to
	// give it storage: give it here, once the unit's declarations are compiled
	// (the analyser typed it; without this the compiler refused it as "not
	// declared" whatever ImplicitTyping said).
	for _, ss := range p.scope.Order {
		if ss.Kind != sema.SymVar || ss.Flags&sema.FImplicit == 0 {
			continue
		}
		if _, ok := c.syms[ss]; !ok {
			if err := c.declSym(ss, p.unit, nil); err != nil {
				return err
			}
		}
	}
	// the bounds of explicit-shape dummies, which may name variables declared
	// after them ("integer :: a(n), n"): every name has its storage now
	for _, fn := range p.afterDecls {
		if err := fn(); err != nil {
			return err
		}
	}
	p.afterDecls = nil
	p.declared = true
	return nil
}

// isSpec reports a specification statement, compiled by declareProc.
func isSpec(s syntax.Stmt) bool {
	switch s.(type) {
	case *syntax.Decl, *syntax.ImplicitStmt, *syntax.UseStmt, *syntax.Interface, *syntax.AttrStmt, *syntax.KeywordStmt, *syntax.TypeDef:
		return true
	}
	return false
}

func (c *compiler) compileProc(p *procCode) error {
	c.enter(p)
	var body []syntax.Stmt
	for _, s := range p.unit.Body {
		if !isSpec(s) {
			body = append(body, s)
		}
	}
	fn, _, err := c.block(body)
	if err != nil {
		return err
	}
	p.body = fn
	return nil
}

// ---- host association ----

// hops is the number of static links between the unit being compiled and the
// unit that owns the variable s.
func (c *compiler) hops(s *sym, at syntax.Node) (int, error) {
	if s.owner == c.cur {
		return 0, nil
	}
	if s.owner.module {
		return -1 - s.owner.modIdx, nil // a module variable: the module's frame
	}
	n := 0
	for p := c.cur; p != nil; p = p.host {
		if p == s.owner {
			return n, nil
		}
		n++
	}
	return 0, c.errf(at, "a variable of another program unit")
}

// hop reaches the frame of a variable's owner: n static links up for n >= 0,
// the frame of module -1-n for n < 0.
func (f *frame) hop(n int) *frame {
	if n < 0 {
		return f.mods[-1-n]
	}
	for ; n > 0; n-- {
		f = f.up
	}
	return f
}

// at is the frame of a variable's owner, h static links up as c.hops counted
// them (0: this frame).
func (f *frame) at(h int) *frame {
	if h != 0 {
		return f.hop(h)
	}
	return f
}

// hostRead is the read of a scalar variable of an enclosing unit.
func hostRead(s *sym, n int) *ex {
	slot := s.slot
	switch s.k {
	case kStr:
		return &ex{k: kStr, slot: -1, s: func(f *frame) string { return f.hop(n).strs[slot] }}
	case kInt:
		return &ex{k: kInt, kd: s.kd, slot: -1, aff: true, avs: slot, avh: n, i: func(f *frame) int64 { return f.hop(n).ints[slot] }, pl: varPlace(s, n)}
	case kLog:
		return &ex{k: kLog, slot: -1, b: func(f *frame) bool { return f.hop(n).ints[slot] != 0 }, pl: varPlace(s, n)}
	}
	return &ex{k: kReal, kd: s.kd, slot: -1, r: func(f *frame) float64 { return f.hop(n).reals[slot] }, pl: varPlace(s, n)}
}

// hostStore is the assignment to a scalar variable of an enclosing unit.
func (c *compiler) hostStore(s *sym, v *ex, n int, at syntax.Node) (stmtFn, error) {
	slot, kd := s.slot, s.kd
	switch {
	case s.k == kInt && v.k == kInt:
		fn := v.i
		return func(f *frame) { x := fn(f); f.hop(n).ints[slot] = narrow(kd, x) }, nil
	case s.k == kInt && v.k == kReal:
		fn := v.r
		return func(f *frame) { x := fn(f); f.hop(n).ints[slot] = narrow(kd, int64(x)) }, nil
	case s.k == kLog && v.k == kLog:
		fn := v.b
		return func(f *frame) { x := fn(f); f.hop(n).ints[slot] = b2i(x) }, nil
	case s.k == kStr && v.k == kStr:
		fn := v.s
		return func(f *frame) {
			x, h := fn(f), f.hop(n)
			h.strs[slot] = s.fit(h, x)
		}, nil
	case s.k == kReal && (v.k == kReal || v.k == kInt):
		fn := v.asReal().r
		return func(f *frame) {
			x := fn(f)
			if kd == 4 {
				x = float64(float32(x))
			}
			f.hop(n).reals[slot] = x
		}, nil
	}
	return nil, c.errf(at, "this assignment to a variable of an enclosing unit is not supported")
}

// ---- calls ----

// binding is one actual argument of a call: it is evaluated in the caller's
// frame into the dummy's slot of the callee's (copy-in), and an actual that is
// a variable gets the dummy's final value back (copy-out) unless the dummy is
// intent(in) or value.
type binding struct {
	set  func(caller, callee *frame)
	out  func(caller, callee *frame) // after every set: what intent(out) does to the actual
	back func(caller, callee *frame)
}

// callee is the compiled procedure a procedure symbol denotes.
func (c *compiler) callee(at syntax.Node, ss *sema.Symbol) (*procCode, error) {
	if ss == nil {
		return nil, c.errf(at, "the procedure is not defined in this file")
	}
	var p *procCode
	if ss.Unit != nil {
		p = c.procs[ss.Unit]
	} else if u := c.externals[ss.Name]; u != nil { // an external procedure of the file, called without an interface
		p = c.procs[u]
	}
	if p == nil && ss.Unit == nil {
		return nil, c.errf(at, "the procedure %s is not defined in this file", ss.Name)
	}
	if p == nil || !p.declared {
		return nil, c.errf(at, "the procedure %s is not available to the evaluator", ss.Name)
	}
	return p, nil
}

// invoker compiles the call of p with args: the function that runs the call
// and returns the frame of the callee (for the result).
func (c *compiler) invoker(at syntax.Node, p *procCode, args []syntax.Arg) (func(*frame) *frame, error) {
	if len(args) > len(p.params) {
		return nil, c.errf(at, "%s takes %d arguments, %d given", p.name, len(p.params), len(args))
	}
	actual := make([]*syntax.Arg, len(p.params))
	pos := true
	for i := range args {
		a := &args[i]
		if a.AltReturn != "" || a.Value == nil {
			return nil, c.errf(at, "an alternate return is not supported")
		}
		if a.Keyword == "" {
			if !pos {
				return nil, c.errf(at, "a positional argument follows a keyword argument")
			}
			actual[i] = a
			continue
		}
		pos = false
		found := false
		for j, name := range p.unit.Params {
			if name.Name == a.Keyword {
				if actual[j] != nil {
					return nil, c.errf(at, "dummy argument %s is associated twice", a.Keyword)
				}
				actual[j], found = a, true
			}
		}
		if !found {
			return nil, c.errf(at, "%s has no dummy argument %s", p.name, a.Keyword)
		}
	}
	// the static link of the callee: the frame of its host, found from here
	n, hasUp := 0, p.host != nil
	switch {
	case p.host != nil && p.host.module:
		n = -1 - p.host.modIdx
	case p.host != nil:
		for h := c.cur; h != p.host; h = h.host {
			if h == nil {
				return nil, c.errf(at, "%s is an internal procedure of another unit", p.name)
			}
			n++
		}
	}
	binds := make([]binding, 0, len(p.params))
	for i, ps := range p.params {
		if actual[i] == nil {
			// an optional dummy that is not present (the analyser refuses a
			// missing nonoptional one): its presence stays 0, nothing is copied
			continue
		}
		b, err := c.bind(at, ps, p.intent[i], actual[i].Value)
		if err != nil {
			return nil, err
		}
		if ps.opt {
			b = c.withPresence(ps, actual[i].Value, b)
		}
		binds = append(binds, b)
	}
	return func(f *frame) *frame {
		var up *frame
		if hasUp {
			up = f.hop(n)
		}
		cf := p.newFrame(f, up)
		for i := range binds {
			binds[i].set(f, cf)
		}
		for i := range binds {
			if binds[i].out != nil {
				binds[i].out(f, cf)
			}
		}
		p.run(cf)
		f.bud.exit(cf)
		f.copied += cf.copied
		for i := range binds {
			if binds[i].back != nil {
				binds[i].back(f, cf)
			}
		}
		return cf
	}, nil
}

// withPresence makes the binding b also give the optional dummy ps its
// presence (F2018 15.5.2.12): the presence of the actual when it is itself an
// optional dummy (an absent one passed on stays absent, p4); for a scalar
// pointer actual, whether it is associated (a disassociated one makes the
// dummy absent, p1, and is then neither read nor written back:
// pointer_check_9 of gfortran.dg); present otherwise.
func (c *compiler) withPresence(ps *sym, a syntax.Expr, b binding) binding {
	pres, set, back := ps.pres, b.set, b.back
	if id, ok := a.(*syntax.Ident); ok {
		if as, _, err := c.symOf(id); err == nil && (as.opt || as.ptr && as.rank == 0) {
			if h, err := c.hops(as, id); err == nil {
				if as.ptr {
					pslot := as.pslot
					b.set = func(f, cf *frame) {
						if f.at(h).ptrs[pslot] != nil {
							set(f, cf)
							cf.ints[pres] = 1
						}
					}
					if back != nil {
						b.back = func(f, cf *frame) {
							if cf.ints[pres] != 0 {
								back(f, cf)
							}
						}
					}
					return b
				}
				from := as.pres
				b.set = func(f, cf *frame) {
					set(f, cf)
					cf.ints[pres] = f.at(h).ints[from]
				}
				return b
			}
		}
	}
	b.set = func(f, cf *frame) {
		set(f, cf)
		cf.ints[pres] = 1
	}
	return b
}

// intrinsicFirstOptional is the position of the first optional argument of
// the intrinsics that have one (F2018 16.9), kind arguments aside (they are
// constants, never a dummy).
var intrinsicFirstOptional = map[string]int{
	"size": 1, "lbound": 1, "ubound": 1, "shape": 1,
	"sum": 1, "product": 1, "maxval": 1, "minval": 1, "maxloc": 1, "minloc": 1,
	"any": 1, "all": 1, "count": 1, "findloc": 2, "norm2": 1, "iany": 1, "iall": 1, "iparity": 1, "parity": 1,
	"pack": 2, "reshape": 2, "cshift": 2, "eoshift": 2, "unpack": 3,
	"min": 2, "max": 2, "index": 2, "scan": 2, "verify": 2, "ishftc": 2,
	"associated": 1,
}

// noOptionalAtOptional refuses an optional dummy argument passed to an
// optional argument of an intrinsic: absent, the intrinsic's argument is
// absent too (F2018 15.5.2.12 p4), which the compiled call cannot express;
// size(a, dim) with dim absent took dim as 0 (size_optional_dim_1 of
// gfortran.dg). A keyword argument is taken as optional.
func (c *compiler) noOptionalAtOptional(n *syntax.Index, name string) error {
	first, ok := intrinsicFirstOptional[name]
	if !ok {
		return nil
	}
	for i, a := range n.Args {
		id, isID := a.Value.(*syntax.Ident)
		if !isID || i < first && a.Keyword == "" {
			continue
		}
		if s, _, err := c.symOf(id); err == nil && s.opt {
			return c.errf(id, "an optional dummy argument passed to an optional argument of %s is not supported yet", name)
		}
	}
	return nil
}

// present compiles PRESENT(a): a is an optional dummy argument of this unit
// or of a host.
func (c *compiler) present(n *syntax.Index) (*ex, error) {
	if len(n.Args) != 1 || n.Args[0].Value == nil {
		return nil, c.errf(n, "present takes one argument")
	}
	var s *sym
	err := c.errf(n, "the argument of present must be an optional dummy argument")
	id, isID := n.Args[0].Value.(*syntax.Ident)
	if isID {
		s, _, err = c.symOf(id)
	}
	if err == nil && !s.opt {
		err = c.errf(id, "%s is not an optional dummy argument supported by the evaluator", id.Name)
	}
	var h int
	if err == nil {
		h, err = c.hops(s, id)
	}
	if err != nil {
		return nil, err
	}
	pres := s.pres
	return &ex{k: kLog, slot: -1, b: func(f *frame) bool { return f.at(h).ints[pres] != 0 }}, nil
}

// bind compiles the association of the actual argument a with the dummy ps.
func (c *compiler) bind(at syntax.Node, ps *sym, intent string, a syntax.Expr) (binding, error) {
	v, err := c.expr(a)
	if err != nil {
		return binding{}, err
	}
	if v.k != ps.k {
		return binding{}, c.errf(a, "the actual argument and its dummy argument differ in type (a type conversion is not part of argument association)")
	}
	slot := ps.slot
	var b binding
	// intent is "value" for a VALUE dummy: copied in like any scalar, never back
	if v.ps != nil && intent != "in" && intent != "value" && (ps.k == kInt || ps.k == kLog || ps.k == kReal) {
		// a reference to a function with a pointer result is a variable (F2018
		// 9.2): the function runs once, and the dummy is copied in from its
		// target and out to it (ptr-func-4 of gfortran.dg)
		tmp := c.cur.nPtrs
		c.cur.nPtrs++
		target, isReal := v.ps, ps.k == kReal
		b.set = func(f, cf *frame) {
			t := target(f)
			if t == nil {
				fail(f, "the actual argument is a disassociated pointer result")
			}
			f.ptrs[tmp] = t
			if isReal {
				cf.reals[slot] = t.getReal()
			} else {
				cf.ints[slot] = t.getInt()
			}
		}
		b.back = func(f, cf *frame) {
			if isReal {
				f.ptrs[tmp].setReal(cf.reals[slot])
			} else {
				f.ptrs[tmp].setInt(cf.ints[slot])
			}
		}
		return b, nil
	}
	switch ps.k {
	case kD:
		// a derived-type dummy is associated with the actual object itself
		if v.dt != ps.dt {
			return binding{}, c.errf(a, "the actual argument is not of type %s", ps.dt.name)
		}
		fn := v.d
		if _, paren := a.(*syntax.Paren); paren {
			// (x) is a value, not the variable x: the dummy gets a copy, so
			// that what the call does to x (another argument, intent(out))
			// does not reach it (interface_assignment_1 of gfortran.dg)
			obj, dt := fn, v.dt
			fn = func(f *frame) *dobj {
				t := dt.newObj(f, nil)
				t.assign(f, obj(f))
				return t
			}
		}
		b.set = func(f, cf *frame) { cf.dobjs[slot] = fn(f) }
		if intent == "out" {
			// after every actual is copied in: call s(e, (e)) copies e
			// before the intent(out) of the first dummy resets it
			b.out = func(f, cf *frame) {
				if o := cf.dobjs[slot]; o != nil {
					o.becomeOut(f)
				}
			}
		}
		return b, nil
	case kGA:
		// the dummy shares the actual's elements, with lower bounds 1; only a
		// whole array (or a value that is not defined) can be shared
		if id, ok := a.(*syntax.Ident); !ok || v.v == nil || !v.v.ga {
			if intent != "in" {
				return binding{}, c.errf(a, "an array section or expression can only be passed to an intent(in) dummy argument yet")
			}
			_ = id
		}
		fn := v.g
		b.set = func(f, cf *frame) {
			g := fn(f)
			cf.gas[slot] = &garr{et: g.et, kd: g.kd, lb: ones(len(g.ext)), ext: g.ext, i: g.i, r: g.r}
		}
		return b, nil
	case kStr:
		fn, n := v.s, ps.clen
		if n < 0 { // character(*): the dummy takes the length of the actual
			b.set = func(f, cf *frame) { cf.strs[slot] = fn(f) }
		} else {
			b.set = func(f, cf *frame) { cf.strs[slot] = fitLen(fn(f), n) }
		}
	case kInt:
		fn := v.i
		b.set = func(f, cf *frame) { cf.ints[slot] = fn(f) }
	case kLog:
		fn := v.b
		b.set = func(f, cf *frame) { cf.ints[slot] = b2i(fn(f)) }
	default:
		fn := v.r
		b.set = func(f, cf *frame) { cf.reals[slot] = fn(f) }
	}
	// copy-out to a component or an element, through its place
	if v.pl != nil && intent != "in" && intent != "value" && c.isDesignator(a) {
		return c.placeBinding(v.pl, ps), nil
	}
	// copy-out: the actual is a plain variable and the dummy can be defined
	id, ok := a.(*syntax.Ident)
	if !ok || intent == "in" || intent == "value" {
		return b, nil
	}
	as, _, err := c.symOf(id)
	if err != nil || as.isC || as.k != ps.k {
		return b, nil
	}
	var tmp int
	var read *ex
	switch ps.k {
	case kStr:
		tmp = c.cur.nStrs
		c.cur.nStrs++
		read = &ex{k: kStr, slot: -1, s: func(f *frame) string { return f.strs[tmp] }}
	case kInt:
		tmp = c.cur.nInts
		c.cur.nInts++
		read = &ex{k: kInt, kd: ps.kd, slot: -1, i: func(f *frame) int64 { return f.ints[tmp] }}
	case kLog:
		tmp = c.cur.nInts
		c.cur.nInts++
		read = &ex{k: kLog, slot: -1, b: func(f *frame) bool { return f.ints[tmp] != 0 }}
	default:
		tmp = c.cur.nReals
		c.cur.nReals++
		read = &ex{k: kReal, kd: ps.kd, slot: -1, r: func(f *frame) float64 { return f.reals[tmp] }}
	}
	st, err := c.storeVar(as, read, id)
	if err != nil {
		return binding{}, err
	}
	switch ps.k {
	case kReal:
		b.back = func(f, cf *frame) { f.reals[tmp] = cf.reals[slot]; st(f) }
	case kStr:
		b.back = func(f, cf *frame) { f.strs[tmp] = cf.strs[slot]; st(f) }
	default:
		b.back = func(f, cf *frame) { f.ints[tmp] = cf.ints[slot]; st(f) }
	}
	return b, nil
}

// isDesignator reports an actual argument that is a component (x%b) or a
// subscripted variable (a(i)): (x), +x and a function reference are values,
// even when their expression is the variable's own (an intrinsic may hand its
// argument back), and nothing is copied out to them.
func (c *compiler) isDesignator(a syntax.Expr) bool {
	switch x := a.(type) {
	case *syntax.Component:
		return true
	case *syntax.Index:
		return !c.isFuncRef(x)
	}
	return false
}

// placeBinding copies the dummy ps in from the place loc fixes when the call
// starts (a(i) is the element of the i of that moment, whatever the call does
// to i), and back out to that same place when it returns.
func (c *compiler) placeBinding(loc func(*frame) *sref, ps *sym) binding {
	tmp := c.cur.nPtrs
	c.cur.nPtrs++
	slot, n := ps.slot, ps.clen
	var b binding
	switch ps.k {
	case kStr:
		b.set = func(f, cf *frame) {
			r := loc(f)
			f.ptrs[tmp] = r
			s := r.o.v[r.ci].s
			if n >= 0 {
				s = fitLen(s, n)
			}
			cf.strs[slot] = s
		}
		// a character component keeps its length
		b.back = func(f, cf *frame) { r := f.ptrs[tmp]; r.o.v[r.ci].s = fitLen(cf.strs[slot], int64(len(r.o.v[r.ci].s))) }
	case kReal:
		b.set = func(f, cf *frame) { r := loc(f); f.ptrs[tmp] = r; cf.reals[slot] = r.getReal() }
		b.back = func(f, cf *frame) { f.ptrs[tmp].setReal(cf.reals[slot]) }
	default: // integer and logical
		b.set = func(f, cf *frame) { r := loc(f); f.ptrs[tmp] = r; cf.ints[slot] = r.getInt() }
		b.back = func(f, cf *frame) { f.ptrs[tmp].setInt(cf.ints[slot]) }
	}
	return b
}

// storeVar compiles an assignment to a scalar variable of this unit or of an
// enclosing one.
func (c *compiler) storeVar(s *sym, v *ex, at syntax.Node) (stmtFn, error) {
	if s.ptr && !s.ga {
		return c.ptrStore(s, v, at)
	}
	n, err := c.hops(s, at)
	if err != nil {
		return nil, err
	}
	if n != 0 {
		return c.hostStore(s, v, n, at)
	}
	return c.store(s, v, at)
}

// callStmt compiles CALL of a procedure defined in the file.
func (c *compiler) callStmt(s *syntax.Call, ss *sema.Symbol) (stmtFn, error) {
	p, err := c.callee(s, ss)
	if err != nil {
		return nil, err
	}
	if p.unit.Kind != syntax.SubroutineUnit {
		return nil, c.errf(s, "%s is not a subroutine", p.name)
	}
	run, err := c.invoker(s, p, s.Args)
	if err != nil {
		return nil, err
	}
	return func(f *frame) { run(f) }, nil
}

// funcCall compiles a reference to a function defined in the file.
func (c *compiler) funcCall(n *syntax.Index, ss *sema.Symbol) (*ex, error) {
	p, err := c.callee(n, ss)
	if err != nil {
		return nil, err
	}
	return c.funcCallArgs(n, p, n.Args)
}

func (c *compiler) funcCallArgs(n *syntax.Index, p *procCode, args []syntax.Arg) (*ex, error) {
	if p.unit.Kind != syntax.FunctionUnit || p.result == nil {
		return nil, c.errf(n, "%s is not a function", p.name)
	}
	run, err := c.invoker(n, p, args)
	if err != nil {
		return nil, err
	}
	r := p.result
	slot := r.slot
	if r.ptr && !r.ga {
		// a scalar pointer result: its target is in the callee's frame.ptrs
		ps, name := r.pslot, p.name
		target := func(f *frame) *sref { return run(f).ptrs[ps] }
		get := func(f *frame) *sref {
			t := target(f)
			if t == nil {
				fail(f, "the result of %s is a disassociated pointer", name)
			}
			return t
		}
		switch r.k {
		case kInt:
			return &ex{k: kInt, kd: r.kd, slot: -1, ps: target, i: func(f *frame) int64 { return get(f).getInt() }}, nil
		case kLog:
			return &ex{k: kLog, slot: -1, ps: target, b: func(f *frame) bool { return get(f).getInt() != 0 }}, nil
		}
		return &ex{k: kReal, kd: r.kd, slot: -1, ps: target, r: func(f *frame) float64 { return get(f).getReal() }}, nil
	}
	switch r.k {
	case kD:
		return &ex{k: kD, slot: -1, dt: r.dt, d: func(f *frame) *dobj { return run(f).dobjs[slot] }}, nil
	case kGA:
		name := p.name
		e := &ex{k: kGA, slot: -1, rank: r.rank, et: r.et, kd: r.kd, g: func(f *frame) *garr {
			g := run(f).gas[slot]
			if g == nil {
				fail(f, "the result of %s is not allocated", name)
			}
			return g
		}}
		if r.ptr {
			// an array pointer result: its target, for p => f()
			e.pg = func(f *frame) *garr { return run(f).gas[slot] }
		}
		return e, nil
	case kStr:
		return &ex{k: kStr, slot: -1, s: func(f *frame) string { return run(f).strs[slot] }}, nil
	case kInt:
		return &ex{k: kInt, kd: r.kd, slot: -1, i: func(f *frame) int64 { return run(f).ints[slot] }}, nil
	case kLog:
		return &ex{k: kLog, slot: -1, b: func(f *frame) bool { return run(f).ints[slot] != 0 }}, nil
	}
	return &ex{k: kReal, kd: r.kd, slot: -1, r: func(f *frame) float64 { return run(f).reals[slot] }}, nil
}

// returnStmt compiles RETURN.
func (c *compiler) returnStmt(s *syntax.Return) (stmtFn, error) {
	if s.Alt != nil {
		return nil, c.errf(s, "an alternate return is not supported")
	}
	c.jumped = true
	return func(f *frame) { f.ctl = ctlReturn }, nil
}
