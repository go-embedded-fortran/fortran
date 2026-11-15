package eval

import (
	"errors"
	"strings"
	"testing"
)

// TARGET only allows a pointer to be associated with a variable. With pointers
// refused, it changes nothing that runs: the attribute and the statement are
// accepted, and POINTER still is a refusal (81 gfortran.dg run tests were
// refused on TARGET alone or before reaching their pointers).
func TestTargetIsAcceptedPointerIsRefused(t *testing.T) {
	out := mustRun(t, NewSession(SessionOptions{}), "integer, target :: a(3)\ninteger :: b\ntarget :: b\na = [1, 2, 3]\nb = sum(a)\nprint *, b\n")
	if strings.TrimSpace(out) != "6" {
		t.Errorf("target: %q, want 6", out)
	}
	_, _, _, err := runCell(t, NewSession(SessionOptions{}), "integer, pointer :: p\n")
	var e *Error
	if !errors.As(err, &e) || !e.Refused {
		t.Errorf("pointer: %v, want a refusal", err)
	}
}
