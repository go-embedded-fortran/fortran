package eval

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/go-ndarray/ndarray"

	"github.com/go-embedded-fortran/fortran/rt"
	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// Error is a refusal or a run-time failure, positioned in the source when the
// compiler knows where.
type Error struct {
	Pos string
	Msg string
	// Err is the cause when there is one: the context's error of a run that was
	// stopped (errors.Is(err, context.DeadlineExceeded)).
	Err error
	// Refused is set when the analyser or the compiler refused the source, so
	// nothing ran (Session.Run); a failure while running leaves it false.
	Refused bool
}

func (e *Error) Unwrap() error { return e.Err }

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
	ints   []int64 // integer and logical (0 or 1) slots
	reals  []float64
	arrs   []*array
	out    io.Writer
	tab    *rt.Table // the I/O units of the Fortran runtime
	start  time.Time
	copied int64
	// ctl is set by EXIT and CYCLE and cleared by the construct they leave;
	// target is the construct name they name ("" for the innermost DO).
	ctl    int
	target string
	// up is the frame of the host of the procedure (its static link), depth
	// the number of activations below this one.
	up     *frame
	depth  int
	mods   []*frame  // the frames of the modules
	strs   []string  // character scalars
	gas    []*garr   // general arrays
	dobjs  []*dobj   // derived-type scalars
	das    [][]*dobj // arrays of derived type
	tmpObj *dobj     // the object a structure constructor is filling
	bud    *budget   // the step budget of the run, shared by every frame of it
	vs     []float64 // the scratch strips of the loop kernels (see vecloop.go)
	mem    int64     // the storage charged to this frame (see memory.go)
	weight int       // what the calls below this one weigh (see callWeight)
	// tmpBase is what the caller's statement had reserved for temporaries when
	// it called this frame; dead is set when the frame has returned.
	tmpBase int64
	dead    bool
}

// chunk is the number of steps between two looks at the budget's total and at
// the context: a step costs one decrement, the rest is paid once per chunk.
const chunk = 1 << 16

// budget bounds a run: every iteration of a DO loop and every procedure call is
// a step. A run that spends it, or whose context is done, fails with an *Error
// at the next chunk boundary, so an infinite loop in untrusted code ends. It is
// counted, not timed, so that it also holds where no timer can fire while the
// program runs (WebAssembly in a browser).
type budget struct {
	left int64 // steps left in the current chunk
	rest int64 // steps left in all
	ctx  context.Context

	// mem is the storage the program holds, in bytes, and maxMem its bound
	// (zero: none); see memory.go.
	mem, maxMem int64
	// tmp is what the temporaries alive in the running statement have reserved
	// (peek).
	tmp int64

	// outCap is the bytes the cell may write to standard output and standard
	// error together (zero: no bound) and outLeft what it has not yet written:
	// an output statement builds no more values than could be printed (see
	// valRoom).
	outCap, outLeft int64
}

func newBudget(ctx context.Context, max int64) *budget {
	if ctx == nil {
		ctx = context.Background()
	}
	if max <= 0 {
		max = math.MaxInt64
	}
	return &budget{left: chunk, rest: max, ctx: ctx}
}

// tick spends one step.
func (b *budget) tick() {
	b.left--
	if b.left < 0 {
		b.refill()
	}
}

// spend charges n steps at once (a strip kernel): as n ticks would, it fails when
// the total is spent or the context is done.
func (b *budget) spend(n int64) {
	b.left -= n
	for b.left < 0 {
		b.rest -= chunk
		if b.rest <= 0 {
			panic(&Error{Msg: "step limit reached: the cell ran too many loop iterations and calls"})
		}
		if err := b.ctx.Err(); err != nil {
			panic(&Error{Msg: "stopped: " + err.Error(), Err: err})
		}
		b.left += chunk
	}
}

func (b *budget) refill() {
	b.rest -= chunk
	if b.rest <= 0 {
		panic(&Error{Msg: "step limit reached: the cell ran too many loop iterations and calls"})
	}
	if err := b.ctx.Err(); err != nil {
		panic(&Error{Msg: "stopped: " + err.Error(), Err: err})
	}
	b.left = chunk - 1
}

// Values of frame.ctl.
const (
	ctlNone = iota
	ctlExit
	ctlCycle
	ctlReturn
)

type (
	intFn  func(*frame) int64
	realFn func(*frame) float64
	boolFn func(*frame) bool
	arrFn  func(*frame) *array
	stmtFn func(*frame)
)

// stopSignal is the unwinding of STOP and ERROR STOP (F2018 11.4).
type stopSignal struct {
	code  int    // the exit status, when the stop code is an integer
	text  string // "STOP ..." / "ERROR STOP ..." on standard error, "" for none
	quiet bool
}

// Program is a compiled file: the main program, the modules it uses and the
// procedures it can call.
type Program struct {
	main *procCode
	mods []*procCode // the modules, in file order: each has a frame for the whole run
}

// procCode is a compiled program unit: the layout of its activation record
// (one slot per scalar variable, array slots), the statements that set up its
// local arrays and the body. An internal procedure has the main program (its
// host) as host; its variables are reached through the static link of its
// frame.
type procCode struct {
	name                 string
	unit                 *syntax.Unit
	scope                *sema.Scope
	host                 *procCode
	nInts, nReals, nArrs int
	nStrs, nGAs          int
	nDs, nDAs            int
	init                 []stmtFn
	body                 stmtFn
	params               []*sym
	intent               []string
	result               *sym // a function's result variable
	declared             bool
	// module is set for a module: its frame lives for the whole run (frame.mods
	// [modIdx]) and the module procedures link to it.
	module bool
	modIdx int

	// the nesting of the statements and of the expressions of the unit, at the
	// moment (nest, xnest) and at the deepest (maxNest, maxX): a call of the
	// unit costs the host's call stack in proportion (see depth_wasm.go).
	nest, xnest, maxNest, maxX int
}

// newFrame makes the activation record of p called from caller (nil for the
// main program), linked to the frame of its host.
func (p *procCode) newFrame(caller, up *frame) *frame {
	f := &frame{
		ints:  make([]int64, p.nInts),
		reals: make([]float64, p.nReals),
		arrs:  make([]*array, p.nArrs),
		strs:  make([]string, p.nStrs),
		gas:   make([]*garr, p.nGAs),
		dobjs: make([]*dobj, p.nDs),
		das:   make([][]*dobj, p.nDAs),
		up:    up,
	}
	if caller != nil {
		f.out, f.tab, f.start, f.depth, f.mods, f.bud = caller.out, caller.tab, caller.start, caller.depth+1, caller.mods, caller.bud
		f.weight = caller.weight + callWeight(p.maxNest, p.maxX)
		if f.bud != nil {
			f.tmpBase = f.bud.tmp
		}
		if f.weight > maxCallWeight {
			fail(f, "procedure %s: more than %d nested calls", p.name, f.depth-1)
		}
		f.bud.tick()
	}
	return f
}

// run executes the setup statements and the body in f.
func (p *procCode) run(f *frame) {
	for _, s := range p.init {
		s(f)
	}
	p.body(f)
}

// Copies is the number of array elements the last Run copied between storage
// the evaluator owns and arrays go-ndarray allocated for itself (the result of
// matmul). It is zero for a program that makes no such call.
type Stats struct {
	Copies int64
	// Exit is the exit status of the program: 0, or the code of a STOP or
	// ERROR STOP (modulo 256, as the operating system reports it); 1 for an
	// ERROR STOP without a code.
	Exit int
}

// MaxArrayElems bounds the number of elements of one array: a larger extent is
// refused at run time, before anything is allocated.
const MaxArrayElems = 1 << 28

// Run executes the program, printing to out; standard error is discarded. A
// bounds violation or a non-conformable whole-array operation is returned as an
// *Error.
func (p *Program) Run(out io.Writer) (Stats, error) { return p.RunIO(out, io.Discard) }

// RunIO is Run with a writer for standard error (unit 0, and the text of STOP
// and ERROR STOP).
func (p *Program) RunIO(out, stderr io.Writer) (st Stats, err error) {
	f := p.main.newFrame(nil, nil)
	f.out, f.tab, f.start, f.bud = out, rt.NewTable(out, stderr, nil, nil), time.Now(), newBudget(nil, 0)
	for _, m := range p.mods { // every module frame exists before any module's initialisation runs
		mf := m.newFrame(nil, nil)
		mf.out, mf.tab, mf.start, mf.bud = f.out, f.tab, f.start, f.bud
		f.mods = append(f.mods, mf)
	}
	for _, mf := range f.mods {
		mf.mods = f.mods
	}
	defer func() {
		f.tab.Flush() // a non-advancing record left open ends at the end of the program
		if r := recover(); r != nil {
			switch r := r.(type) {
			case *Error:
				err = r
			case *stopSignal:
				if r.text != "" && !r.quiet {
					io.WriteString(stderr, r.text+"\n")
				}
				st.Exit = r.code & 0xff
				st.Copies = f.copied
			default:
				panic(r)
			}
			return
		}
		st.Copies = f.copied
	}()
	for i, m := range p.mods {
		for _, s := range m.init {
			s(f.mods[i])
		}
	}
	p.main.run(f)
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
	kLog
	kStr // a character scalar value (an expression; there are no character variables yet)
	kGA  // an array of the general kind (arrays.go)
	kD   // a derived-type scalar (derived.go)
	kDA  // a rank-1 array of a derived type
)

type strFn func(*frame) string

// MaxStringLen bounds a character value built at run time (REPEAT, //).
const MaxStringLen = 1 << 26

type sym struct {
	k      kind
	kd     int // the kind parameter of an integer, real or logical variable
	slot   int
	isC    bool // a parameter
	ci     int64
	cr     float64
	rank   int
	shape  []intFn // constant-shape arrays: allocated at start
	owner  *procCode
	ga     bool   // an array of the general kind: slot indexes frame.gas
	alloc  bool   // an allocatable array
	et     kind   // the class of the elements of a general array
	cs     string // the value of a character named constant
	clen   int64  // the declared length of a character variable; -1 assumed (character(*)), -2 deferred (len=:)
	dt     *dtype // the type of a derived-type variable
	dlb    int64  // the lower bound of an array of derived type
	cdyn   bool   // the length is an expression evaluated when the unit starts, kept in ints[clslot]
	clslot int
}

// ex is a compiled expression.
type ex struct {
	k  kind
	i  intFn
	r  realFn
	b  boolFn
	s  strFn
	a  arrFn
	g  gaFn                 // a general array: its shape and elements
	et kind                 // the class of the elements of a general array
	d  func(*frame) *dobj   // a derived-type object (a live reference)
	da func(*frame) []*dobj // an array of derived type
	dt *dtype
	// kd is the kind parameter of an integer or real value (0 for 8 or none):
	// a real(4) result is rounded to single precision after each operation, an
	// integer(1|2|4) result wraps.
	kd int

	isC  bool
	ci   int64
	cr   float64
	slot int // >= 0 when the expression is a plain scalar variable
	// aff is set when an integer expression is ints[avs]+avo of the frame avh
	// static links up (see hops; 0: this frame)
	// (a variable, or a variable plus or minus a constant): a subscript of that
	// shape is read without a call (see idx).
	aff  bool
	avs  int
	avh  int
	avo  int64
	rank int

	// array expressions that go-ndarray can write into an existing array.
	op   string
	l, x *ex
	// a plain array variable
	v *sym
}

type compiler struct {
	file *syntax.File
	// info is the typed tree: every type, kind, rank, constant value and
	// resolved name of the program comes from it (package sema); the compiler
	// keeps only the storage slot of each variable.
	info  *sema.Info
	scope *sema.Scope
	syms  map[*sema.Symbol]*sym
	// cur is the program unit being compiled, procs all of them.
	modules   []*procCode
	cur       *procCode
	tmps      bool        // the statement being compiled makes temporaries (see stmts)
	lenExpr   syntax.Expr // the length of the character entity being declared
	typedefs  map[string]*syntax.TypeDef
	tdFile    *syntax.File // the file typedefs was last read from
	dtypes    map[*sema.DerivedType]*dtype
	procs     map[*syntax.Unit]*procCode
	externals map[string]*syntax.Unit // the external subroutines and functions of the file
	// loops are the names of the DO constructs enclosing the statement being
	// compiled ("" for an unnamed one), innermost last.
	loops []string
	// jumped is set when an EXIT or CYCLE is compiled: the blocks that contain
	// it check frame.ctl.
	jumped bool
}

func (c *compiler) errf(n syntax.Node, format string, a ...any) error {
	e := &Error{Msg: fmt.Sprintf(format, a...)}
	if n != nil {
		e.Pos = c.file.Position(n.P()).String()
	}
	return e
}

// Compile compiles a parsed file against its typed tree (sema.Analyze of the
// same file; nil means analyse it here). A file the analyser refuses is refused
// with its first diagnostic. Anything outside the prototype's language is
// refused with a positioned error.
func Compile(f *syntax.File, info *sema.Info) (*Program, error) {
	c := &compiler{file: f, syms: map[*sema.Symbol]*sym{}, procs: map[*syntax.Unit]*procCode{}, externals: map[string]*syntax.Unit{}}
	if info == nil {
		var errs syntax.ErrorList
		if info, errs = sema.Analyze(f, nil); len(errs) > 0 {
			return nil, &Error{Pos: errs[0].Pos.String(), Msg: errs[0].Msg}
		}
	}
	c.info = info
	return c.program()
}

func (c *compiler) stmts(body []syntax.Stmt) ([]stmtFn, error) {
	var out []stmtFn
	for _, s := range body {
		n, outer := len(out), c.tmps
		c.tmps = false
		if err := c.stmt(s, &out); err != nil {
			return nil, err
		}
		if c.tmps {
			// the statement makes temporaries (an array or a character value): it
			// starts with none of the last statement's reserved (memory.go). A
			// statement of scalars pays nothing.
			for i := n; i < len(out); i++ {
				fn := out[i]
				out[i] = func(f *frame) {
					f.bud.tmp = f.tmpBase
					fn(f)
				}
			}
		}
		c.tmps = c.tmps || outer
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
	case *syntax.If:
		fn, err := c.ifConstruct(s)
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	case *syntax.LogicalIf:
		fn, err := c.logicalIf(s)
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	case *syntax.Select:
		fn, err := c.selectCase(s)
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	case *syntax.Stop:
		fn, err := c.stop(s)
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	case *syntax.Exit:
		fn, err := c.jump(s.Target, ctlExit, s, "EXIT")
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	case *syntax.Cycle:
		fn, err := c.jump(s.Target, ctlCycle, s, "CYCLE")
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	case *syntax.Where:
		fn, err := c.where(s)
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	case *syntax.TypeDef:
		// the layout of a derived type is built from the analyser's type on first use
		if s.Name != "" {
			return c.errf(s, "a named type definition is not supported")
		}
	case *syntax.Return:
		fn, err := c.returnStmt(s)
		if err != nil {
			return err
		}
		*out = append(*out, fn)
	case *syntax.Continue:
	case *syntax.UseStmt:
		// the intrinsic module iso_fortran_env only supplies named constants,
		// which the analyser has folded; a module of the file is compiled with
		// the file and its names resolve through the analyser's Info
		if s.Module.Name != "iso_fortran_env" && c.info.Modules[s.Module.Name] == nil {
			return c.errf(s, "use of module %s, which is not defined in this file, is not supported", s.Module.Name)
		}
	case *syntax.Interface:
		// generic names are resolved by the analyser (Info.Calls, TV.Sym)
		if s.Abstract {
			return c.errf(s, "abstract interfaces are not supported yet")
		}
	case *syntax.AttrStmt:
		switch s.Attr {
		case "private", "public", "save", "target": // target: see decl
		default:
			return c.errf(s, "the %s statement is not supported yet", s.Attr)
		}
	case *syntax.KeywordStmt:
		switch s.Word {
		case "private", "public", "save", "contains":
		default:
			return c.errf(s, "%q is not supported yet", s.Word)
		}
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

// kindOf is the evaluator's class of a value of the analyser's type t: integers
// (held as int64, wrapped to their kind), logicals (held as 0 or 1 in an
// integer slot), real(4) and real(8) scalars (both held as float64, the first
// rounded to single precision after each operation) and real(8) arrays.
func (c *compiler) kindOf(at syntax.Node, t sema.Type, rank int) (kind, error) {
	var k kind
	switch {
	case t.Basic == sema.Integer && (t.Kind == 1 || t.Kind == 2 || t.Kind == 4 || t.Kind == 8):
		k = kInt
	case t.Basic == sema.Logical && (t.Kind == 1 || t.Kind == 2 || t.Kind == 4 || t.Kind == 8):
		k = kLog
	case t.Basic == sema.Real && (t.Kind == 4 || t.Kind == 8):
		k = kReal
	case t.Basic == sema.Character && t.Kind == 1 && rank == 0:
		return kStr, nil
	case t.Basic == sema.Derived && t.Der != nil && rank == 0:
		return kD, nil
	case t.Basic == sema.Derived && t.Der != nil && rank == 1:
		return kDA, nil
	case t.Basic == sema.Real:
		return 0, c.errf(at, "real(%d) is not supported: only real(4) and real(8)", t.Kind)
	default:
		return 0, c.errf(at, "type %s is not supported", t)
	}
	if rank != 0 {
		if rank == sema.RankUnknown || rank > MaxRank {
			return 0, c.errf(at, "arrays of assumed rank are not supported")
		}
		if k == kReal && t.Kind == 8 && rank <= 2 {
			return kArr, nil // the kernels' arrays; an expression of the general kind is accepted too
		}
		return kGA, nil
	}
	return k, nil
}

// kernelArray reports whether the variable ss keeps the storage of the gate's
// kernels (go-ndarray): real(8), rank 1 or 2, bounds 1, not a dummy.
func kernelArray(ss *sema.Symbol, k kind) bool {
	if k != kReal || ss.Type.Kind != 8 || ss.Rank < 1 || ss.Rank > 2 || len(ss.Dims) != ss.Rank || ss.Flags&sema.FDummy != 0 {
		return false
	}
	if ss.Flags&sema.FAllocatable != 0 {
		return true
	}
	for _, dm := range ss.Dims {
		if !dm.LoKnown || dm.Lo != 1 || !dm.HiKnown || dm.Assumed || dm.Deferred {
			return false
		}
	}
	return true
}

// kd is the kind parameter an ex carries: 0 for real(8), integer(8), logical.
func kdOf(t sema.Type) int {
	if t.Kind == 8 {
		return 0
	}
	return t.Kind
}

// tv is the analyser's annotation of an expression.
func (c *compiler) tv(n syntax.Expr) (sema.TV, error) {
	tv, ok := c.info.Types[n]
	if !ok {
		return tv, c.errf(n, "the analyser has no type for this expression")
	}
	return tv, nil
}

// symOf is the storage of the variable a name denotes.
func (c *compiler) symOf(id *syntax.Ident) (*sym, *sema.Symbol, error) {
	ss := c.info.Uses[id]
	if ss == nil {
		return nil, nil, c.errf(id, "%s is not a variable of the program", id.Name)
	}
	if ss.Origin != nil { // a use-associated name stands for the module's entity
		ss = ss.Origin
	}
	s, ok := c.syms[ss]
	if !ok {
		return nil, ss, c.errf(id, "%s is not declared", id.Name)
	}
	return s, ss, nil
}

func (c *compiler) decl(d *syntax.Decl) error {
	for _, a := range d.Attrs {
		switch a.Name {
		case "parameter", "allocatable", "dimension", "intent", "value", "external", "private", "public", "save",
			"target": // only lets a pointer be associated with the variable; without pointers (refused) it changes nothing that runs

		default:
			return c.errf(d, "attribute %q is not supported", a.Name)
		}
	}
	for i := range d.Entities {
		e := &d.Entities[i]
		ss := c.scope.Names[e.Name.Name]
		if ss == nil {
			return c.errf(e, "%s was not analysed", e.Name.Name)
		}
		if ss.Kind == sema.SymProc {
			continue // "integer, external :: f": a procedure, not storage
		}
		c.lenExpr = e.CharLen
		if c.lenExpr == nil && d.Type != nil {
			c.lenExpr = d.Type.Len
		}
		err := c.declSym(ss, e, e.Init)
		c.lenExpr = nil
		if err != nil {
			return err
		}
	}
	return nil
}

// declSym gives the variable ss of the current program unit its storage; init
// is its initialisation, nil for none.
func (c *compiler) declSym(ss *sema.Symbol, e syntax.Node, init syntax.Expr) error {
	{
		name := ss.Name
		if _, dup := c.syms[ss]; dup {
			return c.errf(e, "%s is declared twice", name)
		}
		param := ss.Kind == sema.SymParam
		if param && ss.Rank != 0 {
			return c.errf(e, "array named constants are not supported yet")
		}
		elem, err := c.kindOf(e, ss.Type, 0)
		if err != nil {
			return err
		}
		if elem == kStr {
			if ss.Rank != 0 {
				return c.errf(e, "character arrays are not supported yet")
			}
			if ss.Flags&sema.FPointer != 0 {
				return c.errf(e, "a pointer character variable is not supported yet")
			}
			_, deferred := c.lenExpr.(*syntax.Triplet)
			if ss.Flags&sema.FAllocatable != 0 && !deferred || deferred && ss.Flags&sema.FAllocatable == 0 {
				return c.errf(e, "character variable %s: only character(len=:), allocatable is supported as an allocatable character", name)
			}
		}
		if ss.Flags&sema.FDummy != 0 && (ss.Rank != 0 && (elem == kStr || ss.Flags&sema.FOptional != 0) || ss.Flags&(sema.FOptional|sema.FPointer|sema.FAllocatable) != 0) {
			return c.errf(e, "dummy argument %s: only scalar, non-optional dummy arguments are supported yet", name)
		}
		if init != nil && (c.cur.unit.Kind == syntax.SubroutineUnit || c.cur.unit.Kind == syntax.FunctionUnit) {
			return c.errf(e, "%s: an initialised variable of a procedure is saved between calls, which is not supported yet", name)
		}
		k := elem
		s := &sym{k: k, kd: kdOf(ss.Type), owner: c.cur}
		if ss.Type.Basic == sema.Derived {
			if param {
				return c.errf(e, "a named constant of derived type is not supported yet")
			}
			return c.declD(ss, e, s, init)
		}
		switch {
		case ss.Rank != 0 && !kernelArray(ss, k):
			if k == kStr {
				return c.errf(e, "character arrays are not supported yet")
			}
			if init != nil {
				// this path used to drop the initialisation silently
				return c.errf(e, "%s: an array with an initialisation is not supported yet (assign it in a statement)", name)
			}
			if err := c.declGA(ss, e, s, k); err != nil {
				return err
			}
			c.syms[ss] = s
			return nil
		case ss.Rank != 0:
			s.k, s.rank, s.slot = kArr, ss.Rank, c.cur.nArrs
			c.cur.nArrs++
			if ss.Flags&sema.FAllocatable == 0 {
				for _, dm := range ss.Dims {
					if !dm.LoKnown || dm.Lo != 1 || !dm.HiKnown || dm.Assumed || dm.Deferred {
						return c.errf(e, "an explicit-shape array needs 1-based constant bounds")
					}
					s.shape = append(s.shape, constInt(dm.Hi).i)
				}
				c.syms[ss] = s
				c.cur.init = append(c.cur.init, allocFn(0, s.slot, s.shape))
				return nil
			}
			for _, dm := range ss.Dims {
				if !dm.Deferred {
					return c.errf(e, "an allocatable array is declared with (:,...)")
				}
			}
		case k == kInt || k == kLog:
			s.slot = c.cur.nInts
			c.cur.nInts++
		case k == kStr:
			s.slot, s.clen = c.cur.nStrs, ss.Type.Len
			c.cur.nStrs++
			_, star := c.lenExpr.(*syntax.Asterisk)
			if _, deferred := c.lenExpr.(*syntax.Triplet); deferred {
				s.clen = -2
			} else if s.clen < 0 && !star && c.lenExpr != nil && ss.Flags&sema.FDummy == 0 && !param {
				// a length that is an expression of the unit's dummy arguments:
				// character(len(x)) :: y
				fn, err := c.intExpr(c.lenExpr)
				if err != nil {
					return err
				}
				s.cdyn, s.clslot = true, c.cur.nInts
				c.cur.nInts++
				slot, ls := s.slot, s.clslot
				c.cur.init = append(c.cur.init, func(f *frame) {
					n := fn(f)
					if n < 0 {
						n = 0
					}
					if n > MaxStringLen {
						fail(f, "character length %d is out of range", n)
					}
					f.ints[ls] = n
					f.bud.charge(f, f, n)
					f.strs[slot] = strings.Repeat(" ", int(n))
				})
			}
			if !param && s.clen >= 0 && ss.Flags&sema.FDummy == 0 {
				// a character variable is blank until it is assigned
				// (made when the variable is, not when the cell is compiled, so
				// that the budget refuses it before it is allocated)
				n := min(s.clen, MaxStringLen)
				var shared string
				if n <= 256 {
					shared = strings.Repeat(" ", int(n))
				}
				slot := s.slot
				c.cur.init = append(c.cur.init, func(f *frame) {
					f.bud.charge(f, f, n)
					if n <= 256 {
						f.strs[slot] = shared
					} else {
						f.strs[slot] = strings.Repeat(" ", int(n))
					}
				})
			}
		default:
			s.slot = c.cur.nReals
			c.cur.nReals++
		}
		c.syms[ss] = s
		if param {
			if ss.Const == nil {
				return c.errf(e, "parameter %s is not a constant expression", name)
			}
			s.isC = true
			if k == kStr {
				s.cs = ss.Const.S
			} else if k == kLog {
				s.ci = b2i(ss.Const.B)
			} else if k == kInt {
				s.ci = ss.Const.I
			} else {
				s.cr = constFloat(ss.Const)
			}
			return nil
		}
		if init != nil {
			v, err := c.expr(init)
			if err != nil {
				return err
			}
			fn, err := c.store(s, v, e)
			if err != nil {
				return err
			}
			c.cur.init = append(c.cur.init, fn)
		}
	}
	return nil
}

func allocFn(h, slot int, shape []intFn) stmtFn {
	return func(f *frame) {
		var n [2]int
		n[1] = 1
		for i, sh := range shape {
			n[i] = int(sh(f))
		}
		own := f.at(h)
		if n[0] >= 0 && n[1] >= 0 && n[0] <= MaxArrayElems && n[1] <= MaxArrayElems {
			f.bud.charge(f, own, 8*int64(n[0])*int64(n[1])+arrayOverhead)
		}
		own.arrs[slot] = newArray(f, len(shape), n[0], n[1])
	}
}

func newArray(f *frame, rank, n1, n2 int) *array {
	if n1 < 0 || n2 < 0 || n1 > MaxArrayElems || n2 > MaxArrayElems || (n2 > 0 && n1 > MaxArrayElems/n2) {
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

// constFloat is the value of a real constant the analyser folded.
func constFloat(k *sema.Const) float64 {
	if k.Type.Basic == sema.Integer {
		return float64(k.I)
	}
	return k.F
}

// expr compiles an expression. Its type, kind, rank and, when the analyser
// folded it (a literal, a named constant, a constant expression), its value
// come from the typed tree: the compiler derives none of them.
func (c *compiler) expr(n syntax.Expr) (*ex, error) {
	if c.cur != nil { // the deepest nesting of expressions of the unit: its call weight
		c.cur.xnest++
		c.cur.maxX = max(c.cur.maxX, c.cur.xnest)
		defer func() { c.cur.xnest-- }()
	}
	tv, err := c.tv(n)
	if err != nil {
		return nil, err
	}
	want, err := c.kindOf(n, tv.Type, tv.Rank)
	if err != nil {
		return nil, err
	}
	var e *ex
	if tv.Rank == 0 && tv.Const != nil {
		switch want {
		case kInt:
			e = constInt(tv.Const.I)
		case kLog:
			e = constBool(tv.Const.B)
		case kStr:
			s := tv.Const.S
			e = &ex{k: kStr, isC: true, slot: -1, s: func(*frame) string { return s }}
		default:
			e = constReal(constFloat(tv.Const))
		}
		e.kd = kdOf(tv.Type)
	} else if e, err = c.expr1(n, tv); err != nil {
		return nil, err
	} else {
		e = fit(e, tv.Type)
	}
	if e.k == kGA && want == kArr {
		want = kGA // a real(8) array of rank 1 or 2 computed in the general kind
	}
	if e.k != want || ((want == kArr || want == kGA) && e.rank != tv.Rank) {
		return nil, c.errf(n, "internal error: the evaluator compiled a %d (rank %d) where the analyser typed %s (rank %d)", e.k, e.rank, tv.Type, tv.Rank)
	}
	if (e.k == kGA || e.k == kArr || e.k == kStr || e.k == kD) && !e.isC {
		c.tmps = true // the value is a temporary: see stmts
	}
	return e, nil
}

func constBool(v bool) *ex {
	return &ex{k: kLog, isC: true, ci: b2i(v), slot: -1, b: func(*frame) bool { return v }}
}

func b2i(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

// fit rounds a real(4) value to single precision and wraps an integer(1|2|4)
// value to its kind (gfortran wraps), unless the expression is already of that
// kind (a variable, an operand-free result, an earlier fit).
func fit(e *ex, t sema.Type) *ex {
	kd := kdOf(t)
	if kd == 0 || e.kd == kd || e.isC {
		return e
	}
	switch {
	case e.k == kReal && t.Basic == sema.Real:
		fr := e.r
		return &ex{k: kReal, kd: kd, slot: -1, r: func(f *frame) float64 { return float64(float32(fr(f))) }}
	case e.k == kInt && t.Basic == sema.Integer:
		fi := e.i
		// integer(4) arithmetic is not wrapped between operations (that would
		// slow every index expression); a store into an integer(4) variable
		// wraps (see narrow)
		switch kd {
		case 1:
			return &ex{k: kInt, kd: kd, slot: -1, i: func(f *frame) int64 { return int64(int8(fi(f))) }}
		case 2:
			return &ex{k: kInt, kd: kd, slot: -1, i: func(f *frame) int64 { return int64(int16(fi(f))) }}
		}
	}
	return e
}

func (c *compiler) expr1(n syntax.Expr, tv sema.TV) (*ex, error) {
	switch n := n.(type) {
	case *syntax.Paren:
		return c.expr(n.X)
	case *syntax.Ident:
		return c.ident(n)
	case *syntax.Unary:
		x, err := c.expr(n.X)
		if err != nil {
			return nil, err
		}
		if x.k == kGA {
			return c.gaUnary(n, x)
		}
		switch n.Op {
		case "+":
			return x, nil
		case "-":
			return c.negate(n, x)
		case ".not.":
			if x.k == kLog {
				b := x.b
				return &ex{k: kLog, slot: -1, b: func(f *frame) bool { return !b(f) }}, nil
			}
		}
		return nil, c.errf(n, "operator %q is not supported", n.Op)
	case *syntax.Binary:
		return c.binary(n, tv)
	case *syntax.ArrayCons:
		return c.arrayCons(n, tv)
	case *syntax.Component:
		return c.component(n)
	case *syntax.Index:
		return c.index(n, tv)
	}
	return nil, c.errf(n, "%T is not supported in an expression", n)
}

func (c *compiler) ident(n *syntax.Ident) (*ex, error) {
	s, _, err := c.symOf(n)
	if err != nil {
		return nil, err
	}
	if s.ga {
		return c.gaIdent(n, s)
	}
	switch s.k {
	case kD:
		return c.objIdent(n, s)
	case kDA:
		return c.daIdent(n, s)
	}
	if !s.isC {
		h, err := c.hops(s, n)
		if err != nil {
			return nil, err
		}
		if h != 0 {
			if s.k == kArr {
				return arrIdent(n.Name, s, h), nil
			}
			return hostRead(s, h), nil
		}
	}
	switch {
	case s.isC && s.k == kInt:
		e := constInt(s.ci)
		e.kd = s.kd
		return e, nil
	case s.isC && s.k == kLog:
		return constBool(s.ci != 0), nil
	case s.isC && s.k == kStr:
		cs := s.cs
		return &ex{k: kStr, isC: true, slot: -1, s: func(*frame) string { return cs }}, nil
	case s.k == kStr:
		slot := s.slot
		return &ex{k: kStr, slot: -1, s: func(f *frame) string { return f.strs[slot] }}, nil
	case s.isC:
		e := constReal(s.cr)
		e.kd = s.kd
		return e, nil
	case s.k == kInt:
		slot := s.slot
		return &ex{k: kInt, kd: s.kd, slot: slot, aff: true, avs: slot, i: func(f *frame) int64 { return f.ints[slot] }}, nil
	case s.k == kLog:
		slot := s.slot
		return &ex{k: kLog, slot: -1, b: func(f *frame) bool { return f.ints[slot] != 0 }}, nil
	case s.k == kReal:
		slot := s.slot
		return &ex{k: kReal, kd: s.kd, slot: slot, r: func(f *frame) float64 { return f.reals[slot] }}, nil
	}
	return arrIdent(n.Name, s, 0), nil
}

// arrIdent is the whole array s, which lives in the frame h static links up
// (h < 0: a module's; see hops).
func arrIdent(name string, s *sym, h int) *ex {
	slot := s.slot
	return &ex{k: kArr, slot: -1, rank: s.rank, v: s, a: func(f *frame) *array {
		a := f.at(h).arrs[slot]
		if a == nil {
			fail(f, "array %s is not allocated", name)
		}
		return a
	}}
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

func (c *compiler) binary(n *syntax.Binary, tv sema.TV) (*ex, error) {
	x, err := c.expr(n.X)
	if err != nil {
		return nil, err
	}
	y, err := c.expr(n.Y)
	if err != nil {
		return nil, err
	}
	if x.k == kStr || y.k == kStr {
		return c.strBinary(n, x, y)
	}
	if x.k == kGA || y.k == kGA {
		return c.gaBinary(n, x, y, tv)
	}
	switch n.Op {
	case "+", "-", "*", "/", "**":
	case "==", "/=", "<", "<=", ">", ">=":
		return c.relational(n, x, y)
	case ".and.", ".or.", ".eqv.", ".neqv.":
		return c.logical(n, x, y)
	default:
		return nil, c.errf(n, "operator %q is not supported", n.Op)
	}
	switch {
	case x.k == kArr || y.k == kArr:
		return c.arrayBinary(n, x, y)
	case tv.Type.Basic == sema.Integer: // both operands are integers (F2018 Table 10.2)
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
		fn = func(f *frame) int64 { return ipow(a(f), b(f)) }
	}
	r := &ex{k: kInt, slot: -1, i: fn}
	if x.aff && y.isC && (n.Op == "+" || n.Op == "-") {
		r.aff, r.avs, r.avh, r.avo = true, x.avs, x.avh, x.avo+y.ci
		if n.Op == "-" {
			r.avo = x.avo - y.ci
		}
	}
	return r, nil
}

// idx is a compiled subscript: ints[s]+o when s >= 0, else fn(f). The common
// subscripts (i, i-1, i+1) are a load and an add, not a closure call.
type idx struct {
	s  int
	o  int64
	fn intFn
}

func newIdx(e *ex) idx {
	if e.aff && e.avh == 0 {
		return idx{s: e.avs, o: e.avo}
	}
	return idx{s: -1, fn: e.i}
}

func (x *idx) get(f *frame) int64 {
	if x.s >= 0 {
		return f.ints[x.s] + x.o
	}
	return x.fn(f)
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
			f.bud.peek(f, tempBytes(p)) // the result is a temporary of the statement
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
		f.bud.peek(f, tempBytes(p))
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

// tempBytes is the storage of a temporary of the shape of p.
func tempBytes(p *array) int64 { return 8*int64(p.n1)*int64(max(p.n2, 1)) + arrayOverhead }

// wrap adopts the result of a go-ndarray operation as an array of the shape of
// like (the result's storage is its own, so no copy is made).
func wrap(f *frame, nd *ndarray.Array, like *array) *array {
	data, ok := ndarray.Data[float64](nd)
	if !ok {
		fail(f, "result of go-ndarray is not float64")
	}
	return &array{nd: nd, data: data, n1: like.n1, n2: like.n2, rank: like.rank}
}

func (c *compiler) index(n *syntax.Index, tv sema.TV) (*ex, error) {
	if comp, ok := n.X.(*syntax.Component); ok {
		return c.componentIndex(n, comp)
	}
	id, ok := n.X.(*syntax.Ident)
	if !ok {
		return nil, c.errf(n, "only a name can be subscripted or called")
	}
	ss := c.info.Uses[id]
	if ss == nil { // not a name of the program: an intrinsic (sema.Analyze resolved it)
		return c.intrinsic(n, id.Name, tv)
	}
	if ss.Kind == sema.SymType && ss.Der != nil {
		return c.structCons(n, ss.Der)
	}
	if ss.Kind == sema.SymProc || ss.Kind == sema.SymGeneric {
		// a function of the file; for a generic the analyser resolved the specific
		if tv.Sym != nil && tv.Sym.Kind == sema.SymProc {
			ss = tv.Sym
		}
		return c.funcCall(n, ss)
	}
	s, _, err := c.symOf(id)
	if err != nil {
		return nil, err
	}
	if s.ga {
		return c.gaIndex(n, id, s)
	}
	if s.k == kStr {
		return c.substring(n, id)
	}
	if s.k == kDA {
		return c.daElement(n, id, s)
	}
	if s.k != kArr {
		return nil, c.errf(n, "%s is not an array", id.Name)
	}
	return c.element(n, id.Name, s)
}

func (c *compiler) element(n *syntax.Index, name string, s *sym) (*ex, error) {
	if len(n.Args) != s.rank {
		return nil, c.errf(n, "%s has rank %d, subscripted with %d", name, s.rank, len(n.Args))
	}
	var ix [2]idx
	for i, a := range n.Args {
		if a.Keyword != "" || a.Value == nil {
			return nil, c.errf(n, "a keyword or alternate-return subscript is not supported")
		}
		if _, sec := a.Value.(*syntax.Triplet); sec {
			return nil, c.errf(n, "array sections are not supported")
		}
		e, err := c.expr(a.Value)
		if err != nil {
			return nil, err
		}
		if e.k != kInt {
			return nil, c.errf(a.Value, "an integer expression is required")
		}
		ix[i] = newIdx(e)
	}
	slot := s.slot
	h, err := c.hops(s, n)
	if err != nil {
		return nil, err
	}
	i0, i1 := &ix[0], &ix[1]
	var fn realFn
	if s.rank == 1 {
		fn = func(f *frame) float64 {
			a := f.at(h).arrs[slot]
			if a == nil {
				unallocated(f, name)
			}
			i := i0.get(f) - 1
			if uint64(i) >= uint64(a.n1) {
				bounds(f, name, 1, i+1, a.n1)
			}
			return a.data[i]
		}
	} else {
		fn = func(f *frame) float64 {
			a := f.at(h).arrs[slot]
			if a == nil {
				unallocated(f, name)
			}
			i, j := i0.get(f)-1, i1.get(f)-1
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

func (c *compiler) intrinsic(n *syntax.Index, name string, tv sema.TV) (*ex, error) {
	if formals, ok := arrayFormals[name]; ok {
		// the array intrinsics; sum and matmul of the kernels' arrays keep their own code
		general := true
		if (name == "sum" || name == "matmul") && len(n.Args) > 0 && n.Args[0].Value != nil {
			if a, err := c.expr(n.Args[0].Value); err == nil && a.k == kArr {
				general = false
			}
		}
		if general {
			return c.arrIntrinsic(n, name, tv, formals)
		}
	}
	args := make([]*ex, len(n.Args))
	for i, a := range n.Args {
		if a.Value == nil || (a.Keyword != "" && !((name == "index" || name == "scan" || name == "verify") && a.Keyword == "back" && i == 2)) {
			return nil, c.errf(n, "keyword arguments of intrinsics are not supported")
		}
		v, err := c.expr(a.Value)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	for _, a := range args {
		if a.k == kGA {
			return c.elementalGA(n, name, tv, args)
		}
	}
	return c.intrinsicWith(n, name, tv, args)
}

func (c *compiler) intrinsicWith(n *syntax.Index, name string, tv sema.TV, args []*ex) (*ex, error) {
	want := func(k int) error {
		if len(args) != k {
			return c.errf(n, "%s takes %d arguments", name, k)
		}
		return nil
	}
	switch name {
	case "merge", "scan", "verify":
		return c.strIntrinsic2(n, name, args)
	case "trim", "len", "len_trim", "adjustl", "adjustr", "index", "ichar", "iachar", "achar", "char":
		return c.strIntrinsic(n, name, args)
	case "iand", "ior", "ieor", "not", "ishft", "popcnt", "leadz", "trailz", "btest", "ibset", "ibclr", "poppar":
		return c.bitIntrinsic(n, name, args)
	case "repeat":
		if err := want(2); err != nil {
			return nil, err
		}
		if args[0].k != kStr || args[1].k != kInt {
			return nil, c.errf(n, "repeat needs a character and an integer")
		}
		s, k := args[0].s, args[1].i
		return &ex{k: kStr, slot: -1, s: func(f *frame) string {
			str, cnt := s(f), k(f)
			if cnt < 0 {
				fail(f, "repeat: negative count %d", cnt)
			}
			if cnt > 0 && int64(len(str)) > MaxStringLen/cnt {
				fail(f, "repeat: the result is longer than %d characters", MaxStringLen)
			}
			f.bud.peek(f, int64(len(str))*cnt) // before the result is built
			return strings.Repeat(str, int(cnt))
		}}, nil
	case "fraction", "exponent", "scale", "spacing":
		if len(args) < 1 || args[0].k != kReal {
			return nil, c.errf(n, "%s needs a real argument", name)
		}
		a, p := args[0].r, 53
		if args[0].kd == 4 {
			p = 24
		}
		switch name {
		case "fraction":
			return &ex{k: kReal, slot: -1, r: func(f *frame) float64 { m, _ := math.Frexp(a(f)); return m }}, nil
		case "exponent":
			return &ex{k: kInt, slot: -1, i: func(f *frame) int64 {
				x := a(f)
				if x == 0 || math.IsInf(x, 0) || math.IsNaN(x) {
					return 0
				}
				_, e := math.Frexp(x)
				return int64(e)
			}}, nil
		case "scale":
			if err := want(2); err != nil {
				return nil, err
			}
			if args[1].k != kInt {
				return nil, c.errf(n, "scale needs an integer exponent")
			}
			e := args[1].i
			return &ex{k: kReal, slot: -1, r: func(f *frame) float64 {
				return math.Ldexp(a(f), int(max(-1<<20, min(1<<20, e(f)))))
			}}, nil
		}
		minE := -1021 // tiny = 2**(minexponent-1)
		if p == 24 {
			minE = -125
		}
		return &ex{k: kReal, slot: -1, r: func(f *frame) float64 {
			x := a(f)
			if x == 0 {
				return math.Ldexp(1, minE-1)
			}
			_, e := math.Frexp(x)
			return math.Ldexp(1, max(e-p, minE-1))
		}}, nil
	case "tiny", "huge", "epsilon":
		// inquiries about a real type: the argument is only evaluated for its
		// type (F2018 16.8.1), the value is a constant of the type
		if len(args) != 1 || args[0].k != kReal {
			return nil, c.errf(n, "%s needs a real argument", name)
		}
		var v float64
		switch {
		case name == "tiny" && args[0].kd == 4:
			v = float64(math.Float32frombits(0x00800000))
		case name == "tiny":
			v = math.Float64frombits(0x0010000000000000)
		case name == "huge" && args[0].kd == 4:
			v = math.MaxFloat32
		case name == "huge":
			v = math.MaxFloat64
		case args[0].kd == 4:
			v = float64(math.Float32frombits(0x34000000))
		default:
			v = math.Float64frombits(0x3cb0000000000000)
		}
		e := constReal(v)
		e.kd = args[0].kd
		return e, nil
	case "real", "dble":
		// the analyser typed the result (real(x) is real(4), real(x, k) and
		// dble(x) are real(k)); expr rounds a real(4) result
		if len(args) < 1 || len(args) > 2 || args[0].k == kArr || args[0].k == kLog {
			return nil, c.errf(n, "%s of this argument is not supported", name)
		}
		return args[0].asReal(), nil
	case "int", "nint", "floor", "ceiling":
		if len(args) < 1 || len(args) > 2 || args[0].k == kArr || args[0].k == kLog {
			return nil, c.errf(n, "%s of this argument is not supported", name)
		}
		if args[0].k == kInt {
			return args[0], nil
		}
		a := args[0].r
		var conv func(float64) float64
		switch name {
		case "int":
			conv = math.Trunc
		case "nint":
			conv = roundHalfAway
		case "floor":
			conv = math.Floor
		default:
			conv = math.Ceil
		}
		kd := kdOf(tv.Type)
		return &ex{k: kInt, kd: kd, slot: -1, i: func(f *frame) int64 { return narrow(kd, int64(conv(a(f)))) }}, nil
	case "aint", "anint", "exp", "log", "sin", "cos", "tan", "atan", "log10":
		if len(args) != 1 || args[0].k != kReal {
			return nil, c.errf(n, "%s needs one real argument", name)
		}
		a := args[0].r
		var g func(float64) float64
		switch name {
		case "aint":
			g = math.Trunc
		case "anint":
			g = roundHalfAway
		case "exp":
			g = math.Exp
		case "log":
			g = math.Log
		case "log10":
			g = math.Log10
		case "sin":
			g = math.Sin
		case "cos":
			g = math.Cos
		case "tan":
			g = math.Tan
		default:
			g = math.Atan
		}
		return &ex{k: kReal, slot: -1, r: func(f *frame) float64 { return g(a(f)) }}, nil
	case "sign":
		if err := want(2); err != nil {
			return nil, err
		}
		if args[0].k == kInt && args[1].k == kInt {
			a, b := args[0].i, args[1].i
			return &ex{k: kInt, slot: -1, i: func(f *frame) int64 {
				x, y := a(f), b(f)
				if x < 0 {
					x = -x
				}
				if y < 0 {
					return -x
				}
				return x
			}}, nil
		}
		if args[0].k != kReal || args[1].k != kReal {
			return nil, c.errf(n, "sign needs two integers or two reals")
		}
		a, b := args[0].r, args[1].r
		return &ex{k: kReal, slot: -1, r: func(f *frame) float64 { return math.Copysign(a(f), b(f)) }}, nil
	case "modulo":
		if err := want(2); err != nil {
			return nil, err
		}
		if args[0].k != kInt || args[1].k != kInt {
			return nil, c.errf(n, "only modulo of two integers is supported")
		}
		a, b := args[0].i, args[1].i
		return &ex{k: kInt, slot: -1, i: func(f *frame) int64 {
			x, d := a(f), b(f)
			if d == 0 {
				fail(f, "modulo by zero")
			}
			r := x % d
			if r != 0 && (r < 0) != (d < 0) {
				r += d
			}
			return r
		}}, nil
	case "min", "max":
		if len(args) < 2 {
			return nil, c.errf(n, "%s takes at least two arguments", name)
		}
		isMax := name == "max"
		if tv.Type.Basic == sema.Integer {
			fns := make([]intFn, len(args))
			for i, a := range args {
				if a.k != kInt {
					return nil, c.errf(n, "%s of mixed types is not supported", name)
				}
				fns[i] = a.i
			}
			return &ex{k: kInt, slot: -1, i: func(f *frame) int64 {
				m := fns[0](f)
				for _, g := range fns[1:] {
					if v := g(f); (isMax && v > m) || (!isMax && v < m) {
						m = v
					}
				}
				return m
			}}, nil
		}
		fns := make([]realFn, len(args))
		for i, a := range args {
			if a.k != kReal {
				return nil, c.errf(n, "%s of mixed types is not supported", name)
			}
			fns[i] = a.r
		}
		return &ex{k: kReal, slot: -1, r: func(f *frame) float64 {
			m := fns[0](f)
			for _, g := range fns[1:] {
				if v := g(f); (isMax && v > m) || (!isMax && v < m) {
					m = v
				}
			}
			return m
		}}, nil
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
		kd := s.kd
		if v.isC {
			k := narrow(kd, v.ci)
			return func(f *frame) { f.ints[slot] = k }, nil
		}
		fn := v.i
		if kd != 0 && v.kd != kd {
			return func(f *frame) { f.ints[slot] = narrow(kd, fn(f)) }, nil
		}
		return func(f *frame) { f.ints[slot] = fn(f) }, nil
	case s.k == kInt && v.k == kReal:
		// intrinsic assignment converts as INT does (F2018 10.2.1.3)
		fn, kd := v.r, s.kd
		return func(f *frame) { f.ints[slot] = narrow(kd, int64(fn(f))) }, nil
	case s.k == kLog && v.k == kLog:
		fn := v.b
		return func(f *frame) { f.ints[slot] = b2i(fn(f)) }, nil
	case s.k == kStr && v.k == kStr:
		fn := v.s
		if s.clen == -2 {
			// a deferred-length variable takes the length of its value: the
			// counter follows it
			return func(f *frame) {
				v := fn(f)
				f.bud.chargeDelta(f, f, int64(len(v))-int64(len(f.strs[slot])))
				f.strs[slot] = v
			}, nil
		}
		return func(f *frame) { f.strs[slot] = s.fit(f, fn(f)) }, nil
	case s.k == kReal && v.k != kArr && v.k != kLog:
		fn := v.asReal().r
		if s.kd == 4 && v.kd != 4 {
			return func(f *frame) { f.reals[slot] = float64(float32(fn(f))) }, nil
		}
		return func(f *frame) { f.reals[slot] = fn(f) }, nil
	}
	return nil, c.errf(at, "this assignment is not supported (an array into a scalar, or a type the evaluator does not convert)")
}

func (c *compiler) assign(s *syntax.Assignment) (stmtFn, error) {
	rhs, err := c.expr(s.RHS)
	if err != nil {
		return nil, err
	}
	switch l := s.LHS.(type) {
	case *syntax.Ident:
		sy, _, err := c.symOf(l)
		if err != nil {
			return nil, err
		}
		if sy.isC {
			return nil, c.errf(l, "%s is a parameter", l.Name)
		}
		if sy.ga {
			return c.assignGA(s, l, rhs, nil)
		}
		if sy.k == kD {
			return c.assignDerived(s, l, rhs)
		}
		if sy.k == kArr {
			return c.assignArray(s, sy, l.Name, rhs)
		}
		return c.storeVar(sy, rhs, s)
	case *syntax.Component:
		return c.assignDerived(s, l, rhs)
	case *syntax.Index:
		if _, isComp := l.X.(*syntax.Component); isComp {
			return c.assignGA(s, l, rhs, nil)
		}
		id, ok := l.X.(*syntax.Ident)
		if !ok {
			return nil, c.errf(l, "unsupported assignment target")
		}
		sy, _, err := c.symOf(id)
		if err == nil && sy.k == kDA {
			return c.assignDerived(s, l, rhs)
		}
		if err == nil && sy.ga {
			return c.assignGA(s, l, rhs, nil)
		}
		if err == nil && sy.k == kStr {
			return c.assignSubstring(s, l, id, sy, rhs)
		}
		if err != nil || sy.k != kArr {
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
	var ix [2]idx
	for i, a := range l.Args {
		e, err := c.expr(a.Value)
		if err != nil {
			return nil, err
		}
		if e.k != kInt {
			return nil, c.errf(l, "an integer expression is required")
		}
		ix[i] = newIdx(e)
	}
	slot := s.slot
	h, err := c.hops(s, l)
	if err != nil {
		return nil, err
	}
	i0, i1 := &ix[0], &ix[1]
	if s.rank == 1 {
		return func(f *frame) {
			v := val(f)
			a := f.at(h).arrs[slot]
			if a == nil {
				unallocated(f, name)
			}
			i := i0.get(f) - 1
			if uint64(i) >= uint64(a.n1) {
				bounds(f, name, 1, i+1, a.n1)
			}
			a.data[i] = v
		}, nil
	}
	return func(f *frame) {
		v := val(f)
		a := f.at(h).arrs[slot]
		if a == nil {
			unallocated(f, name)
		}
		i, j := i0.get(f)-1, i1.get(f)-1
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
	h, err := c.hops(s, at)
	if err != nil {
		return nil, err
	}
	dest := func(f *frame) *array {
		d := f.at(h).arrs[slot]
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

func (c *compiler) call(s *syntax.Call) (stmtFn, error) {
	if ss := c.info.Calls[s]; ss != nil && ss.Unit != nil {
		return c.callStmt(s, ss)
	}
	if comp, ok := s.Proc.(*syntax.Component); ok {
		return c.boundCallStmt(s, comp)
	}
	if id, ok := s.Proc.(*syntax.Ident); ok && id.Name == "move_alloc" {
		return c.moveAlloc(s)
	}
	if id, ok := s.Proc.(*syntax.Ident); ok && c.externals[id.Name] != nil {
		return c.callStmt(s, &sema.Symbol{Name: id.Name})
	}
	id, ok := s.Proc.(*syntax.Ident)
	if !ok || id.Name != "cpu_time" || len(s.Args) != 1 {
		return nil, c.errf(s, "only call cpu_time(t) is supported")
	}
	t, ok := s.Args[0].Value.(*syntax.Ident)
	if !ok {
		return nil, c.errf(s, "cpu_time needs a real variable")
	}
	sy, _, err := c.symOf(t)
	if err != nil || sy.k != kReal || sy.isC {
		return nil, c.errf(s, "cpu_time needs a declared real(8) variable")
	}
	h, err := c.hops(sy, s)
	if err != nil {
		return nil, err
	}
	slot := sy.slot
	return func(f *frame) { f.at(h).reals[slot] = time.Since(f.start).Seconds() }, nil
}

func (c *compiler) allocate(a *syntax.Allocate) (stmtFn, error) {
	if a.Type != nil || len(a.Specs) > 0 {
		return nil, c.errf(a, "only plain allocate(name(extents)) and deallocate(name) are supported")
	}
	var fns []stmtFn
	for _, o := range a.Objects {
		// an array of the general kind
		var gid *syntax.Ident
		var base syntax.Expr = o
		if x, ok := o.(*syntax.Index); ok {
			base = x.X
		}
		isGA := false
		switch x := base.(type) {
		case *syntax.Ident:
			gid = x
			if gs, _, err := c.symOf(gid); err == nil && gs.ga {
				isGA = true
			}
		case *syntax.Component:
			isGA = true
		}
		if isGA {
			fn, err := c.allocateGA(a, o)
			if err != nil {
				return nil, err
			}
			fns = append(fns, fn)
			continue
		}
		if a.Dealloc {
			return nil, c.errf(a, "only deallocate of an allocatable array of the general kind is supported")
		}
		ix, ok := o.(*syntax.Index)
		if !ok {
			return nil, c.errf(o, "allocate needs name(extents)")
		}
		id, ok := ix.X.(*syntax.Ident)
		if !ok {
			return nil, c.errf(o, "allocate needs a name")
		}
		sy, _, err := c.symOf(id)
		if err != nil || sy.k != kArr || sy.shape != nil {
			return nil, c.errf(o, "%s is not an allocatable array", id.Name)
		}
		h, err := c.hops(sy, o)
		if err != nil {
			return nil, err
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
		fns = append(fns, allocFn(h, sy.slot, shape))
	}
	return func(f *frame) {
		for _, fn := range fns {
			fn(f)
		}
	}, nil
}
