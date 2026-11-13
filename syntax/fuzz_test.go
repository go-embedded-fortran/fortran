package syntax

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedInputs are the seeds of the fuzz targets: every file of testdata plus
// the edge cases of the lexer (continuations, strings, BOZ, fixed-form
// oddities). The same bytes are committed under testdata/fuzz (see
// TestSeedCorpusCommitted).
func seedInputs(t testing.TB) map[string][]byte {
	t.Helper()
	m := map[string][]byte{}
	for _, f := range testdataFiles(t) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m[strings.TrimSuffix(filepath.Base(f), ".f90")] = b
	}
	edge := map[string]string{
		"cont_string":   "print *, 'abc&\n   &def', \"x\"\"y\"\nend\n",
		"cont_comment":  "x = 1 + & ! c\n\n ! comment\n & 2\nend\n",
		"boz_kinds":     "i = z'1F' + b\"101\" + o'7' + 1_8 + 1.0_dp + 1d0 + .5e-3\nend\n",
		"dot_ops":       "if (a.eq.b .and. c /= d .or. .not. e) x = a .foo. b\nend\n",
		"semicolons":    "a = 1; b = 2;; c = 3\nend\n",
		"fixed_do":      "      DO 10 I = 1, 10\n   10 CONTINUE\n      END\n",
		"fixed_cont":    "      X = 1 +\n     &    2\n      END\n",
		"fixed_assign":  "      DO 10 I = 1.10\n      END\n",
		"fixed_format":  "      WRITE (6, 10) X\n   10 FORMAT (1X, 5HHELLO, F8.3)\n      END\n",
		"arith_if":      "      IF (X) 10, 20, 30\n   10 CONTINUE\n   20 CONTINUE\n   30 CONTINUE\n      END\n",
		"array_cons":    "a = [integer :: 1, 2, (i, i = 1, 3)]\nb = (/ 1, 2 /)\nend\n",
		"deep_parens":   "x = " + strings.Repeat("(", 300) + "1" + strings.Repeat(")", 300) + "\nend\n",
		"unterminated":  "print *, 'abc\nend\n",
		"label_only":    "10\nend\n",
		"include":       "include 'x.inc'\nend\n",
		"interface":     "interface operator(+)\n module procedure add\nend interface\nend\n",
		"select":        "select case (i)\ncase (1, 3:5)\ncase default\nend select\nend\n",
		"type":          "type, extends(a) :: b\n integer :: x = 1\ncontains\n procedure :: f => g\nend type\nend\n",
		"where_forall":  "where (a > 0) b = 1\nforall (i = 1:n, j = 1:n) a(i,j) = 0\nend\n",
		"implied_do":    "print '(3i4)', (i*i, i = 1, 6)\nread (5, *) (a(i), i = 1, n)\nend\n",
		"tabs_and_bom":  "\xef\xbb\xbf\tprogram p\n\tend\n",
		"illegal_bytes": "x = \x00\xff\xfe\nend\n",
	}
	for k, v := range edge {
		m[k] = []byte(v)
	}
	return m
}

// checkParse is the property shared by the fuzz targets: Parse never panics,
// never reports an internal error, every diagnostic position is valid, and a
// clean parse prints to source that parses to the same AST.
func checkParse(t *testing.T, name string, data []byte, form Form) {
	t.Helper()
	opts := &Options{Form: form, Limits: Limits{MaxSourceBytes: 1 << 20, MaxErrors: 20}}
	f, err := Parse(name, data, opts)
	if f == nil {
		t.Fatal("nil File")
	}
	if err != nil {
		el, ok := err.(ErrorList)
		if !ok || len(el) == 0 {
			t.Fatalf("error of type %T: %v", err, err)
		}
		for _, e := range el {
			if strings.Contains(e.Msg, "internal parser error") {
				t.Fatalf("%v", e)
			}
			if e.Pos.File != "" && (e.Pos.Line < 1 || e.Pos.Col < 1) {
				t.Fatalf("bad position in %v", e)
			}
		}
		return
	}
	out := Print(f)
	f2, err := Parse(name+".printed", []byte(out), &Options{Form: Free, Limits: Limits{MaxSourceBytes: 4 << 20, MaxDepth: 1 << 20}})
	if err != nil {
		// the printed form may exceed a bound the source did not (a statement
		// printed on one line): only a bound refusal is tolerated
		if strings.Contains(err.Error(), "refused") {
			return
		}
		t.Fatalf("printed source does not parse: %v\n%s", err, out)
	}
	if DumpFile(f) != DumpFile(f2) {
		t.Fatalf("AST changed by print/parse\n--- source\n%q\n--- printed\n%s", data, out)
	}
}

func FuzzParseFree(f *testing.F) {
	for _, b := range seedInputs(f) {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) { checkParse(t, "fuzz.f90", data, Free) })
}

func FuzzParseFixed(f *testing.F) {
	for _, b := range seedInputs(f) {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) { checkParse(t, "fuzz.f", data, Fixed) })
}

// FuzzLex runs the scanner and the tokenizer alone, in both source forms:
// every token position must resolve.
func FuzzLex(f *testing.F) {
	for _, b := range seedInputs(f) {
		f.Add(b, false)
		f.Add(b, true)
	}
	f.Fuzz(func(t *testing.T, data []byte, fixed bool) {
		form := Free
		if fixed {
			form = Fixed
		}
		fset := &FileSet{}
		file := fset.add("fuzz", data)
		lim := Limits{MaxSourceBytes: 1 << 20}.fill()
		errs := 0
		errf := func(Pos, string) { errs++ }
		sc := &scanner{fset: fset, opts: &Options{}, form: form, lim: lim, errf: errf, total: len(data)}
		sc.push(file, "", "")
		lx := &lexer{intern: map[string]string{}, errf: errf}
		for {
			line, ok := sc.next()
			if !ok {
				break
			}
			for _, tk := range lx.tokenize(line) {
				if p := fset.Position(tk.Pos); tk.Pos != 0 && p.Line < 1 {
					t.Fatalf("token %v at invalid position %d", tk.Kind, tk.Pos)
				}
			}
		}
	})
}

// TestSeedCorpusCommitted checks that the seeds are committed as Go fuzz
// corpus files, and writes them when SYNTAX_UPDATE_CORPUS is set.
func TestSeedCorpusCommitted(t *testing.T) {
	update := os.Getenv("SYNTAX_UPDATE_CORPUS") != ""
	for _, target := range []string{"FuzzParseFree", "FuzzParseFixed"} {
		dir := filepath.Join("testdata", "fuzz", target)
		for name, b := range seedInputs(t) {
			path := filepath.Join(dir, "seed-"+name)
			want := fmt.Sprintf("go test fuzz v1\n[]byte(%q)\n", b)
			if update {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (regenerate with SYNTAX_UPDATE_CORPUS=1)", err)
			}
			if string(got) != want {
				t.Errorf("%s is stale (regenerate with SYNTAX_UPDATE_CORPUS=1)", path)
			}
		}
	}
}
