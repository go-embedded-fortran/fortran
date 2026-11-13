//go:build !unix

package oracle

import (
	"os"
	"os/exec"
)

func ownGroup(*exec.Cmd) {}

// No address-space limit outside Unix: the helper is never used.
const (
	limitSupported = false
	limitEnforced  = false
)

func childMain(string) { os.Exit(127) }
