//go:build js || wasip1

package eval

// MaxCallDepth bounds the nesting of procedure activations: a deeper call is a
// run-time error (a runaway recursion), decided before another frame is made.
// Under WebAssembly the evaluator's own recursion runs on the host's call
// stack (V8's default one in a browser tab), which a native goroutine's does
// not: a call costs several host frames, and an overflow there is a RangeError
// that kills the whole Go instance, not an error of the cell. The bound is
// the one measured in Chromium with its default stack (Playwright's, V8's
// default of about 1 MB): a recursive function whose body is one statement
// overflows between 2000 and 2500 nested calls, the same inside a DO, a DO, an
// IF, a SELECT CASE and an IF between 600 and 900, so a frame costs 3 to 4
// times more with the nesting of its statements. 200 keeps a margin of 3 on
// the costliest frame measured; go-embedded-r's 300 is the same reasoning.
const MaxCallDepth = 200
