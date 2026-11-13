package syntax_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/corpus"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// EnvCorpora names a directory laid out as the CI fetches the corpora
// (foracle, README "Corpora"): gcc/gcc/testsuite/gfortran.dg,
// llvm-test-suite/Fortran/UnitTests/fcvs21_f95, ... Tests skip without it.
const envCorpora = "FORACLE_CORPORA"

// floors are the parse counts measured at the commit that introduced them
// (README, "The parser"); a drop is a regression. They count files our parser
// accepts, whether or not gfortran does: many gfortran.dg files exist to be
// refused. The comparison with gfortran -fsyntax-only is `foracle parse-rate`.
var floors = []struct {
	name  string
	dirs  []string // alternative locations below $FORACLE_CORPORA (CI's names, then the local ones)
	files int      // files below the directory (the corpus' own count)
	parse int      // floor of files that parse without error
}{
	{"gfortran.dg", []string{"gcc/gcc/testsuite/gfortran.dg"}, 8306, 7490},
	{"gfortran.fortran-torture", []string{"gcc/gcc/testsuite/gfortran.fortran-torture"}, 314, 313},
	{"FCVS", []string{"lts/Fortran/UnitTests/fcvs21_f95", "llvm-test-suite/Fortran/UnitTests/fcvs21_f95"}, 180, 180},
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func TestCorporaParseFloor(t *testing.T) {
	root := os.Getenv(envCorpora)
	if root == "" {
		t.Skipf("$%s is not set: the corpora are fetched by CI, not committed", envCorpora)
	}
	for _, c := range floors {
		dir := ""
		for _, d := range c.dirs {
			if p := filepath.Join(root, filepath.FromSlash(d)); dirExists(p) {
				dir = p
				break
			}
		}
		if dir == "" {
			t.Logf("%s: absent below %s, skipped", c.name, root)
			continue
		}
		es, err := corpus.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		ok := 0
		for _, e := range es {
			fsys := os.DirFS(filepath.Dir(filepath.Join(dir, filepath.FromSlash(e.Path))))
			if _, err := syntax.Parse(filepath.Base(e.Path), e.Source, &syntax.Options{FS: fsys}); err == nil {
				ok++
			}
		}
		t.Logf("%s: %d files, %d parse (%.1f %%)", c.name, len(es), ok, 100*float64(ok)/float64(len(es)))
		if len(es) != c.files {
			t.Errorf("%s: %d files, the pinned revision has %d: wrong revision?", c.name, len(es), c.files)
		}
		if ok < c.parse {
			t.Errorf("%s: %d files parse, the floor is %d", c.name, ok, c.parse)
		}
	}
}
