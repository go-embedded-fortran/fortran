//go:build !(js || wasip1)

package eval

// MaxCallDepth bounds the nesting of procedure activations: a deeper call is a
// run-time error (a runaway recursion), decided before another frame is made.
// Under js and wasip1 the bound is a weight, not a count (depth_wasm.go): a
// browser's call stack is far shallower than a native goroutine's.
const MaxCallDepth = 1 << 15

// maxCallWeight is the bound the weights of the nested calls are summed against.
const maxCallWeight = MaxCallDepth

// callWeight is what a call of a unit whose statements nest maxNest deep and
// whose expressions nest maxX deep weighs: natively, one.
func callWeight(maxNest, maxX int) int { return 1 }
