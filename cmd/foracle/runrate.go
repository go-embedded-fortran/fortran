package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-embedded-fortran/fortran/internal/corpus"
	"github.com/go-embedded-fortran/fortran/internal/parsebench"
)

func runRateCmd() *cobra.Command {
	var dir, out string
	var jobs, topNotes int
	var all bool
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "run-rate",
		Short: "run every \"dg-do run\" file below --dir with eval and with gfortran, and compare stdout, stderr and exit status: identical, both-failed, differs, refused-by-us, gfortran-failed, skipped",
		Long: `The execution differential of bib/fortran.md §10 (M6). Each file is built by
gfortran and run, and run by eval in one session with implicit typing; both
sides are bounded (--timeout each, eval's memory 1 GiB and output 1 MiB).

  identical        stdout, stderr and exit status are equal, and the status is 0
  both-failed      the same, with a status that is not 0: the test stopped
                   itself on both sides, so gfortran, the reference, failed it
                   too (usually for want of the dg-options not passed here)
  differs          eval ran it, or failed while running, and they are not
  refused-by-us    eval's analyser or compiler refused it: nothing ran
  gfortran-failed  gfortran did not build it or did not run it within bounds
  skipped          fixed form (eval.Session reads free form), or dg-shouldfail

Every class is printed, zero included, with the total read: a run that read
nothing is refused rather than reported as zero failures.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" {
				return errors.New("--dir is required")
			}
			es, err := corpus.Load(dir)
			if err != nil {
				return err
			}
			if !all {
				es = parsebench.DgRun(es)
			}
			if len(es) == 0 {
				return fmt.Errorf("no file to run below %s: a run that read nothing is not a result", dir)
			}
			tc, err := locate(cmd.Context(), false)
			if err != nil {
				return err
			}
			recs := parsebench.RateRun(cmd.Context(), tc, es, jobs, parsebench.RunLimits{Timeout: timeout})
			if out != "" {
				f, err := os.Create(out)
				if err != nil {
					return err
				}
				defer f.Close()
				parsebench.RunReport(f, recs)
			}
			n := parsebench.CountRun(recs)
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%d files: identical=%d both-failed=%d differs=%d refused-by-us=%d gfortran-failed=%d skipped=%d\n",
				len(recs), n[parsebench.RunSame], n[parsebench.RunBothFail], n[parsebench.RunDiffers], n[parsebench.RunRefused], n[parsebench.RunGfFailed], n[parsebench.RunSkipped])
			for _, v := range []parsebench.RunVerdict{parsebench.RunDiffers, parsebench.RunRefused} {
				for _, e := range parsebench.TopRunNotes(recs, v, topNotes) {
					fmt.Fprintf(w, "%-13s %5d  %s\n       e.g. %s\n", v, e.Count, e.Msg, e.Eg)
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", "", "corpus root")
	c.Flags().StringVar(&out, "out", "", "write one tab-separated line per file here: path, verdict, note")
	c.Flags().IntVar(&jobs, "jobs", max(1, runtime.NumCPU()/4), "files compared at a time")
	c.Flags().IntVar(&topNotes, "notes", 0, "list the N most frequent reasons of the differs and refused-by-us files")
	c.Flags().BoolVar(&all, "all", false, "every file, not only those containing \"dg-do run\"")
	c.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "wall-clock bound of each side of each file")
	return c
}
