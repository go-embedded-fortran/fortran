//go:build race

package rt

// the race detector adds allocations of its own: the exact-count assertion is
// for the plain build
func init() { raceEnabled = true }
