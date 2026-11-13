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
| M1 front end, part 1 | lexer (free and fixed form, continuation, `include`), parser for the gate's kernels; **the §4.2 gate**, report published | in progress (syntax done) |
| M2 front end, part 2 | semantics to a typed tree, the v1 subset, cell splitter and session module | planned |
| M3 back end | as the gate decides: A = closure compiler over the typed tree, B = lowering to go-embedded-c's IR | planned |
| M4 runtime | I/O (ported from flang-rt), intrinsics on go-ndarray and go-fft | planned |
| M5 notebook bridge | values table, `file:line` errors, cells on desktop and playground | planned |
| M6 conformance | FCVS, gfortran.dg run tests, reference LAPACK built by our front end; performance table | planned |

There is no Fortran evaluator here yet: nothing in this repository executes
Fortran except the oracle's gfortran. The `syntax` package parses it (M1 part
1); the §4.2 gate (evaluator prototype and measurements) is the next pull
request.

## The parser

`github.com/go-embedded-fortran/fortran/syntax` is a pure-Go (`CGO_ENABLED=0`)
lexer and recursive-descent parser producing a typed AST with byte offsets that
resolve to `file:line:col`. The package documentation cites the clauses of
F2018 (J3/18-007r1) that were read for it: 6.3.1 to 6.3.3 (source form, free
and fixed), 6.4 (`INCLUDE`), and 10.1.1 (operator precedence). The AST design
follows LFortran's `AST.asdl` (BSD-3, a design reference); its GLR grammar is not
ported.

```go
f, err := syntax.Parse("prog.f90", src, &syntax.Options{FS: includeFS})
// err is a syntax.ErrorList: "prog.f90:12:7: expected ')', found ','"
for _, u := range f.Units { ... }
fmt.Print(syntax.Print(f))   // canonical free-form source; parses back to the same AST
```

- **Source form** by extension (`.f .for .f77 .ftn` fixed, the rest free, as
  gfortran) or `Options.Form`. Free form: `&` continuation in both contexts,
  comments, `;`. Fixed form: columns 1-5 label, column 6 continuation, columns
  7-72, `C`, `*`, `!` comments, tabs, blanks insignificant (`DO 10 I = 1.10` is an
  assignment), FORMAT kept as text (Hollerith included).
- **`INCLUDE`** resolves through `Options.FS` only: with a nil FS every include
  is refused, the host file system is never read by default, absolute and `..`
  paths are refused, and an include of a file already being included is refused
  (F2018 6.4).
- **Names and keywords** are case-insensitive and normalised to lower case; the
  original spelling is kept in `Ident.Orig` for errors. There are no reserved
  words (`if = 1` is an assignment).
- **Literals**: kinds (`1.0d0`, `1.0_dp`, `1_8`), BOZ, strings with doubled
  quotes in both delimiters, `4_"text"`; operators `.eq.` and `==`, defined
  operators `.foo.`, `(/ ... /)` and `[...]`.
- **Constructs**: program, module, submodule (header), subroutine, function
  (prefixes, `result`, `bind`), interface blocks (generic, operator, abstract),
  `contains`, derived types with type-bound procedures, enumerations,
  declarations with attributes, kinds and initialisers, `implicit`, `use` with
  `only` and renames, `parameter`, `data`, `common`, `equivalence`, `namelist`,
  `entry`, `bind`; `if` (block, logical, arithmetic), `do` (counted, `while`,
  `concurrent`, labelled with shared terminal statement), `select case`, `select
  type`, `select rank`, `where`, `forall`, `associate`, `block`, `goto`
  (plain, computed, assigned), `assign`, `call`, `return`, `stop`, `error stop`,
  `print`/`read`/`write` and the other I/O statements, `allocate`, `deallocate`,
  `nullify`, array sections `a(1:n:2, :)`, implied do loops, pointer assignment.
- **Errors** are `file:line:col: message`, the parser recovers at the next
  statement and never panics. Bounds assert a refusal with an error: source
  bytes (also summed over includes, 16 MiB), nesting depth of constructs and of
  expressions (200), include depth (8), continuation lines of a statement (255,
  the standard's limit, F2018 6.3.2.6 and 6.3.3.5), tokens of a statement
  (262 144) and reported errors (50); `Options.Limits` changes them.
- **Tests**: a printer round trip (print, parse, same AST dump) on every file
  of `testdata/programs` and `testdata/gate` (19 files, all parse) and on a
  construct tour; golden AST dumps (`syntax/testdata/golden`, regenerate with
  `SYNTAX_UPDATE_GOLDEN=1`); three fuzz targets (`FuzzParseFree`,
  `FuzzParseFixed`, `FuzzLex`) whose corpus is committed in
  `syntax/testdata/fuzz` (seeded from the testdata and lexer edge cases; the
  fuzzers also check that a clean parse prints to source that parses to the same
  AST); bomb tests (deep nesting, huge sources, continuation and include
  bombs, a million errors) that assert a refusal under a soft memory limit, run
  in CI with `GOMEMLIMIT=256MiB` and `ulimit -v`.

### Parse rate on the corpora

`foracle parse-rate --dir D [--out report.tsv]` parses every `.f90 .f95 .f03 .f08
.f .for` file below D (a file's own directory is its include path) and runs
`gfortran -fsyntax-only` on the same file, giving the three classes of
bib §8: both accept, fail-only-ours (gfortran accepts, we refuse: a defect or a
documented gap), fail-gfortran-only and fail-both (many `gfortran.dg` files exist
to be refused, or fail semantic checks that a parser does not make). The
numbers below were taken on the pinned revisions with gfortran 15.2.0; the
per-file lines are the `--out` report, which CI uploads in the `corpora-oracle`
artefact, and `syntax/corpus_test.go` asserts the floors (`FORACLE_CORPORA`).

| corpus | files | we parse | gfortran accepts | both accept | fail-only-ours | fail-gfortran-only | fail-both |
|---|---|---|---|---|---|---|---|
| FCVS (`fcvs21_f95`) | 180 | 180 (100 %) | 179 | 179 | 0 | 1 | 0 |
| gcc `gfortran.fortran-torture` | 314 | 313 (99.7 %) | 313 | 312 | 1 | 1 | 0 |
| gcc `gfortran.dg` | 8 306 | 7 496 (90.2 %) | 6 096 | 5 973 | 123 | 1 523 | 687 |

Of the files gfortran accepts, the parser accepts 100 % of FCVS, 99.7 % of the
torture suite and 98.0 % of `gfortran.dg`. The 123 `gfortran.dg` files that
gfortran accepts and we refuse are, by first error: derived-type parameters (30,
excluded from v1, bib §1.1), DEC `parameter x = v` without parentheses and other
extensions (28), preprocessor remnants of `.F90` files (19, `#` lines are
skipped, not interpreted), `contains` followed by `module procedure` (17,
submodules are v1-excluded), coarray statements (7, v1-excluded), files with
more than 255 continuation lines (5, where gfortran is lenient and we apply the
standard's limit on purpose), Hollerith outside `FORMAT`, UTF-16/32 encodings,
and a few single files. LFortran's `integration_tests` were not parsed here
(CI runs them).

### Throughput against `gfortran -fsyntax-only`

`foracle parse-bench --dir D --gf-cache report.tsv` times `syntax.Parse` and
`gfortran -fsyntax-only` on the largest files both accept: one core, rounds
interleaved (our parse, then gfortran, for each file in each round), medians,
the process start of gfortran measured on an empty file and reported next to
the ratio, refused above 1-minute load 8. `go test -bench . ./syntax` gives MB/s
and allocations with `FORACLE_CORPORA` set.

**Not measured yet.** The development machine is shared and its 1-minute load
stayed between 10 and 100 during this work, above the fleet's limit of 8, so
`parse-bench` refused to run (as designed) and no throughput figure is
published rather than a loaded one. The gap to record is expected to be large
in our favour for the wrong reason (gfortran's time is mostly process start and
its middle end, which `-fsyntax-only` does not skip), so the table reports the
empty-file start time next to the ratio. A first unloaded run replaces this
paragraph. Allocation counts are exact under load: parsing the 19 testdata files
is 4 470 allocations (380 KB) per pass, and parsing the largest FCVS files
(86 to 118 KB of fixed form) 5 000 to 7 600 allocations (260 to 400 KB). For scale only, not a measurement: the 8 306 `gfortran.dg` files
parse in 0.6 s of wall time in `TestCorporaParseFloor` (reading, parsing and
resolving includes, several cores, load 10).

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
| `foracle parse-rate --dir D` | parses every Fortran file below D with the `syntax` package and compares with `gfortran -fsyntax-only` (see "The parser") |
| `foracle parse-bench --dir D` | parse throughput against `gfortran -fsyntax-only` on the largest files both accept; refused above load 8 |
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

- No evaluator, and no semantic analysis: the gate of bib §4.2 is the next
  pull request, whose oracle side (`foracle bench`) runs here. Its contenders
  and the decision rule are not implemented, and the bench times were not
  published: a first smoke run was taken at 1-minute load above 6 on a shared
  machine.
- Not parsed yet, of bib §1.1: submodule procedure bodies (`module procedure
  x ... end procedure`; the `submodule` header parses), derived-type
  parameters (refused by design), coarray and team statements (`codimension`
  is refused with a message; `sync`, `critical`, `event`, `lock`, `form team`,
  `fail image` are not recognised), `do concurrent` locality specifiers
  (`local`, `shared`, `reduce`), Hollerith constants outside `FORMAT`, Cray
  pointers and DEC extensions, `#` preprocessor directives (such lines are
  skipped, never interpreted). The text of `FORMAT` statements and of format
  strings is kept as text: its parser is M4. A statement function parses as an
  assignment; telling them apart needs scopes (M2).
- The flang path is not exercised in CI, and the Linux memory limit is tested
  only in CI (the development machine is macOS).
- Reading soypat/go-fortran in depth (bib §10, M0) is not done.
- The org landing page and the docs site wait for M5; this README is the only
  user-facing page at M0.

Licence: BSD 3-Clause (see `LICENSE`, and `NOTICE` for what is only run).
