package oracle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

// Limits bound one execution. A program under test is untrusted: nothing it
// does may outgrow these.
type Limits struct {
	// Timeout is the wall-clock bound (10 s when zero).
	Timeout time.Duration
	// MemoryBytes bounds the address space of the process (1 GiB when zero),
	// through RLIMIT_AS set by /bin/sh before it execs the program (see
	// limitScript). It is enforced
	// on Linux; macOS ignores RLIMIT_AS, so there it is a request, not a bound
	// (Result.MemoryLimited says which). A negative value asks for no limit.
	MemoryBytes int64
	// MaxOutput is the bound on each of stdout and stderr (1 MiB when zero);
	// the process is killed when one is exceeded.
	MaxOutput int
}

func (l Limits) withDefaults() Limits {
	if l.Timeout == 0 {
		l.Timeout = 10 * time.Second
	}
	if l.MemoryBytes == 0 {
		l.MemoryBytes = 1 << 30
	}
	if l.MaxOutput == 0 {
		l.MaxOutput = 1 << 20
	}
	return l
}

// Result is what one run of an executable produced.
type Result struct {
	Stdout, Stderr []byte
	// Exit is the exit status; 0 when the process ended by a signal, timed
	// out or was killed for its output.
	Exit uint32
	// Signal is non-empty when the process ended by a signal (SIGSEGV, SIGABRT
	// …) rather than by exiting.
	Signal string
	// TimedOut reports that the Timeout expired.
	TimedOut bool
	// Truncated reports that an output stream hit MaxOutput.
	Truncated bool
	// MemoryLimited reports that the address-space limit was in force.
	MemoryLimited bool
}

// Init did the work of a re-executed Go helper that lowered the limits and
// exec'd the program. That helper is gone (#9): with RLIMIT_AS below the Go
// runtime's own reservation, any mmap of the runtime between the limit and the
// exec failed and threw "fatal error: runtime: cannot allocate memory" into
// what became the program's stderr. The limits are now set by /bin/sh (see
// limitScript), which has no runtime. Init does nothing; its callers may drop
// it.
func Init() {}

// limitScript lowers the file-size limit (64 MiB) and the address-space limit
// to as bytes, then replaces the shell by "$0" "$@". A refusal (macOS rejects
// an address space below the current size) leaves the program unlimited and
// prints nothing; Result.MemoryLimited reports what is enforced. ulimit -f
// counts 512-byte blocks and -v KiB (POSIX for -f; dash, bash and zsh for -v).
func limitScript(as int64) string {
	return fmt.Sprintf(`ulimit -f %d 2>/dev/null; ulimit -v %d 2>/dev/null; exec "$0" "$@"`, (64<<20)/512, (as+1023)/1024)
}

// capped is a writer that keeps at most max bytes and fails past them.
type capped struct {
	buf       bytes.Buffer
	max       int
	truncated bool
	over      func()
}

var errFull = errors.New("output limit reached")

func (c *capped) Write(p []byte) (int, error) {
	room := c.max - c.buf.Len()
	if len(p) > room {
		c.buf.Write(p[:room])
		c.truncated = true
		if c.over != nil {
			c.over()
		}
		return room, errFull
	}
	return c.buf.Write(p)
}

// Run executes the program at path with args and stdin, under lim, in dir
// (the program's working directory; "" for the current one). The returned
// error is for failures of the harness (the program cannot be started);
// anything the program does, including dying by a signal, is in the Result.
func Run(ctx context.Context, path, dir string, args []string, stdin []byte, lim Limits) (Result, error) {
	lim = lim.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()
	killed := &atomic.Bool{}
	over := func() { killed.Store(true); cancel() }
	out, errw := &capped{max: lim.MaxOutput, over: over}, &capped{max: lim.MaxOutput, over: over}

	name, argv := path, args
	limited := false
	if lim.MemoryBytes > 0 && limitSupported {
		name = "/bin/sh"
		argv = append([]string{"-c", limitScript(lim.MemoryBytes), path}, args...)
		limited = limitEnforced
	}
	cmd := exec.CommandContext(ctx, name, argv...)
	cmd.Dir = dir
	// A fixed environment: the run must not depend on the host's locale or
	// on GFORTRAN_* variables.
	cmd.Env = []string{"LC_ALL=C", "PATH=" + os.Getenv("PATH")}
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = out, errw
	cmd.WaitDelay = 2 * time.Second
	ownGroup(cmd)
	err := cmd.Run()

	res := Result{Stdout: out.buf.Bytes(), Stderr: errw.buf.Bytes(), Truncated: out.truncated || errw.truncated, MemoryLimited: limited}
	if err == nil {
		return res, nil
	}
	if killed.Load() {
		return res, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signal = ws.Signal().String()
			return res, nil
		}
		if c := ee.ExitCode(); c >= 0 {
			res.Exit = uint32(c)
			return res, nil
		}
	}
	return res, fmt.Errorf("oracle: running %s: %w", path, err)
}

// Output compiles src and runs it with no arguments or input: a convenience
// for tests. The build directory is removed before it returns.
func (t *Toolchain) Output(ctx context.Context, src []byte, co CompileOptions, lim Limits) (Result, error) {
	dir, err := os.MkdirTemp("", "foracle-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	exe, err := t.Compile(ctx, dir, src, co)
	if err != nil {
		return Result{}, err
	}
	return Run(ctx, exe, dir, nil, nil, lim)
}
