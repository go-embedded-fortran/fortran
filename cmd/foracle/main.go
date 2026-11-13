// Command foracle drives the reference Fortran compiler (gfortran 15.2.0, run
// natively) as the differential oracle of go-embedded-fortran.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-embedded-fortran/fortran/internal/corpus"
	"github.com/go-embedded-fortran/fortran/internal/golden"
	"github.com/go-embedded-fortran/fortran/internal/oracle"
	"github.com/go-embedded-fortran/fortran/internal/provision"
)

// osExit is replaced by tests.
var osExit = os.Exit

func main() {
	oracle.Init() // a re-executed helper applies the memory limit and never returns
	err := RootCmd().Execute()
	var ec exitCode
	switch {
	case errors.As(err, &ec):
		osExit(int(ec))
	case err != nil:
		osExit(1)
	}
}

// exitCode is returned by `run` to carry the program's exit status.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// RootCmd builds the command tree.
func RootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "foracle",
		Short:         "Fortran oracle: gfortran " + oracle.GfortranVersion + " run natively, bounded",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(versionCmd(), toolchainCmd(), runCmd(), goldenCmd(), corpusCmd(), benchCmd())
	return root
}

func locate(ctx context.Context, flang bool) (*oracle.Toolchain, error) {
	tc, err := oracle.Locate()
	if err != nil {
		return nil, err
	}
	if err := tc.Verify(ctx); err != nil {
		return nil, err
	}
	if flang {
		return tc, tc.VerifyFlang(ctx)
	}
	return tc, nil
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use: "version", Short: "print the pinned versions and the toolchain found", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "gfortran %s\nflang %s (optional, Linux)\n", oracle.GfortranVersion, oracle.FlangVersion)
			if _, err := locate(cmd.Context(), false); err != nil {
				fmt.Fprintf(w, "toolchain: %v\n", err)
			} else {
				fmt.Fprintln(w, "toolchain: ok")
			}
			if tc, err := oracle.Locate(); err == nil && tc.Flang != "" {
				if err := tc.VerifyFlang(cmd.Context()); err != nil {
					fmt.Fprintf(w, "flang: %v\n", err)
				} else {
					fmt.Fprintln(w, "flang: ok")
				}
			}
			return nil
		},
	}
}

func toolchainCmd() *cobra.Command {
	var home string
	var flang bool
	c := &cobra.Command{
		Use: "toolchain", Short: "install the pinned gfortran into --home (conda-forge via micromamba, no root)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if home == "" {
				return errors.New("--home is required")
			}
			return provision.Install(cmd.Context(), provision.Options{Home: home, Log: cmd.ErrOrStderr(), Flang: flang})
		},
	}
	c.Flags().StringVar(&home, "home", os.Getenv(oracle.EnvHome), "directory to install into (default $"+oracle.EnvHome+")")
	c.Flags().BoolVar(&flang, "flang", false, "also install flang "+oracle.FlangVersion+" (Linux amd64 only)")
	return c
}

func compileFlags(c *cobra.Command, co *oracle.CompileOptions) {
	c.Flags().StringVar(&co.Opt, "opt", "-O0", "optimisation flag")
}

func runCmd() *cobra.Command {
	var co oracle.CompileOptions
	c := &cobra.Command{
		Use: "run FILE", Short: "compile FILE (.f90 free form, .f fixed form) with the oracle, run it, and relay stdout, stderr and exit status", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			tc, err := locate(ctx, co.Flang)
			if err != nil {
				return err
			}
			src, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			co.Ext = corpus.FormOf(args[0]).Ext()
			res, err := tc.Output(ctx, src, co, oracle.Limits{})
			if err != nil {
				return err
			}
			cmd.OutOrStdout().Write(res.Stdout)
			cmd.ErrOrStderr().Write(res.Stderr)
			switch {
			case res.Signal != "":
				fmt.Fprintf(cmd.ErrOrStderr(), "signal: %s\n", res.Signal)
				return exitCode(134)
			case res.TimedOut:
				return errors.New("timed out")
			case res.Truncated:
				return errors.New("output over the cap")
			case res.Exit != 0:
				return exitCode(res.Exit)
			}
			return nil
		},
	}
	compileFlags(c, &co)
	c.Flags().BoolVar(&co.Flang, "flang", false, "use flang "+oracle.FlangVersion+" instead of gfortran (Linux only)")
	return c
}

func goldenCmd() *cobra.Command {
	var dir, out string
	var co oracle.CompileOptions
	c := &cobra.Command{
		Use: "golden", Short: "regenerate the golden file from the committed programs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			tc, err := locate(ctx, false)
			if err != nil {
				return err
			}
			recs, err := corpus.Programs(ctx, tc, dir, co, oracle.Limits{})
			if err != nil {
				return err
			}
			f, err := os.Create(out)
			if err != nil {
				return err
			}
			if err := golden.Encode(f, recs); err != nil {
				f.Close()
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d programs -> %s\n", len(recs), out)
			return f.Close()
		},
	}
	c.Flags().StringVar(&dir, "dir", "testdata/programs", "directory of programs")
	c.Flags().StringVar(&out, "out", "testdata/programs.golden", "golden file to write")
	compileFlags(c, &co)
	return c
}

func corpusCmd() *cobra.Command {
	var dir, out string
	var jobs int
	var co oracle.CompileOptions
	c := &cobra.Command{
		Use: "corpus", Short: "run every .f90/.f95/.f03/.f08 (free form) and .f/.for (fixed form) file below --dir through the oracle; one line per file", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" {
				return errors.New("--dir is required")
			}
			ctx := cmd.Context()
			tc, err := locate(ctx, co.Flang)
			if err != nil {
				return err
			}
			es, err := corpus.Load(dir)
			if err != nil {
				return err
			}
			recs := corpus.Run(ctx, tc, dir, es, co, oracle.Limits{Timeout: 20 * time.Second}, jobs)
			w := cmd.OutOrStdout()
			if out != "" {
				f, err := os.Create(out)
				if err != nil {
					return err
				}
				defer f.Close()
				corpus.Report(f, recs)
			} else {
				corpus.Report(w, recs)
			}
			sum := corpus.Summary(recs)
			fmt.Fprintf(w, "%d files: pass=%d compile-fail=%d fail=%d signal=%d timeout=%d harness=%d\n", len(recs),
				sum[corpus.Pass], sum[corpus.CompileFail], sum[corpus.Fail], sum[corpus.Signal], sum[corpus.Timeout], sum[corpus.Harness])
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", "", "corpus root")
	c.Flags().StringVar(&out, "out", "", "write one line per file here instead of to stdout")
	c.Flags().IntVar(&jobs, "jobs", max(1, runtime.NumCPU()/2), "parallel compilations")
	compileFlags(c, &co)
	c.Flags().BoolVar(&co.Flang, "flang", false, "use flang "+oracle.FlangVersion+" instead of gfortran (Linux only)")
	return c
}

func benchCmd() *cobra.Command {
	var dir string
	var rounds int
	var force bool
	c := &cobra.Command{
		Use: "bench", Short: "time the four kernels of the §4.2 gate with gfortran " + oracle.BenchOpt + " (the reference column)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			w := cmd.OutOrStdout()
			load, lerr := oracle.Load1(ctx)
			switch {
			case lerr != nil && !force:
				return fmt.Errorf("the load average is unknown (%v); --force measures anyway", lerr)
			case lerr == nil && load > oracle.MaxLoad && !force:
				return fmt.Errorf("1-minute load %.1f is above %.0f: not measuring (--force overrides)", load, oracle.MaxLoad)
			}
			tc, err := locate(ctx, false)
			if err != nil {
				return err
			}
			ks, err := tc.Bench(ctx, dir, rounds, oracle.Limits{Timeout: 2 * time.Minute, MemoryBytes: 4 << 30})
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "# gfortran %s %s, %d rounds, one core, 1-min load %.1f; seconds (cpu_time around the kernel)\n",
				oracle.GfortranVersion, oracle.BenchOpt, rounds, load)
			for _, k := range ks {
				fmt.Fprintf(w, "%-22s median %9.4f  spread %8.4f  checksum %s\n", k.Name, k.Median, k.Spread, k.Checksum)
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", "testdata/gate", "directory of kernels")
	c.Flags().IntVar(&rounds, "rounds", 7, "rounds (the gate's protocol: 7, median and spread)")
	c.Flags().BoolVar(&force, "force", false, "measure even above load "+fmt.Sprint(int(oracle.MaxLoad))+" or when the load is unknown")
	return c
}
