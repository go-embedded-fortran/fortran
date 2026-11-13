//go:build unix

package oracle

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
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

// childMain runs in the helper process: it lowers RLIMIT_AS, then becomes the
// program. It returns only to exit.
func childMain(prog string) {
	if v := os.Getenv(envChildAS); v != "" {
		n, err := strconv.ParseUint(v, 10, 63)
		if err != nil {
			fmt.Fprintf(os.Stderr, "foracle: bad %s=%q\n", envChildAS, v)
			os.Exit(127)
		}
		// A refusal (macOS rejects values below the current size) leaves the
		// program unlimited; Result.MemoryLimited reports what is enforced.
		_ = syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{Cur: n, Max: n})
	}
	// A program may not fill the disk either (64 MiB per file).
	_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 64 << 20, Max: 64 << 20})
	env := []string{"LC_ALL=C"}
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	argv := append([]string{prog}, os.Args[1:]...)
	err := syscall.Exec(prog, argv, env)
	fmt.Fprintf(os.Stderr, "foracle: exec %s: %v\n", prog, err)
	os.Exit(127)
}
