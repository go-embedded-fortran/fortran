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
