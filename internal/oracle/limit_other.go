//go:build !unix

package oracle

import "os"

// No address-space limit outside Unix: the helper is never used.
const (
	limitSupported = false
	limitEnforced  = false
)

func childMain(string) { os.Exit(127) }
