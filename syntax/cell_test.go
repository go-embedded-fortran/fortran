package syntax

import "testing"

// Options.Cell: loose statements form main-program material with no END,
// ending at EOF or at the next program unit (bib/fortran.md §7).
func TestCellGrammar(t *testing.T) {
	src := "integer :: i\ni = 3\nsubroutine s()\nend subroutine\nprint *, i\nmodule m\nend module\n"
	if _, err := Parse("c.f90", []byte(src), nil); err == nil {
		t.Fatal("the default grammar accepted loose statements without END")
	}
	f, err := Parse("c.f90", []byte(src), &Options{Form: Free, Cell: true})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, u := range f.Units {
		k := u.Kind.String()
		if u.Implicit {
			k += "*"
		}
		kinds = append(kinds, k)
	}
	want := []string{"program*", "subroutine", "program*", "module"}
	if len(kinds) != len(want) {
		t.Fatalf("units %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("units %v, want %v", kinds, want)
		}
	}
	if n := len(f.Units[0].Body); n != 2 {
		t.Errorf("first loose block has %d statements, want 2", n)
	}
	// a stray CONTAINS is an error, not a loop
	if _, err := Parse("c.f90", []byte("x = 1\ncontains\ny = 2\n"), &Options{Form: Free, Cell: true}); err == nil {
		t.Error("contains outside a unit was accepted")
	}
	// an explicit END still closes the loose material
	if _, err := Parse("c.f90", []byte("x = 1\nend\n"), &Options{Form: Free, Cell: true}); err != nil {
		t.Error(err)
	}
}
