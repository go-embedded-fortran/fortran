package eval

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/go-embedded-fortran/fortran/rt"
	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// SessionOptions configure a Session. The zero value is the notebook default:
// implicit none, the evaluator's own bounds, and DefaultMaxSteps.
type SessionOptions struct {
	// ImplicitTyping turns Fortran's implicit typing rules on (bib/fortran.md
	// §7 keeps it off: a typo must not make a real, and cells re-run out of
	// order).
	ImplicitTyping bool
	// MaxSteps bounds one cell: the DO iterations and procedure calls it may
	// make. Zero is DefaultMaxSteps, a negative number is no bound (the context
	// still ends a cell).
	MaxSteps int64
	// Limits bound what the analyser accepts of a cell's source.
	Limits sema.Limits
	// MaxAlloc bounds the storage the session holds, in bytes, counted (see
	// memory.go): every variable, array, derived-type object and character
	// component is charged before it is allocated and credited when it is let
	// go, and a program that would pass the bound fails with an error and
	// allocates nothing for it. The count is the session's own, not the
	// process's heap. Zero is no bound (the playground: 512 MiB, R's bound).
	MaxAlloc int64
	// MaxOutput is the bytes a cell may write to standard output and standard
	// error together, as its host will keep them (zero: no bound). An output
	// statement builds no more values than could be printed within it, so a
	// cell that prints a huge array does not build the text first; what is past
	// the bound is the host's to drop and to say so.
	MaxOutput int64
}

// DefaultMaxSteps is the step bound of a cell: about a minute of the
// evaluator's fastest loops.
const DefaultMaxSteps = 1 << 32

// Session runs the cells of one notebook, in order, as bib/fortran.md §7
// describes: the modules, types, procedures and variables a cell defines are
// in scope in the cells after it, and a variable keeps its value from one cell
// to the next. It is the evaluator's counterpart of sema.Session, which it
// drives for the analysis, so a cell the analyser refuses leaves the session
// as it was.
//
// A Session is not safe for concurrent use.
type Session struct {
	opt   SessionOptions
	an    *sema.Session
	state any
	c     *compiler
	mods  []*frame // the frame of every module compiled so far, by modIdx
	n     int      // the index of the last cell
	vars  map[string]*sema.Symbol
}

// NewSession makes an empty session.
func NewSession(o SessionOptions) *Session {
	s := &Session{opt: o, an: sema.NewSession(sema.SessionOptions{ImplicitTyping: o.ImplicitTyping, Limits: o.Limits}), vars: map[string]*sema.Symbol{}}
	s.state = s.an.Begin()
	s.c = &compiler{syms: map[*sema.Symbol]*sym{}, procs: map[*syntax.Unit]*procCode{}, externals: map[string]*syntax.Unit{}}
	return s
}

// Run analyses, compiles and runs one cell. The cell's standard output goes to
// out and its standard error, with the text of STOP and ERROR STOP, to stderr;
// both are complete when Run returns, also when it fails. STOP ends the cell,
// not the session: the exit status is in Stats.Exit.
//
// A cell the analyser or the compiler refuses is returned as an *Error with the
// position (file cellN.f90, line, column) and changes nothing. A cell that
// fails while it runs (a subscript out of range, a step or context limit)
// keeps what it had done: its variables and definitions stay.
func (s *Session) Run(ctx context.Context, src string, out, stderr io.Writer) (st Stats, err error) {
	s.n++
	ci, next := s.an.AnalyzeNext(s.state, s.n, src)
	if len(ci.Errors) > 0 {
		e := ci.Errors[0]
		return st, &Error{Pos: e.Pos.String(), Msg: e.Msg}
	}
	c := s.c
	nMods := len(c.modules)
	mains, err := c.cell(ci)
	if err != nil {
		c.modules = c.modules[:nMods]
		return st, err
	}
	s.state = next
	for n, v := range ci.Vars {
		s.vars[n] = v
	}

	max := s.opt.MaxSteps
	if max == 0 {
		max = DefaultMaxSteps
	}
	bud := newBudget(ctx, max)
	bud.maxMem = s.opt.MaxAlloc
	if s.opt.MaxOutput > 0 {
		bud.outCap, bud.outLeft = s.opt.MaxOutput, s.opt.MaxOutput
		out, stderr = &countWriter{w: out, b: bud}, &countWriter{w: stderr, b: bud}
	}
	tab := rt.NewTable(out, stderr, nil, nil)
	f := &frame{out: out, tab: tab, start: time.Now(), bud: bud}
	first := len(s.mods)
	for _, m := range c.modules[first:] { // every new module's frame exists before any initialisation runs
		s.mods = append(s.mods, m.newFrame(nil, nil))
	}
	for _, mf := range s.mods {
		mf.out, mf.tab, mf.start, mf.bud, mf.mods = f.out, f.tab, f.start, f.bud, s.mods
	}
	f.mods = s.mods
	// what the session's modules hold from the cells before (a failed cell may
	// have left the counter wrong; the modules' frames are exact)
	bud.mem = held(s.mods)
	defer func() {
		tab.Flush()
		if r := recover(); r != nil {
			switch r := r.(type) {
			case *Error:
				err = r
			case *stopSignal:
				if r.text != "" && !r.quiet {
					io.WriteString(stderr, r.text+"\n")
				}
				st.Exit = r.code & 0xff
			case runtime.Error:
				// a defect of the evaluator must not take a notebook's worker down
				err = &Error{Msg: fmt.Sprintf("internal error in the evaluator: %v", r)}
			default:
				panic(r)
			}
		}
		st.Copies = f.copied
	}()
	for i, m := range c.modules[first:] {
		for _, st := range m.init {
			st(s.mods[first+i])
		}
	}
	for _, p := range mains {
		var up *frame
		if p.host != nil {
			up = s.mods[p.host.modIdx]
		}
		mf := p.newFrame(f, up)
		p.run(mf)
		bud.exit(mf)
	}
	return
}

// Get is the value of the variable name, as the last cell that defined it left
// it: int8..int64 for an integer, float32 or float64 for a real, bool for a
// logical, string for a character; ok is false for a name that is not a
// scalar variable of the session (arrays and derived types are not read
// yet).
func (s *Session) Get(name string) (v any, ok bool) {
	ss := s.vars[name]
	if ss == nil {
		return nil, false
	}
	y := s.c.syms[ss]
	if y == nil || y.owner == nil || !y.owner.module || y.owner.modIdx >= len(s.mods) {
		return nil, false
	}
	f := s.mods[y.owner.modIdx]
	switch y.k {
	case kInt:
		n := f.ints[y.slot]
		if y.isC {
			n = y.ci
		}
		switch y.kd {
		case 1:
			return int8(n), true
		case 2:
			return int16(n), true
		case 4:
			return int32(n), true
		}
		return n, true
	case kLog:
		if y.isC {
			return y.ci != 0, true
		}
		return f.ints[y.slot] != 0, true
	case kReal:
		x := f.reals[y.slot]
		if y.isC {
			x = y.cr
		}
		if y.kd == 4 {
			return float32(x), true
		}
		return x, true
	case kStr:
		if y.isC {
			return y.cs, true
		}
		return f.strs[y.slot], true
	}
	return nil, false
}

// cell compiles the units of an analysed cell: the modules (the session
// module __cell<N> among them) and the main program of the cell, if it has
// one. It returns what runs, in order: the cell's run subroutine, then its
// program. The modules are appended to c.modules; they are initialised by the
// caller.
func (c *compiler) cell(ci *sema.CellInfo) (mains []*procCode, err error) {
	c.file, c.info = ci.File, ci.Info
	c.typeDefs() // read this cell's type definitions now: a later cell may use them
	var order []*procCode
	var add func(u *syntax.Unit, host *procCode) (*procCode, error)
	add = func(u *syntax.Unit, host *procCode) (*procCode, error) {
		sc := c.info.Scopes[u]
		if sc == nil {
			return nil, c.errf(u, "the program unit was not analysed")
		}
		p := &procCode{name: u.Ident.Name, unit: u, scope: sc, host: host}
		if u.Kind == syntax.ModuleUnit {
			p.module, p.modIdx = true, len(c.modules)
			c.modules = append(c.modules, p)
		}
		c.procs[u] = p
		order = append(order, p)
		for _, cu := range u.Contains {
			if _, err := add(cu, p); err != nil {
				return nil, err
			}
		}
		return p, nil
	}
	var prog *syntax.Unit
	for _, u := range c.file.Units {
		switch u.Kind {
		case syntax.ModuleUnit:
			if _, err := add(u, nil); err != nil {
				return nil, err
			}
		case syntax.ProgramUnit:
			if prog != nil {
				return nil, c.errf(u, "a cell holds at most one program")
			}
			prog = u
		default:
			return nil, c.errf(u, "a %s is not supported yet", u.Kind)
		}
	}
	var pmain *procCode
	if prog != nil {
		if pmain, err = add(prog, nil); err != nil {
			return nil, err
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
	if ci.Run != "" {
		for _, p := range order {
			if p.name == ci.Run {
				mains = append(mains, p)
			}
		}
	}
	if pmain != nil {
		mains = append(mains, pmain)
	}
	return mains, nil
}
