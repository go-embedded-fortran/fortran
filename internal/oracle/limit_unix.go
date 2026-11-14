//go:build unix

package oracle

import (
	"os/exec"
	"runtime"
	"syscall"
)

const (
	limitSupported = true
	// macOS accepts setrlimit(RLIMIT_AS) but does not enforce it.
	limitEnforced = runtime.GOOS != "darwin"
)

// ownGroup puts cmd in its own process group and makes its cancellation kill
// the whole group: gfortran is a driver whose cc1/f951/as/ld children would
// otherwise outlive a timeout and pile up (a corpus run of gfortran.dg was
// killed by its runner that way).
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
