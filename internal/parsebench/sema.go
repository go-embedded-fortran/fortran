package parsebench

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/go-embedded-fortran/fortran/internal/corpus"
	"github.com/go-embedded-fortran/fortran/internal/oracle"
	"github.com/go-embedded-fortran/fortran/sema"
)

// SemaVerdict compares our front end (parse and semantic analysis) with
// gfortran -fsyntax-only, which also makes the semantic checks.
type SemaVerdict string

// The verdicts of the semantic differential (bib/fortran.md §8).
const (
	SemaBoth     SemaVerdict = "both-accept"
	SemaOnlyOurs SemaVerdict = "refused-only-by-us"  // gfortran accepts, we refuse
	SemaOnlyGf   SemaVerdict = "accepted-only-by-us" // gfortran refuses, we accept
	SemaNeither  SemaVerdict = "both-refuse"
)

// SemaRec is the verdict on one file.
type SemaRec struct {
	Path    string
	ParseOK bool
	SemaOK  bool   // parsed and analysed without error
	First   string // our first error (parse or semantic)
	Gf      bool
	Verdict SemaVerdict
}

// AnalyzeOne parses and analyses one entry; the error is our first
// diagnostic and whether it is a parse error.
func AnalyzeOne(root string, e corpus.Entry) (parseOK, ok bool, first string) {
	f, err := ParseOne(root, e)
	if err != nil || f == nil {
		msg := "parse error"
		if err != nil {
			msg = firstLine(err.Error())
		}
		return false, false, msg
	}
	_, errs := sema.Analyze(f, nil)
	if len(errs) > 0 {
		return true, false, errs[0].Error()
	}
	return true, true, ""
}

// RateSema runs the analyser on every file of es and compares with the
// verdicts of gfortran: taken from cache where present, else by running it
// with jobs processes (tc nil and no cache entry: the file counts as refused).
func RateSema(ctx context.Context, tc *oracle.Toolchain, root string, es []corpus.Entry, jobs int, cache map[string]bool) []SemaRec {
	recs := make([]SemaRec, len(es))
	sem := make(chan struct{}, max(1, jobs))
	var wg sync.WaitGroup
	for i := range es {
		e := es[i]
		r := &recs[i]
		r.Path = e.Path
		r.ParseOK, r.SemaOK, r.First = AnalyzeOne(root, e)
		gf, cached := cache[e.Path]
		if !cached && tc != nil {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				gf, _ = Gfortran(ctx, tc, root, e)
				r.Gf = gf
				r.Verdict = semaVerdict(r.SemaOK, gf)
			}()
			continue
		}
		r.Gf = gf
		r.Verdict = semaVerdict(r.SemaOK, gf)
	}
	wg.Wait()
	return recs
}

func semaVerdict(ours, gf bool) SemaVerdict {
	switch {
	case ours && gf:
		return SemaBoth
	case gf:
		return SemaOnlyOurs
	case ours:
		return SemaOnlyGf
	}
	return SemaNeither
}

// SemaReport writes one tab-separated line per file: path, parse, sema,
// gfortran, verdict, our first error.
func SemaReport(w io.Writer, recs []SemaRec) {
	for _, r := range recs {
		p, s, g := "refuse", "refuse", "refuse"
		if r.ParseOK {
			p = "parse"
		}
		if r.SemaOK {
			s = "accept"
		}
		if r.Gf {
			g = "accept"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Path, p, s, g, r.Verdict, r.First)
	}
}

// WriteGfVerdicts writes "path TAB accept|refuse" lines, sorted by path.
func WriteGfVerdicts(w io.Writer, recs []SemaRec) {
	rs := append([]SemaRec(nil), recs...)
	sort.Slice(rs, func(i, j int) bool { return rs[i].Path < rs[j].Path })
	for _, r := range rs {
		v := "refuse"
		if r.Gf {
			v = "accept"
		}
		fmt.Fprintf(w, "%s\t%s\n", r.Path, v)
	}
}

// ReadGfVerdicts reads the gfortran verdicts (path -> accepted) from a
// SemaReport or a "path<TAB>accept|refuse" file: any line whose fourth or
// second field is the word accept or refuse.
func ReadGfVerdicts(r io.Reader) map[string]bool {
	m := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		switch {
		case len(f) >= 4 && (f[3] == "accept" || f[3] == "refuse"):
			m[f[0]] = f[3] == "accept"
		case len(f) == 2 && (f[1] == "accept" || f[1] == "refuse"):
			m[f[0]] = f[1] == "accept"
		}
	}
	return m
}

// SemaCounts counts the verdicts.
type SemaCounts struct {
	Files, Parsed, Analysed          int
	Both, OnlyOurs, OnlyGf, Neither  int
	GfAccepted                       int
	ParseRefusedOfGfAccepted         int // refused-only-by-us because the parser refuses
	SemaRefusedOfGfAcceptedAndParsed int // refused-only-by-us by the analyser alone
}

// CountSema counts recs.
func CountSema(recs []SemaRec) SemaCounts {
	var c SemaCounts
	c.Files = len(recs)
	for _, r := range recs {
		if r.ParseOK {
			c.Parsed++
		}
		if r.SemaOK {
			c.Analysed++
		}
		if r.Gf {
			c.GfAccepted++
		}
		switch r.Verdict {
		case SemaBoth:
			c.Both++
		case SemaOnlyOurs:
			c.OnlyOurs++
			if r.ParseOK {
				c.SemaRefusedOfGfAcceptedAndParsed++
			} else {
				c.ParseRefusedOfGfAccepted++
			}
		case SemaOnlyGf:
			c.OnlyGf++
		case SemaNeither:
			c.Neither++
		}
	}
	return c
}

// TopSemaErrors groups the first errors of the files of the given verdict
// that the analyser (not the parser) refused, most frequent first.
func TopSemaErrors(recs []SemaRec, v SemaVerdict, n int) []ErrCount {
	var rs []Rec
	for _, r := range recs {
		if r.Verdict == v && r.ParseOK && r.First != "" {
			rs = append(rs, Rec{Path: r.Path, First: r.First})
		}
	}
	return TopErrors(rs, n)
}

// DgRun keeps only the entries whose source contains "dg-do run" (the
// gfortran.dg files meant to be compiled and run, not refused).
func DgRun(es []corpus.Entry) []corpus.Entry {
	var out []corpus.Entry
	for _, e := range es {
		if bytes.Contains(e.Source, []byte("dg-do run")) {
			out = append(out, e)
		}
	}
	return out
}

// Under is a convenience for tests: the absolute path of an entry.
func Under(root string, e corpus.Entry) string {
	return filepath.Join(root, filepath.FromSlash(e.Path))
}
