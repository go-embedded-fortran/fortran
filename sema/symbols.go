package sema

import "github.com/go-embedded-fortran/fortran/syntax"

// SymKind is what a name denotes (F2018 19.3.1: local identifiers).
type SymKind uint8

// Symbol kinds.
const (
	SymVar     SymKind = iota // variable, dummy argument, function result
	SymParam                  // named constant (F2018 8.5.13)
	SymProc                   // procedure: external, internal, module, dummy
	SymGeneric                // generic interface (F2018 15.4.3.4)
	SymModule                 // module, used or defined
	SymType                   // derived type name
)

// Flags are the attributes of a symbol (F2018 8.5).
type Flags uint32

// Attribute flags.
const (
	FDummy Flags = 1 << iota
	FOptional
	FPointer
	FAllocatable
	FTarget
	FSave
	FExternal
	FIntrinsic
	FPrivate
	FPublic
	FValue
	FResult   // the result variable of a function
	FImplicit // its type came from the implicit rules
	FStmtFunc // statement function
	FUsed     // came in by use association
	FProtected
	FVolatile
	FAssoc    // associate name or construct entity
	FFunction // a procedure known to be a function
	FSub      // a procedure known to be a subroutine
	FDoVar    // active DO variable
)

// Dim is one dimension of an array: known bounds when constant.
type Dim struct {
	Lo, Hi       int64
	LoKnown      bool
	HiKnown      bool
	Assumed      bool // assumed-size (*) last dimension
	Deferred     bool // ':' (assumed-shape, allocatable, pointer)
	AssumedShape bool
}

// Symbol is a named entity of a scope.
type Symbol struct {
	Name   string
	Kind   SymKind
	Type   Type
	Rank   int // -1: assumed rank
	Dims   []Dim
	Flags  Flags
	Intent string // in, out, inout, "" when absent
	Const  *Const // value of a named constant when folded
	Pos    syntax.Pos
	Unit   *syntax.Unit // procedure or module definition
	Scope  *Scope       // the scope of a module or procedure
	Home   *Scope       // the scope the symbol was declared in
	Der    *DerivedType // for SymType
	// Specifics are the procedures of a generic (SymGeneric).
	Specifics []*Symbol
	// Params are the dummy argument names of a procedure, in order.
	Params []string
	// Origin is the symbol a use-associated name stands for.
	Origin *Symbol
}

// Has reports whether every flag of f is set.
func (s *Symbol) Has(f Flags) bool { return s.Flags&f == f }

// Scope is a scoping unit (F2018 19.3.1) or a construct that owns entities.
type Scope struct {
	Parent *Scope
	Names  map[string]*Symbol
	Order  []*Symbol // declaration order, for deterministic reports
	Unit   *syntax.Unit
	Kind   syntax.UnitKind
	// Construct is true for BLOCK, ASSOCIATE, FORALL, DO CONCURRENT and
	// implied-do scopes: implicit symbols are created in the enclosing unit.
	Construct    bool
	ImplicitNone bool
	Implicit     [26]*Type
	// Opaque is set when a used module is unknown: an unresolved name is
	// then not an error, it may come from that module.
	Opaque bool
	// DefaultPrivate is set by a bare "private" statement in a module.
	DefaultPrivate bool
	Name           string
}

func newScope(parent *Scope, u *syntax.Unit) *Scope {
	s := &Scope{Parent: parent, Names: map[string]*Symbol{}, Unit: u}
	if u != nil {
		s.Kind = u.Kind
		s.Name = u.Ident.Name
	}
	if parent != nil {
		s.ImplicitNone = parent.ImplicitNone
		s.Implicit = parent.Implicit
		s.Opaque = parent.Opaque
	}
	return s
}

// Lookup finds name in s or an enclosing scope (host association, F2018
// 19.5.1.4).
func (s *Scope) Lookup(name string) *Symbol {
	for ; s != nil; s = s.Parent {
		if sym, ok := s.Names[name]; ok {
			return sym
		}
	}
	return nil
}

// unit returns the nearest enclosing scope that is not a construct.
func (s *Scope) unit() *Scope {
	for s.Construct && s.Parent != nil {
		s = s.Parent
	}
	return s
}

// Symbols returns the symbols of s in declaration order.
func (s *Scope) Symbols() []*Symbol { return s.Order }

// TV is what the analyser knows about an expression: the typed tree is the
// syntax tree plus this annotation, keyed by node (the design of go/types:
// every back end walks the one tree and asks Info for types, ranks, constant
// values and the symbol a name resolved to).
type TV struct {
	Type  Type
	Rank  int // -1: assumed rank
	Const *Const
	Sym   *Symbol // the symbol a name or designator resolved to
	// Kind of designator: a variable can be defined (assigned).
	Var bool
	// Elemental call or reference to a function, for the back end.
	Call bool
}

// Info is the result of Analyze.
type Info struct {
	// Types annotates every expression the analyser visited.
	Types map[syntax.Expr]TV
	// Uses maps a name occurrence to the symbol it denotes.
	Uses map[*syntax.Ident]*Symbol
	// Scopes maps a program unit to its scope.
	Scopes map[*syntax.Unit]*Scope
	// Modules are the modules defined in the file, by name.
	Modules map[string]*Scope
	// Units are the file's units in source order with their scopes.
	Units []*UnitInfo
}

// UnitInfo pairs a program unit with its scope.
type UnitInfo struct {
	Unit  *syntax.Unit
	Scope *Scope
}
