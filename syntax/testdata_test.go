package syntax

import (
	"os"
	"path/filepath"
	"testing"
)

// testdataFiles lists the Fortran files of the repository's testdata
// (testdata/programs and testdata/gate), which every test must cover.
func testdataFiles(t testing.TB) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{"programs", "gate"} {
		m, err := filepath.Glob(filepath.Join("..", "testdata", dir, "*.f*"))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m...)
	}
	if len(out) < 19 {
		t.Fatalf("expected the 14 programs and 5 gate kernels, found %d files", len(out))
	}
	return out
}

func TestParseTestdata(t *testing.T) {
	for _, f := range testdataFiles(t) {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := Parse(f, src, nil)
		if err != nil {
			t.Errorf("%s: %v", f, err)
		}
		if len(file.Units) == 0 {
			t.Errorf("%s: no program unit", f)
		}
	}
}
