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
| M2 front end, part 2 | semantics to a typed tree, the v1 subset, cell splitter and session module | in progress: package `sema` (scopes, symbols, implicit typing, kinds, constants, expression typing, `use`, derived types: PR 1), the cell splitter, session module and go-notebook-facing analysis (PR 4), generic resolution, argument association, explicit-interface requirements and `intent` at calls (PR 2); shape conformance (PR 3) and `eval` on the typed tree are not done |
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
| `foracle gate [--smoke]` | the whole §4.2 gate: the contenders on the kernels, checksum bounds, the decision rule (see "The gate"); refuses above 1-minute load 8 |
| `foracle parse-rate --dir D` | parses every Fortran file below D with the `syntax` package and compares with `gfortran -fsyntax-only` (see "The parser") |
| `foracle parse-bench --dir D` | parse throughput against `gfortran -fsyntax-only` on the largest files both accept; refused above load 8 |
| `foracle toolchain --home H [--flang]` | installs the pinned toolchain |
| `foracle version` | prints the pinned versions and whether the toolchain was found |

### The gate (bib §4.2)

`foracle gate` decides between (A) a pure-Go evaluator and (B) compiling to
wasm32. The decision rule is fixed in bib §4.2 and is implemented once, in
`internal/gate/rule.go`, with its thresholds (5, 2, 1.2) pinned by a test; the
tests of the rule are table-driven with the edge cases at exactly 5, 2 and 1.2.

| contender | what it is |
|---|---|
| **A** | package `eval` (**prototype**, the seed of M3-A): the `syntax` AST compiled once into closures over typed slots (integer and real slots, no boxing in arithmetic); `real(8)` arrays of rank 1 and 2, column-major, bounds-checked; whole-array expressions and `matmul` are handed to go-ndarray v0.40.3. It accepts exactly the gate kernels' language and refuses the rest with a positioned error. |
| **B-near**, **B-upper** | the C transliterations in `testdata/gate/c/` (line for line, same checksums, same formulas) compiled by clang 23.1.4 to `wasm32-wasip1` at `-O0` and `-O2`, run on wazero v1.12.0 (the version go-embedded-c pins). The array kernels (`matmul`, `a = b*c + d`) call the Go library through a host import, so the operands are copied out of linear memory and the result back inside the timed region, as bib §4.2 says; the copy cost alone is reported next to it. |
| **gfortran** | 15.2.0 `-O2`, native: the reference. |
| **direct** | go-ndarray called straight from Go, no evaluator, no copy: the baseline of A's array kernels. |

The toolchain of B is go-embedded-c's oracle, which keeps its provisioning in
`internal` packages: this repository does not copy it, it asks the `coracle`
binary for it (`go install github.com/go-embedded-c/c/cmd/coracle@v0.2.1`,
then `CORACLE_HOME=<dir> coracle toolchain`) and only uses the layout it installs.

Every contender times its kernel with a clock around the kernel itself, one
core (`GOMAXPROCS=1`), in interleaved rounds (every kernel, every contender, in
each round; 7 rounds, median and spread). **A checksum out of its bound is a
failure, not a time.** The bound is stated in `internal/gate/kernel.go`: the
exact checksum is computed with `math/big` (`internal/gate/ref.go`; K3's by
the exact identity `sum(ab) = sum_k colsum_k(a) rowsum_k(b)` in rational
arithmetic), and a contender is within `1.01 * m * 2^-53 * exact`, `m` being
the number of rounding steps on the longest chain of the kernel (nonnegative
data, so the bound holds for any summation order, FMA or vectorisation: no
contender has to reproduce another's bits). A wrong C kernel is refused by a
test.

**Arrays and go-ndarray.** go-ndarray v0.40.3 is
row-major and cannot adopt a buffer (go-ndarray/ndarray#76, `Wrap`, open).
Contender A allocates every array through go-ndarray in the reversed shape (the
Fortran `a(n1,n2)` is the row-major `(n2,n1)` array) and indexes the array's own
storage (`ndarray.Data` returns it, not a copy), so **no copy is forced when an
array crosses to go-ndarray**, neither for `a = b*c + d` (written by `AddInto`)
nor for `matmul` (`c' = b' a'` on the reversed arrays). The one copy is the
result of `MatMul`, which go-ndarray allocates itself, into `c`'s storage:
n² elements, counted and printed (`A copied N elements`). Under B every array
crosses by copy, both ways.

**Home.** Only the desktop home is implemented: the playground (headless
Chromium, timed inside the page) needs a browser lane that does not exist yet.
The rule needs both homes to pick A, so **with the desktop alone it can only
answer B or "A first"**; the output says so. The browser lane is
[#5](https://github.com/go-embedded-fortran/fortran/issues/5).

`foracle gate --smoke` runs tiny sizes for correctness only, prints no time and
no verdict, and is what CI runs (`gate-smoke`, linux and macOS): a shared runner
is never at a load the gate accepts.

**Figures: not run, load.** The development machine was at 1-minute load 10 to
100 on 2026-11-13, above the gate's limit of 8, and `foracle gate` refuses to
measure there (and discards a run whose load rises above 8 during it): no time
and no verdict is published here. The first unloaded run's table, with its load
samples, replaces this paragraph.

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

## Semantic analysis (M2, part 1)

`github.com/go-embedded-fortran/fortran/sema` turns a parsed file into the
**typed tree**: the syntax tree annotated by `sema.Info` (the design of
`go/types`): `Info.Types` maps every expression to its type, kind, rank and
constant value, `Info.Uses` maps a name to its symbol, `Info.Scopes` maps a
program unit to its scope. Both back ends of bib §4.2 walk the one tree and ask
`Info`. LFortran's ASR is the design reference (BSD-3); no code was ported, so
`NOTICE` is unchanged. Clauses read: F2018 7.4 (types, kinds), 7.5 (derived
types), 8.2 to 8.7 (declarations, attributes, implicit typing), 10.1 and
10.2.1 (expressions, intrinsic assignment), 10.1.12 (constant expressions),
11.1.7 (DO variables), 14.2.2 and 19.3 to 19.5 (use and host association).

```go
f, _ := syntax.Parse("a.f90", src, nil)
info, errs := sema.Analyze(f, &sema.Options{ImplicitNone: true})
// errs: "a.f90:3:5: Symbol 'x' at (1) has no IMPLICIT type"
```

Done in this part: scopes and symbols; implicit typing and `implicit none`
(`Options.ImplicitNone` is the notebook default of bib §7); intrinsic types and
kinds validated against gfortran's kinds; folding of named constants (integer,
real, complex, logical, character; kind 16 is typed but not folded; overflow,
division by zero and a bound on folded character length are errors); typing of
expressions with the promotion rules of Table 10.2; array element, section and
substring versus function reference; intrinsic function result types (a table,
not all of F2018 16.9: a function absent from it types as `Unknown`, which no
check refuses); derived types with components, `extends`, type-bound
procedure names and forward pointer components; use association with `only`,
renames and `private`/`public`; `iso_fortran_env` and `iso_c_binding`
constants; statement functions; statement checks (conditions, DO variables,
`intent(in)`, named constants, ranks of assignment, array constructors).

Diagnostics are `file:line:col: message`. The analyser never panics (a panic
is recovered into an error and the fuzzer treats it as a failure); its bounds
(`Limits`: symbols, folded character length, nesting, errors) are refusals,
asserted by bomb tests.

### Calls (M2, part 2)

Clauses read (J3/18-007r1): 15.4.2.2 (explicit interface), 15.4.3.4.1
(generic interfaces), 15.4.3.4.2 and 10.1.6 (defined operations), 10.2.1.4
(defined assignment), 15.4.3.4.5 (restrictions on generic declarations,
C1511, C1512, C1514), 15.5.2 (argument association: keyword, optional and
positional; type, kind and rank agreement; pointer and allocatable actuals;
assumed-shape, explicit-shape and assumed-size dummies; element sequences),
15.5.2.5 (a pointer dummy with `intent(in)` takes a target), 15.5.2.11
(sequence association: an actual too small for an explicit-shape dummy),
15.5.5.2 (generic resolution) and 15.8.1 (elemental references). `sema/call.go`.

- **Generic resolution.** A reference through a generic name selects the one
  specific whose argument list is consistent with the actuals, looking through
  the generics of the same name in enclosing scopes and merging those of one name
  from two modules; none is an error ("There is no specific function for the
  generic"), the intrinsic of the same name stays accessible. The typed tree
  names the specific: `TV.Sym` of the reference, and `Info.Calls` for a `call`.
- **Ambiguity.** Every pair of specifics of a generic is checked against
  C1514 (1), (2) and (4) and, for `operator(..)` and `assignment(=)`, C1511 and
  C1512; a pair that is not distinguishable is refused with
  "Ambiguous interfaces". The pair is judged only when both specifics belong to
  the scope of the generic (gfortran only warns on a pair that includes a
  use-associated specific, and refuses a reference).
- **Argument association.** Count, keywords, missing and duplicate arguments;
  per argument: type and kind, rank, `pointer`/`allocatable` actuals,
  procedure versus data object; `intent(out)`/`intent(inout)` actuals must be
  definable (a constant, an expression, an `intent(in)` dummy is refused);
  elemental references take array actuals of one rank.
- **Explicit-interface requirements.** A reference through an implicit
  interface is refused when it uses a keyword, or when the callee, defined in
  the same file, has an assumed-shape, assumed-rank, allocatable, pointer,
  target, value, optional, volatile or polymorphic dummy, an array, pointer or
  allocatable result, is elemental or has `bind`.
- **Function versus subroutine.** `call f` on a function and `s(..)` in an
  expression on a subroutine are refused.
- **Defined operations and assignment.** Operands of derived type, or of
  intrinsic types the intrinsic operator does not accept, are resolved through
  the `operator(op)` generic; no match is the error gfortran gives. An
  assignment the intrinsic does not accept needs an `assignment(=)` specific.

Bounds are refusals: `Limits.MaxSpecifics` (512 per generic; the ambiguity
check is quadratic in it) and `Limits.MaxActuals` (4 096 per reference), both
asserted by bomb tests. Leniency as before: an unknown type (`class(*)`,
`type(*)`, an opaque module) is never the reason for a refusal.

### Differential against gfortran 15.2.0

`foracle sema-rate --dir D` parses and analyses every file and compares with
`gfortran -fsyntax-only` (which also makes the semantic checks).
`sema/testdata/gf-*.tsv` are gfortran's verdicts, committed so that
`sema/corpus_test.go` asserts the counts with the corpora but without the
compiler; a drop of both-accept or a rise of either other class fails it.

| corpus | files | both accept | refused only by us | accepted only by us | both refuse |
|---|---|---|---|---|---|
| FCVS | 180 | 179 | 0 | 1 | 0 |
| gcc `gfortran.fortran-torture` | 314 | 313 | 1 (parser) | 0 | 0 |
| gcc `gfortran.dg` | 8 306 | 5 750 | 294 (125 parser, 169 analyser) | 1 230 | 1 032 |
| `gfortran.dg`, `dg-do run` files | 3 151 | 2 674 | 156 | 95 | 226 |

Part 2 (argument association, generic resolution, defined operations) moved
`gfortran.dg` from 5 742 / 302 / 1 292 to 5 750 / 294 / 1 230 (both accept up,
refused only by us down, accepted only by us down): it refuses 62 more of the
files gfortran refuses and, through fixes found on the way (a `use` of an
operator was silently dropped, generics of one name from two modules were not
merged, a renamed entity stayed visible under its old name, the result kind of
`bit_size`, `cmplx`, `char`, `trim` and `bessel_jn`), accepts 8 more that
gfortran accepts. Three files stay refused only by us because gfortran skips
the argument checks on a `!GCC$ ATTRIBUTES NO_ARG_CHECK` directive, which the
lexer drops with the other comments.

The 1 230 files accepted only by us are the `gfortran.dg` tests written to be
refused for reasons not checked yet (shapes, type-bound procedure rules,
`-std` conformance). The refused-only-by-us
files are defects or gaps of ours; the most frequent first errors of the 169
analyser refusals are names with no implicit type (31), kind expressions that
need an intrinsic we do not fold (28), derived types used before a definition
the analyser does not order yet (24), named-constant initialisers that are not
recognised as constant (16), type-bound members (14) and entities not found in
a module (10); the list is `foracle sema-rate --errors N`.
`sema/oracle_test.go` checks 16 negative programs (type mismatch, undeclared
under `implicit none`, rank mismatch, `intent(in)` assignment, named constant,
DO variable, ...) and 5 positive ones against gfortran itself, and the 33
negative and 12 positive programs of part 2 (`sema/call_test.go`: ambiguous
generics, no specific, argument type, kind, rank, count and keyword,
`intent(out)` actuals, function versus subroutine, explicit-interface
requirements, defined operators and assignment, elemental ranks).

`BenchmarkAnalyze` (the 12 programs of `testdata/programs`, one core): 2 389
allocations and 598 KB per pass, exact (part 1: 2 104 and 497 KB); time **not
measured: load** (1-min load 24 when it ran, above the bound of 8), and the comparison with
`gfortran -fsyntax-only` was not run for the same reason.

### The cell splitter and the session module (bib §7)

`sema.Session` analyses the cells of a notebook in order, with the shape of
go-notebook's `analysis.Sequential` (`Begin`, `AnalyzeNext(state, index, src)`;
go-notebook was read, not edited: `analysis/lang.go` at commit `0cb6796`). `syntax.Options.Cell`
is the grammar of a cell: statements outside any program unit are main-program
material that needs no `END` and ends at EOF or at the next unit, so one cell
mixes declarations, statements and program units. A cell is then split:

- top-level declarations and top-level subroutines and functions go into the
  session module `__cell<N>`;
- top-level executable statements are wrapped in the subroutine
  `__cell<N>_run`, a procedure of that module (no call is generated: running
  it is M3);
- every earlier session module and every module an earlier cell defined is an
  implicit `use` of `__cell<N>`;
- names are lower case (the lexer normalises; `Crosses` and the lists are lower
  case); redefinition shadows (a use-associated name is replaced by the local
  declaration; earlier cells keep their binding); implicit typing is off unless
  `SessionOptions.ImplicitTyping`;
- a cell with errors returns the state it was given, so a broken cell leaves
  nothing behind (tested).

`CellInfo` carries what go-notebook's graph needs: `Defines` (variables and
named constants: the names that cross, `Crosses`), `Procedures`, `Types` and
`Modules` (they stay Fortran's), `Uses` (earlier cells' names this cell reads),
`Errors`, and what the back ends need: the transformed `File` and its `Info`.
`sema.GoType` is the values table of bib §7 (`integer(k)` to `int8..int64`,
`real` to `float32|float64`, `complex(4|8)` to `complex64|complex128`,
`logical` to `bool`, `character` to `string`, arrays to `*ndarray.Array`,
derived types opaque; `integer(16)`, `real(10)` and `real(16)` have none). The
adapter that fills `analysis.Cell` belongs to the language registration in
go-notebook and is not written. `FuzzSession` is in the CI fuzz matrix.

## Not yet

- Semantics not done, of bib §1.1: conformance of shapes (only ranks are
  compared), type-bound generics and `pass` (a type-bound reference is not
  resolved), the `!GCC$ ATTRIBUTES NO_ARG_CHECK` directive, `select type` dispatch,
  derived-type I/O, submodules, `entry` result typing, `common`/`equivalence`
  storage rules, `data` value conversion, the go-notebook adapter and the
  "value from Go into a cell" direction of bib §7. `eval` was **not** moved onto the
  typed tree (it still walks the syntax tree: the migration follows shape
  conformance).
- No general evaluator: package `eval` is the gate's
  prototype and accepts only the kernels' language. The gate's playground home
  (headless browser) is not implemented, and its figures were not published
  (see "The gate").
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
