package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/go-embedded-fortran/fortran/internal/gate"
	"github.com/go-embedded-fortran/fortran/internal/oracle"
)

func gateCmd() *cobra.Command {
	var dir, coracle string
	var rounds int
	var smoke, force bool
	c := &cobra.Command{
		Use:   "gate",
		Short: "run the back-end decision gate of bib/fortran.md 4.2: contenders A, B-near, B-upper and gfortran on the kernels, then the rule",
		Long: `Runs the gate kernels (testdata/gate) under contender A (the pure-Go prototype
evaluator, package eval), B-near and B-upper (the C transliterations in
testdata/gate/c, compiled by clang ` + gate.ClangVersion + ` to wasm32-wasip1 at -O0 and -O2, run on wazero),
and gfortran ` + oracle.GfortranVersion + ` -O2 (the reference), on one core, in interleaved rounds. Every
checksum is checked against a stated bound around its math/big value; a checksum
out of bound is a failure, not a time. Then it applies the decision rule and prints
the table, the inputs to the rule and the verdict.

The playground (headless browser) home is not implemented: the rule cannot pick A
without it.

Needs the B toolchain: go install github.com/go-embedded-c/c/cmd/coracle@v0.2.1,
then CORACLE_HOME=<dir> coracle toolchain. Refuses at 1-minute load above ` + fmt.Sprint(int(oracle.MaxLoad)) + ` (and
when the load rises above it during the run), unless --force.

--smoke runs tiny sizes for correctness only, and prints no time.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			tc, err := locate(ctx, false)
			if err != nil {
				return err
			}
			if smoke {
				rounds = 1
			}
			// One core: the Go side (contender A, go-ndarray, wazero) must not use
			// more than gfortran's single thread does.
			defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
			res, err := gate.Run(ctx, gate.Options{
				Dir: dir, Rounds: rounds, Smoke: smoke, Force: force,
				Coracle: coracle, Gfortran: tc, Log: cmd.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			res.Report(cmd.OutOrStdout())
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", "testdata/gate", "directory of kernels (<stem>.f90 and c/<stem>.c)")
	c.Flags().StringVar(&coracle, "coracle-home", os.Getenv(gate.EnvCoracle), "directory made by `coracle toolchain` (default $"+gate.EnvCoracle+")")
	c.Flags().IntVar(&rounds, "rounds", 7, "rounds (the gate's protocol: 7)")
	c.Flags().BoolVar(&smoke, "smoke", false, "tiny sizes, one round: correctness only, no time is printed")
	c.Flags().BoolVar(&force, "force", false, "measure even above load "+fmt.Sprint(int(oracle.MaxLoad))+" or when the load is unknown")
	return c
}
