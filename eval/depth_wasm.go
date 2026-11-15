//go:build js || wasip1

package eval

// MaxCallDepth is the number of nested calls of a unit whose statements nest as
// little as they can, under WebAssembly; a unit with deeper nesting is allowed
// fewer (callWeight). A deeper call is a run-time error decided before another
// frame is made. The evaluator's own recursion runs on the host's call stack
// (V8's default one in a browser tab), which a native goroutine's does not: a
// call costs several host frames, in proportion to the nesting of the
// statements around the recursive call, and an overflow there is a RangeError
// that kills the whole Go instance, not an error of the cell.
//
// Measured in Chromium with its default stack (Playwright's), the depth at which
// a recursive function overflows, by the deepest nesting of its statements
// (the body is level 1): level 2, between 2000 and 2500; level 7, between 600
// and 900; level 29, between 150 and 199. The cost of a call is about
// proportional to that level, so a call weighs its level and the sum of the
// weights is bounded by 1500, under a third of what the stack holds: 750 calls
// at level 2, 51 at level 29.
const MaxCallDepth = 1500

// maxCallWeight is the bound the weights of the nested calls are summed against.
const maxCallWeight = MaxCallDepth

// callWeight is what a call of a unit whose statements nest maxNest deep and
// whose expressions nest maxX deep weighs.
func callWeight(maxNest, maxX int) int { return max(1, maxNest) }
