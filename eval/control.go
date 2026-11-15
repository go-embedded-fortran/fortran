package eval

import (
	"strconv"

	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// seq runs a block of statements in order. A block that can leave through EXIT
// or CYCLE (jumps) stops at the first statement that sets frame.ctl; the others
// keep the plain loop, so a loop body without a jump pays nothing for them.
func seq(fns []stmtFn, jumps bool) stmtFn {
	switch {
	case len(fns) == 0:
		return func(*frame) {}
	case len(fns) == 1:
		return fns[0]
	case !jumps:
		return func(f *frame) {
			for _, s := range fns {
				s(f)
			}
		}
	}
	return func(f *frame) {
		for _, s := range fns {
			s(f)
			if f.ctl != ctlNone {
				return
			}
		}
	}
}

// block compiles a nested block of statements and reports whether it can set
// frame.ctl (contains EXIT or CYCLE); the enclosing blocks then check it too.
func (c *compiler) block(body []syntax.Stmt) (stmtFn, bool, error) {
	saved := c.jumped
	c.jumped = false
	if c.cur != nil { // the deepest nesting of statements of the unit: its call weight
		c.cur.nest++
		c.cur.maxNest = max(c.cur.maxNest, c.cur.nest)
		defer func() { c.cur.nest-- }()
	}
	fns, err := c.stmts(body)
	got := c.jumped
	c.jumped = saved || got
	if err != nil {
		return nil, false, err
	}
	return seq(fns, got), got, nil
}

// boolExpr compiles a logical expression.
func (c *compiler) boolExpr(n syntax.Expr) (boolFn, error) {
	e, err := c.expr(n)
	if err != nil {
		return nil, err
	}
	if e.k != kLog {
		return nil, c.errf(n, "a logical expression is required")
	}
	return e.b, nil
}

func (c *compiler) ifConstruct(s *syntax.If) (stmtFn, error) {
	type arm struct {
		cond boolFn
		body stmtFn
	}
	var arms []arm
	conds := []syntax.Expr{s.Cond}
	bodies := [][]syntax.Stmt{s.Then}
	for _, ei := range s.ElseIfs {
		conds = append(conds, ei.Cond)
		bodies = append(bodies, ei.Body)
	}
	for i, cn := range conds {
		cond, err := c.boolExpr(cn)
		if err != nil {
			return nil, err
		}
		body, _, err := c.block(bodies[i])
		if err != nil {
			return nil, err
		}
		arms = append(arms, arm{cond, body})
	}
	var els stmtFn
	if s.HasElse {
		var err error
		if els, _, err = c.block(s.Else); err != nil {
			return nil, err
		}
	}
	if len(arms) == 1 {
		cond, then := arms[0].cond, arms[0].body
		if els == nil {
			return func(f *frame) {
				if cond(f) {
					then(f)
				}
			}, nil
		}
		return func(f *frame) {
			if cond(f) {
				then(f)
			} else {
				els(f)
			}
		}, nil
	}
	return func(f *frame) {
		for _, a := range arms {
			if a.cond(f) {
				a.body(f)
				return
			}
		}
		if els != nil {
			els(f)
		}
	}, nil
}

func (c *compiler) logicalIf(s *syntax.LogicalIf) (stmtFn, error) {
	cond, err := c.boolExpr(s.Cond)
	if err != nil {
		return nil, err
	}
	var one []stmtFn
	if err := c.stmt(s.Stmt, &one); err != nil {
		return nil, err
	}
	if len(one) != 1 {
		return nil, c.errf(s, "the statement of a logical IF compiled to %d actions", len(one))
	}
	body := one[0]
	return func(f *frame) {
		if cond(f) {
			body(f)
		}
	}, nil
}

// do compiles a DO construct: counted, DO WHILE or unbounded. DO CONCURRENT is
// refused.
func (c *compiler) do(d *syntax.Do) (stmtFn, error) {
	if d.Concurrent || d.Mask != nil {
		return nil, c.errf(d, "DO CONCURRENT is not supported")
	}
	var (
		sy       *sym
		lo, hi   intFn
		step     = intFn(func(*frame) int64 { return 1 })
		while    boolFn
		err      error
		counted  = d.HasVar && d.While == nil
		unbound  = !d.HasVar && d.While == nil
		slot     int
		vh       int // the hops to the frame of the do variable
		hasStepE = d.Step != nil
	)
	switch {
	case counted:
		var ss *sema.Symbol
		sy, ss, err = c.symOf(&d.Var)
		if err != nil || sy.k != kInt || sy.isC || ss == nil {
			return nil, c.errf(d, "the do variable %s must be a declared integer", d.Var.Name)
		}
		slot = sy.slot
		if vh, err = c.hops(sy, d); err != nil {
			return nil, err
		}
		if lo, err = c.intExpr(d.Lo); err != nil {
			return nil, err
		}
		if hi, err = c.intExpr(d.Hi); err != nil {
			return nil, err
		}
		if hasStepE {
			if step, err = c.intExpr(d.Step); err != nil {
				return nil, err
			}
		}
	case d.While != nil:
		if while, err = c.boolExpr(d.While); err != nil {
			return nil, err
		}
	case !unbound:
		return nil, c.errf(d, "this form of DO is not supported")
	}
	c.loops = append(c.loops, d.Name)
	body, jumps, err := c.block(d.Body)
	c.loops = c.loops[:len(c.loops)-1]
	if err != nil {
		return nil, err
	}
	name := d.Name
	// leave handles frame.ctl after the body: it reports whether the loop ends
	// and whether the signal belongs to an enclosing construct.
	leave := func(f *frame) (stop bool) {
		if f.ctl == ctlReturn || (f.target != "" && f.target != name) {
			return true // an outer construct's EXIT or CYCLE
		}
		ctl := f.ctl
		f.ctl, f.target = ctlNone, ""
		return ctl == ctlExit
	}
	var vec *vecKernel
	if counted && !hasStepE && !jumps {
		vec = c.vecLoop(d, sy, vh)
	}
	switch {
	case counted:
		return func(f *frame) {
			l, h, st := lo(f), hi(f), step(f)
			if st == 0 {
				fail(f, "do step is zero")
			}
			n := (h - l + st) / st
			v := l
			vf := f.at(vh) // the frame of the do variable: a session's variable is a module's
			if vec != nil && n >= vecMin && vec.run(f, l, n, f.bud) {
				vf.ints[slot] = l + n
				return
			}
			if !jumps {
				for ; n > 0; n-- {
					f.bud.tick()
					vf.ints[slot] = v
					body(f)
					v += st
				}
				vf.ints[slot] = v
				return
			}
			for ; n > 0; n-- {
				f.bud.tick()
				vf.ints[slot] = v
				body(f)
				if f.ctl != ctlNone && leave(f) {
					return
				}
				v += st
			}
			vf.ints[slot] = v
		}, nil
	case while != nil:
		return func(f *frame) {
			for while(f) {
				f.bud.tick()
				body(f)
				if f.ctl != ctlNone && leave(f) {
					return
				}
			}
		}, nil
	}
	return func(f *frame) {
		for {
			f.bud.tick()
			body(f)
			if f.ctl != ctlNone && leave(f) {
				return
			}
		}
	}, nil
}

func (c *compiler) jump(target string, ctl int, at syntax.Node, what string) (stmtFn, error) {
	if len(c.loops) == 0 {
		return nil, c.errf(at, "%s outside a DO construct", what)
	}
	if target != "" {
		found := false
		for _, n := range c.loops {
			found = found || n == target
		}
		if !found {
			return nil, c.errf(at, "%s names %q, which is not an enclosing DO construct", what, target)
		}
	}
	c.jumped = true
	return func(f *frame) { f.ctl, f.target = ctl, target }, nil
}

// selectCase compiles SELECT CASE on an integer or logical selector (F2018
// 11.1.9.2): a case selector is a value or a range, a case is chosen when its
// selector contains the value, and CASE DEFAULT when no other does.
func (c *compiler) selectCase(s *syntax.Select) (stmtFn, error) {
	x, err := c.expr(s.Expr)
	if err != nil {
		return nil, err
	}
	if x.k != kInt && x.k != kLog {
		return nil, c.errf(s.Expr, "SELECT CASE is supported on integer and logical selectors")
	}
	val := x.i
	if x.k == kLog {
		b := x.b
		val = func(f *frame) int64 {
			if b(f) {
				return 1
			}
			return 0
		}
	}
	type sel struct {
		lo, hi       int64
		hasLo, hasHi bool
	}
	type arm struct {
		sels []sel
		body stmtFn
	}
	var arms []arm
	var def stmtFn
	for _, cs := range s.Cases {
		body, _, err := c.block(cs.Body)
		if err != nil {
			return nil, err
		}
		if cs.Default {
			def = body
			continue
		}
		a := arm{body: body}
		for _, e := range cs.Selectors {
			var sl sel
			bound := func(n syntax.Expr) (int64, error) {
				tv, err := c.tv(n)
				if err != nil {
					return 0, err
				}
				if tv.Const == nil {
					return 0, c.errf(n, "a case selector is a constant expression")
				}
				if tv.Const.Type.Basic == sema.Logical {
					if tv.Const.B {
						return 1, nil
					}
					return 0, nil
				}
				if tv.Const.Type.Basic != sema.Integer {
					return 0, c.errf(n, "a case selector of type %s is not supported", tv.Const.Type)
				}
				return tv.Const.I, nil
			}
			if tr, ok := e.(*syntax.Triplet); ok {
				if tr.Stride != nil {
					return nil, c.errf(e, "a case range has no stride")
				}
				if tr.Lo != nil {
					if sl.lo, err = bound(tr.Lo); err != nil {
						return nil, err
					}
					sl.hasLo = true
				}
				if tr.Hi != nil {
					if sl.hi, err = bound(tr.Hi); err != nil {
						return nil, err
					}
					sl.hasHi = true
				}
			} else {
				v, err := bound(e)
				if err != nil {
					return nil, err
				}
				sl = sel{lo: v, hi: v, hasLo: true, hasHi: true}
			}
			a.sels = append(a.sels, sl)
		}
		arms = append(arms, a)
	}
	return func(f *frame) {
		v := val(f)
		for i := range arms {
			for _, sl := range arms[i].sels {
				if (!sl.hasLo || v >= sl.lo) && (!sl.hasHi || v <= sl.hi) {
					arms[i].body(f)
					return
				}
			}
		}
		if def != nil {
			def(f)
		}
	}, nil
}

// stop compiles STOP and ERROR STOP (F2018 11.4). The stop code is a
// default-integer or character constant expression, or an integer expression
// for the run time; the text on standard error and the exit status are those
// of libgfortran (the goldens are the oracle): "STOP n" and exit status n, "STOP
// message" and status 0, "ERROR STOP n", "ERROR STOP " and status 1 for an
// ERROR STOP without a code, nothing for a bare STOP.
func (c *compiler) stop(s *syntax.Stop) (stmtFn, error) {
	word := "STOP"
	dflt := 0
	if s.Error {
		word, dflt = "ERROR STOP", 1
	}
	var quiet boolFn
	if s.Quiet != nil {
		var err error
		if quiet, err = c.boolExpr(s.Quiet); err != nil {
			return nil, err
		}
	}
	isQuiet := func(f *frame) bool { return quiet != nil && quiet(f) }
	if s.Code == nil {
		text := ""
		if s.Error {
			text = word + " "
		}
		return func(f *frame) { panic(&stopSignal{code: dflt, text: text, quiet: isQuiet(f)}) }, nil
	}
	tv, err := c.tv(s.Code)
	if err != nil {
		return nil, err
	}
	if tv.Rank != 0 {
		return nil, c.errf(s.Code, "the stop code is a scalar")
	}
	if tv.Type.Basic == sema.Character {
		if tv.Const == nil {
			return nil, c.errf(s.Code, "a character stop code must be a constant")
		}
		text := word + " " + tv.Const.S
		return func(f *frame) { panic(&stopSignal{code: dflt, text: text, quiet: isQuiet(f)}) }, nil
	}
	if tv.Type.Basic != sema.Integer {
		return nil, c.errf(s.Code, "the stop code is an integer or a character constant")
	}
	code, err := c.intExpr(s.Code)
	if err != nil {
		return nil, err
	}
	return func(f *frame) {
		n := code(f)
		panic(&stopSignal{code: int(n), text: word + " " + strconv.FormatInt(n, 10), quiet: isQuiet(f)})
	}, nil
}
