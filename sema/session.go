package sema

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-embedded-fortran/fortran/syntax"
)

// The session of bib/fortran.md §7. A notebook is a sequence of cells; the
// session owns one scope chain across them, as LFortran's chained
// TranslationUnits do (bib §3.1):
//
//   - a cell is split into program units (module, submodule, program,
//     subroutine, function, block data) and top-level material;
//   - the top-level declarations (and the top-level subroutines and functions)
//     go into the session module __cell<N>;
//   - the top-level executable statements are wrapped in the subroutine
//     __cell<N>_run, a procedure of that module;
//   - every earlier session module and every module an earlier cell defined is
//     in scope (an implicit USE) for the session module of the cell, hence for
//     its run subroutine and its procedures;
//   - names are case-insensitive: the lexer already normalises them to lower
//     case, and the lower-case name is the one that crosses to Go;
//   - redefinition shadows: the earlier code keeps its binding, a later cell
//     sees the new one (a use-associated name is replaced by the local
//     declaration);
//   - implicit typing is off by default (the session's "implicit none"), on by
//     SessionOptions.ImplicitTyping.

// SessionOptions configure a Session.
type SessionOptions struct {
	// ImplicitTyping turns the implicit typing rules on (bib §7: off by default).
	ImplicitTyping bool
	Limits         Limits
}

// Session analyses the cells of one notebook, in order.
type Session struct{ opt SessionOptions }

// NewSession returns a session; the zero SessionOptions is the notebook default.
func NewSession(o SessionOptions) *Session { return &Session{opt: o} }

type sessionState struct {
	modules map[string]*Scope // session modules and modules defined by cells, by name
	order   []string          // the names of modules, in definition order
	vars    map[string]*Symbol
}

// CellInfo is the analysis of one cell: what go-notebook's graph needs (the
// names defined and used, the errors) and what the back ends need (the
// transformed file and its typed tree).
type CellInfo struct {
	Index int
	// Module is the session module __cell<N>, "" when the cell holds neither
	// declarations, procedures nor statements; Run is __cell<N>_run, "" when
	// the cell has no top-level executable statement.
	Module, Run string
	// Defines are the variables and named constants the cell declares at top
	// level: the names that cross to Go (bib §7).
	Defines []string
	// Procedures, Types and Modules are the top-level subroutines and
	// functions, derived types and modules (and programs) the cell defines:
	// they stay Fortran's and do not cross.
	Procedures, Types, Modules []string
	// Uses are the names the cell reads or calls that earlier cells define.
	Uses   []string
	Errors syntax.ErrorList
	// File is the cell as analysed: the units of the cell with the session
	// module and the run subroutine in place of the loose material.
	File *syntax.File
	Info *Info
	// Vars are the symbols of Defines, with their types.
	Vars map[string]*Symbol
}

// Crosses reports whether the cell that owns this CellInfo makes name
// visible to a cell of another language: variables cross, procedures, types
// and modules stay Fortran's (the shape of go-notebook's Language.Crosses).
func (c *CellInfo) Crosses(name string) bool {
	name = lower(name)
	for _, d := range c.Defines {
		if d == name {
			return true
		}
	}
	return false
}

// Begin returns the state of an empty session. It, AnalyzeNext and the state
// are the shape of go-notebook's analysis.Sequential (analysis/lang.go): the
// state is opaque and a cell with errors returns the state it was given, so a
// broken cell leaves nothing behind. The adapter that fills analysis.Cell
// from CellInfo lives with the language registration in go-notebook, not here.
func (s *Session) Begin() any {
	return &sessionState{modules: map[string]*Scope{}, vars: map[string]*Symbol{}}
}

// AnalyzeNext analyses cell index (its position in the notebook; it names the
// session module) with the state the previous call returned.
func (s *Session) AnalyzeNext(state any, index int, src string) (*CellInfo, any) {
	st, _ := state.(*sessionState)
	if st == nil {
		st = s.Begin().(*sessionState)
	}
	ci := &CellInfo{Index: index}
	f, err := syntax.Parse(fmt.Sprintf("cell%d.f90", index), []byte(src), &syntax.Options{Form: syntax.Free, Cell: true})
	if err != nil {
		ci.Errors, _ = err.(syntax.ErrorList)
		if ci.Errors == nil {
			ci.Errors = syntax.ErrorList{{Msg: err.Error()}}
		}
		return ci, state
	}
	var decls, execs []syntax.Stmt
	var procs, others []*syntax.Unit
	for _, u := range f.Units {
		switch {
		case u.Implicit:
			for _, b := range u.Body {
				if isSpec(b) {
					decls = append(decls, b)
				} else {
					execs = append(execs, b)
				}
			}
		case u.Kind == syntax.SubroutineUnit || u.Kind == syntax.FunctionUnit:
			procs = append(procs, u)
		default:
			others = append(others, u)
		}
	}
	modName := fmt.Sprintf("__cell%d", index)
	var units []*syntax.Unit
	if len(decls) > 0 || len(procs) > 0 || len(execs) > 0 {
		ci.Module = modName
		m := &syntax.Unit{Kind: syntax.ModuleUnit, Ident: syntax.Ident{Name: modName}, HasContains: true}
		if len(f.Units) > 0 {
			m.Pos = f.Units[0].Pos
		}
		for _, name := range st.order { // the implicit USEs
			m.Body = append(m.Body, &syntax.UseStmt{Module: syntax.Ident{Name: name}})
		}
		m.Body = append(m.Body, decls...)
		m.Contains = append(m.Contains, procs...)
		if len(execs) > 0 {
			ci.Run = modName + "_run"
			run := &syntax.Unit{Kind: syntax.SubroutineUnit, Ident: syntax.Ident{Name: ci.Run}, Body: execs}
			run.Pos = execs[0].P()
			m.Contains = append(m.Contains, run)
		}
		units = append(units, m)
	}
	units = append(units, others...)
	g := &syntax.File{Name: f.Name, Form: f.Form, Set: f.Set, Units: units}
	ci.File = g

	opt := &Options{ImplicitNone: !s.opt.ImplicitTyping, Limits: s.opt.Limits, Modules: st.modules, Redefine: true}
	info, errs := Analyze(g, opt)
	ci.Info, ci.Errors = info, errs
	if len(errs) > 0 {
		return ci, state
	}

	own := map[string]bool{}
	ci.Vars = map[string]*Symbol{}
	var modUnit *syntax.Unit // the session module, nil when the cell has none
	if ci.Module != "" {
		modUnit = units[0]
	}
	if sc := info.Scopes[modUnit]; modUnit != nil && sc != nil {
		for _, sym := range sc.Order {
			if sym.Flags&FUsed != 0 || sym.Name == ci.Run {
				continue
			}
			own[sym.Name] = true
			switch sym.Kind {
			case SymVar, SymParam:
				ci.Defines = append(ci.Defines, sym.Name)
				ci.Vars[sym.Name] = sym
			case SymProc, SymGeneric:
				ci.Procedures = append(ci.Procedures, sym.Name)
			case SymType:
				ci.Types = append(ci.Types, sym.Name)
			}
		}
	}
	for _, u := range others {
		ci.Modules = append(ci.Modules, u.Ident.Name)
		own[u.Ident.Name] = true
	}
	uses := map[string]bool{}
	for id, sym := range info.Uses {
		if sym.Flags&FUsed != 0 && !own[id.Name] {
			uses[sym.Name] = true
		}
	}
	for n := range uses {
		ci.Uses = append(ci.Uses, n)
	}
	sort.Strings(ci.Defines)
	sort.Strings(ci.Procedures)
	sort.Strings(ci.Types)
	sort.Strings(ci.Modules)
	sort.Strings(ci.Uses)

	next := &sessionState{modules: map[string]*Scope{}, vars: map[string]*Symbol{}, order: append([]string(nil), st.order...)}
	for k, v := range st.modules {
		next.modules[k] = v
	}
	for k, v := range st.vars {
		next.vars[k] = v
	}
	add := func(name string, sc *Scope) {
		if _, dup := next.modules[name]; !dup {
			next.order = append(next.order, name)
		}
		next.modules[name] = sc
	}
	for _, ui := range info.Units {
		u := ui.Unit
		if u == modUnit && modUnit != nil {
			add(modName, ui.Scope)
		} else if (u.Kind == syntax.ModuleUnit) && info.Scopes[u] != nil && !isNested(u, units) {
			add(u.Ident.Name, ui.Scope)
		}
	}
	for n, v := range ci.Vars {
		next.vars[n] = v
	}
	return ci, next
}

func isNested(u *syntax.Unit, top []*syntax.Unit) bool {
	for _, t := range top {
		if t == u {
			return false
		}
	}
	return true
}

// GoType is the Go type a Fortran value takes when it crosses to Go (bib §7):
// integer(k) to int8..int64, real(4|8) to float32|float64, complex(4|8) to
// complex64|complex128, logical to bool, character to string (trailing blanks
// kept), arrays of those to *ndarray.Array (column-major, 1-based bounds
// recorded as metadata), derived types to an opaque handle. ok is false for a
// type without a Go counterpart (integer(16), real(10), real(16)).
func GoType(t Type, rank int) (goType string, ok bool) {
	var elem string
	switch t.Basic {
	case Integer:
		elem = map[int]string{1: "int8", 2: "int16", 4: "int32", 8: "int64"}[t.Kind]
	case Real:
		elem = map[int]string{4: "float32", 8: "float64"}[t.Kind]
	case Complex:
		elem = map[int]string{4: "complex64", 8: "complex128"}[t.Kind]
	case Logical:
		elem = "bool"
	case Character:
		elem = "string"
		if t.Kind != 1 {
			elem = ""
		}
	case Derived:
		return "opaque", true
	}
	if elem == "" {
		return "", false
	}
	if rank != 0 {
		return "*ndarray.Array", true
	}
	return elem, true
}

// SessionModule reports whether a module name is a session module.
func SessionModule(name string) bool { return strings.HasPrefix(name, "__cell") }
