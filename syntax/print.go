package syntax

import (
	"strconv"
	"strings"
)

// Print renders a parsed file as canonical free-form source: lower case,
// two-space indentation, one statement per line. Parsing the output gives the
// same AST (see Dump), which the tests check on every file of testdata.
func Print(f *File) string {
	pr := &printer{}
	for _, u := range f.Units {
		pr.unit(u, 0)
	}
	return pr.b.String()
}

// PrintTypeSpec renders a type specifier.
func PrintTypeSpec(t *TypeSpec) string { return (&printer{}).typeSpec(t) }

// PrintExpr renders an expression.
func PrintExpr(e Expr) string { return (&printer{}).expr(e) }

type printer struct{ b strings.Builder }

func (pr *printer) line(ind int, label int, s string) {
	for i := 0; i < ind; i++ {
		pr.b.WriteString("  ")
	}
	if label != 0 {
		pr.b.WriteString(strconv.Itoa(label))
		pr.b.WriteByte(' ')
	}
	pr.b.WriteString(s)
	pr.b.WriteByte('\n')
}

func (pr *printer) stmts(list []Stmt, ind int) {
	for _, s := range list {
		pr.stmt(s, ind)
	}
}

func cname(b StmtBase) string {
	if b.Name != "" {
		return b.Name + ": "
	}
	return ""
}

func endName(b StmtBase) string {
	if b.Name != "" {
		return " " + b.Name
	}
	return ""
}

func (pr *printer) unit(u *Unit, ind int) {
	if !u.Implicit {
		var h []string
		h = append(h, u.Prefix...)
		if u.ResultType != nil {
			h = append(h, pr.typeSpec(u.ResultType))
		}
		h = append(h, u.Kind.String())
		s := strings.Join(h, " ")
		switch u.Kind {
		case SubmoduleUnit:
			anc := u.Ancestor.Name
			if u.Parent.Name != "" {
				anc += ":" + u.Parent.Name
			}
			s += " (" + anc + ") " + u.Ident.Name
		default:
			if u.Ident.Name != "" {
				s += " " + u.Ident.Name
			}
		}
		if u.Kind == SubroutineUnit || u.Kind == FunctionUnit {
			var ps []string
			for _, x := range u.Params {
				ps = append(ps, x.Name)
			}
			if len(ps) > 0 || u.Kind == FunctionUnit || u.Params != nil {
				s += "(" + strings.Join(ps, ", ") + ")"
			}
			if u.Result != nil {
				s += " result(" + u.Result.Name + ")"
			}
			if u.HasBind {
				s += " bind(" + pr.exprs(u.Bind) + ")"
			}
		}
		pr.line(ind, u.Label, s)
		pr.stmts(u.Body, ind+1)
	} else {
		pr.stmts(u.Body, ind)
	}
	if u.HasContains {
		pr.line(ind, 0, "contains")
		for _, c := range u.Contains {
			pr.unit(c, ind+1)
		}
	}
	if u.Implicit {
		if u.EndName != "" {
			pr.line(ind, u.EndLabel, "end program "+u.EndName)
			return
		}
		pr.line(ind, u.EndLabel, "end")
		return
	}
	s := "end " + u.Kind.String()
	if u.EndName != "" {
		s += " " + u.EndName
	}
	pr.line(ind, u.EndLabel, s)
}

func (pr *printer) body(ind int, label int, head string, body []Stmt, end string) {
	pr.line(ind, label, head)
	pr.stmts(body, ind+1)
	if end != "" {
		pr.line(ind, 0, end)
	}
}

func (pr *printer) stmt(s Stmt, ind int) {
	switch n := s.(type) {
	case *Unit:
		pr.unit(n, ind)
	case *If:
		pr.line(ind, n.Label, cname(n.StmtBase)+"if ("+pr.expr(n.Cond)+") then")
		pr.stmts(n.Then, ind+1)
		for _, e := range n.ElseIfs {
			pr.line(ind, 0, "else if ("+pr.expr(e.Cond)+") then")
			pr.stmts(e.Body, ind+1)
		}
		if n.HasElse {
			pr.line(ind, 0, "else")
			pr.stmts(n.Else, ind+1)
		}
		pr.line(ind, 0, "end if"+endName(n.StmtBase))
	case *Do:
		h := cname(n.StmtBase) + "do"
		if n.TermLabel != 0 {
			h += " " + strconv.Itoa(n.TermLabel)
		}
		switch {
		case n.While != nil:
			h += " while (" + pr.expr(n.While) + ")"
		case n.Concurrent:
			h += " concurrent (" + pr.forall(n.Controls, n.Mask) + ")"
		case n.HasVar:
			h += " " + n.Var.Name + " = " + pr.expr(n.Lo) + ", " + pr.expr(n.Hi)
			if n.Step != nil {
				h += ", " + pr.expr(n.Step)
			}
		}
		if n.TermLabel != 0 {
			pr.body(ind, n.Label, h, n.Body, "")
		} else {
			pr.body(ind, n.Label, h, n.Body, "end do"+endName(n.StmtBase))
		}
	case *Select:
		pr.line(ind, n.Label, cname(n.StmtBase)+"select case ("+pr.expr(n.Expr)+")")
		for _, c := range n.Cases {
			if c.Default {
				pr.line(ind, 0, "case default")
			} else {
				pr.line(ind, 0, "case ("+pr.exprs(c.Selectors)+")")
			}
			pr.stmts(c.Body, ind+1)
		}
		pr.line(ind, 0, "end select"+endName(n.StmtBase))
	case *SelectType:
		h := cname(n.StmtBase) + "select type ("
		if n.Assoc != "" {
			h += n.Assoc + " => "
		}
		pr.line(ind, n.Label, h+pr.expr(n.Expr)+")")
		for _, c := range n.Cases {
			if c.Kind == "class default" {
				pr.line(ind, 0, "class default")
			} else {
				pr.line(ind, 0, c.Kind+" ("+pr.typeSpec(c.Type)+")")
			}
			pr.stmts(c.Body, ind+1)
		}
		pr.line(ind, 0, "end select"+endName(n.StmtBase))
	case *SelectRank:
		h := cname(n.StmtBase) + "select rank ("
		if n.Assoc != "" {
			h += n.Assoc + " => "
		}
		pr.line(ind, n.Label, h+pr.expr(n.Expr)+")")
		for _, c := range n.Cases {
			switch {
			case c.Default:
				pr.line(ind, 0, "rank default")
			case c.Star:
				pr.line(ind, 0, "rank (*)")
			default:
				pr.line(ind, 0, "rank ("+pr.expr(c.Value)+")")
			}
			pr.stmts(c.Body, ind+1)
		}
		pr.line(ind, 0, "end select"+endName(n.StmtBase))
	case *Where:
		if n.Single != nil {
			pr.line(ind, n.Label, "where ("+pr.expr(n.Mask)+") "+pr.simple(n.Single))
			return
		}
		pr.line(ind, n.Label, cname(n.StmtBase)+"where ("+pr.expr(n.Mask)+")")
		pr.stmts(n.Body, ind+1)
		for _, e := range n.Else {
			if e.Mask != nil {
				pr.line(ind, 0, "elsewhere ("+pr.expr(e.Mask)+")")
			} else {
				pr.line(ind, 0, "elsewhere")
			}
			pr.stmts(e.Body, ind+1)
		}
		pr.line(ind, 0, "end where"+endName(n.StmtBase))
	case *Forall:
		h := "forall (" + pr.forall(n.Indices, n.Mask) + ")"
		if n.Single != nil {
			pr.line(ind, n.Label, h+" "+pr.simple(n.Single))
			return
		}
		pr.body(ind, n.Label, cname(n.StmtBase)+h, n.Body, "end forall"+endName(n.StmtBase))
	case *Associate:
		var as []string
		for _, a := range n.Assocs {
			as = append(as, a.Name.Name+" => "+pr.expr(a.Expr))
		}
		pr.body(ind, n.Label, cname(n.StmtBase)+"associate ("+strings.Join(as, ", ")+")", n.Body, "end associate"+endName(n.StmtBase))
	case *Block:
		pr.body(ind, n.Label, cname(n.StmtBase)+"block", n.Body, "end block"+endName(n.StmtBase))
	case *TypeDef:
		h := "type"
		for _, a := range n.Attrs {
			h += ", " + pr.attr(a)
		}
		h += " :: " + n.Ident.Name
		pr.line(ind, n.Label, h)
		pr.stmts(n.Body, ind+1)
		if n.HasContains {
			pr.line(ind, 0, "contains")
			pr.stmts(n.Bound, ind+1)
		}
		e := "end type"
		if n.EndName != "" {
			e += " " + n.EndName
		}
		pr.line(ind, 0, e)
	case *EnumDef:
		h := "enum"
		for _, a := range n.Attrs {
			h += ", " + pr.attr(a)
		}
		pr.body(ind, n.Label, h, n.Body, "end enum")
	case *Interface:
		h := "interface"
		if n.Abstract {
			h = "abstract interface"
		}
		if n.Generic != "" {
			h += " " + n.Generic
		}
		pr.body(ind, n.Label, h, n.Body, "end interface")
	default:
		pr.line(ind, s.StmtLabel(), pr.simple(s))
	}
}

func (pr *printer) forall(idx []ForallIdx, mask Expr) string {
	var parts []string
	for _, f := range idx {
		s := f.Var.Name + " = " + pr.expr(f.Lo) + ":" + pr.expr(f.Hi)
		if f.Stride != nil {
			s += ":" + pr.expr(f.Stride)
		}
		parts = append(parts, s)
	}
	if mask != nil {
		parts = append(parts, pr.expr(mask))
	}
	return strings.Join(parts, ", ")
}

func (pr *printer) names(ids []Ident) string {
	var s []string
	for _, i := range ids {
		s = append(s, i.Name)
	}
	return strings.Join(s, ", ")
}

func (pr *printer) exprs(es []Expr) string {
	var s []string
	for _, e := range es {
		s = append(s, pr.expr(e))
	}
	return strings.Join(s, ", ")
}

func (pr *printer) args(as []Arg) string {
	var s []string
	for _, a := range as {
		switch {
		case a.AltReturn != "":
			s = append(s, "*"+a.AltReturn)
		case a.Keyword != "":
			s = append(s, a.Keyword+" = "+pr.expr(a.Value))
		default:
			s = append(s, pr.expr(a.Value))
		}
	}
	return strings.Join(s, ", ")
}

func (pr *printer) dims(ds []Dim) string {
	var s []string
	for _, d := range ds {
		switch {
		case d.Rank:
			s = append(s, "..")
		case d.Colon:
			t := ""
			if d.Lo != nil {
				t = pr.expr(d.Lo)
			}
			t += ":"
			if d.Star {
				t += "*"
			} else if d.Hi != nil {
				t += pr.expr(d.Hi)
			}
			s = append(s, t)
		case d.Star:
			s = append(s, "*")
		default:
			s = append(s, pr.expr(d.Hi))
		}
	}
	return "(" + strings.Join(s, ", ") + ")"
}

func (pr *printer) attr(a Attr) string {
	switch {
	case a.Name == "dimension" && a.Dims != nil:
		return "dimension" + pr.dims(a.Dims)
	case a.Name == "intent":
		return "intent(" + a.Intent + ")"
	case a.Args != nil:
		return a.Name + "(" + pr.exprs(a.Args) + ")"
	}
	return a.Name
}

func (pr *printer) typeSpec(t *TypeSpec) string {
	switch t.Name {
	case "doubleprecision":
		return "double precision"
	case "doublecomplex":
		return "double complex"
	case "type", "class":
		return t.Name + "(" + t.Derived + ")"
	case "character":
		switch {
		case t.LenStar:
			if _, ok := t.Len.(*Asterisk); ok {
				return "character*(*)"
			}
			if _, ok := t.Len.(*IntLit); ok {
				return "character*" + pr.expr(t.Len)
			}
			return "character*(" + pr.expr(t.Len) + ")"
		case t.Len != nil && t.Kind != nil:
			return "character(len=" + pr.expr(t.Len) + ", kind=" + pr.expr(t.Kind) + ")"
		case t.Len != nil:
			return "character(len=" + pr.expr(t.Len) + ")"
		case t.Kind != nil:
			return "character(kind=" + pr.expr(t.Kind) + ")"
		}
		return "character"
	}
	switch {
	case t.KindStar:
		return t.Name + "*" + pr.expr(t.Kind)
	case t.Kind != nil:
		return t.Name + "(kind=" + pr.expr(t.Kind) + ")"
	}
	return t.Name
}

func (pr *printer) entity(e Entity) string {
	s := e.Name.Name
	if e.HasDims {
		s += pr.dims(e.Dims)
	}
	if e.CharLen != nil {
		if _, ok := e.CharLen.(*Asterisk); ok {
			s += "*(*)"
		} else {
			s += "*(" + pr.expr(e.CharLen) + ")"
		}
	}
	if e.Init != nil {
		if e.PtrInit {
			s += " => " + pr.expr(e.Init)
		} else {
			s += " = " + pr.expr(e.Init)
		}
	}
	return s
}

func (pr *printer) entities(es []Entity) string {
	var s []string
	for _, e := range es {
		s = append(s, pr.entity(e))
	}
	return strings.Join(s, ", ")
}

// simple renders a statement that fits on one line.
func (pr *printer) simple(s Stmt) string {
	switch n := s.(type) {
	case *BadStmt:
		return "! bad statement"
	case *Assignment:
		return pr.expr(n.LHS) + " = " + pr.expr(n.RHS)
	case *PointerAssign:
		return pr.expr(n.LHS) + " => " + pr.expr(n.RHS)
	case *Decl:
		h := pr.typeSpec(n.Type)
		for _, a := range n.Attrs {
			h += ", " + pr.attr(a)
		}
		if n.DColon {
			h += " ::"
		}
		return h + " " + pr.entities(n.Entities)
	case *AttrStmt:
		h := n.Attr
		if n.Attr == "intent" {
			h += "(" + n.Intent + ")"
		}
		if n.Attr == "bind" {
			h += "(" + pr.exprs(n.Args) + ")"
		}
		if n.DColon {
			h += " ::"
		}
		var items []string
		for _, b := range n.Blocks {
			items = append(items, "/"+b+"/")
		}
		if e := pr.entities(n.Items); e != "" {
			items = append(items, e)
		}
		if len(items) > 0 {
			h += " " + strings.Join(items, ", ")
		}
		return h
	case *Enumerator:
		h := "enumerator"
		if n.DColon {
			h += " ::"
		}
		return h + " " + pr.entities(n.Items)
	case *ParameterStmt:
		return "parameter (" + pr.entities(n.Items) + ")"
	case *ImplicitStmt:
		if n.None {
			return "implicit none"
		}
		var sp []string
		for _, s := range n.Specs {
			var rs []string
			for _, r := range s.Ranges {
				if r[0] == r[1] {
					rs = append(rs, string(r[0]))
				} else {
					rs = append(rs, string(r[0])+"-"+string(r[1]))
				}
			}
			sp = append(sp, pr.typeSpec(s.Type)+" ("+strings.Join(rs, ", ")+")")
		}
		return "implicit " + strings.Join(sp, ", ")
	case *UseStmt:
		h := "use"
		if n.Nature != "" {
			h += ", " + n.Nature + " ::"
		}
		h += " " + n.Module.Name
		var it []string
		for _, u := range n.Items {
			switch {
			case u.Op == "rename":
				it = append(it, u.Local.Name+" => "+u.Remote.Name)
			case u.Op != "":
				it = append(it, u.Op)
			case u.Local.Name != "":
				it = append(it, u.Local.Name+" => "+u.Remote.Name)
			default:
				it = append(it, u.Remote.Name)
			}
		}
		if n.Only {
			h += ", only:"
			if len(it) > 0 {
				h += " " + strings.Join(it, ", ")
			}
		} else if len(it) > 0 {
			h += ", " + strings.Join(it, ", ")
		}
		return h
	case *CommonStmt:
		var bs []string
		for _, b := range n.Blocks {
			bs = append(bs, "/"+b.Name+"/ "+pr.entities(b.Items))
		}
		return "common " + strings.Join(bs, " ")
	case *EquivalenceStmt:
		var ss []string
		for _, set := range n.Sets {
			ss = append(ss, "("+pr.exprs(set)+")")
		}
		return "equivalence " + strings.Join(ss, ", ")
	case *DataStmt:
		var ss []string
		for _, d := range n.Sets {
			ss = append(ss, pr.exprs(d.Objects)+" /"+pr.exprs(d.Values)+"/")
		}
		return "data " + strings.Join(ss, ", ")
	case *NamelistStmt:
		var gs []string
		for _, g := range n.Groups {
			gs = append(gs, "/"+g.Name+"/ "+pr.names(g.Items))
		}
		return "namelist " + strings.Join(gs, ", ")
	case *FormatStmt:
		return "format " + n.Text
	case *ImportStmt:
		if len(n.Names) == 0 {
			return "import"
		}
		return "import :: " + pr.names(n.Names)
	case *KeywordStmt:
		return n.Word
	case *EntryStmt:
		h := "entry " + n.Ident.Name
		if n.Params != nil {
			h += "(" + pr.names(n.Params) + ")"
		}
		if n.Result != nil {
			h += " result(" + n.Result.Name + ")"
		}
		if n.HasBind {
			h += " bind(" + pr.exprs(n.Bind) + ")"
		}
		return h
	case *ProcBinding:
		h := n.Kind
		if n.HasIface {
			h += " (" + n.Iface + ")"
		}
		for _, a := range n.Attrs {
			h += ", " + pr.attr(a)
		}
		if n.DColon {
			h += " ::"
		}
		var it []string
		for _, i := range n.Items {
			t := i.Name
			if i.Target != "" {
				t += " => " + i.Target
			}
			it = append(it, t)
		}
		return h + " " + strings.Join(it, ", ")
	case *LogicalIf:
		return "if (" + pr.expr(n.Cond) + ") " + pr.simple(n.Stmt)
	case *ArithIf:
		return "if (" + pr.expr(n.Expr) + ") " + strconv.Itoa(n.Neg) + ", " + strconv.Itoa(n.Zero) + ", " + strconv.Itoa(n.Plus)
	case *Exit:
		return "exit" + target(n.Target)
	case *Cycle:
		return "cycle" + target(n.Target)
	case *Goto:
		return "go to " + strconv.Itoa(n.Target)
	case *ComputedGoto:
		var ls []string
		for _, l := range n.Labels {
			ls = append(ls, strconv.Itoa(l))
		}
		return "go to (" + strings.Join(ls, ", ") + ") " + pr.expr(n.Expr)
	case *AssignLabel:
		return "assign " + strconv.Itoa(n.Target) + " to " + n.Var.Name
	case *AssignedGoto:
		h := "go to " + n.Var.Name
		if n.Labels != nil {
			var ls []string
			for _, l := range n.Labels {
				ls = append(ls, strconv.Itoa(l))
			}
			h += ", (" + strings.Join(ls, ", ") + ")"
		}
		return h
	case *Call:
		h := "call " + pr.expr(n.Proc)
		if n.HasArgs {
			h += "(" + pr.args(n.Args) + ")"
		}
		return h
	case *Return:
		if n.Alt != nil {
			return "return " + pr.expr(n.Alt)
		}
		return "return"
	case *Stop:
		h := "stop"
		if n.Error {
			h = "error stop"
		}
		if n.Code != nil {
			h += " " + pr.expr(n.Code)
		}
		if n.Quiet != nil {
			h += ", quiet = " + pr.expr(n.Quiet)
		}
		return h
	case *Pause:
		if n.Code != nil {
			return "pause " + pr.expr(n.Code)
		}
		return "pause"
	case *Continue:
		return "continue"
	case *IOStmt:
		h := n.Kind
		if n.HasCtrl {
			h += " (" + pr.args(n.Controls) + ")"
		} else if len(n.Controls) > 0 {
			h += " " + pr.args(n.Controls)
		}
		if n.Format != nil {
			h += " " + pr.expr(n.Format)
			if len(n.Items) > 0 {
				h += ","
			}
		}
		if len(n.Items) > 0 {
			h += " " + pr.exprs(n.Items)
		}
		return h
	case *Allocate:
		h := "allocate"
		if n.Dealloc {
			h = "deallocate"
		}
		h += " ("
		if n.Type != nil {
			h += pr.typeSpec(n.Type) + " :: "
		}
		h += pr.exprs(n.Objects)
		if len(n.Specs) > 0 {
			if len(n.Objects) > 0 {
				h += ", "
			}
			h += pr.args(n.Specs)
		}
		return h + ")"
	case *Nullify:
		return "nullify (" + pr.exprs(n.Objects) + ")"
	}
	return "! unprintable statement"
}

func target(t string) string {
	if t == "" {
		return ""
	}
	return " " + t
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (pr *printer) expr(e Expr) string {
	switch n := e.(type) {
	case nil:
		return ""
	case *Ident:
		return n.Name
	case *IntLit:
		return withKind(n.Text, n.Kind)
	case *RealLit:
		return withKind(n.Text, n.Kind)
	case *ComplexLit:
		return "(" + pr.expr(n.Re) + ", " + pr.expr(n.Im) + ")"
	case *StringLit:
		if n.Kind != "" {
			return n.Kind + "_" + quote(n.Value)
		}
		return quote(n.Value)
	case *LogicalLit:
		s := ".false."
		if n.Value {
			s = ".true."
		}
		if n.Kind != "" {
			s += "_" + n.Kind
		}
		return s
	case *BozLit:
		return string(n.Base) + "'" + n.Digits + "'"
	case *Unary:
		if n.Op == "+" || n.Op == "-" {
			return n.Op + pr.expr(n.X)
		}
		return n.Op + " " + pr.expr(n.X)
	case *Binary:
		if n.Op == "**" {
			return pr.expr(n.X) + "**" + pr.expr(n.Y)
		}
		return pr.expr(n.X) + " " + n.Op + " " + pr.expr(n.Y)
	case *Paren:
		return "(" + pr.expr(n.X) + ")"
	case *Index:
		return pr.expr(n.X) + "(" + pr.args(n.Args) + ")"
	case *Component:
		return pr.expr(n.X) + "%" + n.Name.Name
	case *Triplet:
		s := ":"
		if n.Lo != nil {
			s = pr.expr(n.Lo) + s
		}
		if n.Hi != nil {
			s += pr.expr(n.Hi)
		}
		if n.Stride != nil {
			s += ":" + pr.expr(n.Stride)
		}
		return s
	case *Asterisk:
		return "*"
	case *ArrayCons:
		s := "["
		if n.Type != nil {
			s += pr.typeSpec(n.Type) + " :: "
		}
		return s + pr.exprs(n.Elems) + "]"
	case *ImpliedDo:
		s := "(" + pr.exprs(n.Items) + ", " + n.Var.Name + " = " + pr.expr(n.Lo) + ", " + pr.expr(n.Hi)
		if n.Step != nil {
			s += ", " + pr.expr(n.Step)
		}
		return s + ")"
	}
	return "?"
}

func withKind(t, k string) string {
	if k != "" {
		return t + "_" + k
	}
	return t
}
