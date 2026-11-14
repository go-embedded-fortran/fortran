package syntax

import (
	"strings"
	"testing"
)

// A WHERE statement holds an assignment (F2018 R1041) and a FORALL statement
// an assignment or a pointer assignment (R1055). Anything else is refused at
// parse time: the printer renders only those, and the fuzzer found
// "where (m) <construct>" printed as a WHERE construct with no end.
func TestSingleStatementBodies(t *testing.T) {
	for _, tc := range []struct {
		src, refused string // refused: a fragment of the error, "" when accepted
	}{
		{"where (m) a = b", ""},
		{"where (m) call s", "WHERE statement holds an assignment"},
		{"where (m) print *, a", "WHERE statement holds an assignment"},
		{"where (m) where (m) a = b", "WHERE statement holds an assignment"},
		{"where (m) forall (i = 1:2) a(i) = 0", "WHERE statement holds an assignment"},
		{"forall (i = 1:2) a(i) = 0", ""},
		{"forall (i = 1:2) p(i)%q => t", ""},
		{"forall (i = 1:2) call s(i)", "FORALL statement holds an assignment"},
		{"forall (i = 1:2) where (m) a = b", "FORALL statement holds an assignment"},
		{"forall (i = 1:2) forall (j = 1:2) a(i, j) = 0", "FORALL statement holds an assignment"},
	} {
		src := "program p\n" + tc.src + "\nend program\n"
		f, err := Parse("t.f90", []byte(src), &Options{Form: Free})
		if tc.refused == "" {
			if err != nil {
				t.Errorf("%q: %v", tc.src, err)
				continue
			}
			out := Print(f)
			f2, err := Parse("t.printed.f90", []byte(out), &Options{Form: Free})
			if err != nil || DumpFile(f) != DumpFile(f2) {
				t.Errorf("%q: printed form does not round-trip (%v):\n%s", tc.src, err, out)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.refused) {
			t.Errorf("%q: err = %v, want %q", tc.src, err, tc.refused)
		}
	}
}
