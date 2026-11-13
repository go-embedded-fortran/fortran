package syntax

// The AST. Every node carries a Pos (a byte offset in a FileSet) and is
// typed: expressions, statements and program units are distinct interfaces,
// as in LFortran's AST.asdl (BSD-3, design reference). Names are normalised
// to lower case (F2018 6.2.2: case is not significant outside character
// context); Ident.Orig keeps the original spelling when it differs.

// Node is any AST node.
type Node interface{ P() Pos }

// Span is embedded by every node.
type Span struct{ Pos Pos }

// P returns the position of the node.
func (s Span) P() Pos { return s.Pos }

// Expr is an expression or designator (F2018 clauses 9 and 10).
type Expr interface {
	Node
	expr()
}

// Stmt is a statement, a construct or a program unit.
type Stmt interface {
	Node
	StmtLabel() int
	stmt()
}

// StmtBase is embedded by every statement; Label is 0 when absent.
type StmtBase struct {
	Span
	Label int
	// Name is the construct name ("outer: do ...") when there is one.
	Name string
}

// StmtLabel returns the statement label, 0 if none.
func (s StmtBase) StmtLabel() int { return s.Label }
func (StmtBase) stmt()            {}

// ---- expressions ----

// Ident is a name.
type Ident struct {
	Span
	Name string
	Orig string // original spelling when it differs from Name
}

// IntLit is an integer literal; Kind is the kind parameter ("8", "i8").
type IntLit struct {
	Span
	Text, Kind string
}

// RealLit is a real literal, text as written (lower case: 1.0d0, .5e-3).
type RealLit struct {
	Span
	Text, Kind string
}

// ComplexLit is (re, im).
type ComplexLit struct {
	Span
	Re, Im Expr
}

// StringLit is a character literal, unescaped.
type StringLit struct {
	Span
	Value, Kind string
}

// LogicalLit is .true. or .false.
type LogicalLit struct {
	Span
	Value bool
	Kind  string
}

// BozLit is a binary, octal or hexadecimal constant; Base is 'b', 'o' or 'z'.
type BozLit struct {
	Span
	Base   byte
	Digits string
}

// Unary is a prefix operation: "+", "-", ".not." or a defined operator ".foo.".
type Unary struct {
	Span
	Op string
	X  Expr
}

// Binary is an infix operation. Op is canonical: + - * / ** // == /= < <= >
// >= .and. .or. .eqv. .neqv. or a defined operator ".foo.". Pos is the
// position of the operator.
type Binary struct {
	Span
	Op   string
	X, Y Expr
}

// Paren is a parenthesised expression.
type Paren struct {
	Span
	X Expr
}

// Index is X(Args): an array element, a section, a substring, a function
// reference or a structure constructor; the parser cannot tell (M2 does).
type Index struct {
	Span
	X    Expr
	Args []Arg
}

// Component is X%Name.
type Component struct {
	Span
	X    Expr
	Name Ident
}

// Triplet is lo:hi:stride in a subscript; any part may be nil.
type Triplet struct {
	Span
	Lo, Hi, Stride Expr
}

// Asterisk is "*": a unit or format in an I/O statement, an assumed size.
type Asterisk struct{ Span }

// ArrayCons is [type-spec ::] values, spelled [...] or (/.../).
type ArrayCons struct {
	Span
	Type  *TypeSpec
	Elems []Expr
}

// ImpliedDo is (items, var = lo, hi [, step]).
type ImpliedDo struct {
	Span
	Items        []Expr
	Var          Ident
	Lo, Hi, Step Expr
}

// Arg is an actual argument or a subscript: Value, optionally "Keyword=".
// AltReturn is the label of an alternate return "*10" (Value is nil then).
type Arg struct {
	Span
	Keyword   string
	Value     Expr
	AltReturn string
}

func (Ident) expr()      {}
func (IntLit) expr()     {}
func (RealLit) expr()    {}
func (ComplexLit) expr() {}
func (StringLit) expr()  {}
func (LogicalLit) expr() {}
func (BozLit) expr()     {}
func (Unary) expr()      {}
func (Binary) expr()     {}
func (Paren) expr()      {}
func (Index) expr()      {}
func (Component) expr()  {}
func (Triplet) expr()    {}
func (Asterisk) expr()   {}
func (ArrayCons) expr()  {}
func (ImpliedDo) expr()  {}

// ---- types and declarations ----

// TypeSpec is a type specifier (F2018 7.4): Name is integer, real,
// doubleprecision, complex, logical, character, type or class.
type TypeSpec struct {
	Span
	Name    string
	Derived string // type(Derived), class(Derived); "*" for type(*), class(*)
	Kind    Expr   // kind selector, nil when absent
	Len     Expr   // character length, nil when absent; Star for len=*
	// KindStar is true for the old "real*8" spelling (Kind then holds the 8),
	// LenStar for "character*10".
	KindStar, LenStar bool
}

// Dim is one dimension of an array spec (F2018 8.5.8): lo:hi, n, :, *,
// lo:*. For "n", Lo is nil and Hi is n.
type Dim struct {
	Span
	Lo, Hi Expr
	Colon  bool // a ':' is present
	Star   bool // the upper bound is '*'
}

// Attr is an attribute of a declaration (F2018 8.5): Name is dimension,
// allocatable, pointer, target, parameter, save, intent, optional, public,
// private, external, intrinsic, value, volatile, protected, contiguous,
// asynchronous, pass, nopass, deferred, non_overridable, extends, abstract,
// bind, kind, len, or another word kept verbatim.
type Attr struct {
	Span
	Name   string
	Intent string // for intent: in, out, inout
	Dims   []Dim  // for dimension
	Args   []Expr // for extends(x), bind(c, name='f'), pass(arg)
}

// Entity is one declared name with its optional array spec, character length
// and initialisation ("= value" or "=> target").
type Entity struct {
	Span
	Name    Ident
	Dims    []Dim
	HasDims bool
	CharLen Expr
	Init    Expr
	PtrInit bool
}

// Decl is a type declaration statement (F2018 8.2).
type Decl struct {
	StmtBase
	Type     *TypeSpec
	Attrs    []Attr
	Entities []Entity
	DColon   bool // written with "::"
}

// AttrStmt is an attribute specification statement: Name is dimension,
// allocatable, pointer, target, optional, public, private, save, external,
// intrinsic, value, volatile, protected, contiguous, asynchronous or intent.
type AttrStmt struct {
	StmtBase
	Attr   string
	Intent string
	Items  []Entity // names; common blocks of save in Blocks
	Blocks []string // /name/ items of save
	DColon bool
}

// ParameterStmt is parameter (name = value, ...).
type ParameterStmt struct {
	StmtBase
	Items []Entity
}

// ImplicitStmt is implicit none, or implicit type (letters).
type ImplicitStmt struct {
	StmtBase
	None  bool
	Specs []ImplicitSpec
}

// ImplicitSpec is a type with letter ranges.
type ImplicitSpec struct {
	Type   *TypeSpec
	Ranges [][2]byte
}

// UseStmt is use [[, nature] ::] module [, only: ...] [, local => remote].
type UseStmt struct {
	StmtBase
	Module Ident
	Nature string
	Only   bool
	Items  []UseItem
}

// UseItem is a rename "Local => Remote" or a plain name (Local empty);
// Op is set for operator(x) and assignment(=) items.
type UseItem struct {
	Span
	Local, Remote Ident
	Op            string
}

// CommonStmt is common /name/ items.
type CommonStmt struct {
	StmtBase
	Blocks []CommonBlock
}

// CommonBlock is one /name/ group; Name is "" for blank common.
type CommonBlock struct {
	Name  string
	Items []Entity
}

// EquivalenceStmt lists equivalence sets.
type EquivalenceStmt struct {
	StmtBase
	Sets [][]Expr
}

// DataStmt is data objects /values/.
type DataStmt struct {
	StmtBase
	Sets []DataSet
}

// DataSet is one "objects / values /".
type DataSet struct {
	Objects []Expr
	Values  []Expr
}

// NamelistStmt is namelist /group/ items.
type NamelistStmt struct {
	StmtBase
	Groups []NamelistGroup
}

// NamelistGroup is one namelist group.
type NamelistGroup struct {
	Name  string
	Items []Ident
}

// FormatStmt keeps the parenthesised format specification as text.
type FormatStmt struct {
	StmtBase
	Text string
}

// ImportStmt is import [[::] names].
type ImportStmt struct {
	StmtBase
	Names []Ident
}

// KeywordStmt is a statement that is only a keyword: sequence, contains
// inside a derived type, private/public inside a type, continue handled by
// ContinueStmt.
type KeywordStmt struct {
	StmtBase
	Word string
}

// TypeDef is a derived type definition (F2018 7.5.2).
type TypeDef struct {
	StmtBase
	Ident       Ident
	Attrs       []Attr
	Params      []Ident
	Body        []Stmt // component declarations, private, sequence
	Bound       []Stmt // after "contains": ProcBinding
	EndName     string
	HasContains bool
}

// ProcBinding is a type-bound procedure, generic or final binding, or a
// "module procedure" in an interface block: Kind is procedure, generic,
// final, or module procedure.
type ProcBinding struct {
	StmtBase
	Kind   string
	Iface  string
	Attrs  []Attr
	Items  []ProcItem
	DColon bool
}

// ProcItem is "Name => Target" (Target empty for a bare name). For generic,
// Name is the generic spec, e.g. "operator(+)" or "write(formatted)".
type ProcItem struct {
	Span
	Name   string
	Target string
}

// Interface is an interface block (F2018 15.4.3): Generic is "" for an
// unnamed interface, a name, "operator(+)", "assignment(=)", etc.
type Interface struct {
	StmtBase
	Abstract bool
	Generic  string
	Body     []Stmt // *Unit interface bodies and ProcBinding
}

// UnitKind is the kind of a program unit.
type UnitKind uint8

// Program unit kinds (F2018 clause 14).
const (
	ProgramUnit UnitKind = iota
	ModuleUnit
	SubmoduleUnit
	SubroutineUnit
	FunctionUnit
	BlockDataUnit
)

var unitKindNames = [...]string{"program", "module", "submodule", "subroutine", "function", "block data"}

func (k UnitKind) String() string { return unitKindNames[k] }

// Unit is a program unit, an internal or module procedure, or an interface
// body. A main program without a program statement has Implicit set.
type Unit struct {
	StmtBase
	Kind        UnitKind
	Ident       Ident
	Params      []Ident // dummy arguments; Name "*" is an alternate return
	Prefix      []string
	Result      *Ident
	ResultType  *TypeSpec
	Bind        []Expr // bind(c [, name=...]) arguments, nil when absent
	HasBind     bool
	Parent      Ident // submodule: ancestor[:parent]
	Ancestor    Ident
	Body        []Stmt
	Contains    []*Unit
	HasContains bool
	EndName     string
	EndPos      Pos
	Implicit    bool
}

// ---- executable statements ----

// Assignment is lhs = rhs (F2018 10.2.1).
type Assignment struct {
	StmtBase
	LHS, RHS Expr
}

// PointerAssign is lhs => rhs.
type PointerAssign struct {
	StmtBase
	LHS, RHS Expr
}

// If is an IF construct (F2018 11.1.8).
type If struct {
	StmtBase
	Cond    Expr
	Then    []Stmt
	ElseIfs []ElseIf
	Else    []Stmt
	HasElse bool
}

// ElseIf is one else if (cond) then branch.
type ElseIf struct {
	Span
	Cond Expr
	Body []Stmt
}

// LogicalIf is if (cond) stmt.
type LogicalIf struct {
	StmtBase
	Cond Expr
	Stmt Stmt
}

// ArithIf is the deleted arithmetic IF (Annex B): if (e) neg, zero, pos.
type ArithIf struct {
	StmtBase
	Expr            Expr
	Neg, Zero, Plus int
}

// ForallIdx is "var = lo:hi[:stride]" in forall and do concurrent.
type ForallIdx struct {
	Span
	Var            Ident
	Lo, Hi, Stride Expr
}

// Do is a DO construct (F2018 11.1.7): counted, while, concurrent or
// unbounded. TermLabel is the label of a labelled DO's terminal statement
// (the terminal statement is the last of Body).
type Do struct {
	StmtBase
	TermLabel    int
	Concurrent   bool
	While        Expr
	Var          Ident
	Lo, Hi, Step Expr
	HasVar       bool
	Controls     []ForallIdx // do concurrent
	Mask         Expr
	Body         []Stmt
}

// Exit is exit [name]; Cycle is cycle [name].
type Exit struct {
	StmtBase
	Target string
}

// Cycle is cycle [name].
type Cycle struct {
	StmtBase
	Target string
}

// Select is a SELECT CASE construct (F2018 11.1.9).
type Select struct {
	StmtBase
	Expr  Expr
	Cases []Case
}

// Case is one case block; Selectors holds values and Triplet ranges.
type Case struct {
	Span
	Selectors []Expr
	Default   bool
	Body      []Stmt
}

// SelectType is a SELECT TYPE construct (F2018 11.1.11).
type SelectType struct {
	StmtBase
	Assoc string // associate-name => when present
	Expr  Expr
	Cases []TypeCase
}

// TypeCase is "type is (spec)", "class is (spec)" or "class default".
type TypeCase struct {
	Span
	Kind string // "type is", "class is", "class default"
	Type *TypeSpec
	Body []Stmt
}

// Where is a WHERE statement or construct (F2018 10.2.3).
type Where struct {
	StmtBase
	Mask   Expr
	Single Stmt // where (m) a = b
	Body   []Stmt
	Else   []WhereBranch
}

// WhereBranch is an elsewhere branch; Mask is nil for the final one.
type WhereBranch struct {
	Span
	Mask Expr
	Body []Stmt
}

// Forall is a FORALL statement or construct.
type Forall struct {
	StmtBase
	Indices []ForallIdx
	Mask    Expr
	Single  Stmt
	Body    []Stmt
}

// Associate is an ASSOCIATE construct.
type Associate struct {
	StmtBase
	Assocs []Assoc
	Body   []Stmt
}

// Assoc is "name => selector".
type Assoc struct {
	Span
	Name Ident
	Expr Expr
}

// Block is a BLOCK construct.
type Block struct {
	StmtBase
	Body []Stmt
}

// Goto is go to label; ComputedGoto is go to (labels) [,] expr.
type Goto struct {
	StmtBase
	Target int
}

// ComputedGoto is go to (l1, l2, ...) [,] expr.
type ComputedGoto struct {
	StmtBase
	Labels []int
	Expr   Expr
}

// Call is call proc [(args)]; HasArgs distinguishes "call f" from "call f()".
type Call struct {
	StmtBase
	Proc    Expr
	Args    []Arg
	HasArgs bool
}

// Return is return [alt-return-expr].
type Return struct {
	StmtBase
	Alt Expr
}

// Stop is stop or error stop [code] [, quiet = q].
type Stop struct {
	StmtBase
	Error bool
	Code  Expr
	Quiet Expr
}

// Pause is the deleted pause [code].
type Pause struct {
	StmtBase
	Code Expr
}

// Continue is continue.
type Continue struct{ StmtBase }

// IOStmt is print, read, write, open, close, inquire, rewind, backspace,
// endfile, flush or wait (F2018 clause 12). Controls is the parenthesised
// control list (positional or keyword); Format is the format of print and of
// "read fmt, list" (a Star for "*", a label, a character expression);
// Items is the input/output list.
type IOStmt struct {
	StmtBase
	Kind     string
	Controls []Arg
	HasCtrl  bool
	Format   Expr
	Items    []Expr
}

// Allocate is allocate or deallocate (F2018 9.7).
type Allocate struct {
	StmtBase
	Dealloc bool
	Type    *TypeSpec
	Objects []Expr
	Specs   []Arg // stat=, errmsg=, source=, mold=
}

// Nullify is nullify (pointers).
type Nullify struct {
	StmtBase
	Objects []Expr
}

// EntryStmt is entry name [(args)] [result(r)].
type EntryStmt struct {
	StmtBase
	Ident  Ident
	Params []Ident
	Result *Ident
}

// BadStmt stands for a statement that failed to parse; an error was reported.
type BadStmt struct{ StmtBase }

// File is the result of a parse.
type File struct {
	Name  string
	Form  Form
	Set   *FileSet
	Units []*Unit
}

// Position resolves a Pos of this file.
func (f *File) Position(p Pos) Position { return f.Set.Position(p) }
