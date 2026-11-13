package corpus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-embedded-fortran/fortran/internal/golden"
	"github.com/go-embedded-fortran/fortran/internal/oracle"
)

// Programs compiles and runs every Fortran file directly under dir (the
// committed golden programs, written for this repository) and returns the
// oracle's answer for each, sorted by name. A program that does not compile
// is an error: a golden program must be valid Fortran. A program that timed
// out or wrote past the output cap is an error too.
func Programs(ctx context.Context, tc *oracle.Toolchain, dir string, co oracle.CompileOptions, lim oracle.Limits) ([]golden.Record, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if e.Type().IsRegular() && FormOf(e.Name()) != NotFortran {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	recs := make([]golden.Record, 0, len(names))
	for _, n := range names {
		r, err := program(ctx, tc, filepath.Join(dir, n), co, lim)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		r.Name = strings.TrimSuffix(n, filepath.Ext(n))
		recs = append(recs, r)
	}
	return recs, nil
}

func program(ctx context.Context, tc *oracle.Toolchain, path string, co oracle.CompileOptions, lim oracle.Limits) (golden.Record, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return golden.Record{}, err
	}
	dir, err := os.MkdirTemp("", "foracle-")
	if err != nil {
		return golden.Record{}, err
	}
	defer os.RemoveAll(dir)
	co.Ext = FormOf(path).Ext()
	exe, err := tc.Compile(ctx, dir, src, co)
	if err != nil {
		return golden.Record{}, err
	}
	res, err := oracle.Run(ctx, exe, dir, nil, nil, lim)
	if err != nil {
		return golden.Record{}, err
	}
	if res.TimedOut || res.Truncated {
		return golden.Record{}, fmt.Errorf("timed out or produced too much output")
	}
	return golden.Record{Exit: res.Exit, Signal: res.Signal, Stdout: res.Stdout, Stderr: res.Stderr}, nil
}
