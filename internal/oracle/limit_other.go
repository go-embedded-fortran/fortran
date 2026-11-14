//go:build !unix

package oracle

import "os/exec"

func ownGroup(*exec.Cmd) {}

// No address-space limit outside Unix: there is no /bin/sh to set it.
const (
	limitSupported = false
	limitEnforced  = false
)
