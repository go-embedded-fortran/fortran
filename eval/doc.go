// Package eval is PROTOTYPE contender A of the back-end gate of bib/fortran.md
// §4.2: a pure-Go evaluator over the syntax AST.
//
// It is the seed of M3-A, not the M3-A evaluator: it accepts exactly the
// language the four gate kernels need, and refuses everything else with a
// positioned error. What it accepts:
//
//   - one main program, implicit none, integer / integer(k) and real(8) /
//     double precision scalars, integer and real(8) parameters;
//   - real(8) arrays of rank 1 and 2 with lower bound 1, allocatable or of
//     constant shape, element access, column-major (the first subscript varies
//     fastest), bounds-checked;
//   - counted do loops, assignment, allocate, call cpu_time, print with the
//     a, f and es edit descriptors;
//   - whole-array assignment (array = array, array = scalar, + - * / of two
//     arrays, + and * of an array and a scalar) and matmul, all handed to
//     go-ndarray; sum(array); the scalar intrinsics real, dble, mod, sqrt, abs.
//
// # The typed tree
//
// Compile takes the syntax tree and its sema.Info (sema.Analyze of the same
// file; nil means analyse here). A file the analyser refuses is refused with
// the first diagnostic. Every type, kind, rank and constant value is read from
// Info (an expression the analyser folded, a literal, a named constant, an
// extent, is a constant of the program), a name is resolved through Info.Uses,
// and the compiler keeps only the storage slot of each variable. After
// compiling an expression it checks its own class (integer, real(8), array)
// against the analyser's type: a disagreement is an internal error, never a
// silent difference. The kernels' behaviour is unchanged: the gate's
// correctness smoke and the tests of this package are the witness.
//
// # Model
//
// The program is compiled once into closures over typed slots: an expression
// of type integer is a func(*frame) int64 and one of type real a
// func(*frame) float64, so arithmetic never boxes a value. A frame holds the
// integer slots, the real slots and the array slots.
//
// # Arrays and go-ndarray
//
// go-ndarray v0.40.3 is row-major and cannot adopt a caller's buffer
// (go-ndarray/ndarray#76, Wrap, open). The evaluator therefore allocates every
// array through go-ndarray and keeps the reversed shape: the Fortran array
// a(n1,n2), whose first subscript varies fastest, is the row-major
// *ndarray.Array of shape (n2,n1), and a(i,j) is element [j-1][i-1]. Element
// loops index the array's own storage (ndarray.Data returns it, not a copy, for
// a contiguous row-major array), so no copy is made when an array crosses to
// go-ndarray. matmul(a,b) is the transposed product: c' = b' a' on the
// reversed arrays, so it needs no transposed view either. The only copy is the
// result of go-ndarray's MatMul, which allocates its result, into the
// destination's storage; Copies counts the elements copied so that a run can
// report it.
//
// Not modified, not extended: go-ndarray is used as released.
package eval
