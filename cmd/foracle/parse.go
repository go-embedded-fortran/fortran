package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/go-embedded-fortran/fortran/internal/corpus"
	"github.com/go-embedded-fortran/fortran/internal/oracle"
	"github.com/go-embedded-fortran/fortran/internal/parsebench"
)

func parseRateCmd() *cobra.Command {
	var dir, out, gfCache string
	var jobs, topErrs int
	var noOracle, onlyOurs bool
	c := &cobra.Command{
		Use: "parse-rate", Short: "parse every Fortran file below --dir with the syntax package and compare with gfortran -fsyntax-only", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" {
				return errors.New("--dir is required")
			}
			es, err := corpus.Load(dir)
			if err != nil {
				return err
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
				cache = parsebench.ReadCache(f)
				f.Close()
			}
			recs := parsebench.RateCached(cmd.Context(), tc, dir, es, jobs, cache)
			w := cmd.OutOrStdout()
			if out != "" {
				f, err := os.Create(out)
				if err != nil {
					return err
				}
				defer f.Close()
				parsebench.Report(f, recs)
			}
			s := parsebench.Summarize(recs)
			fmt.Fprintf(w, "%d files: we parse %d (%.1f %%)", s.Files, s.Parsed, 100*float64(s.Parsed)/float64(max(1, s.Files)))
			if tc != nil {
				fmt.Fprintf(w, "; gfortran -fsyntax-only accepts %d: both-accept=%d fail-only-ours=%d fail-gfortran-only=%d fail-both=%d",
					s.GfAccepted, s.Both, s.GfOnly, s.OursOnly, s.Neither)
				if s.GfAccepted > 0 {
					fmt.Fprintf(w, "; of the files gfortran accepts we parse %.1f %%", 100*float64(s.Both)/float64(s.GfAccepted))
				}
			} else if terr != nil {
				fmt.Fprintf(w, "; no oracle (%v)", terr)
			}
			fmt.Fprintln(w)
			if onlyOurs {
				var only []parsebench.Rec
				for _, r := range recs {
					if r.Verdict == parsebench.GfOnly {
						only = append(only, r)
					}
				}
				recs = only
			}
			for _, e := range parsebench.TopErrors(recs, topErrs) {
				fmt.Fprintf(w, "%5d  %s\n       e.g. %s\n", e.Count, e.Msg, e.Eg)
			}
			return nil
		},
	}
	c.Flags().IntVar(&topErrs, "errors", 0, "also list the N most frequent first errors of the refused files")
	c.Flags().StringVar(&gfCache, "gf-cache", "", "take gfortran's verdicts from this earlier --out report instead of running it")
	c.Flags().BoolVar(&onlyOurs, "only-ours", false, "restrict --errors to the files gfortran accepts and we refuse")
	c.Flags().StringVar(&dir, "dir", "", "corpus root")
	c.Flags().StringVar(&out, "out", "", "write one tab-separated line per file here")
	c.Flags().IntVar(&jobs, "jobs", max(1, runtime.NumCPU()/4), "parallel gfortran processes")
	c.Flags().BoolVar(&noOracle, "no-oracle", false, "only our verdict, do not run gfortran")
	return c
}

func parseBenchCmd() *cobra.Command {
	var dir string
	var n, rounds int
	c := &cobra.Command{
		Use: "parse-bench", Short: "parse throughput of the syntax package against gfortran -fsyntax-only on the largest files both accept (one core, interleaved; refused above load 8)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" {
				return errors.New("--dir is required")
			}
			ctx := cmd.Context()
			tc, err := locate(ctx, false)
			if err != nil {
				return err
			}
			es, err := corpus.Load(dir)
			if err != nil {
				return err
			}
			recs := parsebench.Rate(ctx, tc, dir, es, 2)
			load, _ := oracle.Load1(ctx)
			rows, err := parsebench.Bench(ctx, tc, dir, es, recs, n, rounds)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "# gfortran %s -fsyntax-only vs syntax.Parse, %d rounds, one core, 1-min load %.1f before; times are medians\n", oracle.GfortranVersion, rounds, load)
			parsebench.Table(cmd.OutOrStdout(), rows)
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", "", "corpus root")
	c.Flags().IntVar(&n, "top", 5, "number of largest files")
	c.Flags().IntVar(&rounds, "rounds", 7, "interleaved rounds")
	return c
}
