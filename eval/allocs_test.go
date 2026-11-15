package eval

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// runtimeAllocs is every place in eval that allocates while a program RUNS
// (inside a closure the compiler returns, or in a function that is not a
// method of the compiler), by file and function, with how its size is bounded.
// A program's data must not be able to grow memory outside the budget
// (memory.go): each entry says either which charge covers it ("charged: ...")
// or what bounds it independently of the data ("bounded: ...").
//
// TestEveryRuntimeAllocationIsAccounted fails when a site appears, disappears
// or moves to another function, so a new allocation cannot land without being
// classified here. Four go-notebook reviews in a row each found one more
// uncharged path (copies, temporaries, loop conditions, the constructor); this
// table is the audit of the class, not of an instance.
var runtimeAllocs = map[string]struct {
	n   int
	how string
}{
	"arrays.go:acc.add":                 {2, "charged: each growth of the constructor's backing array is peeked before the append makes it (2x the old capacity, Go's growth bound)"},
	"arrays.go:compiler.allocateGA":     {2, "bounded: lb and ext lists, one entry per dimension (rank <= 7); the array itself is newGarr, peeked, then retained"},
	"arrays.go:compiler.assignGA":       {1, "charged: an allocatable's new storage is newGarr (peeked) then retained; the ext copy is one entry per dimension. A whole-array assignment builds no offset list"},
	"arrays.go:compiler.declGA":         {2, "bounded: copies of lb and ext (rank); the array is newGarr, peeked, then retained by its frame"},
	"arrays.go:elemental":               {6, "bounded: per-operand scratch, one entry per operand of the expression (source); the result is newGarr, peeked"},
	"arrays.go:garr.clone":              {4, "charged: every caller peeks garrBytes(size) before clone (derived.go assign); lb and ext are rank-sized"},
	"arrays.go:newGarr":                 {2, "charged: peek(garrBytes(n)) before the data slice is made"},
	"arrays.go:ones":                    {1, "bounded: one entry per dimension (rank)"},
	"arrays.go:printVals":               {4, "charged: valsFit reserves the values, and no more than valRoom are built"},
	"arrays.go:resolve":                 {8, "charged: a triplet's subscripts and a section's offsets are peeked before they are made, and the statement resets its temporaries (assignGA sets c.tmps for a section target); dims and the counters are rank-sized; an element (scalar subscripts) allocates no list"},
	"arrfuncs.go:compiler.arrIntrinsic": {11, "bounded: extent, order, stride and index lists, one entry per dimension (reshape checks the shape's size against the result rank first); results are newGarr, peeked; pack copies into its result, no intermediate list"},
	"arrfuncs.go:compiler.elementalGA":  {2, "bounded: one array per argument (source); the result is newGarr, peeked"},
	"derived.go:compiler.compDefault":   {2, "bounded: lb and ext copies (rank); the component's array is newGarr, peeked"},
	"derived.go:compiler.declD":         {1, "charged: the slots of an array of objects, charge(8*n) before make; each object is newObj, charged"},
	"derived.go:compiler.structCons":    {1, "bounded: ext copy (rank); the component's array is newGarr, peeked"},
	"derived.go:dobj.vals":              {5, "bounded: one value per scalar component of the type (source); array components go through printVals"},
	"derived.go:dtype.newObj":           {4, "charged: charge(objBytes) before anything of the object; the component slice is the type's component count (source); character components are within objBytes; arrays are newGarr, peeked, retained"},
	"eval.go:Program.RunIO":             {1, "bounded: one frame per module of the program (source)"},
	"eval.go:compiler.declSym":          {2, "charged: a character variable's blank value is charge(n) before strings.Repeat"},
	"eval.go:compiler.intrinsicWith":    {1, "charged: REPEAT peeks len*count before strings.Repeat, after the MaxStringLen check"},
	"eval.go:procCode.newFrame":         {8, "bounded: a frame's slots (scalar pointers' among them), counts fixed by the unit's source; frames are bounded by the call depth bound"},
	"print.go:compiler.printItem":       {8, "bounded: one value per scalar output item (source); arrays go through printVals or valsFit, implied-dos through impliedDo's valRoom"},
	"procs.go:compiler.program":         {2, "bounded: one entry per program unit (source), built when the program is compiled"},
	"session.go:Session.Run":            {1, "bounded: one frame per module of the session (source)"},
	"session.go:compiler.cell":          {2, "bounded: one entry per program unit of the cell (source)"},
	"strings.go:compiler.strIntrinsic":  {2, "charged: adjustl and adjustr peek len(s) before building the result"},
	"strings.go:fitLen":                 {1, "bounded: pads to the declared length of the destination, whose storage is charged when the variable is made"},
	"strio.go:compiler.internalIO":      {1, "bounded: one value per item of the I/O list (source)"},
	"vecloop.go:vecBuild.intrinsic":     {2, "bounded: runs while the kernel is built, at compile time; sizes are the expression's (source)"},
	"vecloop.go:vecBuild.node":          {1, "bounded: runs while the kernel is built, at compile time; at most vecNodes nodes"},
	"vecloop.go:vecBuild.real":          {1, "bounded: runs while the kernel is built, at compile time; one leaf per operand (source)"},
	"vecloop.go:vecKernel.run":          {1, "bounded: the kernel's scratch, nbuf*vecChunk floats, constants of the kernel, made once per frame"},
}

// allocSites counts the allocation sites of the eval package that run at run
// time, keyed "file:func".
func allocSites(t *testing.T) map[string]int {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	sites := map[string]int{}
	read := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		read++
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			compiler := false
			fn := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) == 1 {
				rt := fd.Recv.List[0].Type
				if st, ok := rt.(*ast.StarExpr); ok {
					rt = st.X
				}
				if id, ok := rt.(*ast.Ident); ok {
					fn = id.Name + "." + fn
					compiler = id.Name == "compiler"
				}
			}
			key := name + ":" + fn
			var walk func(n ast.Node, inLit bool)
			walk = func(n ast.Node, inLit bool) {
				ast.Inspect(n, func(m ast.Node) bool {
					if lit, ok := m.(*ast.FuncLit); ok && m != n {
						walk(lit.Body, true)
						return false
					}
					if (inLit || !compiler) && isAlloc(m) {
						sites[key]++
					}
					return true
				})
			}
			walk(fd.Body, false)
		}
	}
	if read == 0 {
		t.Fatal("no source file read: a scan that read nothing is not a clean scan")
	}
	return sites
}

// isAlloc reports an allocation of a size that can come from data.
func isAlloc(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.CallExpr:
		switch fn := n.Fun.(type) {
		case *ast.Ident:
			return fn.Name == "append" || fn.Name == "make"
		case *ast.SelectorExpr:
			if pkg, ok := fn.X.(*ast.Ident); ok {
				return (pkg.Name == "strings" || pkg.Name == "bytes") && fn.Sel.Name == "Repeat"
			}
		}
	case *ast.SelectorExpr:
		if pkg, ok := n.X.(*ast.Ident); ok {
			return pkg.Name == "strings" && n.Sel.Name == "Builder" || pkg.Name == "bytes" && n.Sel.Name == "Buffer"
		}
	}
	return false
}

func TestEveryRuntimeAllocationIsAccounted(t *testing.T) {
	sites := allocSites(t)
	var keys []string
	for k := range sites {
		keys = append(keys, k)
	}
	for k := range runtimeAllocs {
		if _, ok := sites[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	bad := false
	for _, k := range keys {
		want, listed := runtimeAllocs[k]
		switch {
		case !listed:
			t.Errorf("%s: %d run-time allocation site(s) not accounted for: charge them to the budget or say what bounds them, then list them in runtimeAllocs", k, sites[k])
			bad = true
		case want.n != sites[k]:
			t.Errorf("%s: %d run-time allocation site(s), runtimeAllocs says %d: look at what changed and classify it", k, sites[k], want.n)
			bad = true
		case strings.TrimSpace(want.how) == "":
			t.Errorf("%s: no reason given", k)
			bad = true
		}
	}
	if bad && os.Getenv("ALLOCS_PRINT") != "" {
		for _, k := range keys {
			fmt.Printf("\t%q: {%d, %q},\n", k, sites[k], runtimeAllocs[k].how)
		}
	}
}
