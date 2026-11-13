package sema_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/corpus"
	"github.com/go-embedded-fortran/fortran/internal/parsebench"
)

// floors are the semantic differential measured when they were introduced
// (README, "Semantic analysis"): both = files our front end accepts and
// gfortran -fsyntax-only accepts (a drop is a regression); refusedOnlyByUs =
// files gfortran accepts and we refuse (a rise is a regression);
// acceptedOnlyByUs = files gfortran refuses and we accept (a rise is a
// regression: many gfortran.dg files exist to be refused for reasons this
// milestone does not check yet). gfortran's verdicts are the committed
// testdata/gf-*.tsv (gfortran 15.2.0, `foracle sema-rate --gf-out`), so the
// test needs the corpora but not the compiler.
var floors = []struct {
	name             string
	dirs             []string
	verdicts         string
	dgRun            bool
	files            int
	both             int
	refusedOnlyByUs  int
	acceptedOnlyByUs int
}{
	{"FCVS", []string{"lts/Fortran/UnitTests/fcvs21_f95", "llvm-test-suite/Fortran/UnitTests/fcvs21_f95"}, "gf-fcvs.tsv", false, 180, 179, 0, 1},
	{"gfortran.fortran-torture", []string{"gcc/gcc/testsuite/gfortran.fortran-torture"}, "gf-torture.tsv", false, 314, 313, 1, 0},
	{"gfortran.dg", []string{"gcc/gcc/testsuite/gfortran.dg"}, "gf-dg.tsv", false, 8306, 5742, 302, 1292},
	{"gfortran.dg (dg-do run)", []string{"gcc/gcc/testsuite/gfortran.dg"}, "gf-dg.tsv", true, 3151, 2667, 163, 95},
}

func TestCorporaDifferentialFloor(t *testing.T) {
	root := os.Getenv("FORACLE_CORPORA")
	if root == "" {
		t.Skip("$FORACLE_CORPORA is not set: the corpora are fetched by CI, not committed")
	}
	for _, c := range floors {
		dir := ""
		for _, d := range c.dirs {
			p := filepath.Join(root, filepath.FromSlash(d))
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				dir = p
				break
			}
		}
		if dir == "" {
			t.Logf("%s: absent below %s, skipped", c.name, root)
			continue
		}
		f, err := os.Open(filepath.Join("testdata", c.verdicts))
		if err != nil {
			t.Fatal(err)
		}
		cache := parsebench.ReadGfVerdicts(f)
		f.Close()
		es, err := corpus.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if c.dgRun {
			es = parsebench.DgRun(es)
		}
		if len(es) != c.files {
			t.Errorf("%s: %d files, the pinned revision has %d: wrong revision?", c.name, len(es), c.files)
		}
		recs := parsebench.RateSema(t.Context(), nil, dir, es, 1, cache)
		s := parsebench.CountSema(recs)
		t.Logf("%s: %d files, parse %d, analysed %d; gfortran accepts %d: both-accept=%d refused-only-by-us=%d accepted-only-by-us=%d both-refuse=%d",
			c.name, s.Files, s.Parsed, s.Analysed, s.GfAccepted, s.Both, s.OnlyOurs, s.OnlyGf, s.Neither)
		if s.Both < c.both {
			t.Errorf("%s: both-accept %d, the floor is %d", c.name, s.Both, c.both)
		}
		if s.OnlyOurs > c.refusedOnlyByUs {
			t.Errorf("%s: refused-only-by-us %d, the ceiling is %d", c.name, s.OnlyOurs, c.refusedOnlyByUs)
		}
		if s.OnlyGf > c.acceptedOnlyByUs {
			t.Errorf("%s: accepted-only-by-us %d, the ceiling is %d", c.name, s.OnlyGf, c.acceptedOnlyByUs)
		}
	}
}
