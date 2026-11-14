//go:build linux

package oracle

import (
	"runtime"
	"runtime/debug"
	"syscall"
	"unsafe"
)

// execLimited lowers RLIMIT_FSIZE and, when as > 0, RLIMIT_AS, then replaces
// the process by prog. It returns only if execve fails.
//
// Everything execve needs is built BEFORE the address-space limit is lowered,
// the collector is off, and both limits and execve are raw system calls: from
// the first prlimit to execve the Go runtime must not map memory. Lowered
// below the runtime's own reservation, RLIMIT_AS makes any later mmap fail,
// and a runtime thread that hits it dies printing "fatal error: " — which our
// thread's execve then cuts short, leaving exactly those 13 bytes on the
// program's stderr while the program itself runs normally (#9: two goldens on
// linux, stdout identical, exit 0). macOS does not enforce RLIMIT_AS, which is
// why it was never seen there.
func execLimited(prog string, argv, env []string, as, fsize uint64) error {
	path, err := syscall.BytePtrFromString(prog)
	if err != nil {
		return err
	}
	argvp, err := syscall.SlicePtrFromStrings(argv)
	if err != nil {
		return err
	}
	envp, err := syscall.SlicePtrFromStrings(env)
	if err != nil {
		return err
	}
	fs := syscall.Rlimit{Cur: fsize, Max: fsize}
	asl := syscall.Rlimit{Cur: as, Max: as}
	debug.SetGCPercent(-1)
	runtime.LockOSThread()

	// No allocation from here on.
	prlimit(syscall.RLIMIT_FSIZE, &fs)
	if as > 0 {
		prlimit(syscall.RLIMIT_AS, &asl)
	}
	_, _, errno := syscall.RawSyscall(syscall.SYS_EXECVE,
		uintptr(unsafe.Pointer(path)),
		uintptr(unsafe.Pointer(&argvp[0])),
		uintptr(unsafe.Pointer(&envp[0])))
	return errno
}

// prlimit sets a limit of the calling process; a refusal is ignored (the
// caller reports what is enforced).
func prlimit(resource int, lim *syscall.Rlimit) {
	syscall.RawSyscall6(syscall.SYS_PRLIMIT64, 0, uintptr(resource), uintptr(unsafe.Pointer(lim)), 0, 0, 0)
}
