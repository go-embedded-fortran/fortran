//go:build unix && !linux

package oracle

import "syscall"

// execLimited lowers RLIMIT_FSIZE and, when as > 0, RLIMIT_AS, then replaces
// the process by prog. It returns only if exec fails. On these systems the
// address-space limit is not enforced (macOS) or not relied on, so the plain
// calls are used; see exec_linux.go for why Linux needs raw ones.
func execLimited(prog string, argv, env []string, as, fsize uint64) error {
	if as > 0 {
		_ = syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{Cur: as, Max: as})
	}
	_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: fsize, Max: fsize})
	return syscall.Exec(prog, argv, env)
}
