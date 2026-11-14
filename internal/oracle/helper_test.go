package oracle_test

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/oracle"
)

// The memory-limit helper must add nothing to a program's output. Issue #9:
// on linux a golden program twice picked up the 13 bytes "fatal error: " on
// stderr, stdout identical, exit 0 — the Go runtime of the helper dying on an
// mmap refused by the lowered RLIMIT_AS, cut short by the execve. The helper
// now lowers the limit and execs with no allocation in between. This runs a
// trivial program many times under a tight limit; any stray byte fails it.
func TestHelperAddsNothingToStderr(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("RLIMIT_AS is enforced on linux only: the failure cannot happen here")
	}
	echo := "/bin/echo"
	if _, err := os.Stat(echo); err != nil {
		t.Skipf("%s: %v", echo, err)
	}
	const runs = 300
	for i := range runs {
		res, err := oracle.Run(context.Background(), echo, "", []string{"hi"}, nil, oracle.Limits{MemoryBytes: 64 << 20})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if !res.MemoryLimited {
			t.Fatalf("run %d: the limit is not in force, so this test measures nothing", i)
		}
		if string(res.Stdout) != "hi\n" || len(res.Stderr) != 0 || res.Exit != 0 {
			t.Fatalf("run %d: stdout %q, stderr %q, exit %d", i, res.Stdout, res.Stderr, res.Exit)
		}
	}
}
