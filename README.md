# fortran — Fortran for go-notebook cells, in pure Go

`github.com/go-embedded-fortran/fortran` is meant to become a Fortran
implementation written in Go (`CGO_ENABLED=0`), embedded in Go programs and run
in the browser through WebAssembly: it will back the ` ```fortran ` cells of
[go-notebook](https://github.com/go-notebook), as
[go-embedded-c/c](https://github.com/go-embedded-c/c) backs its C cells.

The plan is [go-scikit/roadmap `bib/fortran.md`](https://github.com/go-scikit/roadmap/blob/main/bib/fortran.md).
Every stage is checked against a reference, never against expectations written
by hand.

## Status

| milestone | contents | state |
|---|---|---|
| M0 oracle harness | `internal/oracle`, `cmd/foracle`, pinned gfortran 15.2.0, golden file, corpus runner, reference column of the gate | **done** |
| M1 front end, part 1 | lexer (free and fixed form, continuation, `include`), parser for the gate's kernels; **the §4.2 gate**, report published | planned |
| M2 front end, part 2 | semantics to a typed tree, the v1 subset, cell splitter and session module | planned |
| M3 back end | as the gate decides: A = closure compiler over the typed tree, B = lowering to go-embedded-c's IR | planned |
| M4 runtime | I/O (ported from flang-rt), intrinsics on go-ndarray and go-fft | planned |
| M5 notebook bridge | values table, `file:line` errors, cells on desktop and playground | planned |
| M6 conformance | FCVS, gfortran.dg run tests, reference LAPACK built by our front end; performance table | planned |

There is no Fortran implementation here yet: nothing in this repository
executes Fortran except the oracle's gfortran.

## The oracle

The reference is **gfortran 15.2.0** run natively (GCC `releases/gcc-15.2.0`): it
is the compiler LFortran's and llvm-test-suite's reference outputs are made
with. It is GPL and is only ever run as a program, never linked or ported (see
`NOTICE`). "Correct" means the same exit status and the same stdout and stderr
bytes, except where the output is processor-dependent (bib §8).

```
go install github.com/go-embedded-fortran/fortran/cmd/foracle@latest
export FORACLE_HOME=$HOME/foracle-home
foracle toolchain          # installs gfortran 15.2.0 into $FORACLE_HOME, no root
foracle version
foracle run prog.f90       # compile with the oracle, run, relay stdout/stderr/status
```

**How gfortran is obtained.** The bib does not settle it; the choice is the
conda-forge package `gfortran==15.2.0`, installed by a pinned **micromamba**
(one static binary per platform, release `2.10.0-0`, sha256 checked in
`internal/provision/provision.go`) into `$FORACLE_HOME/env`. Homebrew's gcc is
16.x, `apt` needs root, pkgx has no pinned build and building GCC takes an hour
per lane. macOS arm64 and Linux amd64 are supported; macOS needs the Xcode
command line tools (the driver links through the SDK). The packages are
verified by micromamba against the channel's checksums, and `foracle` itself
checks `gfortran --version` and refuses anything but 15.2.0
(`FORACLE_GFORTRAN` points at an existing gfortran instead; the same check
applies). Tests that need the toolchain skip cleanly without it
(`FORACLE_REQUIRE=1` turns the skip into a failure; CI sets it).

**flang 23.1.4** (the LLVM of the C oracle) is optional and Linux only:
`foracle toolchain --flang`, then `--flang` on `run` and `corpus`. The macOS
release has no flang. This path is written but not exercised by CI yet.

Programs are compiled with `-O0 -w -fno-backtrace` (goldens are always `-O0`;
`--opt` changes it elsewhere), the source form chosen by the extension as
gfortran does (`.f90` free, `.f` fixed). Runs are bounded: a wall-clock
timeout (10 s), a cap on stdout and stderr (1 MiB, the process is killed past
it), a fixed environment and an address-space limit (1 GiB, `RLIMIT_AS` applied
by a re-executed helper). The memory limit is enforced on Linux; **macOS accepts
`RLIMIT_AS` but does not enforce it**, so there it is a request, and
`Result.MemoryLimited` says which. There is no limit on Windows, where no oracle
is provisioned.

| command | what it does |
|---|---|
| `foracle golden` | regenerates `testdata/programs.golden` from the 14 programs in `testdata/programs/` |
| `foracle corpus --dir D` | compiles and runs every `.f90 .f95 .f03 .f08` (free form) and `.f .for` (fixed form) file below D; one line per file: path, outcome, exit status, detail |
| `foracle bench` | the reference column of the §4.2 gate: the four kernels of `testdata/gate/` (five programs: K3 as loops and as `matmul`) with gfortran `-O2`, 7 rounds, median and spread; refuses above 1-minute load 8 |
| `foracle toolchain --home H [--flang]` | installs the pinned toolchain |
| `foracle version` | prints the pinned versions and whether the toolchain was found |

### Golden programs

`testdata/programs/*.f90` are 14 original programs: list-directed and formatted
output, integer and real kinds, arrays and sections, column-major order,
modules, derived types (with type-bound procedures and allocatable
components), recursion, strings and internal I/O, `stop`, `stop 'message'`,
`error stop` and stderr. They avoid anything that differs between platforms
(libm functions, addresses): CI regenerates the file on linux/amd64 and
darwin/arm64 and requires no difference. `testdata/programs.golden` records
exit status, stdout and stderr, and the signal if there was one; its decoder
is bounded and fuzzed (`internal/golden`, corpus committed).

### Corpora

None is committed (the gcc testsuite is GPL; FCVS has its own terms; see
`NOTICE`). CI fetches them by commit and runs the oracle over them in a job
that reports and does not gate: many `gfortran.dg` files are written to be
refused or need other files.

| corpus | revision | directory |
|---|---|---|
| gcc `gfortran.dg` | gcc 15.2.0, `5115c7e447fc` | `gcc/testsuite/gfortran.dg` |
| gcc `gfortran.fortran-torture` | gcc 15.2.0, `5115c7e447fc` | `gcc/testsuite/gfortran.fortran-torture` |
| llvm-test-suite FCVS | `19b349c5aa39` | `Fortran/UnitTests/fcvs21_f95` |
| LFortran `integration_tests` | `4f2cd049c87f` | `integration_tests` |

Counts per corpus are not given here: the first CI run of the corpora job
produces them (artefact `corpora-oracle`) and nothing was measured before.

## Not yet

- No lexer, parser or evaluator: M1 starts with the gate of bib §4.2, whose
  oracle side (`foracle bench`) runs here. Its contenders and the decision
  rule are not implemented, and the bench times were not published: a first
  smoke run was taken at 1-minute load above 6 on a shared machine.
- The flang path is not exercised in CI, and the Linux memory limit is tested
  only in CI (the development machine is macOS).
- Reading soypat/go-fortran in depth (bib §10, M0) is not done.
- The org landing page and the docs site wait for M5; this README is the only
  user-facing page at M0.

Licence: BSD 3-Clause (see `LICENSE`, and `NOTICE` for what is only run).
