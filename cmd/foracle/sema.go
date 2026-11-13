package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/go-embedded-fortran/fortran/internal/corpus"
	"github.com/go-embedded-fortran/fortran/internal/parsebench"
)

func semaRateCmd() *cobra.Command {
	var dir, out, gfCache, gfOut string
	var jobs, topErrs int
	var noOracle, dgRun bool
	c := &cobra.Command{
		Use: "sema-rate", Short: "parse and analyse every Fortran file below --dir with the sema package and compare with gfortran -fsyntax-only: both-accept, refused-only-by-us, accepted-only-by-us", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" {
				return errors.New("--dir is required")
			}
			es, err := corpus.Load(dir)
			if err != nil {
				return err
			}
			if dgRun {
				es = parsebench.DgRun(es)
			}
			tc, terr := locate(cmd.Context(), false)
			if noOracle || terr != nil {
				tc = nil
			}
			var cache map[string]bool
			if gfCache != "" {
				f, err := os.Open(gfCache)
				if err != nil {
					return err
				}
				cache = parsebench.ReadGfVerdicts(f)
				f.Close()
			}
			if tc == nil && cache == nil {
				return errors.New("no oracle and no --gf-cache: nothing to compare with")
			}
			recs := parsebench.RateSema(cmd.Context(), tc, dir, es, jobs, cache)
			w := cmd.OutOrStdout()
			if out != "" {
				f, err := os.Create(out)
				if err != nil {
					return err
				}
				defer f.Close()
				parsebench.SemaReport(f, recs)
			}
			if gfOut != "" {
				f, err := os.Create(gfOut)
				if err != nil {
					return err
				}
				defer f.Close()
				parsebench.WriteGfVerdicts(f, recs)
			}
			s := parsebench.CountSema(recs)
			fmt.Fprintf(w, "%d files: parse %d, analysed without error %d; gfortran accepts %d: both-accept=%d refused-only-by-us=%d (parser %d, analyser %d) accepted-only-by-us=%d both-refuse=%d\n",
				s.Files, s.Parsed, s.Analysed, s.GfAccepted, s.Both, s.OnlyOurs, s.ParseRefusedOfGfAccepted, s.SemaRefusedOfGfAcceptedAndParsed, s.OnlyGf, s.Neither)
			for _, e := range parsebench.TopSemaErrors(recs, parsebench.SemaOnlyOurs, topErrs) {
				fmt.Fprintf(w, "%5d  %s\n       e.g. %s\n", e.Count, e.Msg, e.Eg)
			}
			return nil
		},
	}
	c.Flags().IntVar(&topErrs, "errors", 0, "list the N most frequent first errors of the files gfortran accepts and the analyser refuses")
	c.Flags().StringVar(&gfCache, "gf-cache", "", "take gfortran's verdicts from this earlier --out report instead of running it")
	c.Flags().StringVar(&gfOut, "gf-out", "", "write gfortran's verdicts only (path TAB accept|refuse) here: the committed input of the floors")
	c.Flags().StringVar(&dir, "dir", "", "corpus root")
	c.Flags().StringVar(&out, "out", "", "write one tab-separated line per file here")
	c.Flags().IntVar(&jobs, "jobs", max(1, runtime.NumCPU()/4), "parallel gfortran processes")
	c.Flags().BoolVar(&noOracle, "no-oracle", false, "do not run gfortran (use --gf-cache)")
	c.Flags().BoolVar(&dgRun, "dg-run", false, "only files containing \"dg-do run\" (the gfortran.dg tests meant to be run)")
	return c
}
