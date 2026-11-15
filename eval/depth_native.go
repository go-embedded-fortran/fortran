//go:build !(js || wasip1)

package eval

// MaxCallDepth bounds the nesting of procedure activations: a deeper call is a
// run-time error (a runaway recursion), decided before another frame is made.
// Under js and wasip1 it is lower (depth_wasm.go): a browser's call stack is
// far shallower than a native goroutine's.
const MaxCallDepth = 1 << 15
