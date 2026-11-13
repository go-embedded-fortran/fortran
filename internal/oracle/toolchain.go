// Package oracle drives the reference Fortran compiler as a differential
// oracle.
//
// The oracle of go-embedded-fortran is gfortran 15.2.0 run natively: it is the
// compiler LFortran's and llvm-test-suite's reference outputs are made with
// (bib/fortran.md §2 of go-scikit/roadmap). It is GPL and is only ever run as
// a program, never linked or ported. The version is pinned below; Verify
// refuses any other, because a golden produced by another gfortran would be a
// different oracle.
//
// flang 23.1.4 (Linux release tarballs only) is a second, optional oracle.
package oracle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// GfortranVersion is the pinned gfortran release (GCC 15.2.0).
const GfortranVersion = "15.2.0"

// FlangVersion is the pinned LLVM release whose flang is the optional second
// oracle (the LLVM of the C oracle, llvmorg-23.1.4).
const FlangVersion = "23.1.4"

// Environment variables that locate the toolchain.
const (
	EnvGfortran = "FORACLE_GFORTRAN" // the gfortran executable
	EnvFlang    = "FORACLE_FLANG"    // the flang executable (optional)
	EnvHome     = "FORACLE_HOME"     // a directory made by `foracle toolchain`
)

// ErrNoToolchain is wrapped by every error that means "the oracle toolchain is
// not installed here"; tests skip on it, they never fail.
var ErrNoToolchain = errors.New("oracle toolchain not available")

// Toolchain is a located oracle.
type Toolchain struct {
	Gfortran string
	// Flang is the flang executable, or "" when none was asked for or found.
	Flang string
}

// exe is the executable suffix of the host.
func exe() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// HomeGfortran returns where `foracle toolchain` puts gfortran below a home
// directory: the conda-forge environment it creates (see package provision).
// The conda-forge package installs the driver under its triplet name; the
// plain name is accepted when a distribution provides it.
func HomeGfortran(home string) string {
	bin := filepath.Join(home, "env", "bin")
	plain := filepath.Join(bin, "gfortran"+exe())
	if _, err := os.Stat(plain); err == nil {
		return plain
	}
	if m, err := filepath.Glob(filepath.Join(bin, "*-gfortran"+exe())); err == nil && len(m) == 1 {
		return m[0]
	}
	return plain
}

// HomeFlang returns where `foracle toolchain --flang` puts flang.
func HomeFlang(home string) string {
	return filepath.Join(home, "llvm", "bin", "flang"+exe())
}

// Locate finds the toolchain from the environment: $FORACLE_GFORTRAN, or else
// $FORACLE_HOME. It does not run anything.
func Locate() (*Toolchain, error) {
	gf, fl := os.Getenv(EnvGfortran), os.Getenv(EnvFlang)
	if home := os.Getenv(EnvHome); home != "" {
		if gf == "" {
			gf = HomeGfortran(home)
		}
		if fl == "" {
			if p := HomeFlang(home); fileExists(p) {
				fl = p
			}
		}
	}
	if gf == "" {
		return nil, fmt.Errorf("%w: set %s or %s", ErrNoToolchain, EnvHome, EnvGfortran)
	}
	return &Toolchain{Gfortran: gf, Flang: fl}, nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// Verify checks that the gfortran is the pinned release. Anything else is an
// error: a golden produced by another gfortran would be a different oracle.
func (t *Toolchain) Verify(ctx context.Context) error {
	if _, err := os.Stat(t.Gfortran); err != nil {
		return fmt.Errorf("%w: %v", ErrNoToolchain, err)
	}
	got, err := t.Version(ctx)
	if err != nil {
		return err
	}
	if got != GfortranVersion {
		return fmt.Errorf("oracle: gfortran %s is installed, the pinned release is %s", got, GfortranVersion)
	}
	return nil
}

// VerifyFlang checks that the flang is the pinned release.
func (t *Toolchain) VerifyFlang(ctx context.Context) error {
	if t.Flang == "" {
		return fmt.Errorf("%w: no flang (set %s, or run `foracle toolchain --flang`; Linux only)", ErrNoToolchain, EnvFlang)
	}
	out, err := exec.CommandContext(ctx, t.Flang, "--version").Output()
	if err != nil {
		return fmt.Errorf("oracle: %s --version: %w", t.Flang, err)
	}
	got, err := ParseFlangVersion(out)
	if err != nil {
		return err
	}
	if got != FlangVersion {
		return fmt.Errorf("oracle: flang %s is installed, the pinned release is %s", got, FlangVersion)
	}
	return nil
}

// Version returns the gfortran version, from `gfortran --version`.
func (t *Toolchain) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, t.Gfortran, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("oracle: %s --version: %w", t.Gfortran, err)
	}
	return ParseVersion(out)
}

// ParseVersion extracts "15.2.0" from the first line of `gfortran --version`:
//
//	GNU Fortran (conda-forge gcc 15.2.0-20) 15.2.0
//	GNU Fortran (Homebrew GCC 16.2.0) 16.2.0
//
// the last field of a line that starts with "GNU Fortran". The parenthesis is
// the packager's and may hold anything, so only the trailing field counts.
func ParseVersion(out []byte) (string, error) {
	line, _, _ := strings.Cut(string(out), "\n")
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "GNU Fortran") {
		return "", fmt.Errorf("oracle: not a gfortran version line: %q", abbreviate(line))
	}
	f := strings.Fields(line)
	v := f[len(f)-1]
	if v == "" || v[0] < '0' || v[0] > '9' {
		return "", fmt.Errorf("oracle: no version at the end of %q", abbreviate(line))
	}
	return v, nil
}

// ParseFlangVersion extracts "23.1.4" from `flang --version`
// ("flang version 23.1.4 (…)").
func ParseFlangVersion(out []byte) (string, error) {
	const key = "flang version "
	i := bytes.Index(out, []byte(key))
	if i < 0 {
		return "", fmt.Errorf("oracle: no %q in %q", key, abbreviate(string(out)))
	}
	rest := out[i+len(key):]
	end := bytes.IndexAny(rest, " \r\n")
	if end < 0 {
		end = len(rest)
	}
	if end == 0 {
		return "", fmt.Errorf("oracle: empty version in %q", abbreviate(string(out)))
	}
	return string(rest[:end]), nil
}

func abbreviate(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

// Source forms, by file extension. gfortran chooses the form from the
// extension: .f, .for, .ftn are fixed form, .f90 and later free form.
const (
	FreeForm  = ".f90"
	FixedForm = ".f"
)

// CompileOptions are the flags of one compilation.
type CompileOptions struct {
	// Opt is the optimisation flag ("-O0" when empty: goldens are -O0).
	Opt string
	// Ext is the extension of the source file, which selects the source form
	// (".f90" when empty).
	Ext string
	// Extra flags are appended after the fixed ones.
	Extra []string
	// Timeout bounds the compile (60 s when zero).
	Timeout time.Duration
	// Flang compiles with the optional second oracle, flang, instead of
	// gfortran.
	Flang bool
}

// Args returns the compiler arguments for compiling src to out. -w silences
// warnings (the corpora are full of them); there is no -std, so the GNU
// default dialect applies, as it does for the corpora's own runs.
// -fno-backtrace keeps gfortran's runtime from printing addresses.
func (o CompileOptions) Args(src, out string) []string {
	if o.Opt == "" {
		o.Opt = "-O0"
	}
	args := []string{o.Opt, "-w"}
	if !o.Flang {
		args = append(args, "-fno-backtrace")
	}
	args = append(args, o.Extra...)
	return append(args, src, "-o", out)
}

// CompileError is the compiler's refusal of a program (as opposed to a
// failure to run the compiler at all).
type CompileError struct{ Diagnostics string }

func (e *CompileError) Error() string { return "compile: " + e.Diagnostics }

// Compile compiles one Fortran program to a native executable in dir (which
// the caller owns and removes) and returns its path. A *CompileError means
// gfortran rejected the source.
func (t *Toolchain) Compile(ctx context.Context, dir string, src []byte, o CompileOptions) (string, error) {
	if o.Timeout == 0 {
		o.Timeout = 60 * time.Second
	}
	ext := o.Ext
	if ext == "" {
		ext = FreeForm
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	in := filepath.Join(dir, "prog"+ext)
	if err := os.WriteFile(in, src, 0o600); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "prog"+exe())
	compiler := t.Gfortran
	if o.Flang {
		if t.Flang == "" {
			return "", fmt.Errorf("%w: no flang", ErrNoToolchain)
		}
		compiler = t.Flang
	}
	cmd := exec.CommandContext(ctx, compiler, o.Args(in, out)...)
	cmd.Dir = dir // modules (.mod files) land here, not in the caller's tree
	// The conda-forge driver finds its assembler and linker driver ("clang")
	// through PATH: its own directory goes first, so a bare host is enough.
	cmd.Env = append(os.Environ(), "LC_ALL=C",
		"PATH="+filepath.Dir(compiler)+string(os.PathListSeparator)+os.Getenv("PATH"))
	var diag bytes.Buffer
	cmd.Stderr, cmd.Stdout = &diag, &diag
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("oracle: %s: %w", filepath.Base(compiler), ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", &CompileError{Diagnostics: diag.String()}
		}
		return "", fmt.Errorf("oracle: running %s: %w", compiler, err)
	}
	return out, nil
}
